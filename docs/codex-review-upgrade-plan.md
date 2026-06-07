# 搭档审阅报告总结 + 升级计划

**来源文档**（均由搭档撰写，2026-06-05~07）：
- `docs/codex-readonly-review-2026-06-05.md` — Codex 只读代码审阅
- `docs/learn/comments-on-architecture-v1.md` — 架构改进手册
- `docs/learn/LEARN-open-source-architecture-refactor.md` — 开源项目架构参考

**核心判断**（三份文档一致）：QuantLab 不需要架构重置，需要的是**不变量固化重构**（invariant-hardening refactor）。包提取在不变量被测试钉牢之前不应开始。

---

## 已完成（搭档 Item 3，已提交代码）

| 问题 | 修复 | 集成测试 |
|---|---|---|
| Retire 非 CAS 保护 | UPDATE 谓词加 `retired_at IS NULL`；`RowsAffected=0` → `ErrAlreadyRetired` | `TestChampionRepo_RetireCASRejectsStaleHistory` |
| Instance 状态非 CAS 保护 | `UpdateStatus` 带 expected_status 谓词 | `TestInstanceRepo_UpdateStatusCASRejectsStaleState` |
| DeployChampion 无校验 | 联合检查 `(strategy_id, pair, retired_at IS NULL)` + retired 实例写保护 | `TestInstanceRepo_SetActiveChampionRequiresMatchingActiveChampionAndLiveInstance` |

---

## 第1章 — Critical：Agent 幂等性三处 fail-open

**状态：待修复** | 来源：S5B-1 / S5B-2 / S5B-3

根因：`handleTradeCommand` 和 `onOrderEvent` 均将 `idempotency.Get` 的错误丢弃（`_, ok, _ := c.idempotency.Get(...)`）。

| 编号 | 文件 | 缺陷 | 后果 |
|---|---|---|---|
| S5B-1 | `internal/agent/tradecommand.go` | frozen/expired 检查先于幂等性查询 | 已成交 replay 收到 `expired`/`rejected`，可覆盖 TradeRecord |
| S5B-2 | `internal/agent/tradecommand.go` | Get 错误当"未找到"，继续 Put+Submit | 已成交订单被重复提交交易所 |
| S5B-3 | `internal/agent/client.go` `onOrderEvent` | Get 错误当"未知订单"，提前 return | 真实成交漏入 delta_report，OrderUpdate 漏发 |

**修复顺序**（架构手册 Lesson 3 给出的正确 Agent 处理顺序）：
1. 解码 `TradeCommand`
2. 读幂等性记录（idempotency.Get）
3. 若 read error → 返回 internal error，不继续 Submit
4. 若找到记录 → 返回 `duplicate_pending` / `duplicate_terminal`，**无论** frozen/expired 状态
5. 若未找到 → 才进行 frozen/expiry/新命令校验
6. Pre-record pending
7. Submit 到交易所
8. 持久化 accepted/filled 生命周期

**必须补的测试**：
- 已成交 order replay after expiry → 返回 `duplicate_terminal`（不是 `expired`）
- 已成交 order replay while frozen → 返回 `duplicate_terminal`（不是 `rejected`）
- fake Get error on submit path → 无 Submit，无 pending upsert
- fake Get error on fill path → fill 不丢，OrderUpdate / delta_report 正常发

**开源参考**：Hummingbot in-flight order tracker，`InFlightOrder` 状态机，connector 层 "store read error ≠ cache miss" 模式。

---

## 第2章 — Major：`spot_executions` 无 DB 唯一约束

**状态：待修复** | 来源：D4B-2（06-07 addendum 已用真实 DB 验证：两次插入同一对均达到 count=2）

**现状**：`insertFillIfNew` = 先查存在性，再 Insert（check-then-insert，非原子）。两路并发可同时通过检查，产生重复成交行，账本重复计算，误触 auto-freeze。

**修复步骤**：
1. 在 `spot_executions` 加两个 partial unique index：
   - `(client_order_id, trade_id) WHERE trade_id <> 0`
   - `(client_order_id, filled_at_exchange_ms) WHERE trade_id = 0`
2. `InsertSpotExecution` 遇 unique violation → 幂等 no-op（不报错）
3. 同步到：`internal/saas/store/db.go` AutoMigrate raw DDL + Goose **`00002_spot_executions_dedup.sql`**
4. 跑 `TestMigrationsMatchAutoMigrate` 验证 drift

**必须补的测试**：
- 并发双写同一 fill → 只有一行，无 error
- unique violation → 按 no-op 处理，调用方不报错

---

## 第3章 — Major：trade_records 状态非单调

**状态：待修复** | 来源：S5B-4 / C-5

**现状**：`TradeRepo.UpdateTradeStatus` 按 `client_order_id` 无条件覆盖。ack / order_update 乱序或重放时，`partial_filled` / `cancelled` / `rejected` 可覆盖 `filled`。

**状态转移规则**（需在一处编码，见架构手册 Lesson 5）：
```
pending      → partial_filled / filled / cancelled / rejected
partial_filled → filled / cancelled / rejected
filled       → terminal，不可后退
cancelled    → terminal，不可后退
rejected     → terminal，不可后退
```

**修复方向**：`UpdateTradeStatus` 加 terminal 保护谓词：
```sql
WHERE client_order_id = ? AND status NOT IN ('filled', 'cancelled', 'rejected')
```
`RowsAffected=0` 时按幂等 no-op 处理（terminal 状态已是最终态，不算错误）。

**必须补的测试**：
- `filled` 行收到 `partial_filled` ack/order_update → 状态不变
- `filled` 行收到 `rejected` → 状态不变
- expired ack 不能 cancel 已 filled 的 TradeRecord

---

## 第4章 — Major：多实例同账户误冻结

**状态：已决策，待实施** | 来源：L-1（06-06 / 06-07 addendum 多次确认 open）

### 决策：方案 A — 强制每账户最多一个非退役实例

**理由**：
- 代码注释已写 `// v1 assumption: whole-balance anchor, one instance per exchange account`，设计意图明确。
- Genesis funding 以全仓为基线，两个实例在同账户 → expected 加倍，下次 delta_report 必触 auto-freeze，100% 复现。
- 实盘多策略需开多个子账户（Binance 支持），不是多 instance。
- "历史回测多并行"由 GA 引擎的 population 机制承担，与 `strategy_instances` 无关。
- 方案 B（per-instance 资本分配）：改动量大，`PortfolioState` + funding 数学均需重设计，推迟到有真实多账户产品需求时再引入。

**修复步骤**：
1. `internal/saas/store/db.go`：加 partial unique index `(owner_user_id, account_id) WHERE status != 'retired'`
2. Goose **`00003_instance_one_per_account.sql`**：同步该 DDL
3. `CreateInstance` API：unique violation → 409 Conflict，明确错误消息
4. `TestMigrationsMatchAutoMigrate` 通过

**必须补的测试**：
- 同账户第二个非退役实例 → 409 拒绝
- 已退役实例不阻止同账户新建实例
- 两个同账户实例在同一快照下 genesis funding 不产生误冻结（回归）

---

## 第5章 — Medium：Genesis Funding 竞争条件

**状态：待修复** | 来源：Stage 4B "Genesis Funding Claim Happens After The Seed Append"

**现状**：`fundInstance` 先 append genesis PortfolioState，再 `MarkFunded`——两步非原子。并发两路 delta_report 可各自留下 seed 行，baseline 按时间戳而非唯一 claim 决定。

**修复方向**：先 claim，再 append seed：
- `MarkFunded` 带 `funded_at_ms IS NULL` 谓词；`RowsAffected=0` 意味着已被其他路 claim
- 只有 claim 成功的一路才 append genesis PortfolioState
- `MarkFunded` 改为返回 `(claimed bool, error)` 或用 `UPDATE ... RETURNING`

**必须补的测试**：
- 并发双路 fundInstance → 只有一条 genesis seed 行

---

## 第6章 — Medium：RawEvaluateResult Validate 未接入 GA 热循环

**状态：待修复** | 来源：G2C-1 / G2C-2 / G2C-3

**现状**：
- `engine.evaluatePopulation`（`engine.go:414`）调 `adapter.Evaluate` 后直接聚合 `raw.Windows`，无 nil 检查、无 `Validate()`
- Best-gene 重评估（`engine.go:310`）、`RunReview`（`review.go:126`）、`RunOOS`（`oos.go:154`，nil 不 fail-closed）、`RunStress`（`stress.go:53`，nil 当 no-series skip）同样缺保护
- `RawEvaluateResult.Validate()` 自身只校验三态互斥，不校验序列/去重/OOS/cascade 语义

**修复步骤**：
1. 新增 `internal/resultpkg/validate_raw.go`，以方法形式扩展校验：
   ```go
   func (r *RawEvaluateResult) ValidateForIS() error    // 含序列/去重/OOS/cascade
   func (r *RawEvaluateResult) ValidateForOOS() error
   func (r *RawEvaluateResult) ValidateForStress() error
   ```
   **注意**：Stress 中"无 LongestWindowReturns → 跳过"的业务策略留在 `internal/verification/stress.go`，resultpkg 只管 shape/sequence/cascade/field consistency，不知道 Monte Carlo/plan 语义。
2. `evaluatePopulation`：`raw == nil` → worker error；`raw.ValidateForIS() != nil` → fail-closed
3. Best-gene 重评估：同样加 nil + `ValidateForIS` 检查
4. `RunReview`、`RunOOS`、`RunStress`：各自调用对应模式的 validator

**`ValidateForIS` 应拒绝**：
- `raw == nil`
- nil / empty `Windows`
- duplicate windows
- 非规范顺序（6m→2y→5y→10y）
- `WindowOOS` 出现在 IS raw 中
- non-fatal/non-skipped window 的 `Score.Value == nil`
- `SkippedBy` 指向的 Fatal 不存在于更早的窗口
- Fatal 窗口出现在 cascade-skipped 序列之后

**必须补的测试**（6条）：
- nil raw → RunEpoch error，不 panic
- invalid raw → RunEpoch + best-gene re-eval fail before aggregation
- `ValidateForIS` 拒绝：empty / duplicate / non-canonical / OOS-in-IS / bad-cascade / SkippedBy-without-fatal
- RunReview invalid raw → Go error（不是 mismatch 或 OK）
- RunOOS nil raw → fail closed
- RunStress invalid non-nil raw → 不 silent skip

---

## 第7章 — Low / 架构边界

**状态：低优先级，可攒到独立 PR**

| 问题 | 文件 | 建议 |
|---|---|---|
| `sigmoid_v1 → verification` 边界泄漏 | `internal/strategies/sigmoid_v1/evaluate_window.go` | 把 `ComputeSharpeStats` 下沉到 `internal/quant` 或 `internal/fitness`，消除 strategy→engine 依赖 |
| Engine 测试 import 具体策略 | `internal/engine/engine_test.go` 等 | 加 CI import-boundary check（`go list` gate），或改用 stub 策略隔离 |
| `cmd/datafeeder` `sort.Slice` | `cmd/datafeeder/main.go` | 改 `sort.SliceStable`，一行，统一全库规范 |
| toy 不按规范四窗口顺序 | `internal/strategies/toy/toy.go` | 改用 `resultpkg.AllWindowsInEvalOrder()` 迭代，使 toy 成为有效的边界 fixture |
| `internal/adapters/backtest` 目录为空 | `internal/adapters/` | 若短期无填充计划，加注释说明意图；否则删除避免歧义 |

---

## 未解决的产品决策（架构手册 Lesson 9）

以下决策尚未落地为可执行代码，需逐一拍板后才能动相关代码。

| 决策项 | 当前状态 | 建议 |
|---|---|---|
| 每账户实例数限制 | ✅ 已决策：方案A，强制 1 个非退役实例（第4章） | — |
| Genesis funding 全仓锚定 | 随方案A自动成立 | — |
| **Retire 策略（已部署 champion）** | ❌ 未决策 | 建议 v1：若有非退役实例的 `active_champ_id` 指向该 champion，则 422 拒绝 Retire，要求 operator 先换 champion 或 stop 实例 |
| state_sync 重放深度 | ❌ 未决策（Phase 6 前置条件） | 先完成 Phase 1-4；Phase 6 开始前需明确：崩溃重启后追补哪段时间的 fill、是否跨副本、Agent 本地 SQLite 是否足够 |
| SaaS 账本 float64 精度角色 | ❌ 未决策 | 建议 v1：monitoring-only，不用于 settlement；如需结算精度，届时引入 decimal |

---

## 未来阶段：`cmd/saas` 业务服务提取（Phase 5）

**状态：有意推迟，不在当前 Chapter 1-4 范围内**

`cmd/saas/agentmsg.go`（758 行，22 个函数）承担了太多业务职责，是明确的长期技术债：fill dedup、reconciliation 数学、genesis funding、auto-freeze、audit——全在一个 `cmd/` 文件里。

**目标形状**（见架构手册 §Package-Level Target Shape）：
```
internal/saas/execution      — HandleAck / HandleOrderUpdate / InsertFillIfNew / 状态转移策略
internal/saas/reconciliation — ParsePositions / ReconcilePositions / BuildManagedSet / PersistDiscrepancies
internal/saas/funding        — BuildSeedPortfolio / ClaimAndFundInstance / 账户归属不变量
internal/saas/risk           — AutoFreeze debounce / kill-switch 触发策略
cmd/saas                     — config、依赖构建、路由/hub 设置、signal/shutdown
```

**推迟理由**：agentmsg.go 是活跃实盘代码，提取过程中任何依赖方向错误或闭包变量泄漏都可能产生 silent bug。**必须等 Chapter 1-4 的负向测试全部通过后，在有专门"无实盘交易"测试窗口的时机再动**，不得与功能修复混在同一 PR。

**执行原则**：先提取纯函数（reconciliation 数学），再提取有 DB 副作用的路径（funding，需等第5章先完成），最后提取 risk/kill-switch。禁止"移代码同时改语义"。

---

## 优先级总览

| 项目 | 严重性 | 状态 | 建议顺序 |
|---|---|---|---|
| 已完成（Item 3：CAS + DeployChampion） | Major | ✅ Done | — |
| 第1章 Agent fail-open（幂等性） | Critical | 待修 | **1** |
| 第2章 spot_executions DB 唯一约束 | Major | 待修 | **2** |
| 第3章 trade status 单调性 | Major | 待修 | **3** |
| 第4章 多实例同账户（方案A） | Major | 决策已定，待实施 | **4** |
| 第5章 genesis funding 竞争条件 | Medium | 待修 | 5 |
| 第6章 Raw Validate 接入热循环 | Medium | 待修 | 6 |
| 第7章 架构边界（Low 项） | Low | 可延后 | 7 |
| ⑤⑥ Frontend observability | Low | 可与 1-4 并行 | 并行 |
| Phase 5 agentmsg.go 提取 | 技术债 | 有意推迟 | 1-4 完成后专项 |
| Phase 6 durable reconnect replay | 未来 | 需先定产品规格 | 最后 |

**Goose 迁移编号规划**：
- `00002_spot_executions_dedup.sql`（第2章）
- `00003_instance_one_per_account.sql`（第4章）

---

## 附录 A：架构改进手册（`docs/learn/comments-on-architecture-v1.md`）

来源：搭档撰写，2026-06-07。

### 强烈同意的部分

- **Phase 顺序正确**（0→7）：正确性/约束固化优先，包提取在后。这是最重要的一个判断。
- **Avoidance List** 精准，特别是"Do not mix package extraction with untested semantic changes"。
- **Lesson 4（DB 约束拥有并发不变量）** 和 **Lesson 5（状态机需要转移规则）** 直接对应第2、3章。
- **Lesson 8（负向测试是架构，不只是 QA）**：文档中的测试矩阵和我们各章节的"必须补的测试"完全一致。

### 修订和补充

**Phase 5 时机**：文档说"Phase 1-4 之后做"，但我要强调：Phase 5 还需要一个有"无实盘交易"的专项测试窗口，不能仅仅因为 Phase 1-4 完成了就立即开始。758 行虽大，但尚在可控范围，不要因为着急整洁而在实盘期间动它。

**`internal/resultpkg/rawvalidate` 单独包已撤回**：搭档同意，改为方法形式放在 `internal/resultpkg/validate_raw.go`（见第6章）。理由：这些校验是 `RawEvaluateResult` 合约的扩展，没有循环 import，子包只会制造"子包验证父包类型"的绕路。

**Phase 6（Durable Reconnect Replay）需先定产品规格**：文档正确地说"Phase 6 在 Phase 3 之后"，但产品问题更在前：崩溃重启后追补哪段 fill？是否跨副本？Agent 本地 SQLite 够不够？当前 `delta_report` 进程内 buffer 对单进程 Agent 是够用的，v1 不需要 at-least-once 跨重启。Phase 6 标记为"需产品规格"，不列入当前 Roadmap。

**Retire 已部署 champion 策略**：文档列为 open decision，我的建议见上方"未解决的产品决策"表格。

**`internal/adapters/backtest` 目录为空**：文档未提及，已列入第7章 Low 项。

### 与升级计划章节的对应关系

| 架构手册 Phase | 对应升级计划章节 |
|---|---|
| Phase 1: Strategy/Evaluation Contract | 第6章 Raw Validate |
| Phase 2: Live Order Idempotency | 第1章 Agent fail-open + 第3章 状态单调 |
| Phase 3: Fill DB Backstops | 第2章 spot_executions 唯一约束 |
| Phase 4: Account Ownership | 第4章 多实例（已决策方案A） |
| Phase 5: Extract SaaS Services | 有意推迟（见"未来阶段"节） |
| Phase 6: Durable Reconnect Replay | 推迟，需先定产品规格 |
| Phase 7: Control Plane / Observability | ⑤⑥ frontend observability（并行） |

---

## 附录 B：开源项目架构参考（`docs/learn/LEARN-open-source-architecture-refactor.md`）

来源：搭档整理，2026-06-07。

**文档定位**：导航手册，防止"看了别人的项目然后按包结构重写自己系统"的失误。§6 Avoidance List 和 §7 Target Mental Model 是全文最高价值的两节，长期有效。

**项目阅读优先级**（已对原文档修订）：

| 顺序 | 项目 | 对 QuantLab 的价值 |
|---|---|---|
| 1 | NautilusTrader | same-core 设计、typed domain objects、execution model |
| 2 | **Hummingbot**（上调） | in-flight order tracker、connector 幂等性、store read error 处理 |
| 3 | LEAN（下调） | 可插拔 Algorithm/DataFeed/Brokerage；企业级 C# 较重，优先级低于 Hummingbot |
| 4 | vn.py | event engine、gateway 抽象 |
| 5 | Freqtrade | operations as architecture；dry-run、Web UI、状态暴露 |
| 6 | Qlib / Backtrader / Zipline | 研究纪律，QuantLab 这方面已做得好，边际价值低 |

**已从列表删除**：FinRL-X（RL 研究框架，与 QuantLab GA 方向无关，且违背文档自身的 §6 原则）。

**Freqtrade ⑤⑥ observability 可与 Chapter 1-4 并行**：stale-data（⑤）和 env-mismatch（⑥）信号透出到 `/live` 是纯前端工作（约 150-200 行），不触及任何后端持久化，可在 Chapter 1-4 进行期间并行推进。

**已完成的对原文档的修订**：
- ✅ 删除 FinRL-X 条目
- ✅ 将 Hummingbot 优先级调到 LEAN 之前，并更新理由说明
