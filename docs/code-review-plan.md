# QuantLab 代码审阅 Plan

依据 `go-skill-quantlab/SKILL.md`(11 节 + merge-gate checklist),与 quantlab 实际包结构对齐。

## 0. 范围与产出

- **范围**:`internal/`(adapters, agent, api, data, domain, engine, fitness, migrate, quant,
  report, repository, resultpkg, saas, strategies, strategy, verification, wire, wsconn)+
  `cmd/` + `tests/`。
- **不审**:`research/`(Python,SKILL 明示不进 server path)。
- **产出**:每条 finding 标 `severity{blocker|major|minor}` + `file:line` + 对应 SKILL 条款 +
  建议修法。外加两份缺口清单:**缺失的永久测试**、**float64/decimal 边界裁决记录**。
- **方法**:机器可判定项先用工具一次性扫;再按 §1→§6 优先级人工走读,quant-correctness(§6)
  投入最大。

---

## 1. 工具门(SKILL §1 + checklist 末两项)

机器能查的不占人工预算,先建基线:

```
gofmt -l . ; go vet ./... ; staticcheck ./... ; govulncheck ./...
go test -race ./...        # GA/worker 必过
go test ./...
```

- **verify**:四工具 clean;`-race` 全绿。任何 race → blocker(§4)。
- `go.mod` 现为 `go 1.25.0`;SKILL 建议追 1.26.x 并支持最近两个大版本 → 记一条 minor
  「Go 版本落后」。

---

## 2. 并发与生命周期(SKILL §4)— 本仓最高优先

GA 是 unbounded-goroutine / 共享写 重灾区。**重点**:`internal/engine/engine.go`(worker pool)、
`internal/strategy/evolvable.go`。

- 每个 goroutine 有 owner + cancel 路径 + 终止条件;并行度 bounded(errgroup `SetLimit`
  到 `GOMAXPROCS`);结果写 `results[i]` 独立 index,**无 shared-slice append / shared-err**。
- `ctx` 第一参数、贯穿 evaluate/data/DB、不入 struct、不传 nil。
- 交叉验证 CLAUDE.md 不变式:`Adapter.Evaluate` 内**禁起 goroutine**、float 累加**串行**(无
  concurrent reduce)→ 违反即 blocker(同时破坏 §6.1 可复现)。
- **verify**:`-race` 下跑 `TestEvaluateOrderInvariance` / `TestAdapterResetIsolation`。

---

## 3. Quant-correctness(SKILL §6)— 投入最大的一段

### 3.1 可复现性 & 随机源(§6.1)

**重点**:`internal/engine/engine.go`、`internal/strategy/evolvable.go`、
`internal/strategies/toy/toy.go`、`internal/agent/client.go`。

- 每个 stochastic run 有**显式 seed 且随结果记录**(对照 result package
  `reproducibility_metadata`)。
- **禁止跨 goroutine 共享 `*rand.Rand`**;每 worker 用 `seed + workerIndex` 独立 PCG。检查
  `math/rand` vs `math/rand/v2`(top-level 函数自动播种 = 不可复现)。
- **map 迭代序**:任何喂入计算/输出的 map 先 sort key;对照 CLAUDE.md「全用
  `sort.SliceStable`」不变式。
- **verify**:`TestEvaluateDeterministic`、`TestReplayWithinTolerance`、
  `TestMutationScaleLinearity`。

### 3.2 金额/价格算术(§6.2)— 按角色裁决,非「是钱就 decimal」

**定稿裁决(本次讨论结论)。** SKILL §6.2 已同步更新为此口径。

把「money」拆成三个独立诉求:**精确表示(exactness)/ 误差有界(bounded drift)/
可复现(reproducibility)**。「钱禁用 float64」只对 exactness 硬要求时成立。

按**角色**定类型,不按「实盘 vs 回测」:

| 面 | 现状(已实现) | 类型 | 裁决 |
|---|---|---|---|
| 交易所对账 / 成交 / 下单(`internal/agent`, `binance`, `internal/wire`) | `ExchangeFill.FillQuantity/FillPrice/FillFeeAmount` = `decimal.Decimal`;wire `*_decimal` 字段 = decimal-as-string | decimal / string | ✅ 正确,保持 |
| 回测 / fitness / 统计(`internal/fitness`, `ghost_dca.go`, 监控) | float64 | float64 | ✅ 正确(见下) |
| SaaS ledger / 监控降转(`internal/wire/statesync.go:9`) | decimal **降转** float64 | float64 | ⚠️ 须审(见 finding) |

**为何回测内部用 float64 是对的**:回测本就不是 decimal-exact 域 —— bps 费率
(`notional × bps/10000`)、复利、ratio return(Modified Dietz)都产生非终止小数,decimal
也得舍入,只增 allocation(§7)零换精确。这里真正要守的是 **bounded drift**(串行 + 长序列
Kahan/Welford)与 **reproducibility**(顺序固定),不是类型。

**红线**:任何「真实现金结算 ledger / live 成交对账」组件落地,该组件必须 scaled-int/decimal。
**testnet 与实盘共用 decimal 面**(同 API、同 tick/lot filter),testnet 是「在没真钱风险时
先把 decimal 下单/对账链路跑对」,不是更松的档。

**Finding-candidates(本节具体核查项)**:
1. **`statesync.go` 降转 seam**:确认 SaaS 那个 "ledger" 只服务监控/展示(Binance 才是 source
   of truth)。若有真实结算语义,decimal→float64 降转即 blocker。
2. **reconciliation 比较空间**:`OpenOrder` 对账(差异记 `discrepancy_event`)**必须在
   decimal/string 空间比**,不得先转 float64 —— 否则 ULP 级假 discrepancy / 漏判。major。
3. **`ghost_dca.go` 等热路径 naive 累加**:现为 `qty += filled` / `nav[i] = qty*close`,**未上
   Kahan**。量化 10y 窗 + weekly-inject 下的漂移量,再定是否需 Kahan。
4. **浮点 `==` 全仓扫描**:price/PnL 的 `==` 是否带 epsilon + 注释。

### 3.3 Look-ahead / 数据泄漏(§6.3)— 最贵的 bug

**重点**:`internal/fitness/`、`internal/data/`(EvaluablePlan 构造)、
`internal/verification/oos.go`、各 strategy `Evaluate`。

- bar/feature 只用 ≤ 自身 timestamp 的信息;rolling 而非 full-sample 统计;scaler 只 fit 训练窗。
- 信号→成交滞后(t 的信号最早 t+1 执行)。
- **OOS 专审**:对照 CLAUDE.md「DCA baseline 在 OOS bars 重新模拟、不复用 IS」「OOS Fatal 不回写
  IS ScoreTotal」—— leakage 高危面。
- **缺口检查**:SKILL 要求一个**永久泄漏测试**(corrupt 未来数据,断言 cutoff 前决策不变)。盘点
  `tests/` 是否存在;缺则列为 blocker 级测试缺口。

### 3.4 时序对齐 & 时间(§6.4)

**重点**:`internal/data/`、`internal/quant/canonical_json.go`、四窗 crucible。

- 内部 UTC;多序列 join 显式按 timestamp 对齐(不假设 index 对齐)。
- bar-close vs bar-open / 半开区间约定有文档。
- gap 处理:对照 CLAUDE.md `Bar.IsGap/GapType` + `TestGapHandlingNoFakeTrades`,gap 不得静默变
  0 / stale。

### 3.5 数值稳定性(§6.5)

- variance/mean 用 Welford/online(对照已知 Kahan 用法),避免 catastrophic cancellation。
- 指标层 NaN/Inf、除零 guard(零波动、空窗)。**重点**:`internal/fitness/`、
  `internal/verification/`(DSR、stress SBB Monte Carlo)。

---

## 4. 持久层(SKILL §5)

**重点**:`internal/repository/`、`internal/migrate/`、`internal/saas/`。

- `defer rows.Close()` **且** loop 后 `rows.Err()`(漏 `rows.Err()` 静默丢结果集 = 污染回测
  输入,major)。
- 全部 query parameterized;无字符串拼 symbol/date。
- TimescaleDB hypertable 查询有 time-range 边界(chunk exclusion);无全史 `SELECT *`。
- 事务:失败路径全 rollback;不跨层泄漏 tx。

---

## 5. 工程基础(SKILL §2/§3/§8/§10)— 快速走读

- §3 错误**恰好处理一次**(不 log-and-return);`%w` 包裹;`errors.Is/As` 不做字符串匹配。
- §2 命名 MixedCaps、无 stutter;包名 domain-oriented(确认无 util/common/helpers)。
- §8 构造器注入、无隐藏全局、无 premature abstraction。
- §10 slog 结构化日志带 run_id/seed/generation;不日志凭据(broker key、DB 密码、agent
  token —— 留意近期 `--seed-agent-token` 工作)。

---

## 6. 测试与可靠性(SKILL §9)+ 对照 CLAUDE.md §10.1

- 核对 §10.1 的 12 个 priority test 齐全、且 table-driven。
- **永久 fixture 是否存在**:backtest golden/regression、ledger invariant、leakage 测试、
  reproducibility 测试。缺哪个列缺口清单。
- 集成测试用 `//go:build integration`(对照项目惯例:有 Postgres 就用 integration,别引
  sqlite/新驱动)。

---

## 7. Appendix A(service 规则)— 仅审已成服务的部分

batch GA 不审;**只审** `internal/saas/`、`internal/wsconn/`、`internal/agent/`、
`internal/api/`:`http.Client` 复用 + 超时、`resp.Body.Close()`、带退避/幂等的重试、
SIGTERM / 优雅关闭。

---

## 执行顺序

1. 阶段 1(工具门)→ 2. 阶段 3(quant §6,主投入)→ 3. 阶段 2(并发)→ 4. 阶段 4-7。

最终汇总 findings 表(severity / file:line / 条款 / 修法)+ 两份缺口清单(缺失永久测试、
float64/decimal 边界裁决记录)。

---

## Findings 汇总

### 阶段 1(Architecture boundary / SKILL §2 hard boundary)— 2026-06-06 重审

按用户要求轻量重审 architecture boundary,重点复核已知噪声:`sigmoid_v1 -> verification`
依赖、`engine_test -> concrete strategies`。结论:生产 `internal/engine` import graph
仍然干净;未发现新的 Stage 1 边界违规。两个已知问题仍成立,风险低于实盘路径,但会让边界
grep/自动规则长期带豁免,后续更难识别真正违规。

验证:`GOCACHE=/tmp/quantlab-go-cache go list -f '{{.ImportPath}} {{join .Imports " "}}' ./internal/...`
与 `GOCACHE=/tmp/quantlab-go-cache go list -test -f '{{.ImportPath}} {{join .Imports " "}}' ./internal/...`
确认生产/test import graph;`GOCACHE=/tmp/quantlab-go-cache go test ./internal/engine ./internal/strategies/sigmoid_v1 ./internal/verification ./internal/resultpkg ./internal/fitness` 通过。
未提权,未改代码。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| A-1 | major | `internal/strategies/sigmoid_v1/evaluate_window.go:23` + `internal/strategies/sigmoid_v1/evaluate_window.go:244` | Architecture boundary / strategy layer must not depend on verification layer | ⛔ 未修 | `sigmoid_v1` import `quantlab/internal/verification`,并调用 `verification.ComputeSharpeStats(logReturns)`。当前只影响 `LongestWindowStats` / DSR 输入等评估元数据,不直接改 IS `ScoreTotal` 或实盘 Step 路径;但 concrete strategy 依赖 engine-layer verification helper,边界方向错误。修法:把 `ComputeSharpeStats` 下沉到 lower-level package(优先 `internal/quant` 或 resultpkg-adjacent helper),让 `verification` 与 `sigmoid_v1` 都依赖低层。 |
| A-2 | minor | `internal/engine/engine_test.go:13` + `internal/engine/engine_test.go:14` + `internal/engine/engine_sigmoid_test.go:10` + `internal/engine/fatal_audit_test.go:13` | Engine layer must not import concrete strategies / automated boundary checks | ⛔ 未修 | 生产 `internal/engine` 不导入 concrete strategies,但外部测试包 `engine_test` 直接导入 `sigmoid_v1` 和 `toy`。这会让 `grep '"quantlab/internal/strategies'` 对 engine-layer 包长期噪声化,需要测试豁免。修法:engine 单元测试改用本地 fake/stub `strategy.EvolvableStrategy`;真实 `sigmoid_v1` end-to-end 测试迁到 composition/integration 层(如 `internal/saas/epoch`)。 |

**确认通过的不变量**:
- 生产 `internal/engine` imports 仅依赖 `domain/fitness/quant/resultpkg/strategy` 等接口/低层包,未导入 `internal/strategies`。
- `internal/saas/epoch/registry.go:15` import `sigmoid_v1` 是 composition root 例外;文件注释明确 registry 是 `internal/saas` 的 strategy construction 边界。
- `go list -deps ./internal/strategies/sigmoid_v1` 确认其依赖闭包包含 `internal/verification`;不是单纯文本误报。

### 阶段 2(并发与生命周期，§2 / SKILL §4)— 2026-06-02

核心结论:GA 并发模型健全,无 blocker/major。`-race` 全绿(engine/strategies/strategy/fitness);
`TestEvaluateOrderInvariance` / `TestAdapterResetIsolation` / `TestEvaluateDeterministic` 在
`-race` 下通过。worker pool 按独立 index 写、无 shared append/err、无 goroutine 泄漏;
Adapter.Evaluate 零 goroutine + 串行累加;`*rand.Rand` 不跨 goroutine。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| M-1 | minor | `engine.go:217` | §4 bounded 并行度 | ✅ 已修 | `numWorkers` 由 `runtime.NumCPU()` 改 `runtime.GOMAXPROCS(0)`：Go 1.25 cgroup-aware GOMAXPROCS 下，限额容器 `GOMAXPROCS < NumCPU` 会超订 P。 |
| M-2 | minor | `internal/saas/epoch/service.go:202` | §4 goroutine owner+cancel / Appendix A 优雅关闭 | ✅ 已修 | `go s.run(...)` 此前全 detached、用 `context.Background()`、无 shutdown 取消路径、无运行登记表;SIGTERM 中途退出会留 DB 任务行卡 `running`(永不 MarkFailed)、内存 `mu` 重启即丢。修法 **(a)+(b) 都做**(纵深防御):(a) `Service` 加生命周期 `baseCtx`/`stop`(CancelFunc)/`wg`(WaitGroup),`run()` 改跑 `baseCtx`、`defer wg.Done`,新增 `Shutdown(ctx)` 取消+按 deadline 等 `wg.Wait`;终止态 `MarkFailed`/panic 恢复改用全新 5s detached ctx(baseCtx 取消后已不能写 DB);`CreateAndRunTask` 在 `go s.run` 前 `wg.Add(1)`。`main.go` 在 `srv.Shutdown` **之后**调 `svc.Shutdown`(HTTP 先排空,杜绝 `wg.Add` 与 `wg.Wait` 竞态)。引擎 `RunEpoch` 逐 gene 查 `ctx.Err()`(`engine.go:406`),取消亚秒级生效。(b) `EvolutionTaskRepo.SweepOrphans` 启动时把 `queued`+`running` 重置为 `failed`(比 import 版多扫 `queued`——epoch 任务无 poller 续跑),`main.go` 在 `ListenAndServe` 前调用,兜底 kill -9。回归测试:`TestService_Shutdown_CancelsBaseCtxAndReturns` + `TestService_Shutdown_RespectsDeadlineWhenEpochHangs`(均 DB-free)。 |

观察(不立项):某 worker 出错不取消 ctx,兄弟 worker 仍排空剩余 jobs(浪费算力,非 bug);
errgroup 可免费拿取消,价值低。

### 阶段 2B(GA core invariants targeted re-review)— 2026-06-05

按用户要求做 targeted 重审,不全量重来。重点覆盖:`RawEvaluateResult.Validate()` 是否接入 hot
loop、窗口顺序、cascade skipped semantics、score aggregation 是否只在 engine 层、worker adapter
isolation。结论:`sigmoid_v1` 生产路径的顺序/cascade/adapter isolation 正确,score aggregation
仍由 engine/verification replay 调 `fitness.AggregateScoreTotal`;但 RawEvaluateResult 边界校验仍未
接入热路径,且现有 Validate 只校验单窗口三态,不校验 Raw 级 cascade 序列。

验证:`GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/fitness ./internal/engine ./internal/strategies/sigmoid_v1 ./internal/verification` 通过。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| G-1 | major/high | `internal/engine/engine.go:414` + `internal/engine/engine.go:310` + `internal/fitness/aggregate.go:51` | GA score trust / strategy->engine boundary | ⛔ 未修 | `adapter.Evaluate` 后直接 `AggregateScoreTotal(raw.Windows, ...)`,最终 best-gene re-evaluate 也不校验。若 adapter 返回 `Fatal=false, SkippedBy=nil, Value=nil`,聚合器会把它当缺失窗口跳过,静默改变 `ScoreRaw` 与 consistency penalty;`raw == nil` 还可能在 worker goroutine 内 panic。修法:每次 `adapter.Evaluate` 后先 `raw == nil || raw.Validate()!=nil` fail closed,再聚合;best re-evaluate、`RunReview`、`RunOOS` 同样补边界校验。 |
| G-2 | major | `internal/resultpkg/validate.go:69` + `internal/resultpkg/enums.go:145` | cascade skipped semantics / fixed window order | ⛔ 未修 | `RawEvaluateResult.Validate()` 只逐个窗口调用 `CrucibleResult.Validate()`,不校验空结果、重复窗口、canonical 顺序、Fatal 后续是否全 skipped、`SkippedBy` 是否来自真实早期 Fatal。`WindowName.IsValid()` 接受 `WindowOOS`;若 OOS 混进 IS raw,聚合权重为 0 但仍会进入 `validScores`,污染 consistency penalty。修法:新增 Raw 级 cascade validator,基于 `AllWindowsInEvalOrder()` 禁止 OOS、拒绝重复/乱序、校验 Fatal→SkippedBy 链。 |
| G-3 | minor | `internal/strategies/toy/toy.go:207` | fixed window order fixture quality | ⛔ 未修 | `Toy.Evaluate` 直接遍历 `plan.Windows`。toy 不在 production `DefaultRegistry`,所以不是生产 bug;但 engine 测试用 toy 时不能证明 canonical order contract。修法:toy 也按 `AllWindowsInEvalOrder()` 输出,或把 order-invariance 边界测试迁到专用 stub/sigmoid fixture。 |

**确认通过的不变量**:
- `sigmoid_v1` 使用 `resultpkg.AllWindowsInEvalOrder()` 固定 6m→2y→5y→10y。
- `sigmoid_v1` cascade skip 为 `Fatal=false, Value=nil, SkippedBy!=nil`;self-Fatal 为
  `Fatal=true, Value=nil, SkippedBy=nil`。
- worker pool 仍是一 worker 一 adapter,且每次 Evaluate 前 `Reset(plan)`;best re-evaluate 也先 Reset。
- 未发现 concrete `Adapter.Evaluate` 内起 goroutine 或并发 reduce。
- 策略层未发现直接写 `ScoreTotal`;生产分数聚合仍由 engine 调 `fitness.AggregateScoreTotal`。

### 阶段 2C(GA boundary / RawEvaluateResult contract)— 2026-06-06 重审

按今日工作计划第 1 项复查 GA boundary / `RawEvaluateResult` contract。重点覆盖 engine hot
loop、best-gene re-evaluate、`verification.RunReview`、`verification.RunOOS`、`verification.RunStress`
以及 `resultpkg.RawEvaluateResult.Validate()`。结论:既有 guard gap 仍成立,且 verification 的 stress
入口也属于同一类 adapter 输出边界;当前 `sigmoid_v1` producer 本身仍按 canonical order 产出有效
cascade,但 engine/verification 没有 fail-closed 地信任边界。

验证:`GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/fitness ./internal/engine ./internal/verification ./internal/strategies/sigmoid_v1 ./internal/strategies/toy` 通过。
未改代码。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| G2C-1 | major/high | `internal/engine/engine.go:414` + `internal/engine/engine.go:310` + `internal/fitness/aggregate.go:51` | strategy->engine trust boundary / fail closed | ⛔ 未修 | `adapter.Evaluate` 后直接访问 `raw.Windows` 并聚合;`raw == nil` 会在 worker goroutine panic,invalid raw 如 `Fatal=false, SkippedBy=nil, Value=nil` 会被聚合器当 missing/skipped contribution 静默跳过。best-gene re-evaluate 同样不校验。修法:新增统一 guard(例如 `validateAdapterRaw(raw, mode)`),engine hot loop 与 best re-evaluate 在聚合/返回前先检查 `raw == nil || raw.Validate()!=nil` 并返回 error;补 invalid raw fail-closed 单测。 |
| G2C-2 | major | `internal/resultpkg/validate.go:69` + `internal/resultpkg/enums.go:145` | Raw-level cascade contract | ⛔ 未修 | `RawEvaluateResult.Validate()` 只逐个窗口调用 `CrucibleResult.Validate()`,不校验空 windows、重复 window、canonical 6m→2y→5y→10y 顺序、Fatal 后续是否全 skipped、`SkippedBy` 是否来自真实早期 Fatal、IS raw 是否混入 `WindowOOS`。`WindowName.IsValid()` 接受 `WindowOOS`,所以当前 Raw validator 不会挡住 OOS 混入 IS 聚合。修法:Raw validator 增加 mode-aware 或 IS-only cascade sequence 校验;基于 `AllWindowsInEvalOrder()` 拒绝 OOS/重复/乱序并校验 Fatal→SkippedBy 链。 |
| G2C-3 | major | `internal/verification/review.go:126` + `internal/verification/review.go:130` | verification replay boundary | ⛔ 未修 | `RunReview` replay 后直接 `AggregateScoreTotal(raw.Windows, ...)`,没有 Raw 校验。审计 replay 的语义是 strategy/adapter contract violation 应返回 Go error;当前 invalid raw 会被算成 mismatch 或甚至静默匹配。修法:replay 后先校验 `raw` 和 IS cascade sequence;contract violation 返回 error,不要走 `ReviewSummary.Status=mismatch`。 |
| G2C-4 | minor/medium | `internal/verification/oos.go:154` + `internal/verification/oos.go:158` + `internal/verification/stress.go:53` | OOS/stress adapter-output boundary | ⛔ 未修 | `RunOOS` 只局部检查 `len(raw.Windows)==1` 与 non-Fatal nil Value,但没处理 `raw == nil`,也没复用统一 Raw/Crucible 校验;`RunStress` 只看 `raw == nil || len(raw.LongestWindowReturns)==0`,invalid raw 会被当作"无 stress series"跳过。修法:verification 侧共享 adapter raw 校验 helper;OOS 用 single-window/OOS-mode 规则,stress 至少对非 nil raw 做基础 Validate 或明确只接受 valid capture result。 |

**确认通过的不变量**:
- 当前 `sigmoid_v1` adapter 使用 `AllWindowsInEvalOrder()` 输出 canonical cascade,未发现主策略生产 invalid raw。
- `toy` 仍按 `plan.Windows` 输出,不在 production `DefaultRegistry`,但作为 engine fixture 对 canonical order 的证明力弱。
- 现有测试覆盖 happy path、adapter error、Crucible 三态;没有覆盖 engine/verification 对 invalid raw fail-closed 的负例。

### 阶段 2D(Raw validation / GA boundary negative-test follow-up)— 2026-06-06 复审

复审范围:`internal/engine/engine.go`, `internal/resultpkg/validate.go`, `internal/verification/review.go`,
`internal/verification/oos.go`, `internal/verification/stress.go`;重点确认 fail-closed 与 Raw-level cascade
负向测试是否已存在。结论:G2C-1/G2C-2/G2C-3 仍未修, G2C-4 仍是同类边界缺口;当前测试基线通过,
但缺的负向测试仍不存在。未改源码。

验证:`GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/fitness ./internal/engine ./internal/verification ./internal/strategies/sigmoid_v1 ./internal/strategies/toy` 通过。

| ID | severity | file:line | 状态 | 复审结论 / 缺失测试 |
|---|---|---|---|---|
| G2D-1 | major/high | `internal/engine/engine.go:310` + `internal/engine/engine.go:414` + `internal/engine/engine.go:419` | ⛔ 未修 | `evaluatePopulation` 仍在 `adapter.Evaluate` 后直接 `AggregateScoreTotal(raw.Windows, ...)`;若 adapter 返回 `raw == nil`,worker goroutine 会在访问 `raw.Windows` 时 panic;若返回 `Fatal=false, SkippedBy=nil, Value=nil` 这类 invalid raw,聚合器会把该 window 当缺失贡献跳过。best-gene re-evaluate 也只保存 `bestRaw`,不校验。缺失测试:fake adapter 返回 nil raw / invalid raw 时,RunEpoch hot loop 与 best re-evaluate 必须返回 error 而不是 panic 或 silent aggregate。 |
| G2D-2 | major | `internal/resultpkg/validate.go:69` + `internal/resultpkg/enums.go:145` | ⛔ 未修 | `RawEvaluateResult.Validate()` 仍只检查 `Windows != nil` 和逐个 `CrucibleResult.Validate()`;它不拒绝 empty windows、重复 window、非 canonical 顺序、`WindowOOS` 混入 IS、Fatal 后续未 cascade skipped、`SkippedBy` 无真实 earlier Fatal。缺失测试:Raw-level table tests 覆盖上述每个 negative case,并保留合法 canonical cascade 正例。 |
| G2D-3 | major | `internal/verification/review.go:126` + `internal/verification/review.go:130` | ⛔ 未修 | `RunReview` replay 后仍直接聚合 `raw.Windows`,没有把 strategy/adapter contract violation 作为 Go error。缺失测试:stub adapter replay 返回 invalid raw 时,`RunReview` 应返回 error,不得产出 `VerificationStatusMismatch` 或 silent OK。 |
| G2D-4 | medium | `internal/verification/oos.go:154` + `internal/verification/oos.go:158` + `internal/verification/stress.go:53` + `internal/verification/stress.go:57` | ⛔ 未修 | `RunOOS` 只检查 `len(raw.Windows)==1`,且在 nil raw 时会 panic;`RunStress` 对 `raw == nil` 返回 `(nil,nil)`,对 invalid non-nil raw 也没有统一校验。缺失测试:OOS nil/invalid raw fail closed;stress 对 invalid non-nil raw 不得 silently skip。 |

### 阶段 3(quant-correctness，§3 / SKILL §6)— 2026-06-02

核心结论:量化正确性扎实,无 blocker/major。**§6.1 可复现**(EpochSeed 显式+记录、rng 不跨 goroutine、
KahanSum 位级确定、计算路径无 map 迭代)、**§6.3 look-ahead 泄漏**(`closesBuf` 只含 ≤当前 bar →
结构性不可能;crucible warmup 严格在 eval 前、IS 窗全 ≤ IS-end < OOS-start;OOS DCA post-warmup 重算+
Fatal 不回写 IS)、**§6.4 时序**(内部 UTC、半开区间一致、IsGap 排除 bars_hash)、**§6.5 数值稳定**
(StdDev 两遍法+双 KahanSum、Skew/Kurt 有 m2==0 guard、Sharpe std>0 guard)全部干净。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| Q-1 | minor | `internal/verification/sharpe.go:64 meanOf` | §6.5 长序列补偿求和一致性 | ✅ 已修 | `meanOf` 由 naive `+=` 改用 `quant.KahanSum`,与同函数 `StdDev` 的补偿求和口径一致(并修正失真注释)。漂移本可忽略(~5e-13),属一致性/效率改进。 |

**测试缺口(§6.3)— ✅ 已补(2026-06-02)**:此前无「篡改未来 bar → 断言 cutoff 前决策不变」的专门
**永久泄漏回归测试**。结构上泄漏已不可能(`closesBuf` 增量构建,只含 ≤当前 bar),属纵深防御/回归守卫
缺口,非现存 bug。

**修法**:`internal/strategies/sigmoid_v1/evaluate_window_test.go` 加
`TestEvaluateWindow_NoLookaheadCorruptFutureBars`。前因后果:
- **设计要点**:断言对象是 `evaluateWindow` 返回的**逐 bar log-return 序列**,**不是**窗口聚合 score ——
  cutoff 之后的 bar 是该窗口合法 in-sample 数据、会合法改 score;泄漏-freedom 严格是**逐 bar 性质**
  (`preNav` 在 bar i 只用 ≤i 的 close 给持仓 mark-to-market,当前 close 入算非前视)。
- **手法**:同一基准 fixture(flat warmup + 上升 ramp,strategy 实际持 BTC)跑两遍;第二遍把
  index > cutoff 的 OHLC **×1000**(向上 spike,不触发 drawdown/Fatal,保证两遍都产出完整前缀)。
  断言 `retsClean[:cutoff-warmup] == retsCorrupt[:cutoff-warmup]`(bit 级)。
- **positive control**:额外断言第一个 post-cutoff 回报 `rets[prefix]` 两遍**必须不同** —— 证明扰动
  确实到达了 preNav 计算,使前缀相等判定**非空洞**(否则若 strategy 不持仓,前缀恒等会假性通过)。
- **牙齿验证**:临时在 evaluate 循环注入泄漏(每 bar 偷看 `window.Bars[last].Close`)→ 测试如期 FAIL
  (pre-cutoff log-return[34] clean=0.0023 vs corrupt=2.6e-5),确认前缀回报非平凡且守卫真能捕获泄漏;
  随即还原(`evaluate_window.go` 零 diff)。`-race` 全绿。

**命名可追溯性 nit**:§10.1 的 12 个 priority test 行为全覆盖,但命名与 CLAUDE.md 文档不同 ——
`TestEvaluateWindow_GapBarsProduceNoTrades`(=GapHandlingNoFakeTrades)、
`TestEvaluate_CascadeShortCircuitFrom6M`(=CascadeShortCircuit)、
`TestChromosome{Clamp,Validate}*`(=ClampValidateContract)、`TestRunReview_*`(=ReplayWithinTolerance)。
建议在文档加别名映射,或重命名测试对齐。非缺口。

#### float64/decimal 边界裁决记录(交付物)

1. **ghost_dca / simulator 累加 → float64 OK,无需 Kahan**:累加按注入次数(weekly 10y ≤~520)/
   成交次数,**非按 bar**;per-bar NAV 是 `qty*close` 新乘法无累加;相对漂移 ~1e-13,远低于任何判定阈值。
2. **statesync 降转 + reconciliation 比较 → float64 OK**:`cmd/saas/agentmsg.go:reconcilePositions`
   是 `|diff|>minAbsDiff(dust) && driftBps>50bps` 的**容差带**比较,float64 ULP(~1e-15)比容差小 13
   个数量级,不可能产生假 discrepancy/漏判;`expected` 是 SaaS 监控账(bookkeeping)非结算账,Binance
   仍是真值源。符合 §6.2「监控用 float64」红线之内。`denom` 有 `1e-9` 除零 floor。
3. **signal 同根 close 成交 → 文档化 EOB-rebalance 约定,非 look-ahead**:`ComputeSignal` 用
   `closes[last]`、订单在同根 `bar.Close` 成交,与 sigmoid doc §7(line 340-341)一致;对目标权重
   再平衡不构成可利用前视;backtest-vs-live 成交时序差由 friction haircut + 铁律1 边界覆盖。

### 阶段 3B(业务集成不变量，Promote/Retire / test_mode / OOS / kill-switch)— 2026-06-05 重审

重审范围按用户要求聚焦业务边界:Promote/Retire 生命周期、`test_mode` friction 语义、OOS 不污染 IS
score、kill-switch managed-asset scope。结论:核心语义大多正确,但有 3 个业务一致性 major 仍需修。
其中 B-1 与 B-3 和阶段 4 的持久层 findings 重叠,但从业务面看会直接造成结果包、前端状态、
实盘控制面互相不一致;B-2 是本次重审新增的 live reconciliation/kill-switch 风险。

验证(无外部 DB):`GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./internal/saas/epoch ./internal/verification ./cmd/saas ./internal/saas/wshub ./internal/data ./internal/engine` 通过。
未跑 integration/TimescaleDB 测试。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| B-1 | major | `internal/repository/champion.go:138` + `internal/repository/champion.go:145` | Promote/Retire 生命周期 / §4 CAS | ⛔ 未修 | `ChampionRepo.Retire` 先读 `ChampionHistory`,再用 `WHERE id = ?` 更新。两个并发 Retire 都可读到 `retired_at IS NULL`,都通过 `applyRetire`,后一个覆盖前一个的 `retired_at/retired_by/retire_note`,审计归因错误。修法:UPDATE 改 `WHERE id = ? AND retired_at IS NULL`;`RowsAffected==0` 映射 `api.ErrAlreadyRetired`;补 CAS/并发回归测试。 |
| B-2 | major | `cmd/saas/agentmsg.go:511` + `cmd/saas/agentmsg.go:531` + `cmd/saas/agentmsg.go:610` + `internal/repository/instance.go:63` | kill-switch managed-asset scope / live reconciliation | ⛔ 未修 | `ListByAccount` 和 runbook 允许同一 `account_id` 有多个非 retired `StrategyInstance`,但 genesis funding 对每个 fresh instance 都用整账户 exchange snapshot seed。多实例同账户时,每个实例都会拿到整账户 base/USDT;下一轮 reconcile 把所有实例 portfolio 相加,`expected` 被重复计数,managed BTC/USDT 会产生假 drift 并可能 auto-freeze。修法二选一:v1 硬约束一个 account 只能有一个非 retired instance;或引入 per-instance 资金分配/managed balance,禁止每个实例 whole-account seed。补多实例 account 的 genesis funding + auto-freeze 回归测试。 |
| B-3 | major | `internal/api/handlers.go:715` + `internal/repository/instance.go:84` + `internal/repository/instance.go:114` | 实盘控制面状态一致性 / §4 状态转换 CAS | ⛔ 未修 | Instance start/stop 在 handler 读状态后计算 next,repo `UpdateStatus` 只按 `instance_id` 写;并发 terminal retire/其它写入可被旧读覆盖回 `live/paused`。`SetActiveChampion` 也可直接改 retired instance,让前端/控制面显示已退役实例仍部署 champion。修法:状态转换改 CAS(`WHERE instance_id=? AND status IN (...)`)并用 `RowsAffected` 区分 not found/非法转换/竞态;deploy champion 至少加 `status <> 'retired'`。 |

**确认通过的不变量**:
- Promote active champion 唯一性已有 DB partial unique index 兜底:`uq_champion_active`
  (`retired_at IS NULL AND deleted_at IS NULL`),Promote unique violation 映射到 `ErrActiveChampionExists`。
- Promote/Retire 生产 wiring 是 `AuthRequired + RequireAdmin`;operator/viewer 403,admin 200 有测试覆盖。
- `decision_status` 仍严格为 `{pending,promoted,rejected}`;Retire 只通过 `champion_history.retired_at`
  / `retired_at_ms` 表达,不写 `"retired"` 到 result package 或 `gene_records.decision_status`。
- `test_mode=true` 路径正确:task row 保留用户原始 requested friction;epoch plan 用 effective zero
  friction;package/`gene_records` lift 的是 effective friction + `TestMode`;Promote 读 `GeneRecord.TestMode`
  并拒绝。
- OOS 后置于 `RunEpoch`;OOS Fatal 只写 `VerificationLayer.OOSResult failed/red`,不回写 IS
  `Evaluation.ScoreTotal`;DCA baseline 在 OOS eval bars 上重算,不复用 IS baseline。
- kill-switch 的 managed-asset scope 本身正确:`maxFlaggedDriftBps` 只看 `expected` keys(实例 base asset
  + USDT),faucet/unmanaged drift 会记录 discrepancy 但不会触发 freeze。B-2 是 managed baseline 构造错误,
  不是 scope filter 错误。

### 阶段 3C(Business state consistency)— 2026-06-06 重审

重审范围:Promote/Retire 与 DeployChampion 的状态一致性、实例 start/stop/deploy 的 stale-read 覆盖风险、
多实例同 account 的资金基线、SaaS 订单状态单调性。结论:新增 1 个 major,并确认此前 4 个状态一致性
finding 仍未修。`test_mode`、OOS 不污染 IS、`decision_status` 不含 retired、kill-switch unmanaged scope
本身未发现新回归。

验证:`GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/instance ./internal/saas/store` 通过。
首次沙箱内运行因 `httptest.NewServer` 需要监听本地端口失败(`listen tcp6 [::1]:0: socket: operation not permitted`);提权重跑通过。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| C-1 | major | `internal/api/validate.go:139` + `internal/api/handlers.go:697` + `internal/repository/instance.go:114` + `internal/saas/instance/manager.go:298` + `internal/saas/instance/champion_loader.go:35` | DeployChampion / live Tick 状态一致性 | ⛔ 未修 | `DeployChampionRequest.Validate()` 只要求 `challenger_id` 非空,handler 直接调用 `SetActiveChampion`,repo 只按 `instance_id` 写 `active_champ_id`。Tick 后续按 instance 的 `StrategyID` resolve 策略,但用 `active_champ_id` 直接读 challenger blob,没有证明该 challenger 是目标实例 `(strategy_id,pair)` 的 active/unretired champion。可部署 retired champion、其它 pair/strategy 的 challenger、甚至 never-promoted challenger;轻则 Tick decode/load 失败,重则错误基因在当前 account/pair 下发订单。修法:deploy 前读取 instance + champion_history,要求 challenger 匹配 instance `(strategy_id,pair)` 且 `retired_at IS NULL`;写入加 `status <> 'retired'`;定义 Retire 对已部署实例是 block、detach 还是 pause;补 mismatch/retired deploy 回归测试。 |
| C-2 | major | `internal/repository/champion.go:138` + `internal/repository/champion.go:145` | Retire CAS / audit attribution | ⛔ 未修 | 复核 B-1/D-2 仍成立:`Retire` 先读再 `WHERE id = ?` 更新,并发双退可覆盖审计字段。修法同 B-1。 |
| C-3 | major | `internal/api/handlers.go:715` + `internal/repository/instance.go:84` + `internal/repository/instance.go:114` | instance lifecycle CAS | ⛔ 未修 | 复核 B-3/D-4 仍成立:start/stop 旧读可覆盖 terminal retired;deploy 也缺 retired guard。修法同 B-3,并与 C-1 的 active champion 校验合并处理。 |
| C-4 | major | `internal/saas/store/db.go:134` + `internal/repository/instance.go:71` + `cmd/saas/agentmsg.go:511` + `cmd/saas/agentmsg.go:531` + `cmd/saas/agentmsg.go:610` | account-level reconciliation baseline | ⛔ 未修 | 复核 B-2 仍成立:partial unique 只限制同 user/strategy/pair/account,不同 pair/strategy 可共享 account;每个 fresh instance 用整账户 snapshot seed,后续 expected 聚合重复 portfolio,可能 false drift/auto-freeze。修法同 B-2。 |
| C-5 | major | `cmd/saas/agentmsg.go:150` + `cmd/saas/agentmsg.go:189` + `internal/repository/trade.go:42` | order lifecycle monotonicity | ⛔ 未修 | 复核 Stage 5 订单状态问题仍成立:SaaS ack/order_update 将 rejected/expired/partial/cancelled 等状态无条件写入 `TradeRecord`,repo `UpdateTradeStatus` 只按 `client_order_id` 更新。延迟 replay 或旧事件可把 filled 降级。修法:状态转换表 + DB 条件更新;terminal 不可被非 terminal/失败态覆盖;ack 失败态不得覆盖已有 fill。 |

### 阶段 3D(Live reconciliation / multi-instance account risk)— 2026-06-06 专项复查

专项范围:同一 `account_id` 多个非 retired instance 时,genesis funding 是否重复 seed 整账户
snapshot,以及 reconcile 是否把 duplicated expected portfolio 相加后误触发 auto-freeze。结论:
C-4/B-2 当前仍成立,而且这是设计不变量缺失,不是 managed-asset filter 本身的错误。

验证:`GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/store` 通过。
该命令只确认当前相关包测试基线通过;现有测试覆盖单 instance genesis funding、`ListByAccount`
排除 retired、managed/unmanaged drift scope,但没有多 instance 同账户的负向回归测试。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| L-1 | major | `internal/saas/store/db.go:134` + `internal/repository/instance.go:71` + `cmd/saas/agentmsg.go:421` + `cmd/saas/agentmsg.go:511` + `cmd/saas/agentmsg.go:518` + `cmd/saas/agentmsg.go:533` + `cmd/saas/agentmsg.go:610` + `cmd/saas/agentmsg.go:573` + `cmd/saas/agentmsg.go:715` | live reconciliation / account capital ownership | ⛔ 未修 | DB partial unique 只限制 `(owner_user_id,strategy_id,pair,account_id) WHERE status!='retired'`,允许同账户不同 pair/strategy 多个非 retired instance;`ListByAccount` 也明确返回该账户全部非 retired instance。`handleDeltaReport` 把同一账户 snapshot fan-out 给 `reconcile`;未 funded instance 逐个调用 `fundInstance`,而 `buildSeedPortfolio` 用整账户 actual base/USDT seed 每个 instance。下一轮已 funded 后,`reconcile` 把所有 portfolio 累加进同一个 `expected` map,managed set 又来自 `expected` keys,所以重复 baseline 会在 managed BTC/USDT 上形成假 drift,连续超过 freeze line 后触发 `maybeAutoFreeze`。修法二选一:V1 硬约束每个 `account_id` 只能有一个非 retired instance;或引入 per-instance capital allocation / managed-balance ownership,使 genesis funding 只能 seed 属于该 instance 的资金份额。补多实例 account 回归测试:两个 fresh instance 同账户从同一 snapshot funding 后,下一份相同 snapshot 不得产生 discrepancy/auto-freeze;或者创建第二个非 retired instance 必须被拒绝。 |

**证据补充**:
- `internal/repository/instance_integration_test.go:45` 已用 BTC/ETH/BNB 不同 pair 建出同账户 live/idle/paused 三个非 retired instance,并断言 `ListByAccount` 返回 3 个;这证明当前行为不是理论路径。
- `cmd/saas/agentmsg_test.go:273` 的 helper 单测确认 `buildSeedPortfolio` 把 whole base balance seed 到 `FloatBTC`,`USDT` 也 whole-account seed。
- `cmd/saas/genesis_funding_integration_test.go:36` 只覆盖单 instance 三轮 delta_report,没有覆盖同 account 多 instance 的重复 baseline。

### 阶段 5B(live order / idempotency targeted re-review)— 2026-06-06

按今日工作计划复查 Stage 5 live order/idempotency 最高优先级风险。重点覆盖 Agent
`handleTradeCommand` 的幂等顺序、SQLite idempotency `Get` error 处理、`onOrderEvent`
错误路径、SaaS `ack` / `order_update` 状态写入单调性。结论:当前源码没有修掉已记录风险,
并将 submit-path idempotency read-error 从 major 上调为 critical,因为它会 fail-open 到
`Put(preRec)` upsert + exchange submit。

验证:`GOCACHE=/tmp/quantlab-go-cache go test ./internal/agent ./cmd/saas ./internal/repository` 通过。
该命令只证明现有测试基线通过;当前测试没有覆盖 replay-after-terminal、idempotency read-error、
terminal-status downgrade 这些负例。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| S5B-1 | critical | `internal/agent/tradecommand.go:27` + `internal/agent/tradecommand.go:46` + `internal/agent/tradecommand.go:57` + `cmd/saas/agentmsg.go:210` + `cmd/saas/agentmsg.go:159` | live order idempotency / replay safety | ⛔ 未修 | `handleTradeCommand` 先检查 frozen/kill 和 `valid_until_ms`,之后才查 `idempotency.Get`。一个已 filled 的 `client_order_id` 若在 kill-switch 后或过期后 replay,会收到 `rejected`/`expired` 而非 `duplicate_terminal`;SaaS 再把这些 ack 映射成 rejected/cancelled 并无条件写状态。修法:对已知 `client_order_id` 先走幂等 duplicate ack;frozen/expiry 只拒绝 brand-new command;补 filled command replay-after-expiry 和 replay-while-frozen 回归测试。 |
| S5B-2 | critical | `internal/agent/tradecommand.go:57` + `internal/agent/tradecommand.go:105` + `internal/agent/tradecommand.go:111` + `internal/agent/idempotency_sqlite.go:66` + `internal/agent/idempotency_sqlite.go:110` | SQLite idempotency fail-closed | ⛔ 未修 | `existing, ok, _ := c.idempotency.Get(...)` 丢弃 read error。SQLite `Get` 的非 `ErrNoRows` 错误是 transient I/O/error;当前路径把它当 miss,随后 `Put(preRec)`。SQLite `Put` 是 `ON CONFLICT DO UPDATE`,会覆盖已有 accepted/filled 记录为 pending,然后继续 `exchange.Submit`。修法:对 `Get` error fail closed,返回 internal error/reject without submit;禁止 read-unknown 时 upsert 覆盖终态;补 fake-store `Get` error 测试。 |
| S5B-3 | major | `internal/agent/client.go:578` + `internal/agent/client.go:579` + `internal/agent/client.go:624` + `internal/agent/client.go:636` | exchange event durability / delta_report fallback | ⛔ 未修 | `onOrderEvent` 同样丢弃 idempotency `Get` error,然后进入 `!ok` 分支按 unknown order return。真实 Binance fill 若遇到 SQLite read error,会在发送 `OrderUpdate`、加入 `delta_report` buffer、更新本地状态之前被丢弃。修法:区分 not found 和 read failed;read failed 不得当 unknown order 丢弃,至少 log/error metric 并保留 durable retry/报告路径;补 fake-store read-error event 测试。 |
| S5B-4 | major | `cmd/saas/agentmsg.go:150` + `cmd/saas/agentmsg.go:189` + `internal/repository/trade.go:42` + `internal/repository/trade.go:193` | TradeRecord status monotonicity | ⛔ 未修 | SaaS `ack` / `order_update` 映射状态后调用 `UpdateTradeStatus`,repo 只 `WHERE client_order_id = ?` 更新,没有状态转换谓词。旧 `partial_filled`、`cancelled`、`rejected` 可覆盖已 `filled` 行。`MarkPartialIfPending` 已有正确的 pending-only guard,但通用更新没复用。修法:状态转换表 + DB 条件更新;terminal 不可被非 terminal/失败态覆盖;ack 失败态不得覆盖已有 fill;补 terminal replay/downgrade tests。 |

### 阶段 4(持久层，§4 / SKILL §5）— 2026-06-02；2026-06-05 重审

2026-06-05 重审结论:基础持久层仍然偏干净(GORM 参数化、Goose/AutoMigrate 双轨、drift guard
机制完整),但并发不变量上有 3 个仍需修的 major。此前「`Retire` 有 `RowsAffected==0` 防双退」
结论被重审推翻:当前 UPDATE 只按 `id` 写,不是 CAS。`spot_executions` 也仍缺 DB 级唯一约束兜底。
Goose/AutoMigrate drift guard 能证明两条 schema 路径一致,但不会判断业务唯一约束是否缺失。

验证(无外部 DB):`GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/store ./internal/saas/config` 通过。
integration/drift 测试已在 2026-06-06 Stage 4B follow-up 用 `./config.yaml` 补跑通过;`./config.agent.yaml` 是 Agent-only config,不适用于 SaaS DB integration。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| D-1 | major | `internal/repository/champion.go:60`(count-then-insert)+ `internal/saas/store/db.go`(缺索引) | §4 事务隔离 / §5 | ✅ 已修 | 「每 (strategy_id,pair) 至多一个 active champion」此前仅靠事务内 `activeOther` count 守护;READ COMMITTED 下两个并发 Promote 各读 0 → 双双 INSERT → 两个 active champion(静默违反不变式)。修法:db.go 加 `uq_champion_active` 部分唯一索引(`WHERE retired_at IS NULL AND deleted_at IS NULL`)+ Promote 把 unique-violation 映射成 `ErrActiveChampionExists` + 启动前 `assertNoDuplicateActiveChampions`。 |
| D-2 | major | `internal/repository/champion.go:138` + `internal/repository/champion.go:145` | §4 事务隔离 / §5 DB 条件保护 | ⛔ 未修 | `ChampionRepo.Retire` 先读 `ChampionHistory`,再 `WHERE id = ?` 更新。两个并发 Retire 都读到 `retired_at IS NULL` 时,都会通过 `applyRetire`,且都可 `RowsAffected=1`;后一个覆盖 `retired_at/retired_by/retire_note`。修法:UPDATE 改 `WHERE id = ? AND retired_at IS NULL`;`RowsAffected==0` 映射 `api.ErrAlreadyRetired`;补 CAS/并发回归测试。 |
| D-3 | major | `cmd/saas/agentmsg.go:296` + `cmd/saas/agentmsg.go:314` + `internal/saas/store/models.go:476` + `internal/saas/store/migrations/00001_baseline.sql:1340` | §4 事务隔离 / §5 schema invariant | ⛔ 未修 | `spot_executions` dedup 是 SELECT exists 后 INSERT,模型和 Goose 基线只有普通索引。并发重放或 `order_update`/`delta_report` 同时到达可双插重复 fill,新自增 ID 会被 ledger 当作新成交折叠。修法:加部分唯一索引 `(client_order_id, trade_id) WHERE trade_id <> 0` 和 `(client_order_id, filled_at_exchange_ms) WHERE trade_id = 0`;insert 路径把 unique violation 当幂等 no-op;补并发测试;同步 `db.go` raw DDL + goose `00002` 并跑 drift test。 |
| D-4 | major | `internal/api/handlers.go:715` + `internal/repository/instance.go:84` + `internal/repository/instance.go:114` | §4 状态转换由 DB 条件保护 | ⛔ 未修 | Instance start/stop 在 handler 读状态后计算 next,repo `UpdateStatus` 只按 `instance_id` 写;并发 terminal retire/其它写入可被旧读覆盖回 `live/paused`。`SetActiveChampion` 也可直接改 retired instance。修法:状态转换改 CAS(`WHERE instance_id=? AND status IN (...)`)并用 `RowsAffected` 区分 not found/非法转换/竞态;deploy champion 至少加 `status <> 'retired'`。 |

### 阶段 4B(Persistence concurrency invariants)— 2026-06-06 重审

重审范围: `champion_history` active uniqueness 与 Retire CAS、`spot_executions` fill dedup schema backstop、
`strategy_instances` start/stop/deploy 条件写入、以及相邻的 funding/import claim 模式。结论:此前 3 个
major 仍未修;`uq_champion_active` 仍确认有效。新增 2 个修缮点,其中 genesis funding claim ordering 为
medium,import worker claim 为当前单 worker 设计下的 low scalability guard。

验证:`GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/store` 通过。
首次沙箱内运行因 `httptest.NewServer` 需要监听本地端口失败(`listen tcp6 [::1]:0: socket: operation not permitted`);
提权重跑通过。integration/drift follow-up:`./config.agent.yaml` 是 Agent-only config,不满足 SaaS config
schema;改用 `./config.yaml` 后,以下命令提权通过:
`GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/saas/store/ -run TestMigrationsMatchAutoMigrate -args -config=/home/l9g/quantlab/config.yaml`;
`GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml`。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| D4B-1 | major | `internal/repository/champion.go:138` + `internal/repository/champion.go:145` | Retire CAS / audit attribution | ⛔ 未修 | 复核 D-2 仍成立:`Retire` 先 `First` 读 champion row,纯函数 `applyRetire` 只看内存里的 `RetiredAt`,随后 `UPDATE ... WHERE id = ?`。两个并发 Retire 都能读到未 retired,两个 UPDATE 都可 `RowsAffected=1`,后写覆盖先写的 `retired_at/retired_by/retire_note`。修法:UPDATE 改 `WHERE id = ? AND retired_at IS NULL`;`RowsAffected==0` 映射 `api.ErrAlreadyRetired`;补并发双退测试。 |
| D4B-2 | major | `cmd/saas/agentmsg.go:296` + `cmd/saas/agentmsg.go:314` + `internal/repository/trade.go:79` + `internal/saas/store/models.go:476` + `internal/saas/store/migrations/00001_baseline.sql:1340` | spot_executions idempotent fill write | ⛔ 未修 | 复核 D-3/S5B-4 仍成立:`insertFillIfNew` 是 SELECT exists 后 INSERT;repo 注释明确 `SpotExecution has no unique index`;模型只给 `client_order_id/exchange_order_id/trade_id` 普通 index,goose baseline 也只有普通 index。并发 `order_update`/`delta_report` 或旧/新 WS 重放可双插同一 fill,新自增 `id` 会被 `NewExecutionsForInstance` 当作新成交折叠。修法:加 `(client_order_id, trade_id) WHERE trade_id <> 0` 与 `(client_order_id, filled_at_exchange_ms) WHERE trade_id = 0` 唯一约束;insert unique violation 当幂等 no-op;同步 AutoMigrate raw DDL + goose migration;补并发测试并跑 drift test。 |
| D4B-3 | major | `internal/api/handlers.go:715` + `internal/api/handlers.go:735` + `internal/repository/instance.go:84` + `internal/repository/instance.go:114` | strategy_instances 状态转换 CAS | ⛔ 未修 | 复核 D-4/C-3 仍成立:`transitionInstance` handler 先读状态再算 next,`UpdateStatus` 只 `WHERE instance_id = ?`;`SetActiveChampion` 也只按 `instance_id` 写。旧读可把并发 terminal retired 覆盖回 `live/paused`,deploy 可改 retired instance。修法:repo 暴露条件转换 API,例如 `WHERE instance_id=? AND status IN (...)`;用 `RowsAffected` 区分 not found/非法转换/race;deploy 写入至少加 `status <> 'retired'`,并与 C-1 的 active/unretired champion 校验一起处理。 |
| D4B-4 | medium | `cmd/saas/agentmsg.go:637` + `cmd/saas/agentmsg.go:640` + `internal/repository/instance.go:104` + `internal/repository/portfolio.go:44` | genesis funding claim ordering | ⚠️ 可修缮 | `fundInstance` 先 append genesis `PortfolioState`,再 `MarkFunded`;`MarkFunded` 有 `funded_at_ms IS NULL` guard,但不返回 `RowsAffected`,调用方无从知道自己是否赢得 funding claim。并发 delta_report 若用不同 `nowMs` 进入,可留下多条 seed portfolio;`Latest()` 后续按 `now_ms DESC` 选一条,baseline/audit 可能取决于哪个报告时间更新,而不是单一 funding claim。修法:先原子 claim 再 seed,或 claim+seed 放事务;`MarkFunded` 返回 `(claimed bool,error)` / `UPDATE ... RETURNING`;只有 winner append seed;补 concurrent double-funding regression。 |
| D4B-5 | low | `internal/repository/import_job.go:78` + `internal/repository/import_job.go:97` + `cmd/saas/main.go:375` | import job claim scalability guard | ⚠️ 可修缮 | 当前注释和 wiring 是非 saas、单 background worker,所以不是现有生产 bug。但 DB 层没有 claim 保护:`NextQueued` 选 queued oldest 不加锁,`MarkRunning` 只按 `job_id` 更新;若以后多 worker/多副本启用 import,同一 queued job 可被多次执行。修法:扩容前改成 `UPDATE ... WHERE status='queued' ... RETURNING` 或 `SELECT FOR UPDATE SKIP LOCKED`;`MarkRunning` queued-only 并处理 `RowsAffected==0`;补多 worker claim 测试。 |

### 阶段 4C(DeployChampion 状态一致性)— 2026-06-06 专项复查

复查范围:`DeployChampion` / `SetActiveChampion` 是否能证明请求的 challenger 是目标 instance
`(strategy_id,pair)` 的 active、unretired champion,以及是否禁止部署到 retired instance。结论:major 仍未修。
`uq_champion_active` 能保证每个 `(strategy_id,pair)` 最多一个 active champion,但 deploy 路径没有读取并绑定这条 active row。

验证:`GOCACHE=/tmp/quantlab-go-cache go test ./internal/api ./internal/repository ./internal/saas/instance` 通过。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| D4C-1 | major | `internal/api/validate.go:139` + `internal/api/handlers.go:697` + `internal/repository/instance.go:114` + `internal/repository/champion.go:191` + `internal/saas/store/db.go:171` + `internal/saas/instance/manager.go:298` + `internal/saas/instance/champion_loader.go:35` | DeployChampion 状态一致性 / active champion scope / retired instance guard | ⛔ 未修 | `DeployChampionRequest.Validate()` 只检查 `challenger_id` 非空,handler 不读取目标 instance 或 `champion_history`,直接调用 `SetActiveChampion`;repo 只 `WHERE instance_id = ?` 写 `active_champ_id`,没有 `status <> 'retired'` 条件。`ChampionRepo.GetActive` 和 `uq_champion_active` 虽能表达 active/unretired champion 概念,但 deploy 没用它们证明请求 challenger 等于目标 instance `(strategy_id,pair)` 的 active row。Tick 后续按 instance 的 `StrategyID` resolve 策略,却用 `active_champ_id` 直接加载 challenger blob。修法:deploy 前在同一事务/条件 SQL 中读取目标 instance,要求 status 非 retired,并要求 `champion_histories.challenger_id = req.ChallengerID AND strategy_id = inst.StrategyID AND pair = inst.Pair AND retired_at IS NULL`;`RowsAffected==0` 映射 404/422;补 retired instance、retired champion、wrong pair/strategy、never-promoted challenger 回归测试。 |

### Regression test follow-up — 2026-06-06 专项复查

复查范围:所有当前 active high/major finding 是否已有永久回归测试,重点覆盖 Raw validation fail-closed、
Retire CAS、fill dedup concurrency、order terminal-status replay、多实例 account false-freeze。结论:现有测试基线通过,
但多数 active high/major 只有正向或相邻覆盖,缺少能锁住修复的负向回归测试。

验证:
`GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/engine ./internal/verification ./internal/repository ./internal/api ./internal/agent ./cmd/saas` 通过;
`GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml` 通过。

| finding(s) | severity | regression coverage status | evidence / missing permanent test |
|---|---|---|---|
| A-1 | major | ❌ 缺 CI/永久边界测试 | 只有手工 `rg`/`go list` 复查和 `ComputeSharpeStats` 功能测试;没有自动失败的 import-boundary guard 来禁止 `internal/strategies/sigmoid_v1 -> internal/verification`。 |
| G2C-1 / G-1 | major/high | ❌ 缺负向 fail-closed 测试 | `internal/resultpkg/validate_test.go:16` 覆盖 `CrucibleResult` 三态互斥,`internal/engine/engine_test.go:186`/`:216` 覆盖 valid Raw 正向路径;没有 fake adapter 返回 `raw == nil` 或 invalid Raw 后 RunEpoch/best re-evaluate 返回 error 的测试。 |
| G2C-2 / G-2 | major | ❌ 缺 Raw-level 序列测试 | 当前测试只逐个 `CrucibleResult.Validate()`,没有 empty windows、duplicate window、乱序、`WindowOOS` 混入 IS、Fatal 后续未 cascade skipped、`SkippedBy` 无真实 earlier Fatal 的 Raw-level negative cases。 |
| G2C-3 | major | ❌ 缺 replay contract 测试 | `internal/verification/review_test.go:41`/`:68`/`:97` 覆盖 OK、score mismatch、hash/fingerprint short-circuit;没有 invalid replay Raw 应返回 Go error 而非 mismatch/silent match 的测试。 |
| D-1 | major(已修) | ⚠️ 部分覆盖 | `internal/saas/store/db_integration_test.go:79` 验证 `uq_champion_active` DDL 落在正确表且有 partial predicate;仍缺真正插入两个 active champion 或并发 Promote 只允许一个成功的回归测试。 |
| C-1 / D4C-1 | major | ❌ 缺 deploy 负向测试 | `internal/api/instance_handlers_test.go:240` 只测 happy path 写入 `active_champ_id`;没有 retired instance、retired champion、wrong pair/strategy、never-promoted challenger 的拒绝测试。 |
| C-2 / D4B-1 / D-2 | major | ❌ 缺 DB CAS/并发测试 | `internal/repository/champion_test.go:145` 只测内存 `applyRetire` 已 retired 拒绝;没有 repository 层 `WHERE retired_at IS NULL` / `RowsAffected==0` 或双 Retire 并发归因测试。 |
| C-3 / D4B-3 / D-4 | major | ⚠️ 部分覆盖 | `internal/api/instance_handlers_test.go:203` 只证明 handler 读到 retired 时 start 返回 422;没有 stale read 后 `UpdateStatus` 覆盖 terminal retired 的 repo/并发测试,也没有 deploy retired guard 测试。 |
| L-1 / C-4 / B-2 | major | ❌ 缺多实例 false-freeze 负向测试 | `cmd/saas/genesis_funding_integration_test.go:47` 是单实例 genesis funding;`internal/repository/instance_integration_test.go:20` 反而证明同 account 可返回多个非 retired instance;没有两个 fresh instance 同账户从同一 snapshot funding 后不产生 false discrepancy/auto-freeze,或第二个非 retired instance 被拒绝的测试。 |
| D4B-2 / D-3 | major | ⚠️ 部分覆盖 | `cmd/saas/agentmsg_dedup_integration_test.go:33` 覆盖顺序 replay、cross-channel duplicate、same-ms distinct trade_ids;`internal/repository/reconciliation_integration_test.go:22` 覆盖 `ExecutionExists`;但没有两个并发 writers 同时通过 check-then-insert 的测试,也没有 DB unique violation 被当幂等 no-op 的测试。 |
| S5B-1 | critical | ⚠️ 部分覆盖 | `internal/agent/agent_test.go:409` 覆盖 immediate duplicate terminal;`internal/agent/agent_test.go:448` 覆盖 brand-new expired reject;`internal/agent/kill_switch_test.go:17` 覆盖 brand-new frozen reject。缺 filled/terminal `client_order_id` 在 expired 或 frozen 后 replay 仍返回 `duplicate_terminal` 的测试。 |
| S5B-2 | critical | ❌ 缺 idempotency read-error submit-path 测试 | `internal/agent/idempotency_sqlite_test.go` 覆盖 normal Get/Put/upsert/status update,但没有 fake `IdempotencyStore.Get` error 下 `handleTradeCommand` fail closed 且不 submit、不 upsert pending 的测试。 |
| S5B-3 | major | ❌ 缺 exchange-event read-error 测试 | `internal/agent/agent_test.go:681` 覆盖 unknown order drop,`:620` 附近覆盖 known filled event;没有 idempotency `Get` error 时不得按 unknown 丢弃 fill/order_update/delta_report 的测试。 |
| S5B-4 / C-5 | major | ⚠️ 部分覆盖 | `cmd/saas/agentmsg_test.go:11` 覆盖 ack/status 映射且 duplicate_terminal no-op;`internal/repository/trade_integration_test.go:246` 覆盖 `MarkPartialIfPending` 不覆盖 filled;`:286` 覆盖 orphan sweep 不改 terminal。缺通用 `UpdateTradeStatus`/ack/order_update replay 将 `filled` 降级为 `partial_filled`/`cancelled`/`rejected` 的负向测试。 |

### 阶段 5(工程基础，§2/§3/§8/§10）+ §7 Appendix A 服务规则 — 2026-06-02

核心结论:工程基础扎实,无 blocker/major。**§7 Appendix A 服务面全清**:3 个 `http.Client`
(`data/binance_api.go:82`、`data/binance_archive.go:77`、`agent/binance/client.go:188`)都带显式
Timeout 且存在 struct 上复用(非每请求 new);3 个 `.Do()` 站点全 `defer resp.Body.Close()`;
agent 有完整 8 步指数退避 + jitter(`agent/config.go:113`、`client.go:nextBackoff`);SIGTERM 优雅关闭
三 cmd 齐(`cmd/saas/main.go:149` `signal.NotifyContext` + `srv.Shutdown(timeout)` + `BroadcastGracefulShutdown`、
`cmd/agent`、`cmd/datafeeder`)。**§8** 无可变包级全局(仅 `ServerUpgrader` 配置单例 + sentinel err 块),
全构造器注入。**§2** 无 util/common/helpers 包名,domain-oriented。**§3** `%w: %v` 双格式
(`wire/codec.go`、`epoch/service.go:182`、`agentauth/token.go:73`)是「sentinel 用 %w 供 Is 匹配 + 明细用 %v」
标准惯用法,正确。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| E-1 | minor | `internal/agent/handshake.go:53` + `internal/agent/client.go:268 isFatalAuthErr` | §3 错误分类禁字符串匹配(已有类型化判别量) | ✅ 已修 | auth_fail 的 fatal/recoverable 分类此前靠 `strings.Contains(err.Error(), "invalid_token"\|"revoked"\|"schema_mismatch")`,而 handshake.go:53 把类型化的 `wire.AuthFailCode` 连同 **free-form `fail.Reason`** 一起拍进错误串 —— Reason 文本若含 fatal 关键词(如 "token not yet revoked")会把可恢复失败误判成 fatal,agent **永久停止重连**(反之亦可漏判)。修法:handshake 处用类型化 `isFatalAuthCode(fail.Code)` 分类并包裹 `errFatalAuth` sentinel;`isFatalAuthErr` 改 `errors.Is(err, errFatalAuth)`;清 client.go 孤儿 `strings` import + handshake.go 冗余 `var _ = errors.New` 占位符;补永久回归测试 `TestIsFatalAuthErr_DrivenByCodeNotReason`(含 account_mismatch+Reason 含 "revoked" 不得 fatal 的子用例)。**行为保持**:三个 fatal substring 的唯一来源就是 handshake.go:53(已核 `wire` decode 的 `ErrSchemaMismatch` 串是 "schema_version mismatch",不含 "schema_mismatch",从不触发旧逻辑)。 |
| E-2 | minor | `internal/saas/wshub/connection.go:391` | §10 凭据/日志清晰度 | ✅ 已修 | account_mismatch 错误把 `verified.AccountID` 标成 `token=%q`(误导:存的是 token 绑定的 account_id,**非密钥**;安全审阅扫 token 泄漏会误报)。改标签 `token_account=%q`。非泄漏。 |
| E-3 | minor | `internal/agent/handshake.go isFatalAuthCode` + `internal/wire/handshake.go:37` | §3 错误分类 / 重连生命周期 | ✅ 已修(用户决策) | E-1 落地后暴露:`account_mismatch` 仍被当可恢复(退避无限重连)。但 `hello.account_id` 与 token 绑定不符是**配置错误**,同 config 重试永不成功 —— 与 invalid_token/revoked/schema_mismatch 同理(wire 注释原话「retrying with the same config is pointless and noisy」逐字适用)。**决策:归入 fatal**(四个 AuthFailCode 现全 fatal,快速大声失败而非无限退避)。修法:`isFatalAuthCode` 加 `AuthFailAccountMismatch`;修正 wire `AuthFailCode` 陈旧注释(原仅列 InvalidToken/Revoked);回归测试相应翻转 + 强化 fragility 守卫为「可恢复 transport err 含 fatal 词不得 fatal」。`isFatalAuthCode` 的 `default:false` 保留:未来新增 code 默认可恢复直到显式分类。 |
- **结构化日志一致性(§10)**:`epoch/service.go` + `cmd/saas/main.go` 用 stdlib `log.Printf`(内联带
  `task=%s gen=%d`,标识符在但非可查询字段),而 `cmd/agent` 用 slog JSON。标识符齐全,仅风格一致性 nit。
- **seed token 打印**:`cmd/saas/main.go:181` `fmt.Printf` 把新建 agent_token 打到 operator stdout —— 是
  一次性首次披露(类 AWS secret),预期且必要,非泄漏。
- **agent fatal 双日志**:`client.go:241 agent_fatal_auth` + `main.go:87 agent_exited` 各加不同上下文
  (前者标因、后者标进程退出),可接受。
- **M-2(§7 重叠)**:`epoch/service.go:202` detached goroutine 无 shutdown 取消已于阶段 2 立项,**已修**(见阶段 2 findings 表:baseCtx/wg/Shutdown + 启动 SweepOrphans)。
