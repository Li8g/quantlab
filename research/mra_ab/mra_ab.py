"""
mra_ab.py — MRA filter bank vs sigmoid_v1 特征集 A/B (research/mra_ab/README.md).

当前实现:--stage ridge(README §6 闭式基线,一天级证伪通道)。
--stage optuna(三臂 5 折全量)未实现,留待基线通过后再写。

What the ridge stage decides
----------------------------
判决表第 2 行(强证伪):若凸方法在所有 fold × horizon 上都找不到正 OOS alpha
的谱形,TPE 大概率也不会无中生有 → 提前弃案,省掉全部 Optuna 算力。
若存在正 OOS alpha,ŵ 谱形(哪些 band 载荷大)留作 Optuna 结果的交叉验证。

Pipeline
--------
1. klines BTCUSDT 1h 全史(只读),gap mask = open_time 步长 ≠ 1h;
2. EMA bank(span 6..192,bar-0 种子,严格因果)→ d_0..d_5 特征矩阵,
   全序列算一次,fold 只做切片(因果性由递归滤波保证);
3. 每 fold(README §3 anchored 表,IS 末尾扣 purge+embargo=385 bars):
   ridge 闭式解 w = (XᵀX+λI)⁻¹Xᵀy,X 用 IS 统计量标准化,
   y = ln(close[t+k]/close[t]),k ∈ {24,72};λ 网格用 IS 末 20% 验证段选;
4. ŵ 反标准化(w_raw = w_std/σ_k)→ 归一(max|w|=1)→ **取负**接入模拟器
   (targetWeight = 1/(1+exp(β·signal+γ·invBias)) 对 signal 递减,
   预测收益为正应加仓 ⇒ signal 须为负)。
   **幅度标定**:max-norm 后 signal 量纲仍是 d_k 的 bps 级,直接乘 β≈0.88
   会让 sigmoid 饱和在 0.5 平坦区(实测:模拟器退化成恒定 50/50 组合)。
   故 signal 除以其 IS 段标准差(σ 仅取自 IS,因果性不破)——幅度是 1 维
   nuisance,被测对象是谱形;标准化后 exponent ~ β·N(0,1),sigmoid 工作在
   响应区(targetWeight 约 [0.3, 0.7])。Optuna 阶段 A/B 两臂的幅度由搜索维
   自己承担,无此问题;
5. OOS 段跑简化模拟器,报 alpha = strat_cagr − dca_cagr(周度 DCA 同费率)。

Simulator simplifications vs design §4 (shared across all comparisons)
----------------------------------------------------------------------
- β γ 冻结自现役 champion 7550b6 基因(index 3/4): β=0.8839, γ=0;
- 无 volRatio 项 / 无 market-state beta 调制(§6 ridge 只回归 d_0..d_5;
  vol 块属 Optuna 阶段的共享搜索维);
- wedge: |ΔUSD| ≥ $5(minMicroOrderUSD)或 |ΔW| ≥ 0.005(wedge-break)才成交,
  成交即按全量理论额(Go 侧 wedge-break 只放行粉尘单,此处简化);
- close-only 成交,buy 价 close×(1+slip),sell 价 close×(1−slip),fee 计名义;
- gap bar 冻结交易不冻结指标(EMA 按观测序列连续递推);
- OOS 段冷启动:策略与 DCA 都从 100% USDT 起步。

Usage
-----
  python mra_ab.py --stage ridge [--config /home/l9g/quantlab/config.yaml]
                   [--taker-fee-bps 10] [--slippage-bps 5]
                   [--capital 10000] [--out out/]

Read-only; no DB writes. Deterministic (closed-form, no RNG).
"""
import argparse
import csv
import math
import sys
from datetime import datetime, timezone
from pathlib import Path

import numpy as np
import psycopg
import yaml

# ---------------------------------------------------------------- constants

SPANS = [6, 12, 24, 48, 96, 192]  # README §2 [INVENTED v1]
N_BANDS = len(SPANS)
WARMUP_BARS = 385                 # max(span)+1, README §3
PURGE_BARS = 385                  # IS 末尾扣除,覆盖最长滤波记忆与 72-bar 目标
BARS_PER_YEAR = 8760.0
INTERVAL_MS = 3_600_000

# 冻结自 champion 7550b6 (gene_records full_package_json core.champion_gene,
# 索引按 chromosome.go geneDim*): beta=g[3], gamma=g[4]。README §2 共享块。
CHAMPION_BETA = 0.883863939083769
CHAMPION_GAMMA = 0.0

# step.go 镜像 (§5.5)
MIN_ORDER_USD = 5.0
WEDGE_DELTA_W = 0.005

HORIZONS = [24, 72]               # README §6
LAMBDAS = [0.1, 1.0, 10.0, 100.0]
VAL_FRACTION = 0.2                # IS 末 20% 作 λ 选择验证段

# README §3 fold 表。IS 起点统一为数据头(anchored);IS 终点 = OOS 起点,
# 实际拟合段再扣 PURGE_BARS。
FOLDS = [
    ("fold1", "2021-06-01", "2022-06-01"),
    ("fold2", "2022-06-01", "2023-06-01"),
    ("fold3", "2023-06-01", "2024-06-01"),
    ("fold4", "2024-06-01", "2025-06-01"),
    ("fold5", "2025-06-01", "2026-05-01"),  # 数据至 2026-05-01
]

# 嵌套配置方向(§3.1: priceDeviation ≈ 细 band 等权)。余弦相似度对它计算。
NESTED = np.ones(N_BANDS) / math.sqrt(N_BANDS)


# ------------------------------------------------------------------- data

def load_db_dsn(config_path: Path) -> str:
    with config_path.open() as f:
        cfg = yaml.safe_load(f)
    db = cfg["database"]
    return (
        f"host={db['host']} port={db['port']} "
        f"user={db['user']} password={db['password']} "
        f"dbname={db['database']} sslmode={db.get('ssl_mode', 'disable')}"
    )


def load_bars(dsn: str, symbol: str, interval: str):
    """Returns (open_time_ms[n], close[n], gap[n]); gap[t]=True ⇔ bar t 与
    前一 bar 不连续(其前存在缺失 bar)。"""
    with psycopg.connect(dsn) as conn, conn.cursor() as cur:
        cur.execute(
            "SELECT open_time, close FROM klines "
            "WHERE symbol = %s AND interval = %s ORDER BY open_time",
            (symbol, interval),
        )
        rows = cur.fetchall()
    if not rows:
        sys.exit(f"no klines for {symbol} {interval}")
    t = np.array([r[0] for r in rows], dtype=np.int64)
    c = np.array([float(r[1]) for r in rows], dtype=np.float64)
    gap = np.zeros(len(t), dtype=bool)
    gap[1:] = np.diff(t) != INTERVAL_MS
    return t, c, gap


def date_ms(s: str) -> int:
    return int(datetime.fromisoformat(s).replace(tzinfo=timezone.utc).timestamp() * 1000)


# --------------------------------------------------------------- features

def ema(x: np.ndarray, span: int) -> np.ndarray:
    """Bar-0 种子的递归 EMA(镜像 incrIndicatorState 的 seeded-from-bar-0)。"""
    alpha = 2.0 / (span + 1.0)
    out = np.empty_like(x)
    acc = x[0]
    out[0] = acc
    for i in range(1, len(x)):
        acc = alpha * x[i] + (1.0 - alpha) * acc
        out[i] = acc
    return out


def build_features(close: np.ndarray) -> np.ndarray:
    """D[t, k]: README §2 的 d_0..d_5。全序列一次,严格因果。"""
    emas = [ema(close, s) for s in SPANS]
    D = np.empty((len(close), N_BANDS))
    D[:, 0] = (close - emas[0]) / close
    for k in range(1, N_BANDS):
        D[:, k] = (emas[k - 1] - emas[k]) / close
    return D


# ------------------------------------------------------------------ ridge

def ridge_fit(X: np.ndarray, y: np.ndarray, lam: float) -> np.ndarray:
    """闭式解;X 已标准化,无截距(y 近零均值,趋势项本就不该进谱形)。"""
    A = X.T @ X + lam * np.eye(X.shape[1])
    return np.linalg.solve(A, X.T @ y)


def r2(y: np.ndarray, yhat: np.ndarray) -> float:
    ss = float(((y - y.mean()) ** 2).sum())
    return 1.0 - float(((y - yhat) ** 2).sum()) / ss if ss > 0 else 0.0


def fit_fold_horizon(D, close, is_lo, is_hi, horizon):
    """IS 段 [is_lo, is_hi) 上拟合;返回 (w_raw_norm, signal_sigma, lam, is_r2,
    val_r2)。w_raw_norm 是作用在【原始 d_k】上的归一化权重(max|w|=1),未取负;
    signal_sigma 是 IS 段 raw-signal 标准差,模拟器侧用 signal/σ 标定幅度。"""
    hi = is_hi - horizon  # 目标 y[t] 用到 close[t+horizon],不得越过 IS 界
    X_raw = D[is_lo:hi]
    y = np.log(close[is_lo + horizon: hi + horizon] / close[is_lo:hi])

    mu, sd = X_raw.mean(axis=0), X_raw.std(axis=0)
    sd[sd == 0] = 1.0
    X = (X_raw - mu) / sd

    split = int(len(X) * (1.0 - VAL_FRACTION))
    best = None
    for lam in LAMBDAS:
        w = ridge_fit(X[:split], y[:split], lam)
        score = r2(y[split:], X[split:] @ w)
        if best is None or score > best[1]:
            best = (lam, score)
    lam, val_r2 = best

    w_std = ridge_fit(X, y, lam)          # 选定 λ 后全 IS 重拟合
    is_r2 = r2(y, X @ w_std)
    w_raw = w_std / sd                    # 反标准化 → 作用在原始 d_k 上
    denom = np.abs(w_raw).max()
    if denom == 0:
        return np.zeros(N_BANDS), 1.0, lam, is_r2, val_r2
    w_norm = w_raw / denom
    sigma = float((X_raw @ w_norm).std())  # IS 段 signal σ(幅度标定,docstring step 4)
    return w_norm, (sigma if sigma > 0 else 1.0), lam, is_r2, val_r2


# -------------------------------------------------------------- simulator

def simulate(close, gap, D, weights, lo, hi, fee_bps, slip_bps, capital,
             signal_scale=1.0, beta=CHAMPION_BETA, gamma=CHAMPION_GAMMA):
    """[lo, hi) 上的简化回测(docstring 简化清单)。返回 (nav[], mdd)。

    weights 是已含符号约定的模拟器权重(signal = signal_scale·(weights·d);
    signal 越负 targetWeight 越高 — 调用方负责对 ridge ŵ 取负,并以
    signal_scale = 1/σ_IS 做幅度标定,见 docstring step 4)。
    """
    fee = fee_bps / 1e4
    slip = slip_bps / 1e4
    btc, usdt = 0.0, float(capital)
    nav = np.empty(hi - lo)
    peak, mdd = -1.0, 0.0

    for i in range(lo, hi):
        px = close[i]
        equity = btc * px + usdt
        cur_w = btc * px / equity if equity > 0 else 0.0

        if not gap[i]:
            signal = signal_scale * float(D[i] @ weights)
            inv_bias = min(max(cur_w, 0.0), 1.0) - 0.5
            expo = beta * signal + gamma * inv_bias
            expo = min(max(expo, -50.0), 50.0)
            target_w = 1.0 / (1.0 + math.exp(expo))
            delta_w = target_w - cur_w
            trade_usd = delta_w * equity
            if abs(trade_usd) >= MIN_ORDER_USD or abs(delta_w) >= WEDGE_DELTA_W:
                if trade_usd > 0:  # buy
                    spend = min(trade_usd, usdt)
                    if spend > 0:
                        usdt -= spend
                        btc += spend * (1.0 - fee) / (px * (1.0 + slip))
                else:              # sell
                    sell_btc = min(-trade_usd / px, btc)
                    if sell_btc > 0:
                        btc -= sell_btc
                        usdt += sell_btc * px * (1.0 - slip) * (1.0 - fee)

        equity = btc * px + usdt
        nav[i - lo] = equity
        if equity > peak:
            peak = equity
        dd = 1.0 - equity / peak if peak > 0 else 0.0
        if dd > mdd:
            mdd = dd
    return nav, mdd


def dca_nav(close, lo, hi, fee_bps, slip_bps, capital, every=168):
    """周度(168 bar)等额 DCA,同费率。镜像 RunOOS 的'DCA 在 OOS bars 上重模拟'。"""
    fee = fee_bps / 1e4
    slip = slip_bps / 1e4
    n_buys = max(1, (hi - lo + every - 1) // every)
    per = capital / n_buys
    btc, usdt = 0.0, float(capital)
    for j, i in enumerate(range(lo, hi)):
        if j % every == 0 and usdt > 0:
            spend = min(per, usdt)
            usdt -= spend
            btc += spend * (1.0 - fee) / (close[i] * (1.0 + slip))
    return btc * close[hi - 1] + usdt


def cagr(nav_end, nav_start, n_bars):
    if nav_start <= 0 or nav_end <= 0 or n_bars <= 0:
        return float("nan")
    return (nav_end / nav_start) ** (BARS_PER_YEAR / n_bars) - 1.0


# ------------------------------------------------------------------- main

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--stage", choices=["ridge", "optuna"], required=True)
    ap.add_argument("--config", type=Path, default=Path("/home/l9g/quantlab/config.yaml"))
    ap.add_argument("--symbol", default="BTCUSDT")
    ap.add_argument("--interval", default="1h")
    ap.add_argument("--taker-fee-bps", type=float, default=10.0)
    ap.add_argument("--slippage-bps", type=float, default=5.0)
    ap.add_argument("--capital", type=float, default=10_000.0)
    ap.add_argument("--out", type=Path, default=Path(__file__).parent / "out")
    args = ap.parse_args()

    if args.stage == "optuna":
        sys.exit("--stage optuna 未实现:按 README §1 判决表,ridge 基线先行。")

    t, close, gap = load_bars(load_db_dsn(args.config), args.symbol, args.interval)
    print(f"bars: {len(close)}  ({datetime.fromtimestamp(t[0]/1e3, timezone.utc):%Y-%m-%d} "
          f"→ {datetime.fromtimestamp(t[-1]/1e3, timezone.utc):%Y-%m-%d})  gaps: {int(gap.sum())}")
    D = build_features(close)

    rows, spectra = [], []
    any_positive = False
    for name, oos_start, oos_end in FOLDS:
        oos_lo = int(np.searchsorted(t, date_ms(oos_start)))
        oos_hi = int(np.searchsorted(t, date_ms(oos_end)))
        is_lo, is_hi = WARMUP_BARS, oos_lo - PURGE_BARS
        if is_hi - is_lo < 1000 or oos_hi - oos_lo < 1000:
            print(f"{name}: insufficient bars, skipped")
            continue

        dca_end = dca_nav(close, oos_lo, oos_hi, args.taker_fee_bps,
                          args.slippage_bps, args.capital)
        dca_ann = cagr(dca_end, args.capital, oos_hi - oos_lo)

        for horizon in HORIZONS:
            w_hat, sigma, lam, is_r2, val_r2 = fit_fold_horizon(D, close, is_lo, is_hi, horizon)
            sim_w = -w_hat  # 符号约定见 docstring step 4
            nav, mdd = simulate(close, gap, D, sim_w, oos_lo, oos_hi,
                                args.taker_fee_bps, args.slippage_bps, args.capital,
                                signal_scale=1.0 / sigma)
            strat_ann = cagr(nav[-1], args.capital, oos_hi - oos_lo)
            alpha = strat_ann - dca_ann
            n = np.linalg.norm(w_hat)  # w_hat 是 max-norm,余弦需单位化
            cos = float(w_hat @ NESTED / n) if n > 0 else 0.0
            any_positive |= alpha > 0

            rows.append({
                "fold": name, "horizon": horizon, "lambda": lam,
                "is_r2": round(is_r2, 6), "val_r2": round(val_r2, 6),
                "oos_strat_ann": round(strat_ann, 4), "oos_dca_ann": round(dca_ann, 4),
                "oos_alpha": round(alpha, 4), "oos_mdd": round(mdd, 4),
                "cos_nested": round(cos, 4),
            })
            spectra.append({"fold": name, "horizon": horizon,
                            **{f"w{k}": round(float(w_hat[k]), 4) for k in range(N_BANDS)}})
            print(f"{name} h={horizon:3d} λ={lam:<6g} alpha={alpha:+.2%} "
                  f"(strat {strat_ann:+.2%} vs DCA {dca_ann:+.2%}) "
                  f"MDD={mdd:.1%} cos_nested={cos:+.2f}")

    args.out.mkdir(parents=True, exist_ok=True)
    for fname, data in [("ridge_summary.csv", rows), ("ridge_spectra.csv", spectra)]:
        with (args.out / fname).open("w", newline="") as f:
            w = csv.DictWriter(f, fieldnames=list(data[0].keys()))
            w.writeheader()
            w.writerows(data)
    print(f"\nwrote {args.out}/ridge_summary.csv, ridge_spectra.csv")

    print("\n判决表 row 2(强证伪)检查:正 OOS alpha 谱形存在 ="
          f" {'YES — 未触发强证伪,可进 Optuna 阶段' if any_positive else 'NO — 强证伪,建议弃案'}")


if __name__ == "__main__":
    main()
