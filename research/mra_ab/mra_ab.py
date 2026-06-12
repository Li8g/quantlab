"""
mra_ab.py — MRA filter bank vs sigmoid_v1 特征集 A/B (research/mra_ab/README.md).

三个阶段:
  --stage ridge   README §6 闭式基线(已跑完,结论见 README §10:强证伪未触发)
  --stage optuna  三臂(A/B/C)× 5 折 TPE 全量,吸收 README §10.3 修正:
                  ①幅度维(sig_std,σ-target 重参数化的 gain)②wedge 阈值进
                  两臂共享搜索空间 ③复用 ridge 的 σ 标定接线。实现注记 README §11。
  --stage pbo     CSCV/PBO 审计(research/cpcv_pbo/cscv.py,Bailey-Prado 2017):
                  对每个 arm×fold study,把它全部 trial 在该折 IS 段重模拟成
                  日级收益矩阵,直接量化"按 IS 选最优"这个动作的过拟合概率。
                  消费 --stage optuna 留在 PG 的 studies;结论 README §13。

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
  python mra_ab.py --stage ridge  [--config ...] [--taker-fee-bps 10]
                   [--slippage-bps 5] [--capital 10000] [--out out/]
  python mra_ab.py --stage optuna [--trials 500] [--arms A,B,C]
                   [--folds fold1,...] [--storage <sqlalchemy-url>]

quantlab 库只读;optuna 阶段的 study 存到同 PG 实例的独立库
`optuna_mra_ab`(自动建库;--storage 可覆盖,如 sqlite:///mra.db)。
ridge 闭式无 RNG;optuna 阶段 TPE seed=crc32(study 名),全新跑确定性。
"""
import argparse
import csv
import json
import math
import sys
import zlib
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import quote_plus

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


# ===================================================== optuna stage (§5+§10.3)
#
# 设计:README §2 双臂 + Arm C 嵌套对照;§10.3 修正全部吸收:
#   ① B/C 臂幅度维 = sig_std(目标 IS 信号 σ;signal = sig_std·raw/σ_IS(raw),
#      与 ridge 的 1/σ 标定同一接线,§10.3.3)。A 臂幅度由 A1/A2 天然承担。
#   ② wedge 阈值(|ΔW| 死区)为三臂共享搜索维。语义:|ΔW| ≥ wedge 且
#      |ΔUSD| ≥ $5 才成交(AND;ridge 阶段是 OR,等效无死区——正是 §10.1
#      摩擦主导的接线根源,故此处收紧,两臂对称不影响 A/B 效度)。
# 共享简化(三臂同):无 market-state beta 调制(QuietThreshold 不进搜索空间,
# 消 1 个 nuisance 维)、β γ 冻结 champion、无双池/macro(同 ridge docstring)。

FATAL_MDD = 0.70          # config.yaml fitness.fatal_mdd 现值
LAMBDA_PENALTY = 10.0     # README §5: IS alpha − λ·max(0, MDD−FatalMDD)
N_TRIALS_DEFAULT = 500    # README §5 [INVENTED v1]

# 搜索空间 bounds。A1/A2/A3/EMA_L/MAV 镜像 chromosome.go §4.1;
# sig_std/wedge 是 §10.3 新增维 [INVENTED v2,README §11]。
BOUND_A = 1.0                       # A1, A2, A3, w_k ∈ [−1, 1]
EMA_L_LO, EMA_L_HI = 50, 300
MAV_S_LO, MAV_S_HI = 5, 50
MAV_L_LO, MAV_L_HI = 30, 250
SIG_STD_LO, SIG_STD_HI = 0.02, 2.0  # log;β=0.88 ⇒ exponent σ ∈ [0.018, 1.77]
WEDGE_LO, WEDGE_HI = 0.001, 0.2     # log;0.001≈ridge 实效区, 0.2=强换手抑制
VOL_RATIO_MIN, VOL_RATIO_MAX = 0.1, 3.0  # market_state.go clip bounds


def mav_series(close: np.ndarray, window: int) -> np.ndarray:
    """quant.MAVAbsChangeWindow 的全序列向量化:out[t] = mean(|Δclose|) over
    最近 window 个变化(变化区间 [t−window, t])。t < window 处为 0(Go 同语义:
    bars 不足返回 0);warmup=385 > 250 保证模拟段内恒有效。"""
    cs = np.concatenate(([0.0], np.cumsum(np.abs(np.diff(close)))))
    out = np.zeros(len(close))
    out[window:] = (cs[window:] - cs[:-window]) / window
    return out


def vol_ratio_series(close: np.ndarray, mav_s: int, mav_l: int) -> np.ndarray:
    """clip(MAV_s/MAV_l, 0.1, 3.0);MAV_l=0 → 1.0(镜像 ComputeMarketState
    退化分支:无长窗波动 ⇒ ratio 中性)。"""
    ms, ml = mav_series(close, mav_s), mav_series(close, mav_l)
    vr = np.ones(len(close))
    nz = ml != 0
    vr[nz] = np.clip(ms[nz] / ml[nz], VOL_RATIO_MIN, VOL_RATIO_MAX)
    return vr


def shared_block_signal(close: np.ndarray, a3: float, mav_s: int, mav_l: int) -> np.ndarray:
    return a3 * (vol_ratio_series(close, mav_s, mav_l) - 1.0)


def signal_arm_a(close, p):
    """Arm A 价格块(ComputeSignal 镜像):A1·priceDeviation + A2·logReturn;
    logReturn lookback 复用 MAVShort(README §2)。"""
    e = ema(close, p["ema_l"])
    dev = np.divide(close - e, e, out=np.zeros(len(close)), where=e != 0)
    m = p["mav_s"]
    lr = np.zeros(len(close))
    lr[m:] = np.log(close[m:] / close[:-m])
    return p["a1"] * dev + p["a2"] * lr


def signal_bank(D, w, sig_std, is_lo, is_hi):
    """B/C 臂价格块:sig_std·(D·w)/σ_IS(D·w)。σ 只取 IS 段(因果);
    w 的范数被 σ 归一吸收——有效自由度 = 方向 + sig_std,与 ridge 接线一致。"""
    raw = D @ w
    sigma = float(raw[is_lo:is_hi].std())
    if sigma == 0:
        return np.zeros(len(raw))
    return (sig_std / sigma) * raw


def _sim_core(close, gap, signal, lo, hi, fee, slip, capital, beta, gamma,
              wedge, min_order):
    """逐 bar 模拟(被 njit 编译)。语义同 ridge simulate(),差异仅:
    signal 预计算成数组;wedge 是 AND 死区(见 §10.3.2 注记)。"""
    btc, usdt = 0.0, capital
    nav = np.empty(hi - lo)
    peak, mdd = -1.0, 0.0
    for i in range(lo, hi):
        px = close[i]
        equity = btc * px + usdt
        cur_w = btc * px / equity if equity > 0 else 0.0
        if not gap[i]:
            inv_bias = min(max(cur_w, 0.0), 1.0) - 0.5
            expo = beta * signal[i] + gamma * inv_bias
            if expo > 50.0:
                expo = 50.0
            elif expo < -50.0:
                expo = -50.0
            target_w = 1.0 / (1.0 + math.exp(expo))
            delta_w = target_w - cur_w
            trade_usd = delta_w * equity
            if abs(delta_w) >= wedge and abs(trade_usd) >= min_order:
                if trade_usd > 0:
                    spend = min(trade_usd, usdt)
                    if spend > 0:
                        usdt -= spend
                        btc += spend * (1.0 - fee) / (px * (1.0 + slip))
                else:
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


def bar_return_stats(nav: np.ndarray, capital: float):
    """per-bar 简单收益的 (sharpe, skew, excess_kurt)。bar-level 不年化,
    镜像 Go DSR 的口径;只用于两臂同口径比较。"""
    full = np.concatenate(([capital], nav))
    r = full[1:] / full[:-1] - 1.0
    sd = r.std()
    if sd == 0:
        return 0.0, 0.0, 0.0
    z = (r - r.mean()) / sd
    return float(r.mean() / sd), float((z ** 3).mean()), float((z ** 4).mean() - 3.0)


# --- DSR:逐行移植 internal/verification/dsr.go(Bailey & López de Prado) ---

EULER_MASCHERONI = 0.5772156649015329
MIN_TRIALS_FOR_DSR = 5


def normal_inverse(p: float) -> float:
    """Acklam 2003 有理逼近,系数与 dsr.go 完全一致。"""
    a = (-3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02,
         1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00)
    b = (-5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02,
         6.680131188771972e+01, -1.328068155288572e+01)
    c = (-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00,
         -2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00)
    d = (7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00,
         3.754408661907416e+00)
    p_low, p_high = 0.02425, 1.0 - 0.02425
    if math.isnan(p):
        return float("nan")
    if p <= 0:
        return float("-inf")
    if p >= 1:
        return float("inf")
    if p < p_low:
        q = math.sqrt(-2 * math.log(p))
        return (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) / \
               ((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
    if p <= p_high:
        q = p - 0.5
        r = q * q
        return (((((a[0]*r+a[1])*r+a[2])*r+a[3])*r+a[4])*r + a[5]) * q / \
               (((((b[0]*r+b[1])*r+b[2])*r+b[3])*r+b[4])*r + 1)
    q = math.sqrt(-2 * math.log(1 - p))
    return -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) / \
            ((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)


def compute_dsr(observed_sharpe, sharpe_variance, n_trials, horizon_t, skew, excess_kurt):
    if n_trials < MIN_TRIALS_FOR_DSR or sharpe_variance <= 0 or horizon_t < 2:
        return float("nan")
    std_sharpe = math.sqrt(sharpe_variance)
    sr0 = std_sharpe * ((1.0 - EULER_MASCHERONI) * normal_inverse(1.0 - 1.0 / n_trials)
                        + EULER_MASCHERONI * normal_inverse(1.0 - 1.0 / (n_trials * math.e)))
    radicand = 1.0 - skew * observed_sharpe + (excess_kurt / 4.0) * observed_sharpe ** 2
    if radicand <= 0:
        return float("nan")
    sigma_sr = math.sqrt(radicand / (horizon_t - 1))
    if sigma_sr <= 0:
        return float("nan")
    return 0.5 * (1.0 + math.erf((observed_sharpe - sr0) / sigma_sr / math.sqrt(2)))


# --------------------------------------------------------- optuna orchestration

def suggest_params(trial, arm: str) -> dict:
    """三臂搜索空间。共享维(三臂同名同界):a3 / mav_s / mav_l / wedge。
    mav_l < mav_s 时取 mav_s+1(镜像 Go Clamp 强制 short<long;两 bound
    仅在 [30,50] 重叠,扰动罕见)。"""
    p = {
        "a3": trial.suggest_float("a3", -BOUND_A, BOUND_A),
        "mav_s": trial.suggest_int("mav_s", MAV_S_LO, MAV_S_HI),
        "mav_l": trial.suggest_int("mav_l", MAV_L_LO, MAV_L_HI),
        "wedge": trial.suggest_float("wedge", WEDGE_LO, WEDGE_HI, log=True),
    }
    if p["mav_l"] <= p["mav_s"]:
        p["mav_l"] = p["mav_s"] + 1
    if arm == "A":
        p["a1"] = trial.suggest_float("a1", -BOUND_A, BOUND_A)
        p["a2"] = trial.suggest_float("a2", -BOUND_A, BOUND_A)
        p["ema_l"] = trial.suggest_int("ema_l", EMA_L_LO, EMA_L_HI)
    elif arm == "B":
        p["w"] = np.array([trial.suggest_float(f"w{k}", -BOUND_A, BOUND_A)
                           for k in range(N_BANDS)])
        p["sig_std"] = trial.suggest_float("sig_std", SIG_STD_LO, SIG_STD_HI, log=True)
    elif arm == "C":
        # σ 归一后 w_all 只剩符号作用(幅度由 sig_std 承担);保留连续维以贴 §2 定义
        p["w_all"] = trial.suggest_float("w_all", -BOUND_A, BOUND_A)
        p["sig_std"] = trial.suggest_float("sig_std", SIG_STD_LO, SIG_STD_HI, log=True)
    else:
        raise ValueError(arm)
    return p


def build_signal(arm, close, D, d_sum, p, is_lo, is_hi):
    sig = shared_block_signal(close, p["a3"], p["mav_s"], p["mav_l"])
    if arm == "A":
        sig = sig + signal_arm_a(close, p)
    elif arm == "B":
        sig = sig + signal_bank(D, p["w"], p["sig_std"], is_lo, is_hi)
    else:
        sig = sig + signal_bank(d_sum.reshape(-1, 1), np.array([p["w_all"]]),
                                p["sig_std"], is_lo, is_hi)
    return sig


def run_optuna(args, t, close, gap, D):
    import optuna
    from numba import njit as _njit
    optuna.logging.set_verbosity(optuna.logging.WARNING)

    sim = _njit(cache=True)(_sim_core)
    storage = args.storage or make_pg_storage(args.config)
    print(f"storage: {storage}")

    fee, slip, cap = args.taker_fee_bps, args.slippage_bps, args.capital
    d_sum = D.sum(axis=1)  # 嵌套方向:Σd_k 望远镜 = (close−EMA_192)/close
    arms = args.arms.split(",")
    fold_filter = set(args.folds.split(",")) if args.folds else None

    results, spectra, trial_sharpes = [], [], {a: [] for a in arms}
    for arm in arms:
        for name, oos_start, oos_end in FOLDS:
            if fold_filter and name not in fold_filter:
                continue
            oos_lo = int(np.searchsorted(t, date_ms(oos_start)))
            oos_hi = int(np.searchsorted(t, date_ms(oos_end)))
            is_lo, is_hi = WARMUP_BARS, oos_lo - PURGE_BARS

            dca_is = cagr(dca_nav(close, is_lo, is_hi, fee, slip, cap), cap, is_hi - is_lo)
            dca_oos = cagr(dca_nav(close, oos_lo, oos_hi, fee, slip, cap), cap, oos_hi - oos_lo)

            def objective(trial, _arm=arm, _is_lo=is_lo, _is_hi=is_hi, _dca=dca_is):
                p = suggest_params(trial, _arm)
                sig = build_signal(_arm, close, D, d_sum, p, _is_lo, _is_hi)
                nav, mdd = sim(close, gap, sig, _is_lo, _is_hi, fee / 1e4, slip / 1e4,
                               cap, CHAMPION_BETA, CHAMPION_GAMMA, p["wedge"],
                               MIN_ORDER_USD)
                alpha = cagr(nav[-1], cap, _is_hi - _is_lo) - _dca
                sharpe, _, _ = bar_return_stats(nav, cap)
                trial.set_user_attr("is_alpha", round(alpha, 6))
                trial.set_user_attr("is_mdd", round(mdd, 6))
                trial.set_user_attr("is_sharpe", round(sharpe, 8))
                return alpha - LAMBDA_PENALTY * max(0.0, mdd - FATAL_MDD)

            study_name = f"mra_ab_{arm}_{name}"
            seed = zlib.crc32(study_name.encode()) & 0x7FFFFFFF
            study = optuna.create_study(
                study_name=study_name, storage=storage, direction="maximize",
                sampler=optuna.samplers.TPESampler(seed=seed), load_if_exists=True)
            done = sum(t_.state.is_finished() for t_ in study.trials)
            if done < args.trials:
                study.optimize(objective, n_trials=args.trials - done,
                               show_progress_bar=False)

            trial_sharpes[arm].extend(
                t_.user_attrs["is_sharpe"] for t_ in study.trials
                if t_.state == optuna.trial.TrialState.COMPLETE and "is_sharpe" in t_.user_attrs)

            # best trial → OOS 评估(σ/谱形冻结自 IS,镜像 anchored holdout)
            bt = study.best_trial
            p = params_from_best(arm, bt.params)
            sig = build_signal(arm, close, D, d_sum, p, is_lo, is_hi)
            nav, mdd = sim(close, gap, sig, oos_lo, oos_hi, fee / 1e4, slip / 1e4,
                           cap, CHAMPION_BETA, CHAMPION_GAMMA, p["wedge"], MIN_ORDER_USD)
            strat_ann = cagr(nav[-1], cap, oos_hi - oos_lo)
            sharpe, skew, kurt = bar_return_stats(nav, cap)

            row = {
                "arm": arm, "fold": name, "n_trials": len(study.trials),
                "best_is_objective": round(bt.value, 4),
                "is_alpha": round(bt.user_attrs["is_alpha"], 4),
                "is_mdd": round(bt.user_attrs["is_mdd"], 4),
                "oos_strat_ann": round(strat_ann, 4), "oos_dca_ann": round(dca_oos, 4),
                "oos_alpha": round(strat_ann - dca_oos, 4), "oos_mdd": round(mdd, 4),
                "oos_sharpe": round(sharpe, 6), "oos_skew": round(skew, 4),
                "oos_kurt": round(kurt, 4), "oos_bars": oos_hi - oos_lo,
                "dsr": None,  # 跑完该臂全折后回填(pooled variance)
                "params": json.dumps(bt.params, sort_keys=True),
            }
            results.append(row)
            if arm in ("B", "C"):
                w = (p["w"] if arm == "B"
                     else np.full(N_BANDS, p["w_all"]))
                denom = np.abs(w).max()
                wn = w / denom if denom > 0 else w
                nrm = np.linalg.norm(wn)
                spectra.append({"arm": arm, "fold": name,
                                **{f"w{k}": round(float(wn[k]), 4) for k in range(N_BANDS)},
                                "sig_std": round(p["sig_std"], 4),
                                "cos_nested": round(float(wn @ NESTED / nrm), 4) if nrm > 0 else 0.0})
            print(f"{study_name}: IS obj={bt.value:+.4f} → OOS alpha={row['oos_alpha']:+.2%} "
                  f"MDD={mdd:.1%} sharpe(bar)={sharpe:+.4f}")

    # DSR 回填:每臂 pooled Var(IS sharpe) over 全部 trial,N=该臂总 trial 数
    # (README §5 NTrials 纪律:500×5 全计入,不许只报最好 fold)
    for arm in arms:
        sh = np.array(trial_sharpes[arm])
        var = float(sh.var()) if len(sh) else 0.0
        n = len(sh)
        for row in results:
            if row["arm"] == arm:
                d = compute_dsr(row["oos_sharpe"], var, n, row["oos_bars"],
                                row["oos_skew"], row["oos_kurt"])
                row["dsr"] = round(d, 6) if not math.isnan(d) else float("nan")

    args.out.mkdir(parents=True, exist_ok=True)
    with (args.out / "optuna_summary.csv").open("w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=list(results[0].keys()))
        w.writeheader()
        w.writerows(results)
    if spectra:
        with (args.out / "optuna_spectra.csv").open("w", newline="") as f:
            w = csv.DictWriter(f, fieldnames=list(spectra[0].keys()))
            w.writeheader()
            w.writerows(spectra)
    print(f"\nwrote {args.out}/optuna_summary.csv"
          + (", optuna_spectra.csv" if spectra else ""))

    verdict(results, spectra, arms)
    print(f"\ndashboard:  optuna-dashboard '{storage}' --host 127.0.0.1 --port 8089")


def params_from_best(arm: str, raw: dict) -> dict:
    p = dict(raw)
    if p["mav_l"] <= p["mav_s"]:
        p["mav_l"] = p["mav_s"] + 1
    if arm == "B":
        p["w"] = np.array([p.pop(f"w{k}") for k in range(N_BANDS)])
    return p


def verdict(results, spectra, arms):
    """预注册判决表(README §1)逐行评估。只解读,不改阈值。"""
    by = {a: sorted((r for r in results if r["arm"] == a), key=lambda r: r["fold"])
          for a in arms}
    med = {a: float(np.median([r["oos_alpha"] for r in by[a]])) for a in arms}
    print("\n=== 判决表(README §1,预注册)===")
    for a in arms:
        folds_str = ", ".join(f"{r['oos_alpha']:+.1%}" for r in by[a])
        print(f"  Arm {a}: median OOS alpha = {med[a]:+.2%}  (folds: {folds_str})")
    if "A" in med and "B" in med:
        diff = med["B"] - med["A"]
        folds = [r["fold"] for r in by["A"]]
        dsr_wins = 0
        for f in folds:
            d_a = next((r["dsr"] for r in by["A"] if r["fold"] == f), float("nan"))
            d_b = next((r["dsr"] for r in by["B"] if r["fold"] == f), float("nan"))
            if not math.isnan(d_a) and not math.isnan(d_b) and d_b >= d_a:
                dsr_wins += 1
        cos_b = [s["cos_nested"] for s in spectra if s["arm"] == "B"]
        cos_med = float(np.median(cos_b)) if cos_b else float("nan")
        print(f"  B−A median OOS alpha = {diff:+.2%} (row1 需 ≥ +1%/yr)")
        print(f"  DSR(B) ≥ DSR(A): {dsr_wins}/{len(folds)} folds (row1 需 ≥ 3/5)")
        print(f"  median |cos_nested(B)| = {cos_med:+.3f} (row3 > 0.95 ⇒ 容量无用)")
        if diff >= 0.01 and dsr_wins >= 3:
            print("  → row1: H1 成立 — 立项 mra_v1")
        elif abs(cos_med) > 0.95:
            print("  → row3: 容量无用 — 弃案")
        else:
            print("  → row4: 介于其间 — 收集为证据,等 mainnet 实盘偏差数据后复议")
    if "C" in med and "A" in med and "B" in med:
        print(f"  分解(§2):C−A = {med['C']-med['A']:+.2%}(表示形式差,预期≈0) "
              f"B−C = {med['B']-med['C']:+.2%}(容量差=真效应量)")


def make_pg_storage(config_path: Path) -> str:
    """同 PG 实例独立库 optuna_mra_ab(quantlab 库保持只读;README §5 '进现有
    PG' 与 header 只读教义的交点)。不存在则自动 CREATE DATABASE。"""
    with config_path.open() as f:
        db = yaml.safe_load(f)["database"]
    target = "optuna_mra_ab"
    with psycopg.connect(load_db_dsn(config_path), autocommit=True) as conn:
        exists = conn.execute(
            "SELECT 1 FROM pg_database WHERE datname = %s", (target,)).fetchone()
        if not exists:
            conn.execute(f'CREATE DATABASE "{target}"')
            print(f"created database {target}")
    return (f"postgresql+psycopg://{quote_plus(db['user'])}:{quote_plus(db['password'])}"
            f"@{db['host']}:{db['port']}/{target}")


# ================================================== pbo stage (CSCV/PBO 审计)

BARS_PER_DAY = 24


def daily_returns(nav: np.ndarray, capital: float) -> np.ndarray:
    """bar 级 nav → 日级(24 bar 桶)简单收益;首日含从 capital 起步的部分桶。
    CSCV 输入用日级:块 Sharpe 估计更稳,且块长(~100 天)≫ 持仓记忆,
    块间近似独立的假设才站得住(cscv.py 实现注记)。"""
    idx = np.arange(BARS_PER_DAY - 1, len(nav), BARS_PER_DAY)
    full = np.concatenate(([capital], nav[idx]))
    return full[1:] / full[:-1] - 1.0


def run_pbo(args, t, close, gap, D):
    """对每个 arm×fold 的 optuna study:全部 COMPLETE trial 在该折 IS 段
    (= 优化器当时看的那段数据)重模拟 → T_days×N 收益矩阵 → CSCV。
    审计对象是真实发生过的选择事件,矩阵正是优化器用来排名的那段收益。"""
    import optuna
    from numba import njit as _njit
    sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
    from cpcv_pbo.cscv import cscv_pbo

    optuna.logging.set_verbosity(optuna.logging.WARNING)
    sim = _njit(cache=True)(_sim_core)
    storage = args.storage or make_pg_storage(args.config)
    fee, slip, cap = args.taker_fee_bps / 1e4, args.slippage_bps / 1e4, args.capital
    d_sum = D.sum(axis=1)
    arms = args.arms.split(",")
    fold_filter = set(args.folds.split(",")) if args.folds else None

    rows = []
    for arm in arms:
        for name, oos_start, oos_end in FOLDS:
            if fold_filter and name not in fold_filter:
                continue
            oos_lo = int(np.searchsorted(t, date_ms(oos_start)))
            is_lo, is_hi = WARMUP_BARS, oos_lo - PURGE_BARS

            study_name = f"mra_ab_{arm}_{name}"
            study = optuna.load_study(study_name=study_name, storage=storage)
            trials = [tr for tr in study.trials
                      if tr.state == optuna.trial.TrialState.COMPLETE]

            M = np.empty(((is_hi - is_lo - (BARS_PER_DAY - 1) - 1) // BARS_PER_DAY + 1,
                          len(trials)), dtype=np.float64)
            for j, tr in enumerate(trials):
                p = params_from_best(arm, tr.params)
                sig = build_signal(arm, close, D, d_sum, p, is_lo, is_hi)
                nav, _ = sim(close, gap, sig, is_lo, is_hi, fee, slip, cap,
                             CHAMPION_BETA, CHAMPION_GAMMA, p["wedge"], MIN_ORDER_USD)
                M[:, j] = daily_returns(nav, cap)

            r = cscv_pbo(M, args.pbo_blocks)
            rows.append({
                "arm": arm, "fold": name, "n_configs": r["n_configs"],
                "n_days": M.shape[0], "n_blocks": args.pbo_blocks,
                "pbo": round(r["pbo"], 4),
                "p_oos_neg": round(r["p_oos_neg"], 4),
                "median_lambda": round(float(np.median(r["lambdas"])), 4),
                "median_sr_is_best": round(float(np.median(r["sr_is_best"])), 4),
                "median_sr_oos_best": round(float(np.median(r["sr_oos_best"])), 4),
                "degradation_slope": round(r["degradation"][0], 4),
            })
            print(f"{study_name}: PBO={r['pbo']:.1%}  p(OOS SR<0)={r['p_oos_neg']:.1%}  "
                  f"SR best IS→OOS median {np.median(r['sr_is_best']):+.3f}→"
                  f"{np.median(r['sr_oos_best']):+.3f}  slope={r['degradation'][0]:+.3f}")

    args.out.mkdir(parents=True, exist_ok=True)
    with (args.out / "pbo_summary.csv").open("w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=list(rows[0].keys()))
        w.writeheader()
        w.writerows(rows)
    print(f"\nwrote {args.out}/pbo_summary.csv")

    print("\n=== PBO 解读(>50% ⇒ 选择动作大概率在挑噪声)===")
    for arm in arms:
        ps = [row["pbo"] for row in rows if row["arm"] == arm]
        ns = [row["p_oos_neg"] for row in rows if row["arm"] == arm]
        if ps:
            print(f"  Arm {arm}: median PBO = {float(np.median(ps)):.1%} "
                  f"(folds: {', '.join(f'{x:.0%}' for x in ps)})  "
                  f"median p(OOS SR<0) = {float(np.median(ns)):.1%}")


# ------------------------------------------------------------------- main

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--stage", choices=["ridge", "optuna", "pbo"], required=True)
    ap.add_argument("--config", type=Path, default=Path("/home/l9g/quantlab/config.yaml"))
    ap.add_argument("--symbol", default="BTCUSDT")
    ap.add_argument("--interval", default="1h")
    ap.add_argument("--taker-fee-bps", type=float, default=10.0)
    ap.add_argument("--slippage-bps", type=float, default=5.0)
    ap.add_argument("--capital", type=float, default=10_000.0)
    ap.add_argument("--out", type=Path, default=Path(__file__).parent / "out")
    ap.add_argument("--trials", type=int, default=N_TRIALS_DEFAULT,
                    help="optuna: 每 arm×fold 预算(README §5 默认 500)")
    ap.add_argument("--arms", default="A,B,C", help="optuna: 逗号分隔臂子集")
    ap.add_argument("--folds", default="", help="optuna: 逗号分隔 fold 子集(默认全部)")
    ap.add_argument("--storage", default="",
                    help="optuna: sqlalchemy URL 覆盖(默认同 PG 实例独立库 optuna_mra_ab)")
    ap.add_argument("--pbo-blocks", type=int, default=16,
                    help="pbo: CSCV 块数 S(偶数;C(16,8)=12870 条路径)")
    args = ap.parse_args()

    t, close, gap = load_bars(load_db_dsn(args.config), args.symbol, args.interval)
    print(f"bars: {len(close)}  ({datetime.fromtimestamp(t[0]/1e3, timezone.utc):%Y-%m-%d} "
          f"→ {datetime.fromtimestamp(t[-1]/1e3, timezone.utc):%Y-%m-%d})  gaps: {int(gap.sum())}")
    D = build_features(close)

    if args.stage == "optuna":
        run_optuna(args, t, close, gap, D)
        return
    if args.stage == "pbo":
        run_pbo(args, t, close, gap, D)
        return

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
