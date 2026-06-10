"""
quantlab_to_optuna.py — Bridge: QuantLab Postgres → Optuna sqlite.

Two modes:

  --mode winners (default) — 1 trial per task's final winner (gene_records).
    Sparse but each row is a fully-vetted promote-grade challenger.
    Study suffix: "__winners".

  --mode traces — 1 trial per (gen, individual) inside the GA loop
    (evaluation_traces, Phase 1.5). 6000+ trials/task — what Optuna's
    fANOVA / parallel-coord actually need to be meaningful.
    Study suffix: "__traces".

Both modes:
  Study      = (strategy_id, pair, interval) — all rows for this combo
  trial.value = score_total (NULL → TrialState.FAIL)
  intermediates = window_scores per slice (6m=0, 2y=1, 5y=2, 10y=3)
  params     = gene array decoded via per-strategy field registry

Sync model: on-demand. Each run **wipes and rebuilds** the sqlite file —
no incremental state, no drift. Reads DB DSN from QuantLab's config.yaml.
"""
import argparse
import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path

import optuna
import psycopg
import yaml
from optuna.distributions import FloatDistribution, IntDistribution
from optuna.trial import TrialState
from psycopg.rows import dict_row


# ── Strategy gene-field registry ────────────────────────────────────────
# sigmoid_v1: mirrors internal/strategies/sigmoid_v1/chromosome.go §4.1.
# Order MUST match the index constants there.
SIGMOID_V1_FIELDS = [
    ("a1",                         FloatDistribution(-1.0, 1.0)),
    ("a2",                         FloatDistribution(-1.0, 1.0)),
    ("a3",                         FloatDistribution(-1.0, 1.0)),
    ("beta",                       FloatDistribution(0.5, 5.0)),
    ("gamma",                      FloatDistribution(0.0, 3.0)),
    ("ema_short_period",           IntDistribution(5, 100)),
    ("ema_long_period",            IntDistribution(50, 300)),
    ("mav_short_period",           IntDistribution(5, 50)),
    ("mav_long_period",            IntDistribution(30, 250)),
    ("quiet_threshold",            FloatDistribution(0.3, 1.2)),
    ("micro_reserve_pct",          FloatDistribution(0.05, 0.5)),
    ("macro_inject_usd",           FloatDistribution(10.0, 1000.0)),
    ("release_drawdown_threshold", FloatDistribution(0.1, 0.5)),
]
STRATEGY_REGISTRY = {"sigmoid_v1": SIGMOID_V1_FIELDS}

# Crucible window → step index (fixed order per CLAUDE.md "evaluation pipeline").
WINDOW_STEPS = {"6m": 0, "2y": 1, "5y": 2, "10y": 3}


def load_db_dsn(config_path: Path) -> str:
    with config_path.open() as f:
        cfg = yaml.safe_load(f)
    db = cfg["database"]
    return (
        f"host={db['host']} port={db['port']} "
        f"user={db['user']} password={db['password']} "
        f"dbname={db['database']} sslmode={db.get('ssl_mode', 'disable')}"
    )


def decode_gene(strategy_id: str, payload):
    """Return (params, distributions) keyed by `gene__<name>`; None if unknown."""
    fields = STRATEGY_REGISTRY.get(strategy_id)
    if fields is None:
        return None
    if not isinstance(payload, list) or len(payload) != len(fields):
        print(
            f"  WARN: strategy={strategy_id} payload len={len(payload) if isinstance(payload, list) else 'NA'}"
            f" != registry {len(fields)}, skipping trial",
            file=sys.stderr,
        )
        return None
    params, dists = {}, {}
    for (name, dist), val in zip(fields, payload):
        key = f"gene__{name}"
        params[key] = int(round(val)) if isinstance(dist, IntDistribution) else float(val)
        dists[key] = dist
    return params, dists


def extract_window_intermediates(window_scores_json):
    """Parse window_scores → ({step: value} dict, first-fatal-window-or-None)."""
    if not window_scores_json:
        return {}, None
    arr = window_scores_json if isinstance(window_scores_json, list) else json.loads(window_scores_json)
    intermediates: dict[int, float] = {}
    first_fatal = None
    for cr in arr:
        win = cr.get("window")
        step = WINDOW_STEPS.get(win)
        if step is None:
            continue
        sc = cr.get("score") or {}
        val = sc.get("value")
        if val is not None:
            intermediates[step] = float(val)
        if sc.get("fatal") and first_fatal is None:
            first_fatal = win
    return intermediates, first_fatal


def _maybe_loads(blob):
    if blob is None:
        return None
    if isinstance(blob, (bytes, bytearray, memoryview)):
        return json.loads(bytes(blob))
    if isinstance(blob, str):
        return json.loads(blob)
    return blob  # psycopg may already deserialize jsonb to python


def _finalize_trial(params, dists, score_total, intermediates, first_fatal, user_attrs):
    if score_total is None:
        return optuna.trial.create_trial(
            params=params,
            distributions=dists,
            state=TrialState.FAIL,
            user_attrs={**user_attrs, "fatal_window": first_fatal or "?"},
            intermediate_values=intermediates or None,
        )
    return optuna.trial.create_trial(
        params=params,
        distributions=dists,
        value=float(score_total),
        state=TrialState.COMPLETE,
        user_attrs=user_attrs,
        intermediate_values=intermediates or None,
    )


def build_trial_from_winner(row):
    """One gene_records row → FrozenTrial."""
    pkg = _maybe_loads(row["full_package_json"])
    cg = pkg["core"]["champion_gene"]
    if cg.get("encoding") != "json":
        print(f"  WARN: challenger={row['challenger_id']} encoding={cg.get('encoding')!r}, skip", file=sys.stderr)
        return None

    decoded = decode_gene(row["strategy_id"], cg.get("payload"))
    if decoded is None:
        return None
    params, dists = decoded

    intermediates, first_fatal = extract_window_intermediates(_maybe_loads(row["window_scores_json"]))

    user_attrs = {
        "challenger_id": row["challenger_id"],
        "decision_status": row["decision_status"],
        "schema_version": row["schema_version"],
        "fitness_version": row["fitness_version"],
        "fingerprint_version": row["fingerprint_version"],
        "taker_fee_bps": float(row["taker_fee_bps"]),
        "slippage_bps": float(row["slippage_bps"]),
        "test_mode": bool(row["test_mode"]),
    }
    for key in ("score_raw", "consistency_penalty", "max_drawdown",
                "oos_alpha_monthly", "oos_alpha_weekly", "dsr"):
        v = row.get(key)
        if v is not None:
            user_attrs[key] = float(v)
    if row.get("task_id"):
        user_attrs["task_id"] = row["task_id"]
    if row.get("task_status"):
        user_attrs["task_status"] = row["task_status"]
    if row.get("finished_at"):
        user_attrs["finished_at"] = row["finished_at"].isoformat()
    if row.get("deleted_at"):
        user_attrs["soft_deleted_at"] = row["deleted_at"].isoformat()

    return _finalize_trial(params, dists, row.get("score_total"), intermediates, first_fatal, user_attrs)


def build_trial_from_trace(row):
    """One evaluation_traces row → FrozenTrial."""
    payload = _maybe_loads(row["gene_json"])
    decoded = decode_gene(row["strategy_id"], payload)
    if decoded is None:
        return None
    params, dists = decoded

    intermediates, first_fatal = extract_window_intermediates(_maybe_loads(row["window_scores_json"]))

    user_attrs = {
        "task_id": row["task_id"],
        "generation": int(row["generation"]),
        "individual_idx": int(row["individual_idx"]),
        "fingerprint": row["fingerprint"],
        "fatal": bool(row["fatal"]),
    }
    for key in ("score_raw", "consistency_penalty"):
        v = row.get(key)
        if v is not None:
            user_attrs[key] = float(v)
    if row.get("fatal_reason"):
        user_attrs["fatal_reason"] = row["fatal_reason"]
    if row.get("task_status"):
        user_attrs["task_status"] = row["task_status"]

    return _finalize_trial(params, dists, row.get("score_total"), intermediates, first_fatal, user_attrs)


WINNERS_SQL = """
    SELECT g.challenger_id, g.strategy_id, g.pair, g.score_total, g.score_raw,
           g.consistency_penalty, g.max_drawdown, g.window_scores_json,
           g.full_package_json, g.decision_status, g.schema_version,
           g.fitness_version, g.fingerprint_version, g.taker_fee_bps,
           g.slippage_bps, g.test_mode, g.oos_alpha_monthly, g.oos_alpha_weekly,
           g.dsr, g.deleted_at,
           t.task_id, t.status AS task_status, t.finished_at, t."interval"
    FROM gene_records g
    LEFT JOIN evolution_tasks t ON t.challenger_id = g.challenger_id
    {where_clause}
    ORDER BY g.strategy_id, g.pair, g.created_at
"""

TRACES_SQL = """
    SELECT e.task_id, e.generation, e.individual_idx, e.gene_json,
           e.score_total, e.score_raw, e.consistency_penalty,
           e.fatal, e.fatal_reason, e.window_scores_json, e.fingerprint,
           e.deleted_at,
           t.strategy_id, t.pair, t."interval", t.status AS task_status
    FROM evaluation_traces e
    JOIN evolution_tasks t ON t.task_id = e.task_id
    {where_clause}
    ORDER BY t.strategy_id, t.pair, e.task_id, e.generation, e.individual_idx
"""


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--config", default="/home/l9g/quantlab/config.yaml")
    ap.add_argument("--output", default="quantlab_phase1.db")
    ap.add_argument("--mode", choices=("winners", "traces"), default="winners",
                    help="winners = gene_records (1/task); traces = evaluation_traces (every individual)")
    ap.add_argument("--include-deleted", action="store_true", default=True,
                    help="include soft-deleted GORM rows (default True for Phase 1)")
    ap.add_argument("--live-only", dest="include_deleted", action="store_false",
                    help="skip soft-deleted rows")
    args = ap.parse_args()

    dsn = load_db_dsn(Path(args.config))
    out_path = Path(args.output)
    # G2: build into a temp sibling, then os.replace() it into place at the end
    # (atomic within one filesystem). The old in-place wipe-rebuild left a window
    # where the live dashboard read a half-rebuilt file (partial data / 502);
    # writing to a temp and swapping eliminates that window for new readers.
    tmp_path = out_path.with_name(out_path.name + ".tmp")
    storage_url = f"sqlite:///{tmp_path}"
    exported_at = datetime.now(timezone.utc).isoformat()

    if tmp_path.exists():
        tmp_path.unlink()

    if args.mode == "winners":
        sql = WINNERS_SQL.format(where_clause="" if args.include_deleted else "WHERE g.deleted_at IS NULL")
        row_label = "gene_records"
        build = build_trial_from_winner
        suffix = "winners"
    else:
        sql = TRACES_SQL.format(where_clause="" if args.include_deleted else "WHERE e.deleted_at IS NULL")
        row_label = "evaluation_traces"
        build = build_trial_from_trace
        suffix = "traces"

    with psycopg.connect(dsn) as conn:
        with conn.cursor(row_factory=dict_row) as cur:
            cur.execute(sql)
            rows = cur.fetchall()

    print(f"fetched {len(rows)} {row_label} rows from Postgres (mode={args.mode})")

    studies: dict[tuple, list] = {}
    for row in rows:
        key = (row["strategy_id"], row["pair"], row.get("interval") or "unknown")
        studies.setdefault(key, []).append(row)

    print(f"grouped into {len(studies)} studies\n")

    total_added = 0
    for (strat, pair, interval), group in studies.items():
        study_name = f"{strat}__{pair}__{interval}__{suffix}"
        study = optuna.create_study(
            study_name=study_name,
            storage=storage_url,
            direction="maximize",
            load_if_exists=True,
        )
        study.set_user_attr("strategy_id", strat)
        study.set_user_attr("pair", pair)
        study.set_user_attr("interval", interval)
        study.set_user_attr("source", suffix)
        study.set_user_attr("exported_at", exported_at)  # G2: data-freshness stamp
        n_added = 0
        for row in group:
            tr = build(row)
            if tr is None:
                continue
            study.add_trial(tr)
            n_added += 1
        total_added += n_added
        best = None
        try:
            best = study.best_value if n_added > 0 else None
        except ValueError:
            pass  # all trials Fatal → best_value raises
        print(f"  {study_name:<55}  trials={n_added}  best={best!r}")

    # G2: atomic swap — the live dashboard's next sqlite connection sees the
    # fully-built file, never a partial one. (A long-lived dashboard process
    # may keep the old inode open until restarted; the systemd cron in the
    # runbook restarts it after the swap to pick up fresh data.)
    os.replace(tmp_path, out_path)

    print(f"\nwrote {total_added} trials → {out_path} (atomic replace; exported_at={exported_at})")
    print("launch dashboard (binds localhost by default — keep it off public interfaces, G3):")
    print(f"  .venv/bin/optuna-dashboard sqlite:///{out_path} --port 8088")
    print("  # dev on a remote VM reached from the host browser: append --host 0.0.0.0")


if __name__ == "__main__":
    main()
