# 策略 sigmoid_v1

> **状态**：v1 草案（[INVENTED v1]）—— Phase 4 实施前的设计冻结文档。
> **StrategyID**：`sigmoid_v1`
> **路径**：`internal/strategies/sigmoid_v1/`
> **上游框架**：`docs/策略数学引擎.md`（资产三态、Sigmoid 公式、Signal 框架、铁律级约束）
> **下游消费者**：Phase 4 实施 / Phase 5 GA 引擎 / Phase 11 测试

本文档填充上游框架声明的**四个设计空间**。所有具体数值与公式均带 `[INVENTED v1]` 标记，含义是"无具体策略需求来源、由实施者按合理性猜测、待真实回测数据反馈后校准"。冻结条件：完成一次完整的端到端 GA 进化跑通 + 至少一个时间窗口拿到非 Fatal 评分后，由人工评审决定是否升级至 v2。

---

## 1. 概览

`sigmoid_v1` 是 QuantLab 的第一个真实策略（区别于 `toy-validation` 与 `demo` 占位）。其目标不是"赚钱"，而是**端到端验证 GA 引擎 + 评估管线在真实数学策略上的行为**：

- GA 能否在合理代数内收敛到稳定染色体
- 四窗口坩埚的 Fatal 短路是否在真实回撤数据下触发
- DSR / SBB 等加固组件在真实分数分布下的数值稳定性
- 资产三态记账（DeadBTC / FloatBTC / USDT）在 Macro 建仓 + Micro 调仓双循环下的守恒性

策略采用**经典量化骨架**：

- 宏观引擎做**月度 DCA 加仓**，资金记入 DeadBTC（长期持仓池）
- 微观引擎用 **Sigmoid 动态天平**调度 FloatBTC ↔ USDT，跟随 Signal 与库存偏置
- 当 NAV 出现深度回撤时，触发 `ReleaseIntent` 把部分 DeadBTC 翻成 FloatBTC，让微观引擎有更多调仓空间

---

## 2. 设计空间一：市场状态感知层

> 上游：`docs/策略数学引擎.md` §2 —— 必须定义"安静态"。

### 2.1 状态枚举（v1：2 态）

```go
type MarketState string

const (
    MarketStateQuiet  MarketState = "quiet"
    MarketStateActive MarketState = "active"
)
```

**v1 仅 2 态**。更细的市场状态分类（趋势 / 震荡 / 急跌）留给 v2。

### 2.2 判定逻辑

```
volRatio  = clip(MAV_short / MAV_long, 0.1, 3.0)
isQuiet   = (volRatio < quiet_threshold)
```

- `MAV` 定义见上游 §5.6（mean absolute change，close-to-close）
- `MAV_short / MAV_long` 的窗口长度来自染色体 `mav_short_period` / `mav_long_period`
- `quiet_threshold` 是染色体字段；典型范围 `[0.3, 1.2]`，默认 `0.7`

判定**纯依赖 `StrategyInput.Closes`**，不读 `RuntimeState`，满足上游 §2.3 的"每次 Step() 必须能重算"约束（铁律 1）。

### 2.3 状态对微观引擎的影响

| 状态 | `MarketBetaMultiplier` | 粉尘订单 |
|---|---|---|
| `quiet`  | 0.3 | 归零（不下） |
| `active` | 1.0 | 楔形过滤后正常下 |

`MarketBetaMultiplier = 0.3` 用于安静期降低调仓激进度（避免在低波动期被噪音触发交易）。常数 `0.3` 是 `[INVENTED v1]`；若 v2 需要可进化，再加入染色体。

### 2.4 状态对 Signal 合成的影响

v1 **不做状态依赖的权重切换**。所有 `aᵢ` 在两态共用。这是为了减少染色体维度；状态依赖切换需要至少 6 个额外维度（每态 3 个），v1 暂不投入。

---

## 3. 设计空间二：宏观引擎

> 上游：`docs/策略数学引擎.md` §4 —— 只买不卖，必须有死线兜底。

### 3.1 触发条件

| 触发器 | 条件 | 注入数量 |
|---|---|---|
| **月初定投** | `时间戳所在 UTC 月` 与 `LastProcessedBarTime 所在月`不同 | `macro_inject_usd` |
| **死线兜底** | `NowMs - LastMacroBuyMs ≥ 60 days` | `macro_inject_usd × 0.5` |

`LastMacroBuyMs` 持久化在 `RuntimeState` 内；首次 Step() 调用如果 `RuntimeState` 为空，等价于"从未买过"，立即触发死线兜底（保证 cold start 一定有初始仓位）。

### 3.2 订单形式

```go
OrderIntent{
    Kind:          OrderKindMacro,
    Side:          OrderSideBuy,
    OrderType:     OrderTypeMarket,
    QuantityUSD:   macro_inject_usd（或 ×0.5）,
    LimitPrice:    0,
    ClientOrderID: "macro-" + nowMs,
    ValidUntilMs:  nowMs + 60_000,
}
```

### 3.3 资金来源约束

宏观 Buy 的 `QuantityUSD` 不得超过 `SpendableUSDT`（见 §1.1 派生公式）。如果余额不足，**直接跳过这次注入**，不下减额订单（避免与微观调仓争抢资金）。日志记录"insufficient cash for macro buy"，但不返回错误。

### 3.4 资产记账

宏观成交后，资金从 `USDT` 流入 `DeadBTC`（非 `FloatBTC`）。这是与微观 Buy 的关键区别。具体记账由 `internal/adapters/backtest/` 在 Adapter 层完成；`Step()` 只产生意图，不直接修改 `Portfolio`。

---

## 4. 设计空间三：染色体字段清单

> 上游：`docs/策略数学引擎.md` §7 —— 必须给出字段清单、硬边界、Segment 划分。

### 4.1 13 维染色体（Gene 索引 0–12）

| 索引 | 字段名 | 类型 | 范围 | 默认 | 语义 |
|---:|---|---|---|---|---|
| 0 | `a1` | float | [-1, 1] | 0.5 | 价格偏离度权重（v1 不归一，见 §4.3） |
| 1 | `a2` | float | [-1, 1] | 0.3 | 短期对数收益率权重 |
| 2 | `a3` | float | [-1, 1] | 0.2 | 波动率比率（中心化）权重 |
| 3 | `beta` | float | [0.5, 5] | 2.0 | Sigmoid 激进系数 β |
| 4 | `gamma` | float | [0, 3] | 0.5 | 仓位均值回归系数 γ |
| 5 | `ema_short_period` | int* | [5, 100] | 20 | 短期 EMA 周期 |
| 6 | `ema_long_period` | int* | [50, 300] | 100 | 长期 EMA 周期 |
| 7 | `mav_short_period` | int* | [5, 50] | 10 | 短期 MAV 窗口 |
| 8 | `mav_long_period` | int* | [30, 250] | 60 | 长期 MAV 窗口 |
| 9 | `quiet_threshold` | float | [0.3, 1.2] | 0.7 | 安静态判定阈值 |
| 10 | `micro_reserve_pct` | float | [0.05, 0.5] | 0.25 | 微观保留 USDT 比例 |
| 11 | `macro_inject_usd` | float | [10, 1000] | 100 | 月度 DCA 注入金额 |
| 12 | `release_drawdown_threshold` | float | [0.1, 0.5] | 0.3 | DeadBTC 释放触发回撤 |

`int*`：物理类型是 `float64`（`domain.Gene` 元素类型），在 `Clamp` 中四舍五入并截到整数后再写回。

### 4.2 Segments 划分（5 段）

```go
[]SegmentInfo{
    {
        Name:             "signal_weights",
        Dimensions:       []int{0, 1, 2},
        QuantizationStep: []float64{0.05, 0.05, 0.05},
        GeneStep:         []float64{0.2, 0.2, 0.2},
        IsCritical:       true,
        Description:      "信号合成权重 a1, a2, a3（v1 不归一）",
    },
    {
        Name:             "micro_dynamics",
        Dimensions:       []int{3, 4},
        QuantizationStep: []float64{0.1, 0.1},
        GeneStep:         []float64{0.3, 0.2},
        IsCritical:       true,
        Description:      "Sigmoid β 与库存偏置 γ",
    },
    {
        Name:             "feature_periods",
        Dimensions:       []int{5, 6, 7, 8},
        QuantizationStep: []float64{1, 1, 1, 1},
        GeneStep:         []float64{5, 10, 5, 10},
        IsCritical:       true,
        Description:      "EMA 与 MAV 的短长周期；Clamp 后强制 short < long",
    },
    {
        Name:             "state_thresholds",
        Dimensions:       []int{9, 10},
        QuantizationStep: []float64{0.05, 0.02},
        GeneStep:         []float64{0.1, 0.05},
        IsCritical:       false,
        Description:      "市场状态阈值与现金保留比例",
    },
    {
        Name:             "macro_release",
        Dimensions:       []int{11, 12},
        QuantizationStep: []float64{10, 0.02},
        GeneStep:         []float64{50, 0.05},
        IsCritical:       false,
        Description:      "月度注入金额与 DeadBTC 释放回撤阈值",
    },
}
```

- 所有 13 维都被覆盖
- 每段维度数 ∈ [2, 4]，满足上游 §7.4"2–10 个维度"约束
- `IsCritical=true` 的段（前 3 段）会进入 OAT 邻域稳定性测试（Phase 11 之三期）

### 4.3 Clamp 顺序（上游 §7.5 三段式）

```
步骤 1（边界裁剪）: 每维 ClipFloat64(gene[i], lo[i], hi[i])
步骤 2（块内约束）:
    - feature_periods: 取整 + 若 short ≥ long 则 long = short + 1，再截到 hi
    - signal_weights:  无块内约束（v1 不做 L2 归一）
步骤 3（跨段约束）:
    - 暂无（v1 不做跨段约束）
```

**v1 不做 L2 归一**的设计理由（决策 #6 评审记录）：

1. **保护 CLAUDE.md §10.1 必落测试 #6 `TestCrossoverBlockFidelity`**——归一会让 Clamp 后的 signal_weights 段不再字节级等于父代源段（被 norm 改写过），等于在引擎层引入了一个"块级交叉非保真"的例外。v1 阶段优先保护测试合约。
2. **保证 mutation 局部性**——归一让 `Mutate(a₁)` 隐式扰动 a₂、a₃，破坏 GA 的"per-dim 独立高斯"假设，配置的 `GeneStep` 实际步长缩水 30-60% 且非平稳。
3. **Fingerprint 量化稳定**——归一会让 a₃ 的微小变化跨桶推动 a₁ 的量化结果，让"基因型身份证"不稳。

代价：β 与 signal_weights 之间存在 redundant ridge（同一 Exponent 对应一根连续参数曲线），GA 可能在山脊上浪费 5-10 代收敛预算。可接受。

**人类可读性问题**（β 的语义被 magnitude 混淆）通过**报告生成器**解决：报告时算 `direction = a/||a||₂, intensity = β·||a||₂`，**不强求基因自身归一**。

### 4.3.x v2 何时引入归一（见 §13 演进路径）

当下列任一条件满足时，sigmoid_v1 → sigmoid_v2 升级，**届时引入 L2 归一**：

- Champion 库规模超过 ~50 个，需要做跨 Champion 聚类 / 相似度去重
- 跨版本（signal 维度数变化）对比需求出现
- GA 在 30 代预算内反复无法收敛、诊断显示原因是 β-signal_weights 山脊

v2 引入归一时**必须同步重定义** `TestCrossoverBlockFidelity` 为 cosine-similarity 比较（不再是 `reflect.DeepEqual`）。

### 4.4 Validate（硬边界）

Validate 必须保证 Clamp 后的 Gene 满足：

- 所有维度落在 §4.1 表格的范围内
- `ema_short_period < ema_long_period`
- `mav_short_period < mav_long_period`

（v1 不再做 `||a||₂ ≈ 1` 检查——归一已经移除，见 §4.3）

Validate 失败时返回明确的 `error` 描述哪一项违反（用于 `TestClampValidateContract` 调试）。

### 4.5 Fingerprint

每维按 `QuantizationStep` 量化后拼接成字符串再 SHA256：

```
fp = SHA256("seg0:" + quantize(g[0],0.05) + "," + quantize(g[1],0.05) + ... + "|seg1:" + ...)
```

具体串格式以 toy.go 的实现为参考，保持跨策略一致。

---

## 5. 设计空间四：DeadBTC 释放规则

> 上游：`docs/策略数学引擎.md` §6 —— `ReleaseIntent` 不下发 Agent，账本翻账。

### 5.1 触发条件

```
NAV_t   = (DeadBTC + FloatBTC) × Price_t + USDT
peak    = max(NAV_τ for τ ∈ [t-N, t])    (滑动 N 根 bar 的最高 NAV)
drawdown = (peak - NAV_t) / peak
```

当 `drawdown > release_drawdown_threshold` **且**距上次 Release ≥ 7 天时，触发释放。

- 滑动窗口 30 天（**duration**，非 bar 数）；对应的 bar 数 `N = 30天 / barIntervalMs`，由 §8.2 公式动态算
- 7 天冷却期防止连续释放导致 DeadBTC 被快速掏空（同样 duration，不是 bar 数）
- `peak` 在 `RuntimeState` 内增量维护（不每次重扫历史）

**滑窗未满期的行为**（决策 #8 副作用）：Step() 第一次运行后 30 天内，滑窗里的 NAV 都从初始 0 起涨，`peak` 几乎等于 `NAV_t`，`drawdown ≈ 0`——这期间释放规则**等效失效**。这是有意为之：

- 在 6m 评估窗口里，前 30 天 / 180 天 = 17% 期无效，可接受
- 在 2y / 5y / 10y 窗口里几乎可忽略
- 缩短滑窗能减少这段无效期但会让 drawdown 噪音放大；延长会让 6m 窗口里释放规则形同虚设

### 5.2 释放数量

```
release_qty = min(
    DeadBTC × 0.10,                          // 不超过 DeadBTC 的 10%
    FloatBTC × 0.20                          // 不超过当前 FloatBTC 的 20%
)
```

第二项约束防止释放后 FloatBTC 占比过高，破坏 Sigmoid 的目标权重逻辑。

### 5.3 输出形式

```go
ReleaseIntent{
    NowMs:    input.NowMs,
    Quantity: release_qty,
    Reason:   "drawdown_" + fmt.Sprintf("%.2f", drawdown),
}
```

### 5.4 不变式

- `Quantity ≤ DeadBTC`（上游 §6.3）
- `Quantity ≥ 0`
- 单次 Step() 最多产生 **1 个** `ReleaseIntent`（避免多次释放同一回撤）

---

## 6. RuntimeState 内部 Schema

`RuntimeState` 由 `sigmoid_v1` 独占，SaaS 侧不解读。v1 schema：

```go
type RuntimeState struct {
    SchemaVersion        int     `json:"schema_version"`        // v1=1
    LastMacroBuyMs       int64   `json:"last_macro_buy_ms"`
    LastReleaseMs        int64   `json:"last_release_ms"`
    NAVPeakWindowMs      []int64 `json:"nav_peak_window_ms"`    // 滑窗时间戳
    NAVPeakWindowValue   []float64 `json:"nav_peak_window_value"`  // 滑窗 NAV
}
```

- 滑窗滚动维护，bar 数 = `30天 / barIntervalMs`（§8.3，不再硬编码）；超出窗口的旧值被剪掉
- `SchemaVersion` 用于未来字段演进；Step() 启动时如发现 schema 不匹配，应能优雅降级（v1 阶段：直接重置为空 state，记日志）

### 6.1 Adapter.Reset 行为

`Reset(plan)` **必须**清空 `RuntimeState`。上游 §8.4 明确禁止 `Reset` 携带与 Gene 相关的缓存——这是确定性的基础。

---

## 7. Step() 主流程

伪代码（精确实现见 Phase 4c）：

```
func Step(input StrategyInput) StrategyOutput {
    rs := decodeRuntimeState(input.RuntimeState)

    // 1. 解码染色体
    params := decodeChromosome(input.Chromosome)

    // 2. 计算市场状态
    closes := input.Closes
    mavShort := MAVAbsChange_window(closes, params.MAVShort)
    mavLong  := MAVAbsChange_window(closes, params.MAVLong)
    volRatio := clip(mavShort / mavLong, 0.1, 3.0)
    isQuiet  := volRatio < params.QuietThreshold
    var marketBetaMul = 1.0
    if isQuiet { marketBetaMul = 0.3 }

    // 3. 计算 Signal（无量纲）
    emaLong := EMA(closes, params.EMALong)
    priceDeviation := (closes[last] - emaLong[last]) / emaLong[last]
    logReturn      := log(closes[last] / closes[last - params.MAVShort])
    volRatioCentred := volRatio - 1.0
    signal := params.A1*priceDeviation + params.A2*logReturn + params.A3*volRatioCentred

    // 4. 微观 Sigmoid 调仓
    totalEquity   := (input.Portfolio.DeadBTC + input.Portfolio.FloatBTC) * price + input.Portfolio.USDT
    currentWeight := input.Portfolio.FloatBTC * price / max(totalEquity, ε)
    effectiveBeta := max(0.01, params.Beta * marketBetaMul)
    invBias       := clip(currentWeight, 0, 1) - 0.5
    exponent      := effectiveBeta * signal + params.Gamma * invBias
    targetWeight  := clip(1.0 / (1.0 + math.Exp(exponent)), 0, 1)
    deltaWeight   := targetWeight - currentWeight
    theoreticalUSD := deltaWeight * totalEquity

    // 5. 楔形过滤（粉尘订单）
    microOrders := []OrderIntent{}
    if abs(theoreticalUSD) >= minOrderUSD {
        microOrders = append(microOrders, makeMicroOrder(...))
    } else if !isQuiet && wedgeBreak(deltaWeight, volRatio) {
        // 强制最小订单
    }

    // 6. 宏观引擎
    macroOrders := []OrderIntent{}
    if shouldMacroBuy(input.NowMs, input.LastProcessedBarTime, rs.LastMacroBuyMs) {
        macroOrders = append(macroOrders, makeMacroOrder(params.MacroInjectUSD))
        rs.LastMacroBuyMs = input.NowMs
    }

    // 7. DeadBTC 释放
    releaseIntents := []ReleaseIntent{}
    updateNAVPeakWindow(&rs, input.NowMs, totalEquity)
    drawdown := (rs.peakNAV() - totalEquity) / rs.peakNAV()
    if drawdown > params.ReleaseDrawdownThreshold && cooldownExpired(rs.LastReleaseMs, input.NowMs, 7*day) {
        releaseQty := computeReleaseQty(input.Portfolio, params)
        if releaseQty > 0 {
            releaseIntents = append(releaseIntents, ReleaseIntent{...})
            rs.LastReleaseMs = input.NowMs
        }
    }

    return StrategyOutput{
        MacroOrders:    macroOrders,
        MicroOrders:    microOrders,
        ReleaseIntents: releaseIntents,
        RuntimeState:   encodeRuntimeState(rs),
        DebugSnapshot:  debugSnapshot(signal, targetWeight, marketState),
    }
}
```

### 7.1 铁律合规

| 铁律 | 合规检查 |
|---|---|
| 1 (Step 同构) | 无 `if isBacktest` 分支；Step() 不依赖 `os.Getenv` 等 |
| 2 (NowMs 唯一时间源) | 所有时间逻辑用 `input.NowMs`、`input.Timestamps`；无 `time.Now()` |
| 摩擦 | Step() 内不读 `TakerFee`/`Slippage`；订单 `QuantityUSD` 是理论值 |
| 不导入 domain.Bar | 仅用 `input.Closes` / `input.Timestamps` |

---

## 8. MinEvalBars 与数据粒度

策略涉及三类时间相关量，**只有第二类与 bar 粒度耦合**——策略层的处理必须区分清楚：

| 类 | 形式 | 粒度依赖 |
|---|---|---|
| A. 真"时间"量 | NowMs / LastMacroBuyMs / 死线 60 天 / 冷却 7 天，**全部用毫秒比较** | 粒度无关 ✓ |
| B. "Bar 计数"量 | MinEvalBars 返回值、NAV peak 滑窗的 bar 数 | **由 `barIntervalMs` 动态算** |
| C. 染色体周期 | `ema_long_period`、`mav_long_period` 等（基因里的"bar 数") | **粒度绑定**（见 §13 铁律） |

### 8.1 策略构造接受 `barIntervalMs`

```go
type Sigmoid struct {
    barIntervalMs int64   // 60_000 (1m) / 3_600_000 (1h) / 86_400_000 (1d) …
}

func New(barIntervalMs int64) *Sigmoid {
    return &Sigmoid{barIntervalMs: barIntervalMs}
}
```

`barIntervalMs` 由调用方（task 创建路径或 cmd 层）从 `EvolutionTask` 的 `interval` 字段解码后注入。

### 8.2 `MinEvalBars` 动态计算

```go
func (s *Sigmoid) MinEvalBars() int {
    const navPeakDurationMs = int64(30) * 24 * 60 * 60 * 1000  // §5.1 滑窗时长
    navPeakBars := int(navPeakDurationMs / s.barIntervalMs)
    const maxChromosomePeriod = 300  // ema_long_period 上界（§4.1）
    if navPeakBars > maxChromosomePeriod {
        return navPeakBars + 1
    }
    return maxChromosomePeriod + 1
}
```

代入各粒度：

| 粒度 | navPeakBars | maxChromosomePeriod | MinEvalBars |
|---|---:|---:|---:|
| 1m  | 43,200 | 300 | **43,201** |
| 5m  |  8,640 | 300 |  **8,641** |
| 1h  |    720 | 300 |    **721** |
| 4h  |    180 | 300 |    **301**（被染色体周期主导） |
| 1d  |     30 | 300 |    **301** |

### 8.3 NAV peak 滑窗 bar 数同样动态

§6 `RuntimeState.NAVPeakWindow*` 切片的长度上限 = `int(navPeakDurationMs / s.barIntervalMs)`，**不是固定 43200**。每次 `Step()` 时滚动滑窗。

### 8.4 染色体周期保持以 bar 数为单位（v1 设计选择）

`ema_long_period ∈ [50, 300]` 等基因维度的语义**就是 bar 数**，不做 duration 抽象。原因：

- 跨粒度的 EMA 含义本来就不同（1m × 200 bar = 3.3 小时；1h × 200 bar = 200 小时），同一个染色体在不同粒度下是不同策略
- v1 不试图做"粒度可移植染色体"——交由 GA 在每个粒度上分别搜索
- v2+ 如要可移植，把这些字段改成 duration（毫秒）+ Step() 内除 interval 转 bar 数

**铁律（§13）**：sigmoid_v1 的 Champion Gene 仅在其创建时的数据粒度下有效，不得跨粒度 Promote。Promote 校验里必须比对 `EvolutionTask.interval == Champion.spawn_interval`。

---

## 9. 资产记账模拟器（Adapter 内部）

上游 §4.1 说"宏观成交后仓位记入 `DeadBTC`"——这件事**不发生在 Step() 内**。它发生在 `internal/adapters/backtest/` 的撮合模拟器里。该模拟器需要：

- 接收 `StrategyOutput.MacroOrders` / `MicroOrders` / `ReleaseIntents`
- 按 `domain.FrictionParams` 扣除摩擦（用 `quant.ApplyBuyFriction` / `ApplySellFriction`）
- 更新 `PortfolioSnapshot`（DeadBTC / FloatBTC / USDT 三态守恒）
- 把更新后的 `Portfolio` 喂给下一根 bar 的 Step()

具体设计在 Phase 4d 一并产出。**必须有的不变式**：

- 任意时刻 `DeadBTC + FloatBTC + ColdSealedBTC ≥ 0`
- `USDT ≥ 0`（订单余额不足时跳过，绝不允许负余额）
- `ReleaseIntent` 处理前后 `DeadBTC + FloatBTC` 之和不变（账本守恒）
- 摩擦从 `USDT` 与 `FloatBTC` 实际扣除；fee 不影响 DeadBTC 内部数量（DeadBTC 是注入后的 BTC 量，已经扣完 fee）

---

## 10. 测试矩阵（Phase 4 内部）

实施 Phase 4 时必须配套写的测试，**优先**于上游 CLAUDE.md §10.1 列出的 12 个必落测试：

| 测试 | 目的 |
|---|---|
| `TestChromosomeClampIdempotent` | Clamp(Clamp(g)) == Clamp(g) |
| `TestChromosomeValidateAfterClamp` | Clamp 后 Validate 必过 |
| `TestSignalWeightsRangeRespected` | 三个 a 维度 Clamp 后落在 `[-1, 1]`（v2 引入归一后改 cosine 测试） |
| `TestSegmentsCoverage` | 所有 13 维都在某个 Segment 中 |
| `TestStepNoWallClock` | grep 检查 Step() 内无 time.Now/time.Since |
| `TestStepDeterministic` | 同输入两次 Step() 输出字节相同 |
| `TestMarketStateRecomputable` | 不依赖 RuntimeState 即可重算 MarketState |
| `TestMacroBuyCadence` | 月度触发 + 死线兜底各自单测 |
| `TestReleaseRespectsCooldown` | 7 天冷却期严格遵守 |
| `TestReleaseQuantityBounds` | release_qty ≤ DeadBTC × 0.1 且 ≤ FloatBTC × 0.2 |

上游 12 个必落测试中：
- `TestEvaluateDeterministic` / `TestEvaluateOrderInvariance` / `TestAdapterResetIsolation` / `TestClampValidateContract` / `TestSegmentsCoverage` / `TestCrossoverBlockFidelity` / `TestReplayWithinTolerance` / `TestGapHandlingNoFakeTrades` —— **这八个**通过 sigmoid_v1 实施，自然就有了覆盖
- 其余四个属于 GA engine / 哈希层，不在 sigmoid_v1 范围

---

## 11. `[INVENTED v1]` 决策清单（需评审）

汇总本文档中所有未经业务校准、由实施者主观决定的取值。冻结条件标注于每条末尾。

| # | 决策 | 取值 | 冻结条件 |
|---:|---|---|---|
| 1 | 市场状态数 | 2 态（quiet / active） | 跑通 v1 后看是否需要细分 |
| 2 | `MarketBetaMultiplier(quiet)` | 0.3 | 叠加在 §5.5 粉尘归零之上的第二层抑制；跑通后若安静期仍误调仓→缩到 0.1，若完全沉默→扩到 0.5；**不进染色体**（语义与粉尘归零重叠） |
| 3 | 月度触发用 UTC 月历 | UTC | 与 Binance 数据时区对齐 |
| 4 | 死线兜底窗口 | 60 天 | 允许 1 次月度跳过；与 #5 形成"半量 × 半周期 ≈ 月度速率"自洽；改任一项需同步另一项 |
| 5 | 死线兜底注入比例 | 0.5 × `macro_inject_usd` | 真实数据回测后调 |
| 6 | `signal_weights` 归一与范围 | **不归一**，范围 `[-1, 1]` | v2 升级时引入 L2 归一（见 §4.3.x / §13） |
| 7 | EMA/MAV 周期范围 | ema [5,100]/[50,300]，mav [5,50]/[30,250] | sweep 后定；上界改动需同步 §8.2 `maxChromosomePeriod` |
| 8 | NAV peak 滑窗 | 30 天（duration） | 与 monthly DCA 节奏对齐；缩到 14 天加噪、扩到 90 天在 6m 窗口失效；与 #9 冷却期保持 4:1 比例 |
| 9 | 释放冷却期 | 7 天 | 跑通后看是否过紧 |
| 10 | 释放数量约束 | DeadBTC×0.10 且 FloatBTC×0.20 | sweep 后定 |
| 11 | `MinEvalBars` 公式 | `max(30天/interval, 500) + 1` | 公式锁定；只在 NAV 滑窗时长或染色体周期上界变更时改 |
| 12 | 染色体维度数 | 13 | 跑通后看 GA 在 13 维上能否稳定收敛；过多/过少都要调 |
| 13 | RuntimeState schema | v1 | 字段任何增删都要 bump schema_version |

---

## 12. 与上游文档的对齐表

| 本文档章节 | 上游对应 | 状态 |
|---|---|---|
| §2 市场状态 | 策略数学引擎 §2 + Coding Plan §I-2.2 | 已填充 |
| §3 宏观引擎 | 策略数学引擎 §4 + Coding Plan §I-2.5 | 已填充 |
| §4 染色体 | 策略数学引擎 §7 + Coding Plan §I-2.7 + 框架文档 §2.2 | 已填充 |
| §5 DeadBTC 释放 | 策略数学引擎 §6 + Coding Plan §I-2.6 | 已填充 |
| §7 Step() | 策略数学引擎 §8 + Coding Plan §I-2.8 | 伪码已给，正式实现见 Phase 4c |
| §10 测试 | CLAUDE.md §10.1 + Phase 11 | 已映射 |

---

## 13. 版本与演进

| 版本 | 日期 | 变更 |
|---|---|---|
| v1 | 2026-05-18 | 初版草案，13 维染色体，2 态市场，月度 DCA + drawdown release |
| v1.1 | 2026-05-18 | 决策 #11 修订：`MinEvalBars` 改为按 `barIntervalMs` 动态计算（不再硬编码 43201）；染色体周期保持 bar 数单位但**仅在创建粒度下有效**，跨粒度 Promote 禁止 |
| v1.2 | 2026-05-18 | 决策 #6 修订：`signal_weights` 取消 L2 归一，范围 `[-2,2]` 压到 `[-1,1]`；归一推迟到 sigmoid_v2，触发条件见 §4.3.x |
| v1.3 | 2026-05-18 | 决策 #7 修订：EMA/MAV 周期范围收紧——ema_short `[5,200]`→`[5,100]`，ema_long `[50,500]`→`[50,300]`，mav_short `[5,100]`→`[5,50]`，mav_long `[50,500]`→`[30,250]`；§8.2 `maxChromosomePeriod` 500→300，4h/1d bar 上 MinEvalBars 从 501→301 |
| v1.4 | 2026-05-18 | 决策 #8 确认（值不变，30 天保留）；§5.1 补充"滑窗未满期等效失效"副作用说明；§11 冻结条件细化为"与 DCA 节奏对齐 + 4:1 比例约束" |
| v1.5 | 2026-05-18 | 决策 #2 确认（值不变，0.3 保留）；§11 冻结条件细化为"两层防御叠加 + 不进染色体" |
| v1.6 | 2026-05-18 | 决策 #4 确认（值不变，60 天保留）；§11 冻结条件细化为"允许 1 次月度跳过 + 与 #5 协同约束" |

任何**结构性**变更（染色体维度数、Segments 划分、RuntimeState schema、`barIntervalMs` 不再注入策略构造）都视为 v1 → v2 升级，需要：

1. 新建 `sigmoid_v2.md` 文档
2. 新建 `internal/strategies/sigmoid_v2/` 目录
3. `StrategyID = "sigmoid_v2"`（不复用 v1 的 ID）
4. v1 仍然保留，可对比回测

仅参数**取值调整**（不改维度、不改 schema）通过更新本文档完成，并在 §13 表格追加一行说明改了什么、为什么改。
