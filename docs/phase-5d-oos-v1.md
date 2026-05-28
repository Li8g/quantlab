# Phase 5D — OOS Anchored Holdout v1

`[v1 — frozen 2026-05-28]`

这份文档冻结 Phase 5D 的决策、阈值、状态机、与代码位置。早于本文档的 `docs/phase-5d-oos-draft.md` 已被本文档取代。

> **What is OOS Anchored Holdout**：GA 在 IS（in-sample）窗口找冠军 gene 之后，留出 `oos_days` 的"未参与训练"段落让冠军走一遍，量化它在没见过的数据上能跑赢/跑输 DCA baseline 多少。Promote 决策的辅助信号。

---

## 1. Scope

### 1.1 在 v1 范围

- `verification.RunOOS` 实现：跑 OOS bars，算 alpha，产 `OOSResult`
- DecisionColor 三色（green / yellow / red）+ 一灰（gray）分类
- 90 天 OOS eval span floor
- OOS warmup 前缀（与 IS 同 warmupDays）
- OOS Fatal 与 IS ScoreTotal 隔离
- Promote / Retire 路由限 admin
- 历史 challenger 的 `oos_result.status` backfill（`--backfill-oos-blob`）

### 1.2 不在 v1 范围

见 §12.

### 1.3 相关阶段

| 阶段 | 关系 |
|---|---|
| **Phase 5B** SharpeBank + DSR | 独立路径；DSR 走 IS 段，OOS 不影响 DSR |
| **Phase 5C** plan/bars hash | 5D 沿用，`plan_hash` 因 OosWindow.Bars 变化而变 |
| **Phase 6** Tier 2 schema | `/promote` `/retire` admin-only 走 §3.2 contract |
| **Audit phase**（未来） | ReviewBacktest 不在 5D，但 OOS 是它的前置思想 |

---

## 2. 四个用户决策（2026-05-28）

阈值、公式、错误处理路径——四个跨工程/产品边界的设计选择由用户拍板，固化在本节，后续不再重新讨论。

### 2.1 决策 1 — alpha 公式 = 年化超额收益率

不用段内 ratio。详见 §5。

### 2.2 决策 2 — DCA baseline 在 OOS 段重算

不复用 IS DCA。详见 §5.4。

### 2.3 决策 3 — OOS Fatal 不影响 IS ScoreTotal

时序解耦保证。详见 §8。

### 2.4 决策 4 — `insufficient_data` 阈值 = 90 天

按 OOS **eval** 跨度。详见 §7。

---

## 3. DecisionColor 阈值 `[v1 — frozen 2026-05-28]`

### 3.1 阈值表

| Color | 触发条件 |
|---|---|
| **green** | `alpha_monthly_ann ≥ +0.05` **且** `alpha_weekly_ann ≥ 0` |
| **yellow** | 默认池：达不到 green，也未到 red |
| **red** | `alpha_monthly_ann ≤ -0.03` 或 strategy Fatal on OOS |
| **gray** | `Status != ok`（insufficient_data / failed / not_run） |

### 3.2 单位约定

alpha = `strat_ann - dca_ann`。两边都做年化：

```
x_ann  = (1 + x_total) ^ (1/years) - 1
years  = oos_eval_span_ms / (365.25 × 86400 × 1000)
```

`0.05` 是 **+5%/年**，不是段内总差额 5%。

### 3.3 阈值不是按统计显著性设

180 天 OOS 上经验估计：策略 vs DCA 的 tracking error ~8%/年。

→ `SE(alpha_ann) = TE / √years ≈ 8% / √0.49 ≈ 11%/年`

→ 任何 "2σ 显著" 门槛 ≥ ±22%/年。

这不是真实策略能达到的差距。**阈值因此走业务定义，不走显著性。**

### 3.4 不对称的理由：成本错判

| 错判 | 后果 | 成本 |
|---|---|---|
| False green | 误导 reviewer 上 Promote | **高** — 真金白银 |
| False red | reviewer 多看一眼后 re-run | **低** — 5 分钟 |

成本不对称 → 阈值不对称：

- **green 门槛抬高**（+5% 才稀缺、值得 reviewer 注意）
- **red 门槛压低**（-3% 即触发警觉，可接受一些 false alarm）

### 3.5 周线副条件

green 要求 monthly **且** weekly 都不亏，是为了挡一种典型 false positive：

> 月线超额收益靠某次极端 spike，周线整体崩盘。

只看 monthly 会让这种策略拿 green。叠 `weekly_ann ≥ 0` 做副条件，强制要求两个时间尺度都不输 DCA。

### 3.6 备选方案与影响

| 方案（本稿） | 备选 | 备选的影响 |
|---|---|---|
| 不对称 +5%/-3% | 对称 ±3% | green 失稀缺；reviewer 忽略颜色 |
| 不对称 +5%/-3% | 不对称 +8%/-5% | 几乎所有策略落 yellow；颜色信息退化 |
| 月度阈值 + 周度副条件 | 仅看月度 | 周线崩盘看不到 |
| sample-length 无关阈值 | 90d/180d/365d 自适应梯度 | YAGNI；待真数据反馈再说 |

### 3.7 实现

- **常量**：`internal/verification/oos.go`
  - `OOSGreenAlphaMonthlyAnn = 0.05`
  - `OOSRedAlphaMonthlyAnn   = -0.03`
- **分类函数**：`classifyOOSColor(alphaMonth, alphaWeek)`
- **边界回归测试**：`oos_test.go::TestClassifyOOSColor_BoundaryConditions`
  - 8 个 case 锁死 green/yellow/red 边界

---

## 4. OOS Warmup 前缀 `[v1 — frozen 2026-05-28]`

### 4.1 问题

OOS 段第一个 bar 上，策略的内部状态（EMA、MAV、各种 lookback）是冷的。

→ 第一批 bar 的信号偏离真实 indicator 值，策略在 OOS 段早期被人为压低分数。

### 4.2 解法

`data.BuildCrucibleWindows` 给 `OosWindow.Bars` 挂 `warmupDays` 前缀：

```
Bars[ 0 : WarmupLen ]    ← warmup bars，喂 indicator，不计分
Bars[ WarmupLen :    ]   ← eval bars，从这里开始计分
```

`WarmupLen` 与 IS 各窗口的 warmup 同源（默认 365 天）。

### 4.3 IS / OOS 切分对齐

`plan.go` 用 `OosWindow.StartTS`（eval-start 时间戳）切 IS 范围：

```
isBars            : bars  before OosWindow.StartTS
oos warmup prefix : bars  before OosWindow.StartTS（与 IS tail 重叠）
oos eval bars     : bars  ≥ OosWindow.StartTS
```

OOS warmup 与 IS tail 物理上是同一批 bar，但被切到两个独立 slice。**不构成 future leakage** — 策略在 OOS 评估期看不到任何 OOS eval 之后的数据。

### 4.4 不够 warmup 时的策略：严格 drop

`oosWarmupStartTS = oosStartTS - warmupDays*DayMs`

如果 `oosWarmupStartTS < bars[0].OpenTime`：

→ **不构建 OosWindow**（`oos = nil`），`RunOOS` 报 `status=insufficient_data`。

不退而求其次（partial warmup）。理由：

| 半 warmup 的代价 | 严格 drop 的代价 |
|---|---|
| status=ok 但分数被冷启动污染，reviewer 误判 | status=insufficient_data，颜色 gray，reviewer 知道为什么 |

跟 IS 窗口 "evalDays + warmupDays > isSpanMs ⇒ skip" 同语义。

### 4.5 DCA baseline 也用 eval bars only

`RunOOS` 给 DCA 喂 `Bars[WarmupLen:]`，**不**包括 warmup。

理由：策略在 warmup 期不交易，如果 DCA 在 warmup 期已经累积仓位，比较时 DCA 多吃了一段牛市，给基线不公平的 head-start。

### 4.6 实测改进

同 fixture（3000 daily bars / oos=180 / warmup=365）：

| 指标 | warmup=0（旧） | warmup=365（新） | 变化 |
|---|---|---|---|
| `strat_ann` | +2.25%/yr | +2.84%/yr | **+0.59 pp** |
| `dca_ann` | +3.57%/yr | +3.57%/yr | 不变（DCA 也用 eval bars） |
| `alpha` | -1.33%/yr | -0.73%/yr | 缩窄 0.60 pp |
| Notes 字串 | "cold-started" 警告 | `warmup_len=365 bars` | — |

→ 0.6 pp 是冷启动 EMA 偷走的真实 alpha。

### 4.7 实现

- **构建**：`internal/data/crucible.go::BuildCrucibleWindows`
- **IS 切分**：`internal/data/plan.go`（用 `oos.StartTS` 不用 `oos.Bars[0].OpenTime`）
- **消费**：`internal/verification/oos.go::RunOOS`（`evalBars := OosWindow.Bars[WarmupLen:]`）
- **集成回归测**：`internal/saas/epoch/e2e_integration_test.go::TestE2E_OOSAnchoredHoldoutWrites`
  锁字串 `warmup_len=365 bars`

---

## 5. Alpha 公式 `[v1 — frozen 2026-05-28]`

### 5.1 公式

```
strat_log     = SliceScore.Value      // 策略 EVAL 段对数收益
strat_ann     = exp(strat_log / years) - 1

dca_monthly_roi = SimulateGhostDCAMonthly(dcaCfg, evalBars, friction).ROI
dca_monthly_ann = (1 + dca_monthly_roi) ^ (1/years) - 1
                                       // weekly 同形

alpha_monthly_ann = strat_ann - dca_monthly_ann
alpha_weekly_ann  = strat_ann - dca_weekly_ann
```

### 5.2 为什么是年化（决策 1）

候选 A：**段内 ratio**（草稿里写的）

```
alpha = (strat_final_usdt - dca_final_usdt) / dca_total_injected
```

- 优点：实现简单，无需时间归一化
- 缺点：180 天 vs 365 天的 OOS 不可比；BTC 牛市段 ratio 一律偏大

候选 B（本稿）：**年化超额收益率**

- 优点：跨窗口长度可比；对齐量化行业的"年化"通用语
- 缺点：需 `years` 归一化，分母含 sample-size 信息

→ 选 B。schema 字段名（`oos_alpha_monthly` / `oos_alpha_weekly`）不变，**语义**改成年化，由本文档定义。

### 5.3 strategy 段收益来源

`sigmoid_v1.evaluateWindow` 已经只统计 EVAL 段：

- `navAtWarmupStart` 在 `i == WarmupLen` 时锁定
- `navFinal` 在最后一个 bar 后锁定
- `SliceScore.Value = log(navFinal / navAtWarmupStart)`

→ `strat_log` 天然是 EVAL 段的对数收益，不含 warmup 贡献。`RunOOS` 直接读取，无需再切片。

### 5.4 DCA baseline 在 OOS 段重算（决策 2）

候选 A：**复用 IS DCA**（`plan.DCABaselines.Monthly` / `.Weekly`）

- 优点：零额外计算
- 缺点：IS DCA 跑的是 IS bars，结尾时点 ≠ OOS 起点，比较毫无意义

候选 B（本稿）：**OOS 段重算**

```go
dcaMonthly := fitness.SimulateGhostDCAMonthly(dcaCfg, evalBars, plan.Friction)
dcaWeekly  := fitness.SimulateGhostDCAWeekly(dcaCfg, evalBars, plan.Friction)
```

`dcaCfg` 用同任务的 `GhostDCAConfig`；`friction` 用 plan 的 effective friction。

→ 选 B。计算成本可忽略；alpha 数字才有意义。

### 5.5 ROI 是 Modified Dietz / TWR

`GhostDCAResult.ROI` 已用 Modified-Dietz（少注资）或 chain-linked TWR（大额注资）算好。

`(1 + ROI) ^ (1/years) - 1` 直接得到等价年化收益率。

### 5.6 边界情形

- `1 + dca_roi ≤ 0`（DCA 总亏损至本金 ≤ 0）：`annualizeROI` 返 `-1`，避免 `pow` 复数
- `years ≤ 0`（OosWindow.Bars 跨度 ≤ 0）：返 Go 错误，调用方做 task fail（不应发生，是上游 invariant）

### 5.7 备选方案与影响

| 方案（本稿） | 备选 | 备选的影响 |
|---|---|---|
| 年化超额收益率 | 段内 ratio | 不同窗口长度不可比；牛市段 ratio 偏大 |
| OOS 段 DCA 重算 | 复用 IS DCA | 时点不对齐，alpha 无业务含义 |
| TWR/MD ROI 输入 | 简单 `final/initial - 1` | 多次注资时不准 |
| 同时算 monthly + weekly | 仅 monthly | 周线 spike 看不到 |

### 5.8 实现

- **核心函数**：`internal/verification/oos.go::RunOOS`
- **年化助手**：`annualizeROI(roi, years)`
- **DCA 输入**：复用 `internal/fitness/ghost_dca.go::{SimulateGhostDCAMonthly,Weekly}`
- **单测**：`oos_test.go::TestAnnualizeROI` + 多个 RunOOS 路径测试

---

## 6. 状态机 `[v1 — frozen 2026-05-28]`

### 6.1 四态

| Status | DecisionColor | 含义 |
|---|---|---|
| `ok` | green / yellow / red | RunOOS 跑通，alpha 计算完成 |
| `insufficient_data` | gray | OOS 段不存在或 < 90 天 |
| `failed` | red | strategy 在 OOS 段 Fatal 或 NAV 退化 |
| `not_run` | — | OOSPayload 未提供（test 路径 / pre-5D backfill） |

### 6.2 `ok` 的条件

全部满足：

- `plan.OosWindow != nil`
- `OosWindow.Bars[WarmupLen:]` 跨度 ≥ 90 天
- 策略 EVAL 段非 Fatal
- 策略 EVAL 段 NAV 非退化

→ 此时 `OOSAlphaMonthly` / `OOSAlphaWeekly` / `DecisionColor` 均非 nil。

### 6.3 `insufficient_data` 的两条触发

| 触发 | Notes 字段 |
|---|---|
| `plan.OosWindow == nil` | `"no oos window in plan (oos_days not requested, or pre-OOS history too short for full warmup)"` |
| EVAL 段跨度 < 90 天 | `"oos eval span %.1f days < 90 minimum (warmup_len=%d)"` |

→ Task 仍 `succeed`（不 reject）。详见 §7.3。

### 6.4 `failed` 的语义

仅有一条触发：策略 OOS Fatal（drawdown ≥ FatalMDD 或 NAV 退化）。

- `DecisionColor = red`
- `Notes` 写 `"strategy Fatal on OOS: <reason>"`
- alpha 字段为 nil（无法计算）

### 6.5 `not_run` 的语义

`engine.BuildChallengerPackage` 在 `bc.OOSPayload` 为空时，默认写 `Status=not_run`。

生产路径不会出现：`epoch/service.go` 总是调 `RunOOS`，`MarshalOOSPayload` 总返非空 bytes。

→ `not_run` 只在以下情形出现：

- **单测**：直接调 `BuildChallengerPackage` 不喂 `OOSPayload`
- **历史 backfill**：pre-5D 老 challenger 被 `--backfill-oos-blob` 标 not_run（见 §10.3）

---

## 7. 90 天 floor `[v1 — frozen 2026-05-28]`

### 7.1 阈值与单位

- 单位：**OOS eval 跨度天数**（不含 warmup）
- 阈值：90
- 算法：`(last_eval_bar.OpenTime - first_eval_bar.OpenTime) / DayMs`

### 7.2 不是统计驱动

90 天 OOS 上 alpha 噪声仍然大（见 §3.3）；阈值动机是**业务防呆**，不是统计显著。

| 段长 | 估计 SE(alpha_ann) | 阈值能挡的目的 |
|---|---|---|
| 90 天 | ~16%/年 | 挡掉 "30 天 OOS、瞎跑出 +50%/年" 的明显噪声 |
| 180 天 | ~11%/年 | — |
| 365 天 | ~8%/年 | — |

→ 90 是经验阈，不试图保证统计意义。低于这条线，连方向都不可信。

### 7.3 不 reject 整个 task（决策 4 的另一半）

Task 流程：

1. POST `/evolution/tasks`
2. 跑 GA，产 Champion
3. 调 `RunOOS`
4. **不论 OOS 结果，task 都 `succeed`**

如果 OOS 不够 90 天 → `oos_result.status = insufficient_data`，task 仍 succeed。

理由：

- IS 阶段已经产出有效 Champion；不该因 OOS 不充分把整 task 标失败
- Reviewer 看 `decision_color=gray` 自己判断是否 Promote

### 7.4 备选方案与影响

| 方案（本稿） | 备选 | 备选的影响 |
|---|---|---|
| 90 天 eval floor | 30 天 / 60 天 | alpha 噪声更大；方向判断都不可信 |
| 90 天 eval floor | 180 天 / 365 天 | 大量短数据 task 进 gray；Promote 流程经常缺 OOS 信号 |
| 不 reject task | OOS 不足 → task fail | 用户体验差，IS 工作被 OOS 数据问题污染 |
| 按 EVAL 跨度判 | 按 OosWindow.Bars 总跨度判 | warmup 也算入，会让 90 天阈值实际只挡 ~25 天 eval |

### 7.5 实现

- **常量**：`internal/verification/oos.go::MinOOSDays = 90`
- **检查位置**：`RunOOS` 顶部，在调 Adapter 之前
- **单测**：`oos_test.go::TestRunOOS_FloorBoundary_ExactlyMinDays` + `TestRunOOS_InsufficientData_SpanTooShort`

---

## 8. OOS Fatal 与 IS 隔离 `[v1 — frozen 2026-05-28]`

### 8.1 什么是 Fatal

策略在某段 bars 上 drawdown ≥ `plan.FatalMDD` → `SliceScore.Fatal = true`，对应窗口分数为 nil。

### 8.2 决策 3：OOS Fatal 不影响 IS ScoreTotal

IS 阶段的 `ScoreTotal` 已经在 `engine.RunEpoch` 内部算完、写包；OOS 跑出 Fatal 不**反向**修改它。

→ Promote 决策仍能看到 IS 阶段的真实表现（Sharpe / DSR），不会被 OOS 段意外 drawdown 覆盖。

→ Reviewer 通过 `oos_result.status=failed` + `decision_color=red` 看到 OOS 信号，自行权衡。

### 8.3 时序保证

`epoch/service.go::executeEpoch` 序列：

```
1. LoadKLines
2. BuildEvaluablePlan        // 含 OosWindow
3. RunEpoch                  // ← IS ScoreTotal 写包，OOS 还没跑
4. SharpeBank.Add / ComputeDSR
5. RunOOS                    // ← OOS 跑，bc.OOSPayload 填
6. BuildChallengerPackage    // 把 OOSPayload 解到 verif.OOSResult
7. ChallengerRepo.Save
```

→ 时序解耦保证隔离。Step 5 物理上不能改写 Step 3 的输出。

### 8.4 备选方案与影响

| 方案（本稿） | 备选 | 备选的影响 |
|---|---|---|
| OOS 跑在 RunEpoch 后 | OOS 跑在 RunEpoch 内 | IS ScoreTotal 受 OOS 影响；GA 早停 / 选 Champion 行为耦合到 OOS |
| OOS Fatal 仅标 red | OOS Fatal 把 challenger 整体降级 | 失去 IS 信息；reviewer 看不到分项 |
| RunOOS 错误 fail task | RunOOS 错误标 not_run | 隐藏真实代码 bug |

注意第三行的"备选 fail task"——这是当前实现：`RunOOS` 返 Go error 时 `epoch/service.go` 让任务 MarkFailed。**OOS Fatal**（`Status=failed`）和 `RunOOS` 内部 Go error 是两件事：前者是策略行为，后者是 Adapter / 策略代码 bug。

---

## 9. Admin-only Promote / Retire `[v1 — frozen 2026-05-28]`

### 9.1 schema doc 的硬约束

`docs/saas-tier2-schema-v1.md` §3.2：

> - **admin**：全权，含 Promote/Retire
> - **operator**：可创建/管理自有 Instance；可读全部 Instance；**不能 Promote**
> - **viewer**：只读

→ Promote / Retire 是 admin-only 路由。

### 9.2 双闸：AuthRequired + RequireAdmin

```go
// internal/api/handlers.go::Register
if h.AuthRequired != nil && h.RequireAdmin != nil {
    g.POST("/challengers/:id/promote", h.AuthRequired, h.RequireAdmin, h.PromoteChallenger)
    g.POST("/champions/:id/retire",    h.AuthRequired, h.RequireAdmin, h.RetireChampion)
}
```

- `AuthRequired`：解 JWT → 在 ctx 塞 claims
- `RequireAdmin`：检查 claims.Role == admin，不是 admin 返 403

两个 middleware 都必须挂。

### 9.3 nil-bypass for tests

任一为 nil → 路由 fall through 到无闸版本：

```go
} else {
    g.POST("/challengers/:id/promote", h.PromoteChallenger)
    g.POST("/champions/:id/retire",    h.RetireChampion)
}
```

保留现有 handler test 兼容（它们不挂 auth）。

### 9.4 不复用 RequireOperator 的理由

`RequireOperator` 是 `RequireRole(operator, admin)`——把 operator 放进来了。

Promote / Retire 复用它会违反 schema doc 的"operator 不能 Promote"约束。

→ 独立加 `RequireAdmin = RequireRole(admin)`，不参数化角色列表。代码冗余 2 行，语义零歧义。

### 9.5 实现

- **Handlers 字段**：`internal/api/handlers.go::Handlers.RequireAdmin gin.HandlerFunc`
- **路由注册**：同上 §9.2 引文
- **cmd/saas wiring**：`cmd/saas/main.go` `RequireAdmin: middleware.RequireRole(store.UserRoleAdmin)`
- **回归测**：`internal/api/instance_handlers_test.go::TestPromoteRetire_AdminGated`
  - viewer / operator → 403
  - admin → 200

---

## 10. 持久化与历史回填

### 10.1 落点

`OOSResult` 进 `verification.oos_result`，与 `dsr_summary` 并列。

```
ChallengerResultPackage
└── verification
    ├── oos_result      ← Phase 5D 本节
    ├── dsr_summary     ← Phase 5B
    └── review_summary  ← 未来 Audit phase
```

### 10.2 工程层路由

```
RunOOS (verification) ──┐
                        │  *OOSResult
                        ▼
MarshalOOSPayload    ──┐
                        │  json.RawMessage
                        ▼
engine.BuildContext.OOSPayload
                        │
                        ▼
BuildChallengerPackage
  → json.Unmarshal → verif.OOSResult
```

`OOSPayload` 用 `json.RawMessage` 而非 `*OOSResult`，避免 `internal/engine` 反向依赖 `internal/verification`。

### 10.3 Pre-5D 历史 backfill

`--backfill-oos-blob` flag：

- 扫所有 `oos_result.status` 为空字串的 challenger blob
- 改 `Status = not_run` + 一句 Notes 解释来历
- 跨 soft-deleted（用 `q.Unscoped()`），harness 写也走 `tx.Unscoped()`
- Idempotent：第二次跑 scanned=0

详见 `internal/migrate/oos_blob.go` + 文件头注释。

### 10.4 为什么 backfill 不 re-run RunOOS

re-run 需要原任务的 `GhostDCAConfig`（`InitialCapital` / `MonthlyInject`）。

`GAConfigSnapshot` **不**存 GhostDCAConfig（只存 friction / FatalMDD / pop_size 等）。

→ 离线 re-run 只能用今天的 server 默认值算 DCA，alpha 数字不是历史任务"当时"的数。

→ 老老实实标 `not_run` + 让想要真 alpha 的用户跑新 task with `oos_days`。

---

## 11. 实现文件清单

```
internal/verification/
  oos.go                 RunOOS, annualizeROI, classifyOOSColor, MarshalOOSPayload
  oos_test.go            14 unit tests

internal/data/
  crucible.go            BuildCrucibleWindows: warmup prefix on OosWindow
  plan.go                IS slicing by oos.StartTS (not oos.Bars[0])
  plan.go                PlanOptions.MinEvalBars guard (mirror of engine check)

internal/engine/
  package.go             BuildContext.OOSPayload field; BuildChallengerPackage
                         unmarshal → verif.OOSResult, override default not_run

internal/saas/epoch/
  service.go             executeEpoch wires RunOOS post-RunEpoch + DSR

internal/api/
  handlers.go            Handlers.RequireAdmin field + double-gate routes
  instance_handlers_test.go  TestPromoteRetire_AdminGated

cmd/saas/
  main.go                RequireAdmin wiring + --backfill-oos-blob flag

internal/migrate/
  oos_blob.go            NewOOSBlobMigration + transformOOSBlob
  oos_blob_test.go       5 unit tests
  blob.go                harness UPDATE switched to tx.Unscoped()

internal/strategies/sigmoid_v1/
  sigmoid.go             EncodeResult defaults verif to {OOSResult.Status: not_run}
                         (drive-by fix for pre-existing test failure)
```

---

## 12. 不在 v1 范围

- **Walk-forward / rolling OOS**：多段 anchored holdout。要求 GA 跑多次，对接重新设计。
- **`stress_summary`**：独立 verification 字段。Audit phase。
- **`ReviewBacktest`**：全历史回放。Audit phase。
- **Partial warmup 模式**：不够 365 天用 180/90 天 warmup。当前是 strict drop。
- **Sample-length 自适应阈值**：90d/180d/365d 用不同 DecisionColor 阈值。需真数据驱动调参。
- **OOS DSR**：DSR 只走 IS SharpeBank。OOS 段太短，sharpe 不可靠。
- **GhostDCAConfig 进 GAConfigSnapshot**：让 backfill 能 re-run。**真该做**（见 §10.4），但是 schema 改动，留 v2。

---

## 13. 变更日志

| 日期 | 版本 | 变更 |
|---|---|---|
| 2026-05-28 | v1 frozen | 初版冻结；4 决策 + 阈值 + 状态机 + 实现文件清单 |

