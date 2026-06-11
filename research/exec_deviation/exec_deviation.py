"""
exec_deviation.py — 实盘执行偏差观察脚本 (LEARN-ga-rl-bayesian §12.1-① / §12.4-②).

What it measures
----------------
The backtest fills every order at the decision bar's close × (1 ± slippage_bps)
(sigmoid_v1/simulator.go, close-only). Live, the dispatcher converts market
intents to marketable LIMIT IOC at latestClose × (1 ± price_cap_bps) (B2).
This script joins trade_records ↔ spot_executions ↔ klines and reports, per
dispatched order:

  adverse_bps   side-signed deviation of the fill VWAP from the decision-time
                reference close (positive = worse than the reference, i.e. a
                real execution cost). Directly comparable to the backtest's
                slippage_bps assumption.
  unfilled rate share of LIMIT orders that produced zero fills (Binance
                EXPIRED → wire "cancelled"). Caveat: the startup orphan-pending
                sweep also lands on status=cancelled with zero fills, so this
                bucket conflates "IOC missed the book" with "command never
                reached the exchange". Treat it as an upper bound.

Reference price
---------------
ref_close = the latest `--interval` (default 1m, mirroring instance.Manager's
hardcoded 1m BarLoader) kline close with open_time <= trade_records.now_ms_at_saas
— the same source the dispatcher used to price the cap. We deliberately do NOT
use spot_executions.actual_slippage_bps as the primary metric: for limit orders
the agent computes it against the LIMIT price (protocol §5.10), so under B2 it
reads ≈ -cap for a fill at the reference close (decision doc §4.5). It is
echoed as a secondary cross-check only.

Environment discipline (§12.1-① 使用边界)
-----------------------------------------
--environment is REQUIRED and stamped on every output row. testnet/dev runs
validate the pipeline only — testnet's thin book makes price deviation a
liquidity artifact (docs/backlog-6-price-source-divergence.md). Statistical
conclusions (cap calibration, per-order guard thresholds) must come from
mainnet samples exclusively.

Usage
-----
  python exec_deviation.py --environment testnet [--config /home/l9g/quantlab/config.yaml]
                           [--symbol BTCUSDT] [--instance inst-x]
                           [--since 2026-06-01] [--until 2026-07-01]
                           [--interval 1m] [--backtest-slippage-bps 5]
                           [--csv orders.csv]

Reads the DB DSN from QuantLab's config.yaml (same convention as
research/optuna_toy/quantlab_to_optuna.py). Read-only; no writes.
"""
import argparse
import csv
import statistics
import sys
from datetime import datetime, timezone
from pathlib import Path

import psycopg
import yaml
from psycopg.rows import dict_row


def load_db_dsn(config_path: Path) -> str:
    with config_path.open() as f:
        cfg = yaml.safe_load(f)
    db = cfg["database"]
    return (
        f"host={db['host']} port={db['port']} "
        f"user={db['user']} password={db['password']} "
        f"dbname={db['database']} sslmode={db.get('ssl_mode', 'disable')}"
    )


def parse_date_ms(s):
    """ISO date/datetime → epoch ms (UTC). None passes through."""
    if s is None:
        return None
    dt = datetime.fromisoformat(s)
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return int(dt.timestamp() * 1000)


def percentile(sorted_vals, p):
    """Linear-interpolation percentile on a pre-sorted list, p in [0,100]."""
    if not sorted_vals:
        return None
    if len(sorted_vals) == 1:
        return sorted_vals[0]
    k = (len(sorted_vals) - 1) * p / 100.0
    lo = int(k)
    hi = min(lo + 1, len(sorted_vals) - 1)
    return sorted_vals[lo] + (sorted_vals[hi] - sorted_vals[lo]) * (k - lo)


# NB on columns / decision-time anchor:
#   - GORM renders the Go field NowMsAtSaaS as column "now_ms_at_saa_s"
#     (trailing-acronym split).
#   - The recordingDispatcher deliberately leaves now_ms_at_saa_s = 0 in the
#     ledger (cmd/saas/dispatcher.go buildTradeRecord: the wire frame is the
#     source of truth for the dispatched timestamp). So decision_ms falls back
#     to created_at (the pre-dispatch insert happens in the same Tick —
#     ms-level accurate, more than enough for a 1m reference bar).
#   - Both tables are gorm.Model soft-deleted; live rows only.
ORDERS_SQL = """
WITH t AS (
    SELECT *,
           COALESCE(NULLIF(now_ms_at_saa_s, 0),
                    (EXTRACT(EPOCH FROM created_at) * 1000)::bigint
           ) AS decision_ms
    FROM trade_records
    WHERE deleted_at IS NULL
)
SELECT t.client_order_id, t.instance_id, t.symbol, t.side, t.order_type,
       t.quantity_usd, t.limit_price, t.decision_ms,
       t.valid_until_ms, t.status,
       k.close      AS ref_close,
       k.open_time  AS ref_open_time
FROM t
LEFT JOIN LATERAL (
    SELECT close, open_time
    FROM klines
    WHERE symbol = t.symbol
      AND interval = %(interval)s
      AND open_time <= t.decision_ms
    ORDER BY open_time DESC
    LIMIT 1
) k ON true
WHERE (%(symbol)s::text IS NULL OR t.symbol = %(symbol)s)
  AND (%(instance)s::text IS NULL OR t.instance_id = %(instance)s)
  AND (%(since_ms)s::bigint IS NULL OR t.decision_ms >= %(since_ms)s)
  AND (%(until_ms)s::bigint IS NULL OR t.decision_ms < %(until_ms)s)
ORDER BY t.decision_ms
"""

FILLS_SQL = """
WITH t AS (
    SELECT *,
           COALESCE(NULLIF(now_ms_at_saa_s, 0),
                    (EXTRACT(EPOCH FROM created_at) * 1000)::bigint
           ) AS decision_ms
    FROM trade_records
    WHERE deleted_at IS NULL
)
SELECT e.client_order_id, e.fill_quantity, e.fill_price,
       e.filled_at_exchange_ms, e.actual_slippage_bps, e.trade_id
FROM spot_executions e
JOIN t ON t.client_order_id = e.client_order_id
WHERE e.deleted_at IS NULL
  AND (%(symbol)s::text IS NULL OR t.symbol = %(symbol)s)
  AND (%(instance)s::text IS NULL OR t.instance_id = %(instance)s)
  AND (%(since_ms)s::bigint IS NULL OR t.decision_ms >= %(since_ms)s)
  AND (%(until_ms)s::bigint IS NULL OR t.decision_ms < %(until_ms)s)
ORDER BY e.filled_at_exchange_ms, e.id
"""


def adverse_bps(side, vwap, ref_close):
    """Side-signed deviation in bps; positive = worse than ref (a cost)."""
    raw = (vwap / ref_close - 1.0) * 1e4
    return raw if side == "buy" else -raw


def fnum(v):
    """Coerce DB numerics (float / Decimal / None) to float-or-None."""
    return None if v is None else float(v)


def build_order_rows(orders, fills_by_order, environment):
    """One analysis row per dispatched order."""
    rows = []
    for o in orders:
        fills = fills_by_order.get(o["client_order_id"], [])
        qty = sum(fnum(f["fill_quantity"]) for f in fills)
        notional = sum(fnum(f["fill_quantity"]) * fnum(f["fill_price"]) for f in fills)
        vwap = notional / qty if qty > 0 else None
        ref = fnum(o["ref_close"])
        dev = None
        if vwap is not None and ref is not None and ref > 0:
            dev = adverse_bps(o["side"], vwap, ref)
        first_fill_ms = min((f["filled_at_exchange_ms"] for f in fills), default=None)
        rows.append({
            "environment": environment,
            "client_order_id": o["client_order_id"],
            "instance_id": o["instance_id"],
            "symbol": o["symbol"],
            "side": o["side"],
            "order_type": o["order_type"],
            "status": o["status"],
            "decision_ms": o["decision_ms"],
            "n_fills": len(fills),
            "fill_qty_base": qty if qty > 0 else None,
            "fill_notional_quote": notional if qty > 0 else None,
            "vwap": vwap,
            "ref_close": ref,
            "ref_lag_ms": (o["decision_ms"] - o["ref_open_time"])
                          if o["ref_open_time"] is not None else None,
            "adverse_bps": dev,
            "limit_price": fnum(o["limit_price"]),
            "first_fill_latency_ms": (first_fill_ms - o["decision_ms"])
                                     if first_fill_ms is not None else None,
            "mean_agent_slippage_bps": (
                sum(fnum(f["actual_slippage_bps"]) for f in fills) / len(fills)
            ) if fills else None,
        })
    return rows


def dev_stats(devs):
    """Distribution summary for a list of adverse_bps values."""
    if not devs:
        return None
    s = sorted(devs)
    return {
        "n": len(s),
        "mean": statistics.fmean(s),
        "median": statistics.median(s),
        "p05": percentile(s, 5),
        "p95": percentile(s, 95),
        "min": s[0],
        "max": s[-1],
    }


def fmt_stats(st):
    return (f"n={st['n']}  mean={st['mean']:+.2f}  median={st['median']:+.2f}  "
            f"p05={st['p05']:+.2f}  p95={st['p95']:+.2f}  "
            f"min={st['min']:+.2f}  max={st['max']:+.2f}")


def main():
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--environment", required=True,
                    choices=("mainnet", "testnet", "dev"),
                    help="REQUIRED. Stamped on every output row; non-mainnet "
                         "runs are pipeline validation only.")
    ap.add_argument("--config", default="/home/l9g/quantlab/config.yaml")
    ap.add_argument("--symbol", default=None)
    ap.add_argument("--instance", default=None)
    ap.add_argument("--since", default=None, help="ISO date/datetime (UTC), inclusive")
    ap.add_argument("--until", default=None, help="ISO date/datetime (UTC), exclusive")
    ap.add_argument("--interval", default="1m",
                    help="kline interval for the reference close (manager "
                         "prices with hardcoded 1m; change only if that changes)")
    ap.add_argument("--backtest-slippage-bps", type=float, default=5.0,
                    help="the backtest fill assumption to compare against")
    ap.add_argument("--max-ref-lag-min", type=float, default=15.0,
                    help="exclude orders whose reference kline is older than "
                         "this at decision time (default 15 = the live "
                         "max_bar_staleness guard; a staler ref means the "
                         "dispatcher itself priced on stale data — useless "
                         "for cap calibration). 0 disables the filter.")
    ap.add_argument("--csv", default=None, help="write per-order rows to this path")
    args = ap.parse_args()

    params = {
        "interval": args.interval,
        "symbol": args.symbol,
        "instance": args.instance,
        "since_ms": parse_date_ms(args.since),
        "until_ms": parse_date_ms(args.until),
    }

    dsn = load_db_dsn(Path(args.config))
    with psycopg.connect(dsn) as conn:
        with conn.cursor(row_factory=dict_row) as cur:
            cur.execute(ORDERS_SQL, params)
            orders = cur.fetchall()
            cur.execute(FILLS_SQL, params)
            fills = cur.fetchall()

    fills_by_order = {}
    for f in fills:
        fills_by_order.setdefault(f["client_order_id"], []).append(f)

    rows = build_order_rows(orders, fills_by_order, args.environment)

    # ── summary ──────────────────────────────────────────────────────
    print("=" * 72)
    print(f"execution deviation report   environment={args.environment}")
    print(f"generated_at={datetime.now(timezone.utc).isoformat()}")
    if args.environment != "mainnet":
        print("⚠️  NON-MAINNET SAMPLE — pipeline validation only. Price deviation")
        print("    on testnet is a thin-liquidity artifact (backlog-6); do NOT")
        print("    calibrate caps/guards from these numbers.")
    print("=" * 72)

    filters = {k: v for k, v in
               (("symbol", args.symbol), ("instance", args.instance),
                ("since", args.since), ("until", args.until))
               if v is not None}
    print(f"filters: {filters or '(none)'}   ref interval: {args.interval}")
    print(f"orders: {len(rows)}   fills: {len(fills)}")
    if not rows:
        print("no trade_records matched — nothing to report.")
        return

    by_type_side = {}
    for r in rows:
        by_type_side.setdefault((r["order_type"], r["side"]), []).append(r)
    print("\n── order census (type/side → status histogram) ──")
    for (otype, side), grp in sorted(by_type_side.items()):
        hist = {}
        for r in grp:
            hist[r["status"]] = hist.get(r["status"], 0) + 1
        print(f"  {otype:>7}/{side:<4} n={len(grp):<4} "
              + "  ".join(f"{k}={v}" for k, v in sorted(hist.items())))

    # Deviation stats over orders that actually filled AND have a fresh
    # reference. A reference older than the staleness guard means the
    # dispatcher priced on stale data — the deviation is then a data-feed
    # artifact, not an execution cost.
    max_lag_ms = args.max_ref_lag_min * 60_000
    candidates = [r for r in rows if r["adverse_bps"] is not None]
    if max_lag_ms > 0:
        measured = [r for r in candidates
                    if r["ref_lag_ms"] is not None and r["ref_lag_ms"] <= max_lag_ms]
        ref_stale = [r for r in candidates if r not in measured]
    else:
        measured, ref_stale = candidates, []
    ref_missing = [r for r in rows if r["n_fills"] > 0 and
                   (r["ref_close"] is None or r["ref_close"] <= 0)]
    print(f"\n── adverse_bps: fill VWAP vs decision close "
          f"(+ = worse; backtest assumes {args.backtest_slippage_bps:g}) ──")
    if ref_missing:
        print(f"  ⚠️ {len(ref_missing)} filled order(s) lack a {args.interval} "
              f"reference kline — excluded (import gap?)")
    if ref_stale:
        print(f"  ⚠️ {len(ref_stale)} filled order(s) excluded: reference kline "
              f"older than {args.max_ref_lag_min:g} min at decision time "
              f"(stale data feed; see --max-ref-lag-min)")
    st = dev_stats([r["adverse_bps"] for r in measured])
    if st is None:
        print("  no filled orders with a reference close — nothing to measure.")
    else:
        print(f"  all          {fmt_stats(st)}")
        for side in ("buy", "sell"):
            sub = dev_stats([r["adverse_bps"] for r in measured if r["side"] == side])
            if sub:
                print(f"  {side:<4}         {fmt_stats(sub)}")
        worse = sum(1 for r in measured
                    if r["adverse_bps"] > args.backtest_slippage_bps)
        print(f"  orders worse than backtest assumption "
              f"({args.backtest_slippage_bps:g} bps): {worse}/{len(measured)}"
              f"  ({100.0 * worse / len(measured):.1f}%)")
        lat = sorted(r["first_fill_latency_ms"] for r in measured
                     if r["first_fill_latency_ms"] is not None)
        if lat:
            print(f"  first-fill latency ms: median={percentile(lat, 50):.0f}"
                  f"  p95={percentile(lat, 95):.0f}")
        agent_slip = [r["mean_agent_slippage_bps"] for r in measured
                      if r["mean_agent_slippage_bps"] is not None
                      and r["order_type"] == "limit"]
        if agent_slip:
            print(f"  (cross-check) agent actual_slippage_bps mean over limit "
                  f"orders: {statistics.fmean(agent_slip):+.2f} — limit-price "
                  f"referenced, reads ≈ -cap by construction (B2 §4.5); "
                  f"informational only.")

    # Unfilled-IOC upper bound (EXPIRED rides wire status=cancelled).
    limit_orders = [r for r in rows if r["order_type"] == "limit"]
    print("\n── unfilled LIMIT orders (IOC EXPIRED upper bound) ──")
    if not limit_orders:
        print("  no limit orders in sample.")
    else:
        unfilled = [r for r in limit_orders if r["n_fills"] == 0]
        cancelled_unfilled = [r for r in unfilled if r["status"] == "cancelled"]
        print(f"  limit orders: {len(limit_orders)}   zero-fill: {len(unfilled)}"
              f"   zero-fill+cancelled: {len(cancelled_unfilled)}"
              f"   rate: {100.0 * len(cancelled_unfilled) / len(limit_orders):.1f}%")
        print("  caveat: conflates IOC-missed-the-book with orphan-pending sweep")
        print("  (never dispatched); treat as an upper bound on true EXPIRED.")

    if args.csv:
        fieldnames = list(rows[0].keys())
        with open(args.csv, "w", newline="") as f:
            w = csv.DictWriter(f, fieldnames=fieldnames)
            w.writeheader()
            w.writerows(rows)
        print(f"\nwrote {len(rows)} order rows → {args.csv}")


if __name__ == "__main__":
    sys.exit(main())
