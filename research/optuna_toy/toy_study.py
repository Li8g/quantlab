"""
QuantLab → Optuna 命名映射玩具
================================

  Optuna 概念              QuantLab 概念
  --------------------     -----------------------------------
  Study                ←→  EvolutionTask
  Trial                ←→  Challenger
  trial.params         ←→  Gene
  trial.value          ←→  ScoreTotal
  intermediate value   ←→  SliceScore (per-window)
  TrialPruned          ←→  Fatal cascade short-circuit
  best_trial / value   ←→  Champion
  study.user_attrs     ←→  EvolutionTask 元数据 (instrument, interval, ...)
  trial.user_attrs     ←→  ResultPackage 附加字段

跑完后用 `optuna-dashboard sqlite:///quantlab_toy.db` 起 UI，
浏览器打开 http://127.0.0.1:8080 即可。
"""

import math
import random

import optuna
from optuna.trial import TrialState


# ──────────────────────────────────────────────────────────────
# Crucible: 四窗口 = 6m / 2y / 5y / 10y，权重 0.10 / 0.20 / 0.30 / 0.40
# 评估顺序固定；MDD >= FatalMDD 立即 cascade short-circuit。
# ──────────────────────────────────────────────────────────────
WINDOWS = [("6m", 0.10), ("2y", 0.20), ("5y", 0.30), ("10y", 0.40)]
FATAL_MDD = 0.40
LAMBDA_CONS = 0.3  # 一致性惩罚系数（v1-raw-std）


def simulate_window_eval(gene: dict, window_name: str, rng: random.Random):
    """模拟一个窗口的评估，返回 (raw_score, mdd)。

    完全是凭空 toy 公式，唯一目的：让不同 gene → 不同 score / mdd 分布，
    让 dashboard 的散点 / 平行坐标图有东西看。
    """
    base = (
        0.5 * math.tanh(gene["lookback_window"] / 100)
        - 0.6 * abs(gene["threshold"] - 0.5)
        + 0.3 * (1 - gene["stop_loss_pct"] * 5)
        + 0.4 * (1 - abs(gene["position_size_pct"] - 0.5) * 2)
        + rng.gauss(0, 0.15)
    )
    # 长窗口 MDD 天然更高
    mdd_floor = {"6m": 0.05, "2y": 0.12, "5y": 0.20, "10y": 0.25}[window_name]
    mdd = mdd_floor + 0.4 * gene["position_size_pct"] + rng.uniform(-0.05, 0.20)
    return base, max(0.0, mdd)


def objective(trial: optuna.Trial) -> float:
    # ── Gene 采样（命名前缀 gene__ 让 dashboard 里参数排在一起）──
    gene = {
        "lookback_window":   trial.suggest_int("gene__lookback_window", 5, 200),
        "threshold":         trial.suggest_float("gene__threshold", 0.0, 1.0),
        "stop_loss_pct":     trial.suggest_float("gene__stop_loss_pct", 0.01, 0.20),
        "position_size_pct": trial.suggest_float("gene__position_size_pct", 0.10, 1.00),
    }

    # 每个 trial 用确定性 seed，模拟 reproducibility（QuantLab 也走 deterministic）
    rng = random.Random(trial.number * 7919)

    # ── 四窗口 cascade ──
    score_total = 0.0
    window_scores = []
    for step, (window_name, weight) in enumerate(WINDOWS):
        raw_score, mdd = simulate_window_eval(gene, window_name, rng)

        # report → dashboard "Intermediate Values" 曲线（横轴 step = 窗口序号）
        trial.report(raw_score, step=step)

        trial.set_user_attr(f"window__{window_name}__score", raw_score)
        trial.set_user_attr(f"window__{window_name}__mdd", mdd)
        trial.set_user_attr(f"window__{window_name}__weight", weight)

        # Fatal cascade short-circuit
        if mdd >= FATAL_MDD:
            trial.set_user_attr("fatal_window", window_name)
            trial.set_user_attr("fatal_mdd", round(mdd, 4))
            raise optuna.TrialPruned(
                f"Fatal at {window_name}: MDD={mdd:.3f} >= {FATAL_MDD}"
            )

        window_scores.append(raw_score)
        score_total += weight * raw_score

    # 一致性惩罚（v1-raw-std）：减去 λ * std(window_scores)
    mean = sum(window_scores) / len(window_scores)
    std = (sum((s - mean) ** 2 for s in window_scores) / len(window_scores)) ** 0.5
    score_total -= LAMBDA_CONS * std

    trial.set_user_attr("score_total", round(score_total, 4))
    trial.set_user_attr("consistency_penalty", round(LAMBDA_CONS * std, 4))
    trial.set_user_attr("schema_version", "v5.3.3")
    trial.set_user_attr("fitness_version", "v1-raw-std")
    return score_total


def main() -> None:
    storage = "sqlite:///quantlab_toy.db"
    study = optuna.create_study(
        study_name="evolution_task__demo_btcusdt_1h",
        direction="maximize",
        storage=storage,
        load_if_exists=True,
        sampler=optuna.samplers.TPESampler(seed=42),
    )
    study.set_user_attr("instrument", "BTCUSDT")
    study.set_user_attr("interval", "1h")
    study.set_user_attr("schema_version", "v5.3.3")
    study.set_user_attr("fitness_version", "v1-raw-std")
    study.set_user_attr("fingerprint_version", "fp-v1")

    study.optimize(objective, n_trials=200)

    n_complete = sum(1 for t in study.trials if t.state == TrialState.COMPLETE)
    n_fatal    = sum(1 for t in study.trials if t.state == TrialState.PRUNED)
    print()
    print(f"Total challengers : {len(study.trials)}")
    print(f"  COMPLETE        : {n_complete}")
    print(f"  FATAL (pruned)  : {n_fatal}")
    print()
    print(f"Champion ScoreTotal : {study.best_value:.4f}")
    print(f"Champion Gene       : {study.best_params}")
    print()
    print("启动 dashboard:")
    print(f"  optuna-dashboard {storage}")
    print("然后浏览器打开 http://127.0.0.1:8080")


if __name__ == "__main__":
    main()
