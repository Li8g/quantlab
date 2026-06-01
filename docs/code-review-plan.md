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
