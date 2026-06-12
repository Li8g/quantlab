"""
cscv.py — CSCV/PBO(Bailey, Borwein, López de Prado, Zhu 2017
"The Probability of Backtest Overfitting")的纯函数实现。

回答的问题
----------
给定一次参数搜索的全部 N 个候选在同一段时间上的收益矩阵,"按 IS 成绩选出的
最优候选,放到 OOS 上烂掉"的概率(PBO)是多少。PBO > 50% ⇒ 选择过程大概率
在挑噪声。与 DSR 互补:DSR 折减单个 Sharpe 的多重检验幸运度,PBO 直接审计
"选择"这个动作本身。

算法(CSCV,组合对称交叉验证)
------------------------------
1. 把 T 个时间段切成 S 个等长块(S 偶数);
2. 穷举 C(S, S/2) 种"一半块当 IS、另一半当 OOS"的组合(S=16 → 12870 条路径);
3. 每条路径:在 IS 上算每个候选的 Sharpe,取 IS 最优 n*;算 n* 在 OOS 上的
   相对名次 ω = rank/(N+1),λ = logit(ω);
4. PBO = P(λ ≤ 0)(= IS 冠军在 OOS 掉到中位以下的路径占比)。

实现注记
--------
- 块内统计(sum / sum² / count)可加 ⇒ 任意块组合的 Sharpe 由块统计线性合成,
  全部组合一次 matmul 算完(12870×S @ S×N),无逐组合循环;
- 输入应是低频化收益(日级):bar 级噪声会让块 Sharpe 估计不稳,且 CSCV 假设
  段间近似独立——块长 ≫ 策略持仓记忆时该假设才近似成立;
- purge/embargo:本实现对块边界**不**做 purge(块长天级 ≫ 1h 指标记忆时边界
  渗漏 ≈ 单 bar 量级,可忽略);若用于持仓周期接近块长的策略,先加大块长。

输出(per 路径分布,不是单点):
  pbo            P(λ ≤ 0)
  lambdas        C(S,S/2) 条 λ
  sr_is_best     每条路径 IS 冠军的 IS Sharpe
  sr_oos_best    同一冠军的 OOS Sharpe
  p_oos_neg      P(IS 冠军的 OOS Sharpe < 0)
  degradation    (slope, intercept):全体候选 OOS SR ~ IS SR 的回归(挑选无关
                 的"水位下降"基线;slope ≪ 1 ⇒ IS 优势整体不可迁移)

纯 numpy,无 RNG,确定性。被 mra_ab.py --stage pbo 消费;对任何
"T×N 收益矩阵 + 一次选择事件"可复用(历史 challenger 审计、GT-Score 等)。
"""
from itertools import combinations
from math import comb

import numpy as np

EPS_VAR = 1e-30


def cscv_pbo(returns: np.ndarray, n_blocks: int = 16) -> dict:
    """returns: T×N(时间段 × 候选)收益矩阵;n_blocks: 偶数块数 S。"""
    if n_blocks % 2 != 0:
        raise ValueError("n_blocks must be even")
    T, N = returns.shape
    if T < n_blocks * 2:
        raise ValueError(f"T={T} too short for {n_blocks} blocks")

    blocks = np.array_split(np.arange(T), n_blocks)
    s1 = np.stack([returns[b].sum(axis=0) for b in blocks])          # S×N
    s2 = np.stack([(returns[b] ** 2).sum(axis=0) for b in blocks])   # S×N
    cnt = np.array([len(b) for b in blocks], dtype=np.float64)       # S

    combos = list(combinations(range(n_blocks), n_blocks // 2))
    assert len(combos) == comb(n_blocks, n_blocks // 2)
    masks = np.zeros((len(combos), n_blocks))
    for i, c in enumerate(combos):
        masks[i, list(c)] = 1.0

    def sharpe(m):  # m: n_combo×S 0/1 掩码 → n_combo×N 的块组合 Sharpe
        n = m @ cnt
        mu = (m @ s1) / n[:, None]
        var = (m @ s2) / n[:, None] - mu ** 2
        return mu / np.sqrt(np.maximum(var, EPS_VAR))

    sr_is = sharpe(masks)
    sr_oos = sharpe(1.0 - masks)

    best = np.argmax(sr_is, axis=1)                       # n_combo
    rows = np.arange(len(combos))
    sr_is_best = sr_is[rows, best]
    sr_oos_best = sr_oos[rows, best]

    # ω = IS 冠军在 OOS 上的相对名次(含自身,/(N+1) 保证 ω ∈ (0,1))
    omega = (sr_oos <= sr_oos_best[:, None]).sum(axis=1) / (N + 1)
    lambdas = np.log(omega / (1.0 - omega))

    # 全体候选的 OOS~IS 回归(选择无关的迁移性基线)
    x, y = sr_is.ravel(), sr_oos.ravel()
    slope, intercept = np.polyfit(x, y, 1)

    return {
        "pbo": float((lambdas <= 0).mean()),
        "lambdas": lambdas,
        "sr_is_best": sr_is_best,
        "sr_oos_best": sr_oos_best,
        "p_oos_neg": float((sr_oos_best < 0).mean()),
        "degradation": (float(slope), float(intercept)),
        "n_combos": len(combos),
        "n_configs": N,
    }


if __name__ == "__main__":
    # 自检:①纯噪声候选 → PBO ≈ 0.5(IS 冠军 OOS 名次均匀);②植入一个
    # 真优势候选 → PBO 应显著低于噪声基线。确定性种子。
    rng = np.random.default_rng(7)
    T, N = 2000, 200
    noise = rng.normal(0, 0.01, (T, N))
    r = cscv_pbo(noise, 16)
    print(f"pure noise   : PBO={r['pbo']:.3f} (expect ≈0.5)  "
          f"p_oos_neg={r['p_oos_neg']:.3f}")
    edged = noise.copy()
    edged[:, 0] += 0.004  # 真优势:SR_bar≈0.4
    r2 = cscv_pbo(edged, 16)
    print(f"1 true edge  : PBO={r2['pbo']:.3f} (expect ≪ noise)  "
          f"p_oos_neg={r2['p_oos_neg']:.3f}")
    assert 0.3 < r["pbo"] < 0.7, "noise PBO should hover around 0.5"
    assert r2["pbo"] < 0.1, "true-edge PBO should be near 0"
    print("self-test OK")
