"""
optuna_ga_meta.py — 接入点 A:Optuna TPE 元优化 GA 超参数(README 本目录)。

每个 trial = 一次完整 evolution task(POST → 轮询 → 取分)。设计要点
(全部预注册于 README,此处只标行为):
  - 搜索空间:pop_size(int log [16,256])× elite_ratio([0.02,0.30]);
    max_generations 由等额评估预算 B 派生(README §4 公式),非独立维;
  - fatal_mdd / 费率 / spawn_mode 锁定,不搜(README §2-2:语义旋钮会给出
    退化梯度);test_mode=false(真费率);
  - 目标 = best challenger 的 score_total;Fatal best(score_total=null)
    给固定差分 FATAL_SCORE(合法结果,该进代理模型);task failed 则 raise
    (基础设施问题,trial=FAIL,不进代理模型);
  - 任务单飞:409/ErrTaskInProgress → 等待重试;
  - seed 不可控(服务端 UnixNano)⇒ 目标有噪声,解读纪律见 README §6。

用法:
  ../optuna_toy/.venv/bin/python optuna_ga_meta.py [--trials 50]
      [--server http://127.0.0.1:8090] [--budget-evals 3000]
      [--storage <url>] [--out out/]

默认 server = :8090(meta 专用实例)。不要指到 :8080 生产 lab 实例
(SharpeBank 污染,README §3)。
"""
import argparse
import csv
import json
import time
import urllib.error
import urllib.request
from pathlib import Path
from urllib.parse import quote_plus

import psycopg
import yaml

FATAL_SCORE = -5.0          # Fatal best 的固定差分 [INVENTED v1]
POLL_SEC = 3.0
BUSY_RETRY_SEC = 5.0
TASK_TIMEOUT_SEC = 1800.0   # 单 trial 上限;超时视为基础设施失败


def http_json(method: str, url: str, body: dict | None = None, timeout=30):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method,
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read() or b"{}")


def derive_generations(budget: int, pop: int, ratio: float) -> int:
    """README §4:evals = pop + (gens−1)×(pop−nElite),nElite=max(1,int(pop·ratio))。"""
    n_elite = max(1, int(pop * ratio))
    if pop >= budget:
        return 1
    gens = round(1 + (budget - pop) / (pop - n_elite)) if pop > n_elite else 1
    return max(1, gens)


def run_task(server: str, req_body: dict) -> str:
    """POST 任务;单飞锁忙时(409/conflict)等待重试。返回 task_id。"""
    url = f"{server}/api/v1/evolution/tasks"
    while True:
        try:
            return http_json("POST", url, req_body)["task_id"]
        except urllib.error.HTTPError as e:
            if e.code == 409:
                time.sleep(BUSY_RETRY_SEC)
                continue
            raise RuntimeError(f"create task: HTTP {e.code}: {e.read().decode()[:200]}")


def wait_task(server: str, task_id: str) -> dict:
    deadline = time.monotonic() + TASK_TIMEOUT_SEC
    while True:
        st = http_json("GET", f"{server}/api/v1/evolution/tasks/{task_id}")
        if st["status"] in ("succeeded", "failed", "cancelled"):
            return st
        if time.monotonic() > deadline:
            raise RuntimeError(f"task {task_id} timeout after {TASK_TIMEOUT_SEC}s")
        time.sleep(POLL_SEC)


def make_pg_storage(config_path: Path) -> str:
    """同 PG 实例独立库 optuna_ga_meta(与 mra_ab 的 storage 同模式)。"""
    with config_path.open() as f:
        db = yaml.safe_load(f)["database"]
    target = "optuna_ga_meta"
    dsn = (f"host={db['host']} port={db['port']} user={db['user']} "
           f"password={db['password']} dbname={db['database']} "
           f"sslmode={db.get('ssl_mode', 'disable')}")
    with psycopg.connect(dsn, autocommit=True) as conn:
        if not conn.execute("SELECT 1 FROM pg_database WHERE datname=%s",
                            (target,)).fetchone():
            conn.execute(f'CREATE DATABASE "{target}"')
            print(f"created database {target}")
    return (f"postgresql+psycopg://{quote_plus(db['user'])}:{quote_plus(db['password'])}"
            f"@{db['host']}:{db['port']}/{target}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--server", default="http://127.0.0.1:8090")
    ap.add_argument("--config", type=Path,
                    default=Path(__file__).parent / "config.metaopt.yaml",
                    help="只用于推导 optuna storage 的 PG 凭据(默认 meta 实例 :5433)")
    ap.add_argument("--trials", type=int, default=50)
    ap.add_argument("--budget-evals", type=int, default=3000)
    ap.add_argument("--strategy", default="sigmoid_v1")
    ap.add_argument("--pair", default="BTCUSDT")
    ap.add_argument("--interval", default="1h")
    ap.add_argument("--taker-fee-bps", type=float, default=10.0)
    ap.add_argument("--slippage-bps", type=float, default=5.0)
    ap.add_argument("--fatal-mdd", type=float, default=0.70)
    ap.add_argument("--storage", default="")
    ap.add_argument("--study", default="ga_meta_btcusdt_1h_b3000")
    ap.add_argument("--out", type=Path, default=Path(__file__).parent / "out")
    args = ap.parse_args()

    import optuna
    optuna.logging.set_verbosity(optuna.logging.WARNING)

    # 防呆:server 必须活着,且(尽力)确认不是生产实例
    try:
        http_json("GET", f"{args.server}/api/v1/evolution/tasks?limit=1", timeout=5)
    except Exception as e:
        raise SystemExit(f"server {args.server} 不可达(先按 README §5 起 meta 实例): {e}")
    if ":8080" in args.server:
        raise SystemExit("拒绝指向 :8080(生产 lab 实例)— SharpeBank 污染,README §3")

    storage = args.storage or make_pg_storage(args.config)
    print(f"storage: {storage}\nserver:  {args.server}\n"
          f"budget:  {args.budget_evals} evals × {args.trials} trials")

    rows = []

    def objective(trial: "optuna.Trial") -> float:
        pop = trial.suggest_int("pop_size", 16, 256, log=True)
        ratio = trial.suggest_float("elite_ratio", 0.02, 0.30)
        gens = derive_generations(args.budget_evals, pop, ratio)
        trial.set_user_attr("max_generations", gens)

        body = {
            "strategy_id": args.strategy, "pair": args.pair,
            "interval": args.interval,
            "pop_size": pop, "max_generations": gens, "elite_ratio": ratio,
            "fatal_mdd": args.fatal_mdd,
            "taker_fee_bps": args.taker_fee_bps,
            "slippage_bps": args.slippage_bps,
            "spawn_mode": "random_once", "test_mode": False,
        }
        t0 = time.monotonic()
        task_id = run_task(args.server, body)
        st = wait_task(args.server, task_id)
        dur = time.monotonic() - t0

        if st["status"] != "succeeded":
            raise RuntimeError(f"task {task_id} {st['status']}: "
                               f"{st.get('failure_reason')}")  # trial=FAIL,不进代理模型

        ch_id = st["challenger_id"]
        summary = http_json("GET", f"{args.server}/api/v1/challengers/{ch_id}")
        score = summary.get("score_total")
        fatal = score is None

        evals = gens_actual = None
        try:
            pkg = http_json("GET", f"{args.server}/api/v1/challengers/{ch_id}/package")
            ss = (pkg.get("diagnostics") or {}).get("search_stats") or {}
            evals, gens_actual = ss.get("evaluations_total"), ss.get("generations")
        except Exception:
            pass  # search_stats 是诊断件,取不到不挡 trial

        for k, v in [("task_id", task_id), ("challenger_id", ch_id),
                     ("fatal", fatal), ("evaluations_total", evals),
                     ("generations_actual", gens_actual),
                     ("duration_s", round(dur, 1))]:
            trial.set_user_attr(k, v)
        rows.append({"trial": trial.number, "pop_size": pop,
                     "elite_ratio": round(ratio, 4), "max_generations": gens,
                     "generations_actual": gens_actual,
                     "evaluations_total": evals,
                     "score_total": score, "fatal": fatal,
                     "duration_s": round(dur, 1), "task_id": task_id})
        val = FATAL_SCORE if fatal else float(score)
        print(f"trial {trial.number:3d}: pop={pop:3d} ratio={ratio:.3f} "
              f"gens={gens:3d} → score={val:+.4f}"
              f"{' (FATAL)' if fatal else ''}  evals={evals}  {dur:.0f}s")
        return val

    study = optuna.create_study(
        study_name=args.study, storage=storage, direction="maximize",
        sampler=optuna.samplers.TPESampler(seed=42), load_if_exists=True)
    done = sum(t.state.is_finished() for t in study.trials)
    if done < args.trials:
        study.optimize(objective, n_trials=args.trials - done)

    args.out.mkdir(parents=True, exist_ok=True)
    if rows:
        with (args.out / "meta_summary.csv").open("a", newline="") as f:
            w = csv.DictWriter(f, fieldnames=list(rows[0].keys()))
            if f.tell() == 0:
                w.writeheader()
            w.writerows(rows)
        print(f"\nappended {len(rows)} rows → {args.out}/meta_summary.csv")

    bt = study.best_trial
    print(f"\nbest so far: score={bt.value:+.4f} params={bt.params} "
          f"(注意噪声纪律:README §6)")
    print(f"dashboard:  optuna-dashboard '{storage}' --host 127.0.0.1 --port 8089")


if __name__ == "__main__":
    main()
