# QuantLab → Optuna (Phase 1 Frontend)

**`quantlab_to_optuna.py`** 是 Phase 1 的桥：读 QuantLab Postgres → 写 Optuna sqlite。
on-demand 同步（每次跑都 wipe-rebuild，无双源问题）。

```bash
.venv/bin/python quantlab_to_optuna.py             # → quantlab_phase1.db (默认含 soft-deleted)
.venv/bin/python quantlab_to_optuna.py --live-only # 只看 gorm.Model 未删的 row
.venv/bin/optuna-dashboard sqlite:///quantlab_phase1.db --host 0.0.0.0 --port 8088
# → http://<VM-IP>:8088
```

**Mapping**:

| Optuna                     | QuantLab                                                              |
|----------------------------|-----------------------------------------------------------------------|
| Study                      | `(strategy_id, pair, interval)` 组合（聚合所有 task 的 winner）        |
| Trial                      | 1 个 GeneRecord（一个 task 的 final winner）                          |
| trial.params (gene__*)     | `full_package_json.core.champion_gene.payload`（按 strategy registry 解码） |
| trial.value                | `score_total`                                                          |
| intermediate_values        | window_scores 4 个（6m=step0, 2y=1, 5y=2, 10y=3）                     |
| user_attrs                 | challenger_id / task_id / decision_status / dsr / friction / soft_deleted_at 等 |

**新加 strategy 怎么办**：在 `STRATEGY_REGISTRY` 加一行 `(name, FloatDistribution/IntDistribution)` 列表，
顺序对齐该策略的 chromosome.go gene index 常量。未知 strategy 的 trial 会被 skip 并 stderr 警告。

**Phase 1 不覆盖**：live monitoring（看 task 跑到第几代）、promote/retire 操作。
Promote 还走 QuantLab REST API（`POST /api/v1/challengers/:id/promote`）。

---

## Optuna Toy（原始 spike，已被 quantlab_to_optuna.py 取代）

把 QuantLab 的 EvolutionTask / Challenger / Gene / ScoreTotal 概念映射到 Optuna 上跑一个 toy study，
然后用 optuna-dashboard 点一遍 UI，记录"我要 / 我不要"作为 frontend wireframe 的输入。

## 概念映射

| Optuna               | QuantLab                       |
|----------------------|--------------------------------|
| Study                | EvolutionTask                  |
| Trial                | Challenger                     |
| trial.params         | Gene                           |
| trial.value          | ScoreTotal                     |
| intermediate value   | SliceScore (per-window)        |
| TrialPruned          | Fatal cascade short-circuit    |
| best_trial           | Champion                       |
| study.user_attrs     | EvolutionTask 元数据           |
| trial.user_attrs     | ResultPackage 附加字段         |

## 用法

```bash
cd research/optuna_toy
./.venv/bin/python toy_study.py            # 生成/追加 200 trials 到 quantlab_toy.db
./.venv/bin/optuna-dashboard sqlite:///quantlab_toy.db --host 0.0.0.0 --port 8088
# 浏览器打开 http://127.0.0.1:8088（本机）或 http://<VM-IP>:8088（VMware host）
# 注意：避开 8080，那是 QuantLab server.http_listen 的默认端口
```

重复跑 `toy_study.py` 会往同一个 study 里继续追加 trials（`load_if_exists=True`）。
想从头开始就删掉 `quantlab_toy.db`。

## 点 UI 时要回答的问题

每个视图都问自己：**"如果把 trial 换成 challenger、param 换成 gene、value 换成 ScoreTotal，这个视图我要不要？"**

建议至少看完这几个 tab，每个记 1-2 条决定：

- **History** — challenger 一览，最高 ScoreTotal 趋势
- **Hyperparameter Importance** — 哪个 gene 字段对 ScoreTotal 影响最大
- **Parallel Coordinate** — gene 多维相关性
- **Slice** — 单个 gene 字段对 ScoreTotal 的影响
- **Contour** — 两个 gene 字段的交互
- **Trials (table)** — challenger 表格 + filter（要不要这个 layout？）
- **Trial Detail → Intermediate Values** — 四窗口 cascade 曲线
- **Trial Detail → User Attributes** — 我们的 window scores / mdd / fatal 信息

把所有"我要 / 我不要"写在 `wireframe_notes.md` 里，再开始画自己的 wireframe。

## 注意

这个 toy 的 fitness 公式是凭空捏的，**不代表真实策略**，唯一目的是让 dashboard 有东西可看。
不要从这里的"重要参数排名"读出任何关于真实 gene 的结论。
