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

### 阶段 2(并发与生命周期，§2 / SKILL §4)— 2026-06-02

核心结论:GA 并发模型健全,无 blocker/major。`-race` 全绿(engine/strategies/strategy/fitness);
`TestEvaluateOrderInvariance` / `TestAdapterResetIsolation` / `TestEvaluateDeterministic` 在
`-race` 下通过。worker pool 按独立 index 写、无 shared append/err、无 goroutine 泄漏;
Adapter.Evaluate 零 goroutine + 串行累加;`*rand.Rand` 不跨 goroutine。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| M-1 | minor | `engine.go:217` | §4 bounded 并行度 | ✅ 已修 | `numWorkers` 由 `runtime.NumCPU()` 改 `runtime.GOMAXPROCS(0)`：Go 1.25 cgroup-aware GOMAXPROCS 下，限额容器 `GOMAXPROCS < NumCPU` 会超订 P。 |
| M-2 | minor | `internal/saas/epoch/service.go:202` | §4 goroutine owner+cancel / Appendix A 优雅关闭 | ⏳ backlog | `go s.run(...)` 全 detached、用 `context.Background()`、无 shutdown 取消路径、无运行登记表。SIGTERM 中途退出会留 DB 任务行卡 `running`(永不 MarkFailed)；内存 `mu` 重启即丢。修法:(a) 注入 shutdown ctx 让 `executeEpoch` 可取消,或 (b) 启动 sweep 把孤儿 `running` 行重置为 failed。与 Appendix A(阶段 7)重叠。 |

观察(不立项):某 worker 出错不取消 ctx,兄弟 worker 仍排空剩余 jobs(浪费算力,非 bug);
errgroup 可免费拿取消,价值低。

### 阶段 3(quant-correctness，§3 / SKILL §6)— 2026-06-02

核心结论:量化正确性扎实,无 blocker/major。**§6.1 可复现**(EpochSeed 显式+记录、rng 不跨 goroutine、
KahanSum 位级确定、计算路径无 map 迭代)、**§6.3 look-ahead 泄漏**(`closesBuf` 只含 ≤当前 bar →
结构性不可能;crucible warmup 严格在 eval 前、IS 窗全 ≤ IS-end < OOS-start;OOS DCA post-warmup 重算+
Fatal 不回写 IS)、**§6.4 时序**(内部 UTC、半开区间一致、IsGap 排除 bars_hash)、**§6.5 数值稳定**
(StdDev 两遍法+双 KahanSum、Skew/Kurt 有 m2==0 guard、Sharpe std>0 guard)全部干净。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| Q-1 | minor | `internal/verification/sharpe.go:64 meanOf` | §6.5 长序列补偿求和一致性 | ✅ 已修 | `meanOf` 由 naive `+=` 改用 `quant.KahanSum`,与同函数 `StdDev` 的补偿求和口径一致(并修正失真注释)。漂移本可忽略(~5e-13),属一致性/效率改进。 |

**测试缺口(§6.3)**:无「篡改未来 bar → 断言 cutoff 前决策不变」的专门**永久泄漏回归测试**。
结构上泄漏已不可能(closesBuf 只含 ≤当前 bar),属纵深防御/回归守卫缺口,非现存 bug。建议补一条。

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

### 阶段 4(持久层，§4 / SKILL §5）— 2026-06-02

核心结论:全 GORM,基础扎实。**rows.Close/Err**(无 raw `.Query()`/`.Rows()`,GORM 内部托管)、
**参数化**(全 `?` 占位;唯一 `Sprintf` 拼接是 `create_hypertable` 的 int64 常量 chunk 间隔,
注释解释了 polymorphic-type 42804 无法用 `?`,无注入面)、**time-range**(`LoadKLines` 的
`open_time BETWEEN ? AND ?` 支持 chunk exclusion;全史加载是 GA 有意语义;`Coverage` 全表 GROUP BY
有 `[perf]` 注释)、**事务**(全闭包形式自动 rollback/commit,`Retire` 有 `RowsAffected==0` 防双退)
全部干净。

| ID | severity | file:line | 条款 | 状态 | 修法 |
|---|---|---|---|---|---|
| D-1 | major | `internal/repository/champion.go:60`(count-then-insert)+ `internal/saas/store/db.go`(缺索引) | §4 事务隔离 / §5 | ✅ 已修 | 「每 (strategy_id,pair) 至多一个 active champion」此前仅靠事务内 `activeOther` count 守护;READ COMMITTED 下两个并发 Promote 各读 0 → 双双 INSERT → 两个 active champion(静默违反不变式)。兄弟表 `strategy_instances`/`import_jobs` 都有部分唯一索引兜底,champion_history 独缺。修法:db.go 加 `uq_champion_active` 部分唯一索引(`WHERE retired_at IS NULL AND deleted_at IS NULL`,匹配应用层"active"语义)+ Promote 把 unique-violation(复用 `isUniqueViolation`)映射成 `ErrActiveChampionExists` + 修正错误声称事务已防竞态的包注释。**启动前校验**:`assertNoDuplicateActiveChampions` 在建索引前检测重复并给可操作报错;`scripts/preflight_champion_dup_check.sql` 为独立 operator 诊断。 |
