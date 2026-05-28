# Phase 5D — OOS Anchored Holdout 实现草稿

状态：**草稿，未冻结**。阈值需要业务/统计上拍板，工程量按当前接口估。

## 1. 现状盘点

- `data/plan.go:55` 已在 `BuildEvaluablePlan` 内切出 `OosWindow`（按 request `oos_days` 决定段长）
- `domain.EvaluablePlan.OosWindow` 字段存在
- `resultpkg.OOSResult` struct 完整：`Status` / `OOSAlphaMonthly` / `OOSAlphaWeekly` / `DecisionColor` / `Notes`
- **缺**：消费 `plan.OosWindow` 跑回测的代码 + 写 `OOSResult` 进 package 的链路
- engine 默认显式写 `not_run`（已修，commit 待出）

## 2. 接口设计（草稿）

新建 `internal/verification/oos.go`：

```go
// RunOOS replays bestGene over plan.OosWindow and compares the final
// equity against a DCA baseline simulated on the same OOS bars.
// Returns OOSResult with Status=ok on success, insufficient_data when
// OosWindow is nil / shorter than MinOosDays, failed on adapter error
// or strategy Fatal.
//
// Determinism: deterministic given (strat, plan, bestGene). Caller is
// expected to construct a fresh Adapter via strat.NewAdapter(oosPlan).
func RunOOS(
    strat strategy.EvolvableStrategy,
    plan *domain.EvaluablePlan,    // full IS plan; OosWindow lifted from here
    bestGene domain.Gene,
    dca fitness.GhostDCAConfig,
) (*resultpkg.OOSResult, error)
```

实现要点：

1. **构造 OOS-only plan**：clone `plan`，把 `Windows` 替换为 `[]CrucibleWindow{*plan.OosWindow}`，`OosWindow` 置 nil（避免递归）。其他字段（Friction/InitialUSDT/DCA）原样。
2. **跑回测**：`adapter := strat.NewAdapter(oosPlan); adapter.Reset(oosPlan); raw := adapter.Evaluate(bestGene)`。
3. **基线**：用 `fitness.SimulateGhostDCAMonthly/Weekly` 在 OOS bars 上各跑一遍，拿到 baseline `FinalEquity`。
4. **alpha 计算**：
   ```
   oos_alpha_monthly = (strategy_final_usdt - dca_monthly_final_usdt) / dca_monthly_total_injected
   oos_alpha_weekly  = (strategy_final_usdt - dca_weekly_final_usdt)  / dca_weekly_total_injected
   ```
   分母用 injected 而非 final，避免负 equity 出 weird 比例。**TODO 拍板：用 ratio 还是 monthly-equivalent 年化？**
5. **决策颜色**：见 §3 阈值草稿。
6. **错误处理**：
   - `OosWindow == nil` → `Status=insufficient_data`，Notes 写原因。
   - OOS bars < MinOosDays（默认 30？） → 同上。
   - `raw.Windows[0].Score.Fatal == true` → `Status=failed`，DecisionColor=red，Notes 带 fatal reason。
   - Adapter / Strategy 自身报错 → 返 Go err 让上游决定。

## 3. DecisionColor 阈值（草稿，强 TODO）

> 这一节是我拍脑袋的，**用户和量化端必须 review**。所有数字加 `[TBD]` 标记。

| Color  | 触发条件 | 语义 |
|--------|----------|------|
| green  | `oos_alpha_monthly >= +0.05 [TBD]` **且** `oos_alpha_weekly >= 0 [TBD]` | OOS 跑赢 DCA 5%+ 且周线没崩 |
| yellow | 其余且 `oos_alpha_monthly >= -0.05 [TBD]` | 大致跟 DCA，没明显跑赢也没崩 |
| red    | `oos_alpha_monthly <= -0.05 [TBD]` 或 `Fatal` | OOS 跑输 DCA 5%+，或 OOS 段 MDD 触发 fatal |
| gray   | `Status != ok` | 数据不够 / 实现错误 |

阈值悬而未决的几个问题：
- 5% 是不是太松？8.8y BTC 牛市段，DCA 自己就有 +N00% 收益，5% 差额对真实统计差异敏感度低。
- 是否要按 OOS 段长度做归一化？180 天的 5% vs 365 天的 5% 不一样。考虑改成"年化超额收益率"。
- weekly 维度是否独立加阈值，还是只看 monthly？monthly 已经是主要业务对标。
- Sharpe / IS-OOS-Sharpe-差 应不应该进入颜色决策？现在没考虑——OOS 只能样本太小算 sharpe 不靠谱。

**建议**：先用上表 ship，在 PromoteLayer Note 里强制要求 reviewer 复核阈值合理性，让真实数据回流再收紧。

## 4. 工程量分解

| 任务 | 工作量 | 备注 |
|------|--------|------|
| `internal/verification/oos.go` + 单测 | 0.5d | 含 mock adapter 单测 + 边界 case |
| `internal/engine/package.go` 加 `BuildContext.OOSPayload`，覆盖默认 not_run | 0.1d | 镜像现有 `DSRSummary` 模式 |
| `internal/saas/epoch/service.go` 在 RunEpoch 之后、BuildChallengerPackage 之前 wire RunOOS | 0.2d | 注意 `OosDays==nil` 跳过；不影响 DSR 路径 |
| 集成测：req with oos_days → package.verification.oos_result.status=ok | 0.3d | 真 sigmoid_v1 strategy + 小批 bars |
| 阈值评审 + 调参 | **不可估** | 需要用户/量化端拍板 |
| 文档：docs/进化计算引擎.md 章节补 + 这份草稿冻结 | 0.2d | |

合计代码 ~**1.3 工作日**（不含阈值评审）。

## 5. Open Questions（开 5D 前必须答）

1. alpha 公式：ratio (`(s-b)/injected`) vs 年化超额收益率？后者更可比但实现更复杂（要做时间归一化）。
2. DecisionColor 阈值：数字 + 是否按 OOS 段长归一化？
3. OOS Fatal 是否影响 IS 阶段的 ScoreTotal？提议：**不影响**。OOS 是 post-GA 校验，IS score 已经定了；OOS Fatal 只标 red 给 reviewer，让 promote 决策自己挡。
4. DCA baseline 用 IS 段已算的还是 OOS 段重算？提议：**OOS 段重算**。IS 的 DCA 用 IS bars 起点收尾，跟 OOS 时段不对齐没意义。
5. 当 `oos_days != nil` 但请求的窗口太短（warmup + crucible 后剩不够 30 天）：是 reject 整个 task，还是接受 task 但 OOS 标 `insufficient_data`？提议：**后者**——别为 OOS 失败拒整个 GA。

## 6. 不在 5D 范围

- Walk-forward / rolling OOS（多个 anchored 段）
- Stress 测试（`stress_summary` 字段）—— 独立 phase
- ReviewBacktest（`review_summary`）—— 独立 phase

## 7. 上线门槛

- 单测 100% 通过
- 跑一次 `oos_days=180` 的真数据 task，验证 `verification.oos_result.status="ok"` + alpha 字段非 nil
- 用户/量化端签字接受阈值表
