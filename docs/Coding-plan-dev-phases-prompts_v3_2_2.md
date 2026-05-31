# QuantLab 从零复刻完整构建 Plan (v3.2)

> **本版与 v3.1 的差异(基于搭档对基线文档的硬伤诊断)**
>
> v3.2 是配合三份基线文档升级而做的同步修订:
> - 《Go struct 冻结版定义草案》v2 → **v3**
> - 《进化系统 Go-only JSON Schema》v5.3.2 → **v5.3.3**
> - 《进化系统 程序框架规划》v5.4 → **v5.4.1**
>
> 基线升级解决三处真硬伤,coding plan 同步对齐。共 5 处修订(M14–M18):
>
> | 编号 | 章节 | 修订要点 |
> |---|---|---|
> | M14 | II-3.4 + II-3.5 + Phase 5A | **拆分两阶段 result**:策略 Adapter / EvolvableStrategy.Evaluate 返回 `*RawEvaluateResult`(仅 `Windows []CrucibleResult` + `FrictionActual`,**物理上不含 `ScoreTotal`**);引擎组装 `EvaluationLayer`(含 `ScoreTotal`)写入结果包。类型系统强保证策略不能写 `ScoreTotal`。**取代 v3.1 M01 的"靠注释和共享函数"软约束** |
> | M15 | II-3.2 + Phase 5B + II-3.6 | **`DecisionStatus` 枚举重命名 `approved` → `promoted`**。三态变为 `pending / promoted / rejected`。语义对齐"已晋升为 Champion"的状态机真实终态 |
> | M16 | II-3.1 + Phase 5D + Phase 2 | **`GAConfigSnapshot` 字段语义改为"生效值"**(非请求镜像)。`test_mode=true` 时 `taker_fee_bps`/`slippage_bps` 直接写入 0,消除快照内的逻辑自相矛盾。**删除 v3.1 GeneRecord 中冗余的 `TakerFeeBpsActual`/`SlippageBpsActual` 字段**;请求原始值(意图)由 `EvolutionTask` 表单独记录 |
> | M17 | II-3.4 + II-3.5 | **基线文档注释加固**:框架文档 §5.1 / §5.6 明确两个 Evaluate 的语义边界与职责分工;Go struct 草案对 `Bar` 结构标注"`IsGap`/`GapType` 是元数据字段,**不参与 `bars_hash` 与持久化哈希**" |
> | M18 | II-3.7 + Phase 0 + Phase 10 | **`schema_version` 升级到 `v5.3.3`**(对应基线 schema 同步升级);常量包 `versions.go` 同步更新;启动时校验三件套版本号与基线一致 |
>
> ---
>
> **本版与 v3 的差异(继承自 v3.1,搭档审阅意见的修订)**
>
> v3.1 是 v3 的精修补丁,**结构与 Phase 划分完全不变**;修订集中在与三份基线契约对齐的若干细节,以及消除若干歧义表述。共 13 处修订(12 处实质修订 + 1 处仅补充测试)。
>
> | 编号 | 章节 | 修订要点 |
> |---|---|---|
> | M01 | II-3.4 EvolvableStrategy 接口表 | ~~`Evaluate` 返回类型由 `[]CrucibleResult` 改为 `*EvaluateResult`~~ **v3.2 进一步修订为 `*RawEvaluateResult`(M14)** |
> | M02 | I-3.6 / I-3.7 / I-3.9 / Phase 5B / Phase 5C | **窗口权重固定不重新归一化**;两类窗口缺失语义分开:plan 构建期不足 → 不创建该窗口;评估期不足 `MinEvalBars()` → `SliceScore.Fatal=true, reason="insufficient_bars"`(Fatal 传染) |
> | M03 | I-3.7 + 结果包字段 | 分量(`monthly_score` / `weekly_score` / `base_score` / `turnover_penalty`)写入 **`CrucibleResult.AlphaBreakdown`**(json.RawMessage,二期收紧) |
> | M04 | I-3.8 + Phase 5B | 强化:**评估循环必须按 6m → 2y → 5y → 10y 固定顺序执行**,违反会让 `SkippedBy` 枚举失效 |
> | M05 | I-3.13 + canonical_json.go 注释 | `bars_hash` 序列化范围固化为 **完整 OHLCV + OpenTime** |
> | M06 | I-4.1 / I-4.4 | `DecisionColor` 明确为 **`VerificationLayer.OOSResult.DecisionColor`** 子字段 |
> | M07 | II-3.5 Adapter Reset 语义 | **保留缓存必须与具体 Gene 无关**;任何依赖 Gene 内容的缓存必须清空;`TestAdapterResetIsolation` 必须覆盖该约束 |
> | M08 | II-3.7 版本治理 | 版本号集中导出至 **`/internal/resultpkg/versions.go`** 常量包;fitness 公式通过 **`FitnessCalculator` 接口**注入抽象层 |
> | M09 | Phase 2 GeneRecord 定义 | `DecisionStatus` 严格为 **PromoteLayer 三态镜像**;~~`retired`~~ 永远不写入 GeneRecord,由 ChampionHistory 表独立管理 (**v3.2 M15 进一步将 `approved` 改为 `promoted`**) |
> | M10 | 全文 + Phase 5B + Phase 11 | 删除 **`friction_disabled`** 独立字段;统一通过 **`core.ga_config.test_mode == true`** 推导;`EvaluationResult.FrictionActual` 记录实际生效摩擦值;`TestFrictionDisabledNotPromotable` 重命名为 `TestTestModeNotPromotable` (**v3.2 M16 进一步要求 `GAConfigSnapshot` 中摩擦字段也存生效值,删除 GeneRecord 中冗余 Actual 字段**) |
> | M11 | Phase 5B + I-4.2 | **DSR 写入 `VerificationLayer.DSRSummary`**,不再提及 PromoteLayer 关联字段 |
> | M12 | Phase 11 | 11 条**必落测试**(对齐框架文档 §10.1)与 9 条**扩展测试**分层标注;补充 **`TestCompareFitnessFingerprintCollision`**;明确**所有排序必须用 `sort.SliceStable`,禁止 `sort.Slice`** |
> | M13 | 附录 A | 新增 v3 → v3.1 行 |
>
> ---
>
> **本版与 v2 的差异（顶层视角,继承自 v3）**
>
> 1. **结构重组**:全文划分为三大部分 —— **Part I 数学/算法/策略规格**、**Part II 软件系统需求与编码计划**、**Part III 未来版本迭代**。Part I 与 Part II 严格按"做什么 / 怎么做"切分;Part III 收纳所有不进入原型阶段的能力。
> 2. **对齐三份新基线**:以《进化系统_程序框架规划_v5_4_1》、《进化系统_v5_4_Go-only_JSON_Schema_v533》(内版号 v5.3.3)、《Go_struct_冻结版定义草案_v3》为契约基线。EvolvableStrategy 接口由 9-verb 升级到 **14-verb**;`Verify` → **`ReviewBacktest`**;四窗口由 `6m/2y/5y/full` 改为 **`6m/2y/5y/10y`**;OOS 简化为 **Anchored Holdout**(取消 `embargo_days`)。
> 3. **新增机制**:**级联短路**(6m→2y→5y→10y 命中 Fatal 立即终止)、**`CompareFitness` Sum-type 排序**(禁止 nil 解引用与哨兵数值)、**`plan_hash` / `bars_hash`**(SHA256(canonical JSON))、**Adapter 状态隔离接口**(`NewAdapter / Reset / Close`)、**`fatal_audit_sample_rate`** 5% 抽样审计。
> 4. **结果包冻结**:`ChallengerResultPackage` 固化为五层 `core / evaluation / verification / diagnostics / promote`;`PromoteLayer.decision_status` 仅三态(v3 基线为 `pending / approved / rejected`,**v3.2 M15 重命名为 `pending / promoted / rejected`**);`retired` 由 `champion_history` 表独立管理;版本三件套 `schema_version="v5.3.3"` / `fitness_version="v1-raw-std"` / `fingerprint_version="fp-v1"`。
> 5. **未来化项**:v2 的 Phase 12 Web 前端、Phase 14 AI 信号层、Phase 9.5 大部分 Prometheus 矩阵、ReviewBacktest 全量历史回顾、Sacred Holdout、邻域稳定性、种群快照、Audit API、Diversity Rescue 第二层 —— 全部移入 Part III。

---

# 阅读地图

本文档分三部分。**强烈建议按部分顺序阅读，不要跨部分跳跃。**

| 部分 | 回答的问题 | 适合谁读 |
|---|---|---|
| **Part I — 数学 / 算法 / 策略规格** | "系统该做什么？按什么数学规则做？" | 量化研究员、策略设计者、想理解系统行为的所有人 |
| **Part II — 软件系统需求与编码计划** | "如何用 Go 实现？按什么阶段交付？" | 软件工程师、Cursor / Claude Code 协作者 |
| **Part III — 未来版本迭代** | "原型阶段不做什么？后续阶段补什么？" | 产品路线规划者、技术负责人 |

Part I 不含任何 Go 包路径、SQL、API 路由、Docker 配置 —— 全部是数学与算法语义。Part II 不重新解释数学公式，而是引用 Part I 的章节号。

---

---

# ============================================
# Part I — 数学 / 算法 / 策略规格
# ============================================

# I-1. 系统定位与领域边界

## I-1.1 系统是什么（领域视角）

一款面向量化交易投资者的**全天候智能量化管理工具**：通过华尔街级风控状态机、遗传算法参数寻优，将复杂的动态策略降维成普通人能一键托管的 SaaS 财富水库。

## I-1.2 引擎层 / 策略层 的硬边界

系统在领域上分为两层，这条边界比物理部署边界更重要：

- **引擎层（Engine Layer）**：负责种群生命周期、数据窗口构建、适应度评估调度、结果交付、回放与 Promote 流程。**不得读取**策略内部字段名、信号公式、仓位三态语义。
- **策略层（Strategy Layer）**：负责定义基因语义、约束修复、策略回测行为、具体适应度输入特征与验证逻辑。引擎只通过抽象接口（见 II-3.4 的 14-verb 接口）操作 Gene。

引擎层依赖的契约都在 Part I 定义；策略内部的具体公式不在本文档范围。

## I-1.3 不属于本文档的内容

以下内容应由**策略文档**单独定义，本文档不涉及：

- 染色体字段表（字段名、类型、语义、默认值、边界）
- 具体信号公式（除 I-2.4 Sigmoid 框架本身）
- 仓位状态机
- 策略私有 ReviewBacktest 逻辑
- Segment 划分（哪些字段归属哪个 Segment、理由）

---

# I-2. 策略数学（Step() 域）

## I-2.1 资产结构三态

策略层的资产组合分为三种形态：

- **DeadBTC**：长期沉睡仓位，宏观引擎建仓后进入此态，不参与微观调仓
- **FloatBTC**：浮动仓位，参与 Sigmoid 微观调仓
- **ColdSealedBTC**：冷封存仓位（可选，原型阶段可不实现）

派生公式：

```
TotalEquity        = (DeadBTC + FloatBTC + ColdSealedBTC) × Price + USDT
ReserveFloor       = max(MinReserveUSDT, micro_reserve_pct × TotalEquity)
SpendableUSDT      = max(0, USDT − ReserveFloor)
CurrentMicroWeight = (FloatBTC × Price) / max(MicroEquityBase, ε)
```

`micro_reserve_pct` 默认 0.25，属于可进化染色体字段（详细范围见策略文档）。

## I-2.2 市场状态感知层（设计空间）

**【策略设计空间一】** 由策略文档定义市场状态分类、特征、判断逻辑。

引擎层只要求：

- 必须定义一个名为 **"安静态"** 的状态，用于 I-2.4 微观引擎中的粉尘订单归零判定。
- 状态判断在 `Step()` 内完成，不读取墙钟，所有时间信息来自 `StrategyInput.NowMs`（见 II-1.2 铁律 2）。

## I-2.3 信号与目标函数框架

策略的"市场观点"通过一个无量纲的 `Signal` 标量传递给微观引擎。`Signal` 通常由多个因子线性合成：

```
Signal = a₁·X₁ + a₂·X₂ + a₃·X₃ + ...
```

其中：

- `Xᵢ` 是策略层定义的无量纲特征（如归一化偏离度、动量、波动率比率等）
- `aᵢ` 是合成权重，属于**染色体的一部分**，由 GA 在历史数据上搜索最优值
- `Signal` 正值倾向减仓，负值倾向加仓（与 I-2.4 Sigmoid 公式的方向约定一致）

## I-2.4 Sigmoid 动态天平（微观引擎核心创新）

### 本质：仓位弹簧系统

Sigmoid 动态天平用 Sigmoid 函数实时计算**目标持仓权重**，通过买卖浮动仓使实际权重趋近目标权重。它是一个与信号来源无关的通用框架。

### 核心公式

```
CurrentWeight  = FloatBTC × Price / TotalEquity

EffectiveBeta  = max(0.01, β × MarketBetaMultiplier)
InventoryBias  = clamp(CurrentWeight, 0, 1) − 0.5
Exponent       = EffectiveBeta × Signal + γ × InventoryBias

TargetWeight   = 1 / (1 + e^Exponent), clamp(0, 1)
```

### 公式解读

| 场景 | Signal | Exponent 方向 | TargetWeight | 动作 |
|---|---|---|---|---|
| 信号看空 | 正 | 增大 | < 0.5 | 减仓 |
| 信号看多 | 负 | 减小 | > 0.5 | 加仓 |
| 仓位 > 0.5，γ > 0 | 任意 | 额外增大 | 进一步压低 | 均值回归 |
| 仓位 < 0.5，γ > 0 | 任意 | 额外减小 | 进一步拉高 | 均值回归 |

参数语义：
- **β（激进系数）**：越大调仓越频繁
- **γ（仓位偏置系数）**：`γ=0` 时纯信号驱动，`γ>0` 时叠加均值回归力
- **MarketBetaMultiplier**：来自上层市场状态感知层

### 理论订单与楔形过滤

```
DeltaWeight    = TargetWeight − CurrentWeight
TheoreticalUSD = DeltaWeight × TotalEquity
```

粉尘拦截规则（防止无效小额交易）：

- `|TheoreticalUSD| ≥ 最小阈值`：直接下单
- `|TheoreticalUSD| ∈ (0, 最小阈值)`：仅在**非安静态** **且** 满足楔形突破条件时强制最小订单，否则归零
- 楔形突破条件：`|DeltaWeight| ≥ 阈值` 或 `VolatilityRatio ≥ 阈值`
- `VolatilityRatio = clip(MAV短期 / MAV长期, 0.1, 3.0)`，MAV 为平均绝对涨跌（非 ATR）

## I-2.5 宏观引擎（设计空间）

**【策略设计空间二】** 由策略文档定义。引擎层只约定：

- 长期建仓节奏，原则上**只买不卖**
- 死线兜底机制（极端市况下的强制建仓回退）
- 宏观成交后将仓位记入 `DeadBTC`，不进入 `FloatBTC` 微观调仓循环

## I-2.6 DeadBTC 释放规则

**【策略设计空间四】** 由策略文档定义 DeadBTC → FloatBTC 的释放条件与节奏。

引擎层约定：释放意图通过 `StrategyOutput.ReleaseIntents` 返回，**不向 Agent 下发**为交易指令；由 SaaS 侧执行账本翻账操作。

## I-2.7 可进化参数契约（Chromosome 设计空间）

**【策略设计空间三】** 由策略文档定义。必须包含：

- 字段清单（名称 / 类型 / 语义 / 默认值 / 边界）
- 硬边界（Clamp 修复规则）
- 结构约束（块内约束、权重和归一化、互斥项等）
- **染色体（参与代内交叉变异）vs SpawnPoint（Epoch 级冻结，不进入基因组）** 的清晰区分

### Segment 划分（与 GA 块级交叉强相关）

策略必须通过 `Segments()` 接口返回 `[]SegmentInfo`，定义"语义耦合基因块"：

```
SegmentInfo {
    Name              string    // 唯一命名，例如 "macro_dca", "micro_dynamics"
    Dimensions        []int     // 该 Segment 内的基因维度索引
    QuantizationStep  []float64 // 各维度 Fingerprint 量化精度
    GeneStep          []float64 // 各维度 Mutate 步长
    IsCritical        bool      // 是否参与邻域稳定性 OAT 测试（三期）
    Description       string    // 文档说明
}
```

约束：

- 所有基因维度必须被覆盖，任一维度只能出现在一个 Segment 中
- `len(Dimensions) == len(QuantizationStep) == len(GeneStep)`
- Segment 顺序必须稳定（同一实例生命周期内 `Segments()` 返回相同结果）
- 每个 Segment 含 2–10 个维度；单维度 Segment 等价于无块结构，应避免

## I-2.8 StrategyInput / StrategyOutput 契约

**StrategyInput 必须包含：**

| 字段 | 类型 | 说明 |
|---|---|---|
| `NowMs` | `int64` | 当前时间戳（毫秒），由调用方注入，`Step()` 内部**唯一时间源** |
| `Closes` | `[]float64` | 收盘价序列 |
| `Timestamps` | `[]int64` | 对应时间戳（毫秒） |
| `Portfolio` | `PortfolioSnapshot` | 资产三态快照 |
| `Params` | `Chromosome + SpawnPoint` | 当前冠军参数包 |
| `LastProcessedBarTime` | `int64` | 上次处理的 bar 时间戳 |

**StrategyOutput 必须包含：**

| 字段 | 类型 | 说明 |
|---|---|---|
| `MacroOrders` | `[]OrderIntent` | 宏观下单意图 |
| `MicroOrders` | `[]OrderIntent` | 微观下单意图 |
| `ReleaseIntents` | `[]ReleaseIntent` | DeadBTC → FloatBTC 转换意图，不下发 Agent |
| `RuntimeState` | 策略内部状态 | 持久化用 |
| `DebugSnapshot` | optional | 调试用，含 `Signal` / `TargetWeight` / `MarketState` 等 |

---

# I-3. 进化算法（GA 域）

## I-3.1 GA 顶层流程与生命周期

单次进化任务（Epoch）的生命周期：

1. 创建任务 → 冻结 SpawnPoint
2. 构建 EvaluablePlan（含四窗口 + OOS Holdout + DCA 双基准 + Friction）
3. 初始化种群
4. 进入代内循环（评估 → 排序 → 精英 → 锦标赛 → 块级交叉 → 变异 → Clamp/Validate）
5. 输出 Challenger
6. 执行 Anchored OOS Holdout 单次验证
7. 等待人工 Promote
8. Champion 替换或保留现任 Champion

## I-3.2 Gene / Segment / SpawnPoint

- **Gene**：固定长度的抽象向量，由策略层定义语义。引擎层不得直接读取内部字段名。
- **Segment**：见 I-2.7。
- **SpawnPoint**：Epoch 级固定外生变量，包含 `CapitalPolicy` / `RiskBounds` / 其他策略运行所需但不进入基因编码的出生点条件。**不参与交叉、不参与变异、不进入 Fingerprint。**

### SpawnMode 枚举（冻结）

| 枚举值 | 语义 |
|---|---|
| `inherit` | 继承当前 champion 的 SpawnPoint |
| `random_once` | 任务排队时随机生成一次并冻结 |
| `manual` | 由请求体中的 `spawn_point` 字段指定 |

## I-3.3 块级正交交叉（Block Crossover）

基于 `Segments()` 实施块级正交交叉，**而不是逐字段混合**：

1. 对每个 Segment **整体**掷 50% 硬币决定父代来源
2. 块内完整继承，不做维度级混合
3. 调用 Clamp（顺序：边界裁剪 → 块内约束修复 → 跨段约束修复）
4. 调用 Validate
5. 失败时回退到父代原样拷贝（随机选 p1 或 p2）并记录 `crossover_fallback` 事件

## I-3.4 变异：独立 Bernoulli + 高斯扰动

```
对每个 Segment seg：
    对每个维度 (localIdx, geneIdx) ∈ seg.Dimensions：
        若 rng.Float64() < prob：
            delta = rng.NormFloat64() × seg.GeneStep[localIdx] × scale
            child[geneIdx] = c[geneIdx] + delta
返回 strategy.Clamp(child)
```

- `prob` 控制变异密度，`scale` 控制幅度，**两者完全独立**
- `scale` 为全局 `GeneStep` 乘数，不依赖单维度差异
- 相关变异（多维高斯采样）属可选增强，推迟到 v6+；原型阶段接口签名中**不预留 `eliteStats` 参数**，避免污染

## I-3.5 Fingerprint 量化哈希

Fingerprint 作为 Epoch 内重复评估缓存键：

- 量化精度来自 `SegmentInfo.QuantizationStep`（按基因语义分维度量化，**不**使用全局统一精度）
- 原型阶段先支持 exact 量化哈希；DSR 近亲判定（fingerprint_distance）可后续引入

`fingerprint_version="fp-v1"` 写入结果包；量化逻辑或哈希算法变更需升级此版本号。

## I-3.6 四窗口坩埚 + Anchored OOS Holdout

### 四窗口结构（冻结）

| Window | Span | Weight |
|---|---:|---:|
| `10y` | 全量最长序列 | 0.40 |
| `5y` | 1825 天 | 0.30 |
| `2y` | 730 天 | 0.20 |
| `6m` | 183 天 | 0.10 |

**实现约束：**

- 权重为系统默认值（0.40/0.30/0.20/0.10 是 √T 加权的近似方便值，所有实现必须使用此预设）
- 评估区间前可附最多 1200 天 warmup 前缀，warmup 不进入评分
- 严禁 future leakage
- **窗口权重固定不重新归一化**（v3.1 修订）。两类"窗口缺失"语义分开处理：
  - **Plan 构建期不足**：在 `BuildCrucibleWindows()` 阶段，若 IS 段长度不足以容纳某窗口的"评估区间 + warmup"，**该窗口不进入 `plan.Windows`**。该 challenger 的 `ScoreRaw` 在数值上自然变小（少了那部分加权贡献），无需重新归一化 —— 这本身就是"样本不足"的合理惩罚信号。
  - **评估期不足**：在 `Adapter.Evaluate()` 阶段，若窗口 bar 数 < `EvolvableStrategy.MinEvalBars()`，该窗口 `SliceScore.Fatal=true, reason="insufficient_bars"`，按 Fatal 传染规则（I-3.8）使 `ScoreTotal.Fatal=true`。这是异常情况(plan 构建未拦截到的边界情形),应当全局 Fatal 而非局部跳过。

### Anchored OOS Holdout

**v2 → v3 修订**：取消 v2 的 `embargo_days` 字段；OOS 采用**固定锚定 Holdout**语义：

- `oos_days` 为整数，从最新 bar 往前推固定天数作为 OOS Holdout
- **Epoch 创建时冻结**，后续 Epoch 不滚动进入未来 IS
- `oos_days = null` 或缺省 = 不启用 OOS
- OOS 段位于 IS 段之后；IS 与 OOS 之间不再需要 embargo 缓冲（Anchored 语义已经保证时间不前进）

## I-3.7 适应度公式与窗口聚合

### 单窗口评分（双基准 min + 绝对/差分回撤）

```
monthly_score    = (ROI_s - ROI_m) - λ_diff × max(0, MDD_s - MDD_m)
weekly_score     = (ROI_s - ROI_wk) - λ_diff × max(0, MDD_s - MDD_wk)
base_score       = min(monthly_score, weekly_score)
                   - λ_abs × max(0, MDD_s - DD_base)²
turnover_penalty = λ_turn × max(0, T_annual - T_extreme)³
slice_score      = base_score - turnover_penalty
```

各分量独立输出至 **`CrucibleResult.AlphaBreakdown`**（v3.1 修订）。原型阶段 `AlphaBreakdown` 类型为 `json.RawMessage`，序列化形如：

```json
{
  "monthly_score": 0.0123,
  "weekly_score":  0.0089,
  "base_score":    0.0089,
  "turnover_penalty": 0.0012
}
```

二期(Part III-5)收紧为正式 struct。

### ROI 口径

- 默认 Modified Dietz
- 单子期间注资比例 > 10% 时切换精确 TWR

### 窗口加权聚合（v3.1 修订:权重固定）

```
ScoreRaw   = Σ_{w ∈ plan.Windows} (Weight_w × SliceScore_w.Value),  按 plan.Windows 固定权重求和,不重新归一化
ScoreTotal = ScoreRaw - λ_cons × ConsistencyPenalty   (除非任一窗口 Fatal)
```

- **权重固定**:`Weight_w` 为 I-3.6 表中的预设值,不因 `plan.Windows` 缺失窗口而重算。如果 plan 只包含 6m 和 2y(IS 段太短),那么 `ScoreRaw` 数值上等于 `0.10×S_6m + 0.20×S_2y`,缺失窗口贡献为 0。
- **Fatal 传染**:任一窗口 `Fatal=true`(MDD 超标 / 数据不足 / 无效路径) → `ScoreTotal.Fatal=true, ScoreTotal.Value=nil`,无论该窗口权重多大。

## I-3.8 Fatal 规则与级联短路（含 CompareFitness）

### Fatal 触发

`MDD_s >= FatalMDD` 时该窗口触发 Fatal。`FatalMDD` 默认 **0.70**。

### 级联短路（v2 → v3 新增 / v3.1 强化）

**默认行为:从短到长 `6m → 2y → 5y → 10y` 级联短路。**

> **⚠ 唯一合法评估顺序(v3.1 强化)**:引擎对 `plan.Windows` 的评估循环**必须按 `6m → 2y → 5y → 10y` 固定顺序**执行,这是 `SkippedBy` 枚举(`cascaded_from_6m` / `cascaded_from_2y` / `cascaded_from_5y`)成立的前提。
> - 6m Fatal → 跳过 2y/5y/10y,三者 `SkippedBy="cascaded_from_6m"`
> - 2y Fatal → 跳过 5y/10y,两者 `SkippedBy="cascaded_from_2y"`
> - 5y Fatal → 跳过 10y,`SkippedBy="cascaded_from_5y"`
> - 10y Fatal → 没有更长窗口可跳,自身 Fatal
>
> 若实现采用其他顺序(如从长到短),`SkippedBy` 枚举将失效、`TestCascadeShortCircuit` 必然失败。

一旦某个窗口触发 Fatal：

- 该窗口 `SliceScore.Fatal = true`
- **立即终止**，跳过后续更长窗口
- `ScoreTotal` 直接记为 Fatal（Sum-type 传染规则）

短路节省约 70% 计算量（早期代 Fatal 率通常 40-60%）。

被短路跳过的窗口语义：`SliceScore.Fatal=false, SliceScore.Value=null, CrucibleResult.SkippedBy="cascaded_from_6m"`（或 `..._2y` / `..._5y`）。

### SliceScore 三态互斥（CrucibleResult 上下文）

| 状态 | `Fatal` | `SkippedBy` | `Value` |
|---|---|---|---|
| 正常评估完成 | `false` | `null` | **非 null** |
| 被级联短路跳过 | `false` | **非 null** | `null` |
| 自身触发 Fatal | `true` | `null` | `null` |

三种状态互斥；任意两个不能同时成立。

### CompareFitness 排序约束（v2 → v3 新增）

`SliceScore.Value` 为 `*float64`，`Fatal=true` 或被短路跳过时为 `nil`。Go `sort.Slice` 对 nil 指针直接解引用会 Panic。

**引擎必须封装专用比较函数，三层优先级：**

```
比较规则：
  Normal > Fatal              // Fatal 个体永远排在最后
  Fatal vs Fatal → 平局       // 用 Fingerprint 字典序做稳定排序
  Normal vs Normal → 按 Value 降序
```

**禁止**向 `Value` 写入 `-99999`、`-1e18` 等哨兵数值规避 nil 判断。对应测试：`TestCompareFitnessNilSafe`。

### Fatal 诊断信息获取

对每代 Fatal 个体随机抽取 `fatal_audit_sample_rate`（默认 **0.05**，即 5%）进行**完整四窗口评估**，结果写入 `diagnostics.fatal_audit_samples`，**不参与本代 GA 排序**。计算代价极低，且不冲突短路。

## I-3.9 一致性惩罚（v1-raw-std）

使用窗口间 dispersion 惩罚：

```
ConsistencyPenalty = λ_cons × σ({SliceScore_w.Value | w ∈ plan.Windows,
                                  Score.Fatal=false, Score.SkippedBy=nil})
```

**Fatal 隔离规则**:任一窗口 Fatal 时直接 `ScoreTotal = Fatal`,**跳过**一致性惩罚(Fatal Sum-type 传染优先)。被短路跳过的窗口(`SkippedBy` 非 nil)也不参与 σ 计算。

**v3.1 修订**:"plan 阶段缺失"的窗口(I-3.6 Plan 构建期不足情形)**根本不在 `plan.Windows` 中**,自然不进入集合;集合大小 < 2 时 σ 退化为 0,一致性惩罚为 0(这是合理的:只有一个或零个有效窗口时谈不上"一致性")。

### 版本号约定

- `fitness_version="v1-raw-std"`：原型阶段使用原始 `SliceScore` 标准差，`λ_cons=0.3`
- `fitness_version="v2-zscore"`：升级到 z-score 标准化方案，`λ_cons=0.02`（消除量纲偏置）

**跨 `fitness_version` 的 challenger 分数不直接比较**，前端必须标注版本标签。

## I-3.10 Ghost DCA 双基准（月度 + 周度）

保留双基准对照：

- **Monthly DCA**：月初注入 `MonthlyInject` 全部买入
- **Weekly DCA**：每 7 自然日注入 `MonthlyInject / 4.33` 全部买入

两个基准必须使用与策略**相同的摩擦参数**（`EvaluablePlan.Friction`），保证对照公平。

## I-3.11 摩擦模型一致性

所有 `SimulateGhostDCA` / `Evaluate` / `OOS` / `MC` 路径必须从**同一份 `FrictionParams`** 读取：

| 字段 | 默认值 | 说明 |
|---|---|---|
| `TakerFeeBps` | 10 (0.10%) | 单边 taker 费率 |
| `SlippageBps` | 5 (0.05%) | 单边滑点 |
| `MakerFeeBps` | nil | 可选 |
| `SpreadBPS` | nil | 可选 |

**`test_mode=true` 时强制 `TakerFeeBps=0, SlippageBps=0`**(v3.1 修订:不再使用独立的 `friction_disabled` 标志;**v3.2 进一步修订(M16)**:`GAConfigSnapshot.taker_fee_bps` / `slippage_bps` 也直接存储生效值 0,消除自相矛盾)。结果包通过 `core.ga_config.test_mode == true` 推导本次评估禁用摩擦,且 `core.ga_config.taker_fee_bps / slippage_bps` 与 `evaluation.friction_actual` 数值一致均为 0。`test_mode=true` 的 challenger **不可 Promote**。

任务原始请求的摩擦意图(如用户请求时 `taker_fee_bps=10`)由 `EvolutionTask` 表的 `requested_taker_fee_bps` / `requested_slippage_bps` 字段单独记录,不进入结果包。

## I-3.12 收敛检测与救援

### 层一：变异斜坡（应对适应度停滞）

- 触发条件：连续 N 代无改进（条件 A）
- 动作：提升 `MutationProbability` 与 `MutationScale`（每次 ×1.25），上限分别为 0.55 和 3.0

### 层二：多样性救援（应对种群塌缩）

- 触发条件：条件 A **且**种群基因熵低于历史滑动均值 50%（条件 B）
- 动作：
  - 从 Top 30 个体中用**贪心 max-min diversity 算法**选出 5 个相互 Fingerprint 距离最大化的核心精英保留
  - 剩余约 98% 按 60% 完全随机 / 25% 对核心精英施加 5×GeneStep 强变异 / 15% 精英杂交后代重新填充
  - 救援后冻结 3 代不再触发收敛检测

> **原型阶段简化**：层一在 Phase 5 落地；层二预留接口但不强制实现（详见 Part III-1）。

## I-3.13 可复现性元数据

每个 Challenger 必须携带完整重放上下文：

| 字段 | 说明 |
|---|---|
| `epoch_seed` | 本次 Epoch 的随机种子 |
| `data_version` | 数据版本快照 |
| `engine_version` | 引擎代码版本 |
| `strategy_version` | 策略代码版本 |
| `schema_version` | 结果包 JSON schema 版本（`v5.3.3`）|
| `fitness_version` | 适应度公式版本（`v1-raw-std`）|
| `fingerprint_version` | 指纹量化逻辑版本（`fp-v1`）|
| `hardware_signature` | 格式 `{GOOS}/{GOARCH}/{CPU型号}`，例 `linux/amd64/Intel-Xeon-E5-2680v4` |
| `go_version` | Go 编译版本 |
| `build_id` | 构建产物 ID |
| `plan_hash` | **SHA256(EvaluablePlan canonical JSON)**，小写 Hex（64 字符）|
| `bars_hash` | **SHA256(全量 K 线序列 canonical JSON)**，小写 Hex（64 字符）|

> **v3.1 修订:`bars_hash` 序列化范围固化为"完整 OHLCV + OpenTime"。** 即对每根 bar 序列化 `{OpenTime, Open, High, Low, Close, Volume}` 六字段(不含 QuoteVolume / NumTrades / Source 等元数据),含 warmup 段,按 OpenTime 升序排列。该约定写入 `internal/quant/canonical_json.go` 文件顶部注释,作为项目级冻结契约。原 v3 附录 C Q4 标记为"待决"的问题在 v3.1 关闭。

**可复现性承诺：容差级**而非位级：

- `ScoreTotal` 差异 ≤ 1e-6
- Chromosome 字段差异 ≤ 1e-9（绝对）
- 跨硬件 / 跨编译产物不做承诺，仅靠 `hardware_signature` 标记边界

---

# I-4. 验证数学（Verification 域）

## I-4.1 OOS 单次验证（Anchored Holdout）

OOS 窗口在 Epoch 创建时**冻结**，使用与 IS 相同的策略代码与摩擦参数，对 OOS 段单次评估并产出 **`VerificationLayer.OOSResult`**(v3.1 修订),其中包含:

- `oos_alpha_monthly`：相对 Monthly DCA 的超额
- `oos_alpha_weekly`：相对 Weekly DCA 的超额
- `decision_color`:`VerificationLayer.OOSResult.DecisionColor` 的四态枚举 `green / yellow / red / gray`(见 I-4.4)

OOS 影响 Promote **参考决策**，但不影响 GA 代内排序。

## I-4.2 DSR（Bailey & López de Prado 2014）

闭式公式：

```
γ_em = 0.5772  (Euler-Mascheroni)
SR₀  = √Var(Sharpe) × [(1 − γ_em) · Φ⁻¹(1 − 1/N) + γ_em · Φ⁻¹(1 − 1/(N·e))]
σ_SR = √[(1 − skew·SR_obs + (excessKurt/4)·SR_obs²) / (T − 1)]
DSR  = Φ((SR_obs − SR₀) / σ_SR)
```

参数说明：

- `N`：SharpeBank 累积样本数（同 `strategy_id × pair`）
- `Var(Sharpe)`：SharpeBank 内 Sharpe 的方差
- `SR_obs`：本次 challenger 的观测 Sharpe
- `T`：回测 horizon（bar 数）
- `skew` / `excessKurt`：策略收益序列的偏度与超额峰度
- `Φ` / `Φ⁻¹`：标准正态 CDF / 逆 CDF

**可靠性阈值**：`N < 5` 时 DSR 返回 NaN（前端展示为"积累中"灰色态）。

DSR 仅为人工 Promote 辅助指标，**不得参与 GA 代内适应度排序**。

## I-4.3 SBB Monte Carlo（Politis-White 自动块长）

按 Politis-White (2004) + Patton-Politis-White (2009) 实现：

1. 用 ACF 计算最多 `lag = ceil(√n + log₁₀(n))` 的自相关
2. 选 `m`：找到首个连续 `k_n = max(5, log₁₀(n))` 个 lag 的 `|acf| < 2·√(log₁₀(n)/n)` 的位置
3. 计算 `g = Σ_{k=-m}^{m} λ(k/m) · |k| · acf[|k|]`（`λ` 为 flat-top window）
4. 计算 `d_SB = 2·(Σ_{k=-m}^{m} λ(k/m) · acf[|k|])²`
5. `b_opt = (2·g² / d_SB · n)^(1/3)`
6. 截断到 `[100, 1440]`，估计失败回退到 `sbb_block_len_fallback=300`

MC 模拟使用稳定 block bootstrap：块长 `~ Geom(1/blockLenMean)`；输出破产概率、5/50/95 分位最终权益、最坏 1% MDD。

## I-4.4 Promote 决策颜色门槛

**字段归属**(v3.1 修订):

- `DecisionColor` 是 **`VerificationLayer.OOSResult.DecisionColor`** 的字段,**不是顶层枚举**,**不属于 `PromoteLayer`**。
- DSR 写入 **`VerificationLayer.DSRSummary`**(v3.1 修订:不再放在 PromoteLayer 关联字段)。
- 前端 Promote 决策面板通过联合查询 `VerificationLayer.OOSResult.DecisionColor` + `VerificationLayer.DSRSummary` 给出展示。

OOS / DSR 通过颜色门槛展示给人工审批者，**不阻断 Promote**：

| 颜色 | 语义 |
|---|---|
| `green` | 各项指标在期望范围内，建议 Promote |
| `yellow` | 部分指标边缘，需审慎 |
| `red` | 多项指标不达标，不建议 Promote |
| `gray` | 数据不足（如 SharpeBank N<5），无法判断 |

具体阈值由二期前端实现（见 III-1）。

---

# I-5. 关键数学参数表

| 模块 | 参数 | 默认值 | 来源章节 |
|---|---|---|---|
| GA 调度 | `PopSize` | 300 | I-3.1 |
| GA 调度 | `MaxGenerations` | 25 | I-3.1 |
| GA 调度 | `EliteRatio` | 0.05 | I-3.1 |
| GA 调度 | `TournamentSize` | 3 | I-3.1 |
| GA 调度 | `MutationProbability` | 0.15 | I-3.4 |
| GA 调度 | `MutationScale` | 1.0 | I-3.4 |
| GA 调度 | `MutationProbabilityMax` | 0.55 | I-3.12 |
| GA 调度 | `MutationScaleMax` | 3.0 | I-3.12 |
| GA 调度 | `MutationRampFactor` | 1.25 | I-3.12 |
| GA 调度 | `EarlyStopPatience` | 5 代 | I-3.12 |
| GA 调度 | `EarlyStopMinDelta` | 0.001 | I-3.12 |
| 适应度 | `FatalMDD` | **0.70** | I-3.8 |
| 适应度 | `λ_diff`（差分回撤系数） | 1.5 | I-3.7 |
| 适应度 | `λ_abs`（绝对回撤系数） | 2.0 | I-3.7 |
| 适应度 | `DD_base`（软惩罚起点） | 0.40 | I-3.7 |
| 适应度 | `λ_cons`（一致性惩罚 v1-raw-std） | 0.3 | I-3.9 |
| 适应度 | `λ_turn`（换手惩罚系数） | 0.1 | I-3.7 |
| 适应度 | `T_extreme`（换手惩罚起点） | 工程经验 | I-3.7 |
| 摩擦 | `TakerFeeBps` | 10 (0.10%) | I-3.11 |
| 摩擦 | `SlippageBps` | 5 (0.05%) | I-3.11 |
| OOS | `OosDays` | 180 | I-3.6 |
| MC | `SbbBlockLenFallback` | 300 | I-4.3 |
| 抽样 | `FatalAuditSampleRate` | 0.05 | I-3.8 |
| 窗口权重 | 10y / 5y / 2y / 6m | 0.40 / 0.30 / 0.20 / 0.10 | I-3.6 |
| 微观 | EMA/σ 窗长 | 21 | 策略侧 |
| 微观 | VolRatio 长窗口 | 112 | 策略侧 |
| 微观 | VolRatio 短窗口 | 16 | 策略侧 |
| 微观 | 最小订单阈值 | 10.1 USDT | 策略侧 |
| 账本 | `micro_reserve_pct` 默认 | 0.25 | I-2.1 |

> **参数治理**：见 II-3.6 的不可调 / 可调分类。`fitness_version` / `fingerprint_version` 变更时分数不直接跨版本比较。

---

---

# ============================================
# Part II — 软件系统需求与编码计划
# ============================================

# II-1. 系统架构与不可推翻约束

## II-1.1 三端物理部署

- **`saas`（云端）**：决策大脑，执行 `Step()`，下发交易指令，**不持有任何 API Key**
- **`agent`（用户本地）**：执行手，只负责调用交易所下单并上报结果，**不含任何策略代码**
- **`lab`（本地算力机）**：实验室，专跑 GA 进化与回测，连同一个 Postgres 实例，**不下发真实交易**

## II-1.2 六条铁律（v2 修订版，对齐 v3 新基线）

在开始前，把这六条铁律贴在显眼的地方。违反任何一条都要立即停下来。

1. **策略同构**：回测与实盘必须调用同一个 `Step()` 实现，内部禁止 `if isBacktest` 分支。
2. **策略纯函数**：`Step()` 内部禁止读取墙钟（`time.Now()` / `time.Since()`）、禁止网络请求、禁止数据库读写、禁止任何文件 I/O。当前时间通过 `input.NowMs` 注入。
3. **API Key 物理隔离**：交易所凭证只存在于 `config.agent.yaml`，永不进入 SaaS 侧。
4. **Schema 双轨制**：开发期使用 `GORM AutoMigrate` 快速迭代，生产期切换到 **Atlas 版本化迁移**。
5. **无量纲计算**：价格相关计算使用对数收益率或比率，禁止跨标的比较绝对价格。
6. **单一 Postgres + TimescaleDB 扩展**：不分库；Redis 仅做缓存。Postgres 启用 TimescaleDB 扩展，对 `klines` 表用 hypertable 与列存压缩。

> **v3 新增的隐含铁律（不进入"六条"，但同等强度）：**
>
> - **CompareFitness 强制**：禁止对 `*float64 SliceScore.Value` 直接解引用排序；禁止写入哨兵数值。详见 I-3.8 与必落测试 `TestCompareFitnessNilSafe`。
> - **Adapter 状态隔离**：每个 worker 必须独占 Adapter 实例，`Reset(plan)` 由引擎在每次 `Evaluate` 前**强制调用**，策略层无法绕过。详见 II-3.5。
> - **RNG 路径**：全引擎 RNG 必须从 `EpochSeed` 派生，禁止直接调用 `rand.Seed()` 或使用全局 `rand.New`。

## II-1.3 数据所有权与单一 Postgres

| 数据所有权 | 存储 | 备注 |
|---|---|---|
| 业务实体（用户、策略实例、订单等）| Postgres | 标准 GORM 模型 |
| 历史 K 线 | Postgres + TimescaleDB hypertable | 7 天 chunk，30 天压缩 |
| K 线缺口 | Postgres `kline_gaps` 表 | 显式记录，禁止 datafeeder 内插值 |
| 冠军基因缓存 | Redis | key: `champion:{strategyID}` |
| 会话 / 热路径 | Redis | TTL 管理 |
| SharpeBank（DSR 累积） | Postgres | 跨 Epoch 累积 |

---

# II-2. 模块划分与代码组织

## II-2.1 顶层目录骨架

```
/cmd
  /saas              # SaaS 主服务入口
  /agent             # 本地 Agent 入口
  /lab               # Lab 模式入口（可与 saas 共享 binary 通过 app_role 切换）
  /datafeeder        # K 线数据导入 CLI
/internal
  /domain            # Gene / SegmentInfo / SpawnPoint / SliceScore / Bar / CrucibleWindow / EvaluablePlan
  /engine            # Epoch 生命周期 / worker pool / 收敛检测 / 种群管理
  /strategy          # EvolvableStrategy 抽象接口 / Adapter 接口
  /strategies/[策略名]   # 具体策略实现（仅 Step() 与 Adapter 实现）
  /fitness           # 单窗口评分 / DCA 双基准 / ScoreTotal 聚合 / 一致性惩罚
  /verification      # OOS / DSR / SBB 等验证流程
  /data              # K 线读取 / Gap 检测 / EvaluablePlan 构建
  /repository        # 数据库存取 / 结果包持久化 / SharpeBank
  /report            # challenger 报告生成 / 诊断输出
  /api               # HTTP handler / 任务创建查询 / Promote / Retire
  /quant             # 数学基础工具（EMA / StdDev / ACF / Friction 等无状态纯函数）
  /resultpkg         # 结果包类型与枚举（边界对象冻结）
  /saas
    /store           # GORM 模型 / db.go / redis.go
    /auth            # JWT 工具
    /config          # 配置加载
    /logger          # 结构化日志
    /metrics         # 基础 Prometheus 指标（最小集）
    /ws              # WebSocket Hub
    /cron            # Cron 调度
    /instance        # 实例生命周期管理
  /agent             # Agent 端实现（订单执行 / 心跳）
  /adapters
    /backtest        # 回测适配器（实现 Adapter 接口）
/tests               # 所有测试（11 条优先测试列表见 II-4.Phase 11）
/research            # Python 研究脚本（不进入服务器主链路）
/docs                # 三份真源文档
```

> **关于命名差异说明**：框架文档 v5.4 使用 `/domain` `/engine` `/strategy` 等顶层包名，本编码计划在 `/internal` 下复用相同语义。两者不冲突，差别只是 Go module 内的具体位置。

## II-2.2 Go-only Server Prototype 的依据

原理验证阶段服务器端**全部使用 Go 实现**，理由：

1. **减少跨语言 schema 对齐成本**：内部模块优先通过 Go struct 与 interface 协作，仅对 HTTP / 数据库 JSON / 结果包等外部边界维持稳定契约。
2. **更容易保证确定性**：避免不同编程语言在浮点、空值、默认值、随机数边界上的差异干扰 `Evaluate` 与 Replay 的容差级一致性。
3. **更容易实现状态隔离**：`Adapter.Reset()`、worker 独占实例、顺序无关性等约束可在单语言运行时内统一实现与测试。
4. **测试体系更集中**：`TestEvaluateDeterministic` / `TestEvaluateOrderInvariance` / `TestAdapterResetIsolation` / `TestCascadeShortCircuit` / `TestCompareFitnessNilSafe` 等关键测试统一纳入 Go 测试体系。

Python 仅限**离线分析与实验辅助**（在 `/research`），不进入在线服务、任务调度、核心评估、结果持久化和 API 服务链路。

## II-2.3 包导入边界（重要）

- **引擎层禁止 import 具体策略实现**。引擎只通过 `EvolvableStrategy` 接口操作 Gene。
- **策略包禁止 import** `/internal/saas/ga`（避免循环依赖）。具体策略的 `Evolvable` 适配器放在 `/internal/saas/ga/[策略名]_evolvable.go`。
- **策略包禁止 import** `quant.Bar` —— 策略内核只依赖 `Closes`、`Timestamps`、`PortfolioSnapshot`。
- **摩擦不出现在策略内** —— 摩擦扣除发生在回测适配器 / Agent 成交回报里，不在 `Step()` 决策里。

---

# II-3. 数据契约（边界对象冻结）

> 本节是 **API 契约 + 结果包契约 + 持久化契约** 的总览。所有 Go struct 定义以《Go_struct 冻结版定义草案 v3》为准；所有 JSON schema 以《进化系统_v5_4_Go-only_JSON_Schema_v533》（内版号 v5.3.3）为准。

## II-3.1 API 请求响应契约

### CreateEvolutionTaskRequest

对应 `POST /api/v1/evolution/tasks`。

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `strategy_id` | string | ✓ | 策略唯一标识 |
| `pair` | string | ✓ | 交易对，如 `BTCUSDT` |
| `pop_size` | int ≥ 1 | ✓ | 种群大小，建议 200-400 |
| `max_generations` | int ≥ 1 | ✓ | 最大代数，建议 25-40 |
| `elite_ratio` | float ∈ [0,1] | ✓ | 精英保留比例 |
| `fatal_mdd` | float ∈ [0,1] | ✓ | Fatal 回撤门槛 |
| `taker_fee_bps` | float ≥ 0 | ✓ | 单边 taker 费率 |
| `slippage_bps` | float ≥ 0 | ✓ | 单边滑点 |
| `spawn_mode` | enum | ✓ | `inherit` / `random_once` / `manual` |
| `test_mode` | **bool** | ✓ | `true` = 烟雾测试（强制 Pop=10/Gen=3，禁用摩擦，结果不可 Promote）|
| `oos_days` | int ≥ 1 \| null | — | Anchored OOS Holdout 天数；`null` = 不启用 OOS |
| `fatal_audit_sample_rate` | float ∈ [0,1] \| null | — | Fatal 抽样比例，默认 0.05 |
| `spawn_point` | object \| null | — | 仅 `spawn_mode=manual` 时使用 |

### EvolutionTaskStatusResponse

对应 `GET /api/v1/evolution/tasks/:task_id`。

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `task_id` | string | ✓ | |
| `status` | enum | ✓ | `queued / running / succeeded / failed / cancelled` |
| `current_generation` | int ≥ 0 | ✓ | |
| `best_score` | float \| null | — | |
| `challenger_id` | string \| null | — | |
| `failure_reason` | string \| null | — | |

### PromoteChallengerRequest / RetireChampionRequest

对应 `POST /api/v1/challengers/:id/promote` 和 `POST /api/v1/champions/:id/retire`。

| 字段 | 类型 | 必填 |
|---|---|---|
| `reviewed_by` | string (minLen 1) | ✓ |
| `decision_note` | string \| null | — |

## II-3.2 结果包契约：ChallengerResultPackage 五层

```
ChallengerResultPackage
├── core              # 策略 ID / 基因载荷 / 复现元数据 / 配置快照 / 三件套版本号
├── evaluation        # 各窗口 CrucibleResult / ScoreTotal / 摩擦实际值 / 间隙统计
├── verification      # OOS 结果 / ReviewSummary (二期) / DSR / Stress (后期)
├── diagnostics       # 救援日志 / Clamp 日志 / 换手指标 / fatal_audit_samples
└── promote           # 决策状态(pending/promoted/rejected,v3.2 修订) / 审批备注 / 审批人
```

### 关键约束

- `core.schema_version="v5.3.3"`、`core.fitness_version="v1-raw-std"`、`core.fingerprint_version="fp-v1"`
- `core.champion_gene.encoding="json"`（原型阶段唯一合法值；未来支持其他编码需升级 `fingerprint_version`）
- `evaluation.window_scores` 必须包含全部四个窗口（被短路跳过的也要在内，`SkippedBy` 非 null）
- `promote.decision_status` 仅三态(v3.2 修订):`pending / promoted / rejected`,**不含 `retired`**(原 v3 基线 `approved` 在 v3.2 M15 重命名为 `promoted`,语义对齐"已晋升为 Champion")
- Champion 退役通过独立接口 `POST /api/v1/champions/:id/retire` 处理，状态写入 `champion_history` 表，不在结果包

## II-3.3 持久化元数据

### ReproducibilityMetadata（详见 I-3.13）

`plan_hash` / `bars_hash` 算法：

```
SHA256(canonical_json(EvaluablePlan)) → 小写 Hex 64 字符
SHA256(canonical_json(K线全序列含 warmup)) → 小写 Hex 64 字符
```

`hardware_signature` 格式：`{GOOS}/{GOARCH}/{CPU型号}`，例 `linux/amd64/Intel-Xeon-E5-2680v4`。仅作为可复现性边界标记，不作为强约束。

### GAConfigSnapshot(v3.2 修订:存生效值)

结果包 `core.ga_config` 必须镜像任务**生效**参数(非原始请求)。**必含 `pair`**(脱离交易对的参数快照无法复现 EvaluablePlan)。

**v3.2 关键修订(M16)** —— 字段语义从"请求镜像"改为"生效值":

| 字段 | v3 (基线 v2) | v3.2 (基线 v3) |
|---|---|---|
| `taker_fee_bps` | 原始请求值(如 10) | **生效值**:`test_mode=true` 时为 0 |
| `slippage_bps` | 原始请求值(如 5) | **生效值**:`test_mode=true` 时为 0 |
| `test_mode` | bool | bool(不变,作为推导锚点) |

**消除自相矛盾**:旧版本中 `ga_config.test_mode=true` 同时 `ga_config.taker_fee_bps=10` 在 JSON 中并存,需要跨字段推导。v3.2 后两者数值上一致:`test_mode=true` ⇒ `taker_fee_bps=0`。

**原始请求意图**:由 `EvolutionTask` 数据库表的 `requested_taker_fee_bps` / `requested_slippage_bps` 字段单独记录,供审计用途;**不进入结果包**。

**与 v3.1 GeneRecord 的关系**:v3.1 引入的 `TakerFeeBpsActual` / `SlippageBpsActual` 冗余字段**在 v3.2 删除**;由于 `GAConfigSnapshot.taker_fee_bps` 本身已是生效值,镜像到 GeneRecord 后名为 `TakerFeeBps` 即可,无需 Actual 后缀。`EvaluationLayer.FrictionActual` 字段保留(作为评估时实际使用值的双重记录,与 ga_config 应严格一致)。

### AuditSampleSummary

```
{
  "sample_id": "...",
  "score_total": { ... },
  "window_scores": [ ... ],    // 可选，含完整四窗口
  "notes": "..."
}
```

## II-3.4 EvolvableStrategy 14-verb 接口（v2 → v3 关键升级）

**v2 的 9-verb 接口已废弃**。v3 对齐框架文档 v5.4 §5.1：

| Method | 责任 |
|---|---|
| `StrategyID()` | 返回策略唯一标识 |
| `Segments() []SegmentInfo` | 返回完整的 SegmentInfo（含 Dimensions / QuantizationStep / GeneStep / IsCritical） |
| `Sample(rng) Gene` | 从合法空间采样 Gene |
| `Clamp(g) Gene` | 修复越界与结构约束（含边界裁剪、权重归一化、互斥项修复、离散档位映射、整数窗口取整）|
| `Validate(g) error` | 检查 Gene 合法性 |
| `Crossover(p1, p2, rng) Gene` | 基于 `Segments()` 实施块级正交交叉 |
| `Mutate(c, prob, scale, rng) Gene` | 独立 Bernoulli + 高斯扰动；`scale` 是全局 GeneStep 乘数 |
| `Fingerprint(c) string` | 基于 `SegmentInfo.QuantizationStep` 量化后哈希 |
| `Evaluate(ctx, c, plan) (*RawEvaluateResult, error)` | 在 plan 上评估基因,返回**纯评估结果**`RawEvaluateResult`(仅含 `Windows []CrucibleResult` + `FrictionActual`,**物理上不含 `ScoreTotal`**);v3.2 修订(M14):取代 v3.1 的 `*EvaluateResult`,通过类型系统强保证策略不能写 `ScoreTotal`;`ScoreTotal` 由引擎在 `RunEpoch` 调用 `fitness.AggregateScoreTotal` 后填充至 `EvaluationLayer` |
| `ReviewBacktest(ctx, c, spawn, bars) error` | 全量历史回顾回测，**仅在 Promote 后调用**，不影响决策 |
| `EncodeResult(c, spawn) ChallengerResultPackage` | 编码结果包 |
| `DecodeElite(blob) Gene` | 从历史精英结果恢复 Gene |
| `MinEvalBars() int` | 返回最低有效 bar 数 |
| `NewAdapter(plan) (Adapter, error)` | 为每个 worker 创建独立 Adapter 实例 |

### 与 v2 的差异点

| v2 | v3 |
|---|---|
| `CrossoverSegments() [][]int` | **`Segments() []SegmentInfo`**（保留量化精度、Mutate 步长、IsCritical 标记） |
| `Verify` | **`ReviewBacktest`**（语义重命名，明确为 Promote 后展示用，不影响决策） |
| 9 个动词 | **14 个动词**（新增 `Clamp` / `Validate` / `Fingerprint` / `MinEvalBars` / `NewAdapter`，明确化原本散落的能力） |
| Mutate 带 `eliteStats` 参数 | **删除**该参数，避免接口污染（相关变异留给 v6+） |

### 两阶段评估结果(v3.2 修订:类型级强保证)

v3.2 通过类型系统强制划分"评估"与"聚合"两阶段:

**阶段 1 — 策略产出(纯评估)**:

```go
// /internal/strategy/types.go
type RawEvaluateResult struct {
    Windows        []CrucibleResult   // 各窗口评估结果(仅 SliceScore,无 ScoreTotal)
    FrictionActual FrictionParams     // 评估时实际生效摩擦(test_mode=true 时为 {0, 0})
    // 不含 ScoreTotal —— 物理上策略不可能填写
}
```

策略侧的 `EvolvableStrategy.Evaluate(...)` 与 `Adapter.Evaluate(gene)` 均返回 `*RawEvaluateResult`。

**阶段 2 — 引擎组装(聚合)**:

```go
// /internal/resultpkg/types.go
type EvaluationLayer struct {
    WindowScores   []CrucibleResult   // 镜像 RawEvaluateResult.Windows
    ScoreTotal     ScoreTotal         // 引擎调 fitness.AggregateScoreTotal 填充
    FrictionActual FrictionParams     // 镜像 RawEvaluateResult.FrictionActual
    // 其他评估元数据(BarsEvaluated 总数、Duration 等)
}
```

`EvaluationLayer` 由引擎在 `RunEpoch` 中组装:

```go
// /internal/engine/engine.go(伪代码)
raw, _ := adapter.Evaluate(gene)
scoreTotal := e.fitnessCalc.AggregateScoreTotal(raw.Windows, weights)
layer := EvaluationLayer{
    WindowScores:   raw.Windows,
    ScoreTotal:     scoreTotal,
    FrictionActual: raw.FrictionActual,
}
// layer 写入 ChallengerResultPackage.Evaluation
```

**职责契约**:

| 阶段 | 谁负责 | 产出类型 |
|---|---|---|
| 单 gene 单窗口评估 | `Adapter.Evaluate(gene)` (worker) | `*RawEvaluateResult` |
| 级联短路 + 多窗口组合 | `EvolvableStrategy.Evaluate(ctx, gene, plan)` | `*RawEvaluateResult` |
| ScoreTotal 计算 | `fitness.AggregateScoreTotal(...)` (引擎共享) | `ScoreTotal` |
| 结果包 evaluation 层组装 | 引擎 `RunEpoch` | `EvaluationLayer` |

策略层永远不接触 `ScoreTotal`,这是类型层面的硬保证。多目标 GA(NSGA-II / Pareto)等未来演进只需扩展 `EvaluationLayer`,策略层零改动。

## II-3.5 Adapter 接口与状态隔离（v2 → v3 新增 / v3.2 签名修订）

```go
type Adapter interface {
    Reset(plan *EvaluablePlan) error            // 引擎在每次 Evaluate 前强制调用
    Evaluate(gene Gene) (*RawEvaluateResult, error)   // v3.2 修订:返回纯评估结果,无 ScoreTotal
    Close() error
}
```

### 引擎调度伪代码

```go
adapter, _ := strategy.NewAdapter(plan)   // Epoch 启动时每个 worker 调用一次
defer adapter.Close()

for gene := range workQueue {
    adapter.Reset(plan)                   // 引擎强制，每次 Evaluate 前
    result, _ := adapter.Evaluate(gene)
    results[gene.index] = result
}
```

### Reset 语义

**必须清空**:持仓态、指标缓存、交易历史缓存、连续亏损等业务计数器,以及**任何依赖 Gene 内容的中间结果**(v3.1 强调)。

**可保留**(复用以提升性能):K 线数据 buffer、DCABaseline 缓存、临时计算 buffer(每次写前自动清空)。

**v3.1 安全边界**:保留项必须是**与具体 Gene 无关的全局共享数据**(只读或临时):

- ✅ K 线序列(由 plan 注入,不因 Gene 变化)
- ✅ DCABaseline(由 plan 预计算,基因无关)
- ✅ 容量预分配但每次写前清零的临时 buffer(如 indicator 计算的滚动窗口)
- ❌ 任何含 Gene 参数(如 β / γ / EMA 窗长)计算后的中间值
- ❌ 任何在前一次 Evaluate 中由特定 Gene 写入的状态

`TestAdapterResetIsolation` 必须覆盖该约束:构造两个差异较大的 Gene A 和 B,断言 `[A, B]` 顺序评估的结果与 `[B, A]` 顺序在容差内一致。

**Reset 不完整会导致评估顺序影响结果，直接破坏容差级确定性承诺。**

## II-3.6 枚举冻结清单(v3.2 修订:`approved` → `promoted`)

```json
{
  "decision_status":     ["pending", "promoted", "rejected"],
  "run_status":          ["queued", "running", "succeeded", "failed", "cancelled"],
  "verification_status": ["not_run", "ok", "failed", "insufficient_data"],
  "decision_color":      ["green", "yellow", "red", "gray"],
  "skipped_by":          ["cascaded_from_6m", "cascaded_from_2y", "cascaded_from_5y"],
  "spawn_mode":          ["inherit", "random_once", "manual"],
  "window":              ["6m", "2y", "5y", "10y"],
  "champion_gene_encoding": ["json"]
}
```

> **v3.2 修订(M15)**:`DecisionStatus` 中 `approved` 改为 `promoted`,对齐状态机真实终态语义 —— 审批通过 = 已晋升为 Champion。`pending → promoted` 是审批 + 升级的合并语义,`pending → rejected` 是否决终态。已 promoted 的 challenger 在 `ChampionHistory` 表有对应记录,后续退役由 `champion_history.retired_at` 管理。

## II-3.7 三件套版本号

```
schema_version      = "v5.3.3"   # 结果包 JSON 结构版本
fitness_version     = "v1-raw-std"  # 适应度公式与参数版本
fingerprint_version = "fp-v1"    # 指纹量化逻辑版本
```

**跨版本兼容规则：**

- `schema_version` 不同：允许字段兼容读取（新系统读旧包填默认，旧系统读新包忽略未知字段）
- `fitness_version` 不同：**不直接比较分数**（公式语义已变），前端必须标注版本标签
- 大版本升级：单独提供迁移脚本

### 版本号常量包(v3.1 新增)

所有三件套版本号集中导出至 `/internal/resultpkg/versions.go`,避免在代码中散落硬编码:

```go
// /internal/resultpkg/versions.go
package resultpkg

const (
    // Schema 版本号(随基线 JSON Schema 升级而升级)
    SchemaVersionV533       = "v5.3.3"   // v3.2 当前(M14 拆分 RawEvaluateResult + M15 promoted + M16 GAConfigSnapshot 生效值)
    // SchemaVersionV532    = "v5.3.2"   // 已废弃,仅用于历史结果包反序列化兼容

    FitnessVersionV1RawStd  = "v1-raw-std"
    FitnessVersionV2ZScore  = "v2-zscore"   // 二期启用
    FingerprintVersionFpV1  = "fp-v1"

    // 当前生效版本
    CurrentSchemaVersion      = SchemaVersionV533
    CurrentFitnessVersion     = FitnessVersionV1RawStd
    CurrentFingerprintVersion = FingerprintVersionFpV1
)
```

API 响应、结果包生成、Phase 10 启动校验、Docker 环境变量校验等所有用到版本号的地方,**必须从此包读取**,禁止硬编码字符串字面量。`grep -rn '"v5.3.3"\|"v1-raw-std"\|"fp-v1"' internal/ | grep -v internal/resultpkg/versions.go` 应无结果(除测试 fixture 外)。

### FitnessCalculator 抽象层(v3.1 新增)

为未来无缝切换 `fitness_version`(原型阶段 `v1-raw-std` → 二期 `v2-zscore`),将适应度公式抽象为接口:

```go
// /internal/fitness/calculator.go
type FitnessCalculator interface {
    Version() string
    SliceScore(window CrucibleResult, dca DCABaselines, friction FrictionParams) SliceScore
    AggregateScoreTotal(windows []CrucibleResult, weights map[WindowName]float64) ScoreTotal
}

// 原型阶段实现
type RawStdCalculator struct {
    LambdaDiff, LambdaAbs, DDBase, LambdaCons, LambdaTurn, TExtreme float64
}
func (c *RawStdCalculator) Version() string { return resultpkg.FitnessVersionV1RawStd }
// ...
```

引擎通过依赖注入获取 `FitnessCalculator`,未来切换 `v2-zscore` 时只需新增 `ZScoreCalculator` 实现并改一处 wire,核心引擎代码不变。

### 参数治理

| 类别 | 含义 | 可修改性 |
|---|---|---|
| 物理常数 | 数学/算法定义（如 Euler-Mascheroni γ=0.5772） | 不可改 |
| 工程约束 | 确定性/隔离等工程硬约束 | 不可改 |
| 设计选择 | 价值判断（如 `FatalMDD=0.70`、`DD_base=0.40`） | 中 |
| 工程经验 | 待实证标定（如 `λ_turn=0.1`、邻域采样 N=30） | 高 |

不可调（工程约束级）：哈希算法定义、14-verb 接口必需动词、禁止并发归约、Evaluate 内部禁止 goroutine。

---

# II-4. 编码阶段计划

> **总体节奏**：Phase 0/1/1.5/2 是基础设施；Phase 3/4/5/5.5 是核心引擎；Phase 6/7/8/9 是服务化能力；Phase 10/11/13 是收尾。Phase 12 Web 前端和 Phase 9.5 完整观测均移入 Part III-1。

## Phase 0 — 环境初始化与 AI 协作基础设施

### 目标

在项目根目录建立 AI 工作约束文件，初始化 Go 项目依赖。

### Prompt

```
帮我完成以下三件事：

第一，在项目根目录创建 CLAUDE.md 约束文件，内容包括以下几部分：

"唯一功能真源"部分：声明当前功能只依据 docs/ 下的三份文档（系统总体拓扑结构、策略数学引擎、进化计算引擎 v5.4.1），三份文档没有定义的功能不进入实现。同时声明三份冻结基线：
- Go struct 冻结版定义草案 v3
- 进化系统 v5.4 Go-only JSON Schema（内版号 v5.3.3）
- 进化系统 程序框架规划 v5.4.1

"工作顺序"部分：列出四条规则——涉及策略和回测先读对应文档；涉及 Go 后端遵守 GORM Code-First；涉及价格计算优先无量纲表达；涉及架构边界保持 SaaS-Strategy-Agent 分工不做预防性解耦。

"核心约束"部分：列出 v3 的六条铁律与三条隐含铁律。特别强调：
- 铁律 2：禁止读取墙钟（time.Now / time.Since），通过 input.NowMs 显式注入
- 铁律 4：开发用 AutoMigrate，准备生产前切到 Atlas 版本化迁移
- 铁律 6：Postgres 启用 TimescaleDB 扩展
- 隐含铁律：CompareFitness 强制 / Adapter 状态隔离 / RNG 路径单一

"代码目录"部分：列出 cmd/saas/ cmd/agent/ cmd/datafeeder/ internal/domain/ internal/engine/ internal/strategy/ internal/strategies/[策略名]/ internal/fitness/ internal/verification/ internal/data/ internal/repository/ internal/report/ internal/api/ internal/quant/ internal/resultpkg/ internal/adapters/backtest/ internal/saas/* 的职责说明。

"验证命令"部分：go list ./... 、 go test ./... 和 go test ./... -race

第二，初始化 Go 项目并安装依赖：gin、gorm + postgres driver、go-redis、golang-jwt、robfig/cron、zap、gorilla/websocket、testify、shopspring/decimal（金额精确计算）、prometheus/client_golang（基础指标暴露）。
另外为 atlasgo cli 留好位置，但不作为运行时依赖。

第三，为整个项目构建 AGENT SKILL 文档，至少包含：系统架构师、量化交易数学专家、Go 后端专家、部署与运维专家、数据工程师（Phase 1.5 用）。
```

### 预期产出

- `CLAUDE.md`
- `go.mod` + `go.sum`
- 基础目录骨架
- AGENT SKILL 文档

---

## Phase 1 — 三份真源文档

### 1A. 系统总体拓扑结构文档

```
帮我在 docs/ 目录下创建"系统总体拓扑结构.md"。这份文档定义系统有哪些物理端、有哪些逻辑模块、状态如何在它们之间流转、以及系统的生命周期动作。不含任何具体策略公式。

文档结构如下：

第 0 章：架构哲学
第 1 章：三端物理部署形态（saas / agent / lab）
第 2 章：app_role 三态行为矩阵（saas / lab / dev）
第 3 章：逻辑模块与职责边界（domain / engine / strategy / fitness / verification / data / repository / report / api）
第 4 章：全局状态总线（单一 Postgres + TimescaleDB 扩展 + Redis 仅缓存；数据所有权表）
第 5 章：WebSocket 通信协议（消息类型全表 / TradeCommand / DeltaReport / 状态收敛与自愈机制）
第 6 章：系统级生命周期动作（初始化 / Cron Tick 驱动 Step() 完整流程 / Agent 断线重连指数退避 / 优雅停机与状态快照）
第 7 章：不可推翻的技术决策（六条铁律 + 三条隐含铁律，含每条的修订理由）
第 8 章：时间语义（铁律 2）
  - 系统中所有"当前时间"都从 input.NowMs 流转，禁止从墙钟读取
  - 回测时 NowMs = bar.OpenTime + bar.IntervalMs
  - 实盘时 NowMs = 真实时钟（但读取发生在 cron tick 外圈，而不是 Step() 内部）
```

### 1B. 策略数学引擎文档

```
帮我在 docs/ 目录下创建"策略数学引擎.md"。这份文档是策略逻辑的数学规格书。

直接对应本编码计划 Part I §I-2 的所有内容：

第 0 章：引擎身份
第 1 章：资产结构三态（I-2.1）
第 2 章：市场状态感知层（I-2.2，必须定义"安静态"）
第 3 章：信号与目标函数框架（I-2.3）
第 4 章：宏观引擎（I-2.5，策略设计空间）
第 5 章：微观引擎（I-2.4，Sigmoid 动态天平公式与解释直接复用）
第 6 章：DeadBTC 释放规则（I-2.6）
第 7 章：可进化参数契约 Chromosome（I-2.7，含 Segment 划分）
第 8 章：StrategyInput / StrategyOutput 契约（I-2.8）
```

### 1C. 进化计算引擎文档

```
将《进化系统_程序框架规划_v5_4_1》原文完整放入 docs/进化计算引擎.md。
不要修改任何内容；它是本项目 GA 部分的唯一规格。

同时将《进化系统_v5_4_Go-only_JSON_Schema_v533》（内版号 v5.3.3）放入 docs/进化计算引擎_数据契约.md。
将《Go_struct 冻结版定义草案 v3》放入 docs/进化计算引擎_Go_struct_草案.md。

这三份联合构成 GA 部分的完整规格。

关键章节速读（框架文档 v5.4）：
- §2.6 SliceScore Sum-type 三态语义 + CompareFitness 排序规则
- §3.5 Adapter 生命周期与状态隔离
- §6.5 Fatal 规则与级联短路（含 fatal_audit_sample_rate 抽样审计）
- §6.6 一致性惩罚（v1-raw-std）
- §7 并发、确定性与缓存（worker pool / 顺序无关性 / Fingerprint 缓存）
- §8 结果包五层结构
- §10.1 11 条必落地测试
```

---

## Phase 1.5 — 历史 K 线数据导入与时序存储

### 目标

把 BTC/USDT 等若干现货对的完整 1m K 线历史导入本地 Postgres + TimescaleDB，作为 GA 回测的数据基础。

### Context

本 Phase 解决三件事：

1. **数据从哪里来**：币安公开数据归档 `data.binance.vision`，按月 zip 打包，每个 zip 配套 `.CHECKSUM` 文件用于 sha256 校验。这比走 REST API 快 10-100×，且无 rate limit 问题。
2. **数据存哪里**：铁律 6 选定 TimescaleDB，本 Phase 落地。
3. **数据完整性怎么保证**：1m K 线常有缺失 bar（交易所维护等），如果回测代码默默"补 0"或"前向填充"，GA 会学到伪信号。Phase 1.5 显式标注缺口。

### Prompt

```
请实现 cmd/datafeeder/main.go 和 internal/data/ 包，负责把币安公开 K 线归档导入本地 Postgres + TimescaleDB。

一、Postgres 扩展启用与表结构

在 internal/saas/store/db.go 的 NewDB() 中，AutoMigrate 完成后追加执行：
- CREATE EXTENSION IF NOT EXISTS timescaledb;
- 对 klines 表执行 SELECT create_hypertable('klines', 'open_time', if_not_exists => TRUE, chunk_time_interval => 7 * 24 * 60 * 60 * 1000);  -- 7 天一个 chunk

KLine 表的 GORM 定义（internal/saas/store/models.go）：
  - Symbol     string (size:16, index)
  - Interval   string (size:8,  index)
  - OpenTime   int64  (毫秒，primary key 复合的一部分)
  - Open/High/Low/Close/Volume  float64
  - QuoteVolume float64
  - NumTrades   int32
  - Source      string (size:16, 默认 "binance.vision")
  - 在 (Symbol, Interval, OpenTime) 上建唯一复合索引

新增 KLineGap 表：
  - Symbol / Interval / GapStartMs / GapEndMs / DetectedAt
  - 回测时如 EvaluablePlan 的 K 线区间与 KLineGap 有交集，buildEvaluablePlan 应直接 fatal 或记录 warn

二、归档下载器 internal/data/binance_archive.go

实现以下函数：
- DownloadMonthly(symbol, interval string, year, month int) ([]byte, error)
  URL: https://data.binance.vision/data/spot/monthly/klines/{SYMBOL}/{INTERVAL}/{SYMBOL}-{INTERVAL}-{YYYY}-{MM}.zip
- DownloadDaily(symbol, interval string, date time.Time) ([]byte, error)
- VerifyChecksum(data []byte, expectedSha256 string) error
- ParseKlineCsv(zipData []byte) ([]KLine, error)

三、API 回退兜底 internal/data/binance_api.go

近 1-2 天未归档部分走 REST API：
- 端点 GET https://api.binance.com/api/v3/klines
- limit=1000 每次拉一页，按时间倒推分页
- 添加 150ms 间隔避免 rate limit
- 遇 429 / 418，指数退避重试

四、Import Orchestrator internal/data/orchestrator.go

ImportSymbol(symbol, interval, startTime, endTime) 流程：
1. 查询 KLine 表确认已有覆盖区间
2. 计算缺失区间，按月切分
3. 对每个月份调用 DownloadMonthly → VerifyChecksum → ParseKlineCsv → 批量 COPY 入库
   （用 pgx 的 CopyFrom 接口；1m 数据 1 个月 ~ 43200 行，批量 COPY 比逐行 INSERT 快 50-100×）
4. 对最近未归档天数走 API 回退
5. 对每个新插入的 (Symbol, Interval) 区间，扫描时间戳连续性，将缺口写入 KLineGap

五、CLI 入口 cmd/datafeeder/main.go

支持子命令：
- datafeeder import --symbol BTCUSDT --interval 1m --from 2017-08-17 --to 2025-12-31
- datafeeder verify --symbol BTCUSDT --interval 1m
- datafeeder stats

铁律：
- 所有 OpenTime 使用毫秒级 int64（与币安 API 一致）
- 不在 datafeeder 内做任何"插值填充"——缺口由 KLineGap 表记录，回测层显式处理
- 价格字段使用 float64（回测计算性能优先）；订单金额精确计算在 Agent 侧用 decimal
```

### 验收要点

- 单标的 BTC/USDT 1m 全历史（约 9 年，~470 万行）导入耗时 < 20 分钟
- 启用 TimescaleDB 压缩后，470 万行 1m 数据磁盘占用 < 250 MB
- `datafeeder verify` 能正确识别已知 Binance 维护窗口

---

## Phase 2 — 基础设施层（Config + DB + Auth）

### 目标

搭建系统的物理基础：配置加载、GORM 数据库模型定义、Redis 客户端、JWT 工具。

### Prompt

```
请阅读 docs/系统总体拓扑结构.md，然后为我实现 Go 项目的基础设施层。

总体约束：模块使用 gin 框架，ORM 为 GORM + postgres driver，日志使用 zap。

一、internal/saas/config/config.go

定义 Config 结构体（AppRole / Database / Redis / JWT / Server / Friction / GA / DataFeed），从 config.yaml 加载。AppRole 取值 "saas" / "lab" / "dev"。

Friction 子配置（对应 I-3.11）：
  - TakerFeeBps int   default 10
  - SlippageBps int   default 5

GA 子配置：
  - PopSize / MaxGenerations / EliteRatio / FatalMDD / OosDays / FatalAuditSampleRate / SbbBlockLenFallback

二、internal/saas/store/models.go

用 GORM struct 定义所有核心数据模型：
- User
- StrategyTemplate
- StrategyInstance
- PortfolioState
- RuntimeState
- SpotLot
- TradeRecord
- SpotExecution
- AuditLog
- EvolutionTask:
    TaskID / StrategyID / Pair / Status / CurrentGeneration
    RequestedTakerFeeBps / RequestedSlippageBps    (v3.2 M16:用户请求的原始摩擦意图,审计用)
    TestMode bool                                   (用户请求的 test_mode 标志)
    SpawnMode / OosDays / FatalAuditSampleRate
    EpochSeed / CreatedAt / FinishedAt / FailureReason
- GeneRecord(结果包持久化镜像):
    ChallengerID / StrategyID / Pair
    ScoreTotal / ScoreRaw / ConsistencyPenalty / MaxDrawdown
    WindowScoresJSON / WindowAlphaMonthlyJSON / WindowAlphaWeeklyJSON
    OosAlphaMonthly / OosAlphaWeekly
    DSR / DSRTrialsN / DSRTrialsVar
    EpochSeed / DataVersion / EngineVersion / StrategyVersion
    SchemaVersion / FitnessVersion / FingerprintVersion
    HardwareSignature / GoVersion / BuildID
    PlanHash / BarsHash
    TakerFeeBps / SlippageBps / TestMode  (v3.2 M16 修订:生效值,删除原 v3.1 的 TakerFeeBpsActual/SlippageBpsActual 冗余字段)
    SbbBlockLength
    DecisionStatus / DecisionNote / ReviewedAtTs / ReviewedBy
    FullPackageJSON (整个 ChallengerResultPackage 序列化)

  **v3.2 字段约束**:
  - `DecisionStatus` 严格为 `PromoteLayer.DecisionStatus` 的三态镜像(`pending` / `promoted` / `rejected`,v3.2 M15 重命名),`retired` **永远不进入** GeneRecord
  - Champion 退役状态由独立的 `ChampionHistory.RetiredAt` 管理,与 GeneRecord 完全解耦
  - `TakerFeeBps` / `SlippageBps` 字段为**生效值**(v3.2 M16):`TestMode=true` 时为 0;通过 `TestMode == true` 推导本次评估是否禁用摩擦
  - 用户请求的**原始摩擦意图**(如 10/5)只在 `EvolutionTask` 表的 `RequestedTakerFeeBps` / `RequestedSlippageBps` 字段记录,不进入 GeneRecord 和结果包
- KLine + KLineGap（Phase 1.5 已定义）
- SharpeBank（DSR 输入累积器）：
    StrategyID / PairID（联合 index）
    ChallengerID / ObservedSharpe / BacktestHorizonT / Skew / Kurtosis / CreatedAt
- ChampionHistory：
    StrategyID / Pair / ChallengerID（曾任冠军）
    PromotedAt / RetiredAt / RetiredBy / RetireNote

三、internal/saas/store/db.go

NewDB(cfg) 函数：
1. 建立 Postgres 连接
2. CREATE EXTENSION IF NOT EXISTS timescaledb;
3. AutoMigrate 所有模型
4. 对 klines 表执行 create_hypertable
5. 配置 Timescale 压缩策略：chunk_time_interval = 7d，压缩 30 天以上的 chunk

四、internal/saas/store/redis.go

实现 Get/Set/Del 三个基础方法。

五、internal/saas/auth/service.go

实现 SignToken(userID uint, role string) 和 ParseToken(tokenStr string)，使用 golang-jwt。

六、internal/resultpkg/enums.go（新增，按 Go struct 冻结版 v2 §2.1）

定义所有冻结枚举常量：
- TaskStatus / DecisionStatus / VerificationStatus / DecisionColor
- WindowName / SkippedBy / SpawnMode
- 版本常量 SchemaVersionV533 = "v5.3.3" 等(v3.2 升级,从基线 v5.3.2 → v5.3.3 同步,详见 II-3.7)

七、internal/api/types.go（按 Go struct 冻结版 v2 §3）

定义 API 请求响应 struct：
- CreateEvolutionTaskRequest
- EvolutionTaskStatusResponse
- PromoteChallengerRequest
- RetireChampionRequest

每个 struct 都实现 Validate() 方法，按草案 §5 实现。

八、internal/resultpkg/types.go（按 Go struct 冻结版 v2 §4）

定义结果包所有 struct(SliceScore / CrucibleResult / ScoreTotal / ResultCore / **EvaluationLayer**(v3.2 修订,原 EvaluationResult,含 WindowScores + ScoreTotal + FrictionActual) / VerificationLayer / DiagnosticsLayer / PromoteLayer / ChallengerResultPackage / ReproducibilityMetadata / GAConfigSnapshot / ChampionGenePayload / SpawnPointPayload)。

实现 CrucibleResult.Validate() 校验三态互斥（草案 §5.3）。
实现 ChallengerResultPackage.Validate() 校验五层非零 + 三件套版本号一致（草案 §5.4）。
```

---

## Phase 3 — 量化数学基础层（internal/quant）

### 3A. 基础数学工具

```
请实现 internal/quant/ 目录下的数学基础工具。

math.go：实现以下无状态纯函数
- EMA / StdDev / MAVAbsChange / ClipFloat64 / RoundToUSDT
- ACF(series, maxLag) []float64    // FFT-based 自相关函数，SBB 块长估计要用
- Skewness / ExcessKurtosis        // DSR 输入
- KahanSum(series) float64         // 串行累加用，确定性优先

data.go：
- Bar：OpenTime / Open / High / Low / Close / Volume
- StrategyInput（含 NowMs int64，铁律 2）
- StrategyOutput
- PortfolioSnapshot

closes.go：ACL 降级工具
- ExtractCloses / ExtractTimestamps
说明：现货策略的 OHLCV 降级在这里完成；策略内核禁止直接依赖 Bar。

friction.go（对应 I-3.11）：
- FrictionParams struct { TakerFeeBps, SlippageBps int; Disabled bool }
- ApplyBuyFriction(notionalUSD, price float64, fp FrictionParams) (filledQty, feeUSD, slippageCost float64)
- ApplySellFriction(qty, price float64, fp FrictionParams) (filledQuoteUSD, feeUSD, slippageCost float64)
- 所有 SimulateGhostDCA / Evaluate / OOS / MC 路径必须从同一份 FrictionParams 读取

canonical_json.go（新增，v3 关键）：
- CanonicalJSON(v any) ([]byte, error)
  按字段名字典序排序输出 JSON，浮点用固定精度（17 位有效数字），用于 plan_hash / bars_hash 计算
- Sha256Hex(data []byte) string
  小写 hex 编码，固定 64 字符
```

### 3B. 资产仓位管理 / 3C. 微观引擎 / 3D. 市场状态感知 / 3E. 宏观引擎 / 3F. Chromosome

按 Part I §I-2 与策略文档实现，本编码计划不重复策略内部细节。重点提醒：

- 策略内核**禁止** `import "internal/quant".Bar` —— 只能用 `Closes` / `Timestamps`
- 策略内核**禁止** `time.Now()` / `time.Since()`
- 策略内核**禁止**出现 `TakerFee` / `Slippage` 字段
- `Step()` 必须满足铁律 1（同构，无 `if isBacktest`）和铁律 2（NowMs 注入）

### 3G. Ghost DCA 双基准

```
请实现 internal/fitness/ghost_dca.go，提供"被动 DCA 双基准"模拟器（对应 I-3.10）。

GhostDCAConfig：
- InitialCapital float64
- MonthlyInject float64

SimulateGhostDCAMonthly(config, bars, friction) GhostDCAResult
  - 月初注入 MonthlyInject 全部买入
  - 每笔买入按 friction 扣除 fee + slippage

SimulateGhostDCAWeekly(config, bars, friction) GhostDCAResult
  - 每 7 自然日注入 MonthlyInject / 4.33 全部买入
  - 摩擦同上

GhostDCAResult 含：
- FinalEquity / TotalInjected / MaxDrawdown / ROI
- ROI 使用 Modified Dietz；单子期间注资比例 > 10% 时切换精确 TWR

MaxDrawdown 计算：基于 NAV 曲线峰值到谷底的最大相对回撤。
```

---

## Phase 4 — 策略模块（Step() 主函数）

```
请阅读 docs/策略数学引擎.md，然后实现 internal/strategies/[策略名]/ 目录下的策略模块。

关键铁律：

- Step() 函数体内禁止 time.Now() / time.Since()
- 所有"当前时间"逻辑必须基于 input.NowMs
- 例如计算"距上次决策时长"：dt = input.NowMs - input.LastProcessedBarTime
- Step() 内部不应使用 friction 参数——摩擦扣除发生在回测适配器 / Agent 成交回报里
- Step() 必须满足回测/实盘同构（铁律 1），禁止 if isBacktest 分支

实现完成后逐项确认：
- grep -nE 'time\.Now|time\.Since|http\.|sql\.|os\.Open' internal/strategies/  期望无结果
- grep -n 'isBacktest' internal/strategies/                                     期望无结果
- grep -n 'quant\.Bar' internal/strategies/                                     期望无结果
- grep -n 'TakerFee\|Slippage' internal/strategies/                             期望无结果
```

---

## Phase 5 — 遗传算法进化引擎（GA Engine）

### 架构关系

```
EvolutionEngine（调度器，不知道染色体字段名）
    ↓ 通过 EvolvableStrategy 14-verb 接口
[YourStrategy]Evolvable（策略侧适配器）
    ↓ 通过 NewAdapter(plan) 创建 worker 独占 Adapter
[YourStrategy]Adapter（实现 Reset / Evaluate / Close）
    ↓ 内部调用
RunBacktest（回测核心循环，统一注入 FrictionParams）
    ↓ 调用
Step()（策略纯函数，与实盘相同）
```

### 5A. EvolvableStrategy 接口 + Adapter

```
请阅读 docs/进化计算引擎.md §5（14-verb 接口定义）和 §3.5（Adapter 生命周期），然后实现 internal/strategy/evolvable.go 和 internal/saas/ga/[策略名]_evolvable.go。

internal/strategy/evolvable.go 定义(v3.2 M14 修订):

- Gene = any
- DCABaseline struct { FinalEquity, TotalInjected, MaxDrawdown float64 }
- DCABaselines struct { Monthly, Weekly DCABaseline }
- EvaluablePlan struct（Pair / Spawn / LotStep / LotMin / Windows / DCABaselines / OosWindow / Friction / AggregateCache）
- CrucibleResult struct（来自 internal/resultpkg）
- **RawEvaluateResult struct**(v3.2 新增):
    Windows []CrucibleResult        // 各窗口评估结果(仅 SliceScore + AlphaBreakdown,无 ScoreTotal)
    FrictionActual FrictionParams   // 评估时实际生效摩擦
  注意:**该 struct 物理上不包含 ScoreTotal 字段**,这是 v3.2 M14 类型级强约束的核心
- EvolvableStrategy interface 14 个方法:
    StrategyID() / Segments() / Sample() / Clamp() / Validate() / Crossover() / Mutate() / Fingerprint()
    Evaluate() / ReviewBacktest() / EncodeResult() / DecodeElite() / MinEvalBars() / NewAdapter()
- Adapter interface 3 个方法:Reset(plan) / Evaluate(gene) (*RawEvaluateResult, error) / Close()

internal/saas/ga/[策略名]_evolvable.go：

- Sample：均匀采样合法边界内，调用 Clamp 修复
- Clamp：实现边界裁剪、权重归一化、互斥项修复、离散档位映射、整数窗口取整（顺序：边界裁剪 → 块内约束 → 跨段约束）
- Validate：检查 Gene 合法性
- Crossover：实现块级正交交叉——按 Segments() 整体掷 50% 决定继承哪个父代，块内完整继承不做维度级混合；失败回退到父代原样拷贝并记录 crossover_fallback 事件
- Mutate：每维度 Bernoulli(prob) 触发，量为 NormFloat64() × GeneStep × scale，最后调用 Clamp
- Fingerprint：按 SegmentInfo.QuantizationStep round 后做 FNV-1a-64，输出 hex 字符串
- Evaluate(ctx, gene, plan) (*RawEvaluateResult, error)(v3.2 M14):在 plan 上评估基因,返回纯评估结果。内部按 6m → 2y → 5y → 10y 固定顺序逐窗口调用 Adapter.Evaluate,遵守级联短路规则(I-3.8)。**返回值物理上不含 ScoreTotal** —— 该字段由引擎在 RunEpoch 中调用 `fitness.AggregateScoreTotal(...)` 后填充至 `EvaluationLayer`
- ReviewBacktest：原型阶段返回 nil 即可（三期落地，见 Part III-2）
- EncodeResult:构造完整 ChallengerResultPackage(五层)。**注意(v3.2 M14)**:策略层只产出 `RawEvaluateResult`,**不直接构造 `EvaluationLayer`**;`EncodeResult` 接收已由引擎组装好的 `EvaluationLayer`(含 ScoreTotal)作为输入,再与 core / verification / diagnostics / promote 一起打包成 `ChallengerResultPackage`。签名建议为:`EncodeResult(c, spawn, eval *EvaluationLayer, verif *VerificationLayer, diag *DiagnosticsLayer) ChallengerResultPackage`
- DecodeElite：从 ChampionGenePayload.Payload (json.RawMessage) 反序列化
- MinEvalBars：返回 EMA 最长窗口 + 最短统计稳定期之和
- NewAdapter:构造策略侧 Adapter 实例。Reset(plan) 时**清空持仓态/指标缓存/连续亏损计数器**以及**任何依赖 Gene 内容的中间值**;**仅保留与 Gene 无关的全局共享数据**(K 线 buffer / DCABaseline 缓存 / 容量预分配但每次写前清零的临时 buffer)。该约束由 `TestAdapterResetIsolation` 验证

注意包位置约束：放在 internal/saas/ga/ 包下，避免策略包→ga 包→策略包的导入循环。
```

### 5B. GA 主引擎

```
请阅读 docs/进化计算引擎.md §2-§7，然后实现 internal/engine/engine.go。

EvolutionEngine 结构体字段：
- evolvable EvolvableStrategy
- genomeStore / sharpeBank
- db / logger
- 配置（Part I §I-5 默认值）：
    PopSize(300) / MaxGenerations(25) / EliteRatio(0.05) / TournamentSize(3)
    MutationProbability(0.15) / MutationScale(1.0)
    MutationProbabilityMax(0.55) / MutationScaleMax(3.0) / MutationRampFactor(1.25)
    EarlyStopPatience(5) / EarlyStopMinDelta(0.001)
    Lambda_diff(1.5) / Lambda_abs(2.0) / DDBase(0.40) / Lambda_cons(0.3)
    FatalMDD(0.70) / FatalAuditSampleRate(0.05)

EpochConfig：
- PopSize / MaxGenerations / LotStepSize / LotMinQty / OnProgress
- SpawnPointOverride
- Friction *FrictionParams
- OosDays *int  // nil = 不启用 OOS
- EpochSeed uint64

RunEpoch 流程：

步骤一：构建 EvaluablePlan
- 从数据库拉取历史 K 线（含 KLineGap 检查，命中缺口立即 fatal）
- 调用 BuildCrucibleWindows 构建四个 IS 窗口（6m/2y/5y/10y）+ OOS Holdout 窗口
- 对每个 IS 窗口分别预计算 Monthly + Weekly Ghost DCA 基线（含摩擦扣除）
- 封装为 EvaluablePlan（含 Friction 引用、SbbBlockLenFallback 等）
- 计算 plan_hash = Sha256Hex(CanonicalJSON(EvaluablePlan))
- 计算 bars_hash = Sha256Hex(CanonicalJSON(全量 K 线序列含 warmup))

步骤二：种群初始化
- index 0 始终为当前种子冠军原样
- 剩余按 10% 精英克隆 / 40% 强化变异 / 50% 完全随机
- 所有 RNG 操作必须从 EpochSeed 派生确定性子 seed（Phase 5.5 的 rng.go）

步骤三：并发评估初始种群（见 evaluatePopulation）

步骤四：主进化循环
- 通过 CompareFitness 排序(Sum-type 三层优先级,禁止直接解引用 *Value);**使用 sort.SliceStable,禁止 sort.Slice**(v3.1 强化)
- 收敛检测、变异斜坡触发
- Diversity Rescue 层一（变异斜坡）落地；层二接口预留不实现
- 进度回调 OnProgress
- 产生下一代：精英保留 → tournamentSelect → Crossover → Mutate

步骤五:Challenger 写入(v3.2 M14 关键流程修订)
- 取最优个体(CompareFitness 第一名),从评估缓存中取出其 `*RawEvaluateResult`(策略产出)
- **引擎组装 EvaluationLayer**(v3.2 M14):
    layer := EvaluationLayer{
        WindowScores:   raw.Windows,
        ScoreTotal:     fitness.AggregateScoreTotal(raw.Windows, weights, lambdaCons, fitnessVer),
        FrictionActual: raw.FrictionActual,
    }
- 调用 EncodeResult(gene, spawn, &layer, &verifLayer, &diagLayer) 组装完整五层 ChallengerResultPackage
- 计算 SharpeBank 输入 (observed_sharpe, skew, kurtosis, horizonT) 写入 SharpeBank 表
- 当 SharpeBank.size(strategy, pair) ≥ 5 时计算 DSR,**写入 verification.dsr_summary**(v3.1 修订:不再放 PromoteLayer 关联字段;前端展示通过联合查询 verification 层取得)
- 把 EpochSeed、DataVersion、EngineVersion、plan_hash、bars_hash、hardware_signature、go_version、build_id 写入 ReproducibilityMetadata
- 把 OOS 评估结果写入 verification.oos_result(含 OosAlphaMonthly/Weekly + **OOSResult.DecisionColor**)
- 把 fatal 个体 5% 抽样审计结果写入 diagnostics.fatal_audit_samples
- **GAConfigSnapshot 存生效值**(v3.2 M16):test_mode=true 时 ga_config.taker_fee_bps/slippage_bps 已被覆写为 0,与 evaluation.friction_actual 数值一致;**不再写入独立的 friction_disabled 字段**(v3.1)

evaluatePopulation 函数（关键约束）：
- Workers = min(runtime.NumCPU(), len(population))
- 每个 worker 独占 Adapter（通过 evolvable.NewAdapter(plan)）
- worker 内 RNG 从 EpochSeed + workerID 派生，不与主线程共享
- worker 内不消费主 RNG
- 评估只用 worker 局部 RNG；锦标赛/交叉/变异等"种群级"操作回主线程串行执行
- Fingerprint 缓存用 sync.Map
- 每次 Evaluate 前引擎强制调用 adapter.Reset(plan)
- **窗口评估循环必须按 6m → 2y → 5y → 10y 固定顺序执行**(v3.1 强化),任一窗口 fatal 时按级联短路规则跳过后续窗口,被跳过的窗口 SkippedBy 取值由触发 Fatal 的窗口决定;ScoreTotal 直接为 Fatal(Sum-type 传染)

tournamentSelect 函数：主线程串行调用，使用主 RNG。

CompareFitness 函数（internal/engine/compare.go）：

func CompareFitness(a, b ScoreTotal, aFp, bFp string) int {
    aFatal, bFatal := a.Fatal, b.Fatal
    if aFatal && bFatal {
        if aFp < bFp { return -1 }
        if aFp > bFp { return 1 }
        return 0  // 同 fingerprint，稳定排序
    }
    if aFatal { return 1 }   // a 劣
    if bFatal { return -1 }  // b 劣
    // 两者均 Normal：按 Value 降序
    if a.Value == nil || b.Value == nil {
        panic("CompareFitness: Normal score with nil Value")  // Validate 应已拦截
    }
    if *a.Value > *b.Value { return -1 }
    if *a.Value < *b.Value { return 1 }
    return 0
}

**ScoreTotal 计算的代码位置(v3.2 M14)**:必须在 `internal/fitness/aggregate.go` 的 `AggregateScoreTotal` 函数内实现,**禁止出现在任何 internal/strategies/* 或 internal/saas/ga/* 路径下**。该函数严格按 Part I §I-3.7 / §I-3.9(权重固定,不重新归一化):

```go
// /internal/fitness/aggregate.go
func AggregateScoreTotal(
    windows []CrucibleResult,
    weights map[WindowName]float64,
    lambdaCons float64,
    fitnessVer string,
) ScoreTotal {
    // v3.1 M02:权重固定,不重新归一化
    if 任一 window 的 SliceScore.Fatal == true(MDD 超标 / insufficient_bars / invalid_path):
        return ScoreTotal{ Fatal: true, Value: nil }

    valid_scores := [w.Score.Value for w in windows if w.Score.Fatal=false and w.SkippedBy=nil]
    var sigma float64
    if len(valid_scores) < 2:
        sigma = 0.0   // 单窗口或零窗口时一致性惩罚为 0
    else:
        sigma = stddev(valid_scores)
    // 按固定权重加权,缺失窗口贡献 0,不重算分母
    scoreRaw := Σ_{w ∈ windows} (weights[w.Window] × w.Score.Value)
    return ScoreTotal{
        Fatal: false,
        Value: &(scoreRaw - lambdaCons * sigma),
    }
}
```
```

### 5C. 坩埚窗口构建（v3 修订：Anchored Holdout，取消 embargo）

```
请实现 internal/data/crucible.go。

CrucibleWindow 结构体：
- Label string  // "6m" / "2y" / "5y" / "10y" / "oos"
- Weight float64
- Bars []Bar     // 含 warmup 前缀
- EvalStartMs int64
- EvalEndMs int64
- IsOOS bool

BuildCrucibleWindows(bars []Bar, warmupDays int, oosDays *int) []CrucibleWindow

切分逻辑（取消 embargo，使用 Anchored Holdout 语义）：
1. 若 oosDays != nil 且 > 0：
   - OOS 段 = 全量 K 线最末尾 oosDays 天
   - IS 段 = 全量 - OOS
   否则：
   - IS 段 = 全量
2. 在 IS 段内构建四个评估窗口：
   - "6m"   评估区间 = IS 段最后 183 天，  warmup 1200 天，Weight=0.10
   - "2y"   评估区间 = IS 段最后 730 天，  warmup 1200 天，Weight=0.20
   - "5y"   评估区间 = IS 段最后 1825 天， warmup 1200 天，Weight=0.30
   - "10y"  评估区间 = IS 段全部，         warmup 1200 天，Weight=0.40
3. 若 IS 段较短不足某窗口(评估区间 + warmup 长度超过 IS 段长度),**该窗口不进入 plan.Windows**;**剩余窗口权重保持 I-3.6 固定值,不重新归一化**(v3.1 修订)。该 challenger 的 ScoreRaw 在数值上等于"现有窗口按原始权重的加权和",自然地反映"样本不足"的惩罚
4. OOS 窗口（IsOOS=true）单独返回，不计入加权聚合

严格检查未来数据泄露：
- 每个窗口 bars 内最后一根 bar 的 OpenTime 不得越过 EvalEndMs
- 所有 IS 窗口的 EvalEndMs ≤ OOS 起点（若启用 OOS）
- warmup 段的 bar 必须先于 EvalStartMs

注意：v3 取消了 v2 的 embargo_days 字段。Anchored Holdout 通过冻结 OOS 段起点保证 IS 不前进，不再需要缓冲段。
```

### 5D. 进化任务服务与 HTTP Handler

```
请阅读 docs/进化计算引擎_数据契约.md §4（API Schema），然后实现进化任务服务层和 HTTP Handler。

internal/saas/epoch/service.go：

CreateAndRunTask 流程：
- 检查互斥锁，已有任务在跑则返回错误
- 解析 CreateEvolutionTaskRequest(参数集见 II-3.1);**记录请求原始摩擦意图到 EvolutionTask.RequestedTakerFeeBps / RequestedSlippageBps**(v3.2 M16)
- 调用 req.Validate() 做字段校验(包括 spawn_mode 枚举、test_mode bool、oos_days >= 1 等)
- **生成 GAConfigSnapshot 时**(v3.2 M16:存生效值):若 test_mode=true,直接将 taker_fee_bps/slippage_bps **覆写为 0** 写入 snapshot;若 test_mode=false,使用请求值。这样结果包内不再有自相矛盾的 `{test_mode:true, taker_fee_bps:10}` 状态
- 在 DB 创建 EvolutionTask 记录(含 RequestedTakerFeeBps / RequestedSlippageBps 原始意图)
- 异步启动 runEpoch goroutine

internal/api/handler_evolution.go：

POST /api/v1/evolution/tasks                       创建并启动进化任务（lab/dev only）
GET  /api/v1/evolution/tasks/:task_id              返回 EvolutionTaskStatusResponse
GET  /api/v1/challengers/:challenger_id            返回 challenger 元数据 + 概览
GET  /api/v1/challengers/:challenger_id/package    返回完整 ChallengerResultPackage（五层）
POST /api/v1/challengers/:challenger_id/promote    人工 Promote
POST /api/v1/champions/:champion_id/retire         独立的 Champion 退役接口
GET  /api/v1/genome/champion                        返回当前冠军基因包

Promote handler：
- 校验 challenger 当前 promote.decision_status == "pending"
- 校验 core.ga_config.test_mode != true(v3.1:test_mode 产物不可 Promote,通过该字段推导而非独立 friction_disabled 标志)
- 接受 PromoteChallengerRequest（reviewed_by 必填，decision_note 可选）
- 写入 promote.decision_status = "promoted" / "rejected"(v3.2:原 "approved" 重命名为 "promoted"),写入 reviewed_at_ts 与 reviewed_by
- 若 promoted,更新 champion 表与 champion_history 表(写入 champion_history.promoted_at)

Retire handler：
- 独立处理 Champion 退役（不在 PromoteLayer 内）
- 写入 ChampionHistory.RetiredAt / RetiredBy / RetireNote
```

---

## Phase 5.5 — GA 工程加固（DSR / SBB / RNG / 哈希）

### 目标

落地三份基线提到但需要独立实现的几个关键基础设施：DSR、SBB 自动块长、确定性 RNG、canonical hash。

### Prompt

```
请实现以下加固模块：

一、internal/repository/sharpe_bank.go

SharpeBankRepo：
- Add(strategyID, pairID string, entry SharpeBankEntry) error
- Stats(strategyID, pairID string) (n int, sharpeVariance float64, err error)
- 内部用 SharpeBank GORM 表存储

二、internal/verification/dsr.go（对应 I-4.2）

ComputeDSR(observedSharpe, sharpeVariance float64, nTrials int, horizonT, skew, excessKurt float64) float64

实现 Bailey & López de Prado 2014 闭式公式（详见 I-4.2）。

注意：n < 5 时返回 NaN（可靠性阈值），由调用方处理为"积累中"灰色显示。
实现 NormalCDF / NormalInverse 数值近似（Acklam 算法或现成库）。

三、internal/verification/sbb.go（对应 I-4.3）

OptimalBlockLength(returns []float64) int

按 Politis-White (2004) + Patton-Politis-White (2009) 实现六步算法（详见 I-4.3）。

RunMonteCarlo(returns []float64, blockLenMean int, nIter int, seed uint64) MCReport
- 块长 ~ Geom(1/blockLenMean)
- 计算破产概率、5/50/95 分位最终权益、最坏 1% MDD
- MCReport 写入 diagnostics.stress_summary（v3 阶段可为空对象）

四、确定性 RNG 注入 internal/engine/rng.go

- SplitMix64(seed uint64) uint64        // master seed splitter
- DeriveWorkerRNG(masterSeed uint64, workerID int) *rand.Rand
- DeriveMutationRNG(masterSeed, generationID, individualID uint64) *rand.Rand
- DeriveCrossoverRNG(masterSeed, generationID uint64) *rand.Rand

全引擎 RNG 必须经过此路径派生，禁止直接调用 rand.Seed() 或使用全局 rand.New。

五、CanonicalJSON 序列化（已在 Phase 3A 实现，本 Phase 验证）

确保 internal/quant/canonical_json.go 满足：
- 字段名字典序排序输出
- 浮点固定 17 位有效数字
- 时间戳整型直出
- 同输入两次序列化结果完全相同
- 用于 plan_hash / bars_hash 计算

六、grep 校验

- grep -rn 'rand\.Seed\|rand\.New(rand\.NewSource' internal/ | grep -v 'internal/engine/rng.go'
  期望仅 rng.go 出现
- grep -rn 'time\.Now()' internal/strategies/
  期望无结果
- grep -rn '\\*[a-zA-Z]*\\.Value' internal/engine/ | grep -v 'CompareFitness\\|nil check'
  期望：对 SliceScore.Value 的直接解引用只发生在 nil 检查之后
```

---

## Phase 6 — 实例生命周期 + Cron Tick

```
请阅读 docs/系统总体拓扑结构.md §6，然后实现实例生命周期管理。

internal/saas/instance/manager.go：

Tick 函数（cron 每分钟扫描 RUNNING 实例时调用）：

步骤一：幂等桶去重检查
步骤二：读取 PortfolioState 和 RuntimeState
步骤三：加载冠军参数包
步骤四：ACL 外圈处理 + 构造 NowMs
        nowMs := time.Now().UnixMilli()  // 仅在 Tick 外圈调用一次
        input := quant.StrategyInput{
            NowMs: nowMs,
            Closes: closes,
            Timestamps: timestamps,
            Portfolio: portfolio,
            Params: params,
            LastProcessedBarTime: portfolioState.LastProcessedBarTime,
        }
步骤五：构建 StrategyInput
步骤六：调用 Step()
步骤七：持久化 RuntimeState
步骤八：处理释放意图（DeadBTC → FloatBTC 翻账，不下发 Agent）
步骤九：翻译 OrderIntent 为 TradeCommand，写 pending SpotExecution，通过 WS Hub 下发
步骤十：更新 LastProcessedBarTime

铁律：Tick 函数是 Step() 之外唯一允许调用 time.Now() 的地方（属于 cron tick 外圈）。

internal/saas/cron/scheduler.go：每分钟扫描，为每个 RUNNING 实例并发启动 Tick goroutine。
```

---

## Phase 7 — LocalAgent

（与 v2 内容基本一致，关键点：）

- Agent 端必须独立实现摩擦观察（实际成交价 vs 期望价的偏差应记录到 `SpotExecution.ActualSlippageBps`），方便后续比对 GA 假设的摩擦参数是否仍代表当前实盘条件
- API Key 永不上送 SaaS（铁律 3）
- 断线重连指数退避（初始 1s，最大 5min）
- 心跳间隔 30s，超过 90s 未心跳则 SaaS 端标记 STALE 并暂停下单

---

## Phase 8 — WebSocket Hub

（与 v2 内容基本一致）

- 消息类型：`hello` / `auth` / `auth_ok` / `trade_command` / `delta_report` / `ping` / `pong` / `error`
- 鉴权超时 10s
- 未鉴权连接 10s 内强制断开

---

## Phase 9 — REST API

```
v3 路由总集（在 v2 基础上扩展）：

# 进化任务（与 II-3.1 对齐）
POST /api/v1/evolution/tasks                        创建并启动进化任务（lab/dev only）
GET  /api/v1/evolution/tasks/:task_id               EvolutionTaskStatusResponse
GET  /api/v1/evolution/tasks                        列出最近 N 个任务

# Challenger 与 Champion
GET  /api/v1/challengers/:challenger_id             challenger 元数据 + 概览
GET  /api/v1/challengers/:challenger_id/package     完整 ChallengerResultPackage（五层）
POST /api/v1/challengers/:challenger_id/promote     Promote
POST /api/v1/champions/:champion_id/retire          Retire（独立接口）
GET  /api/v1/genome/champion                         当前冠军基因包
GET  /api/v1/champions/history                       Champion 历史（含退役记录）

# 数据与诊断
GET  /api/v1/data/coverage                          每个 (Symbol, Interval) 的数据覆盖区间和缺口数
POST /api/v1/data/import                            触发 datafeeder import（lab only）
GET  /api/v1/data/gaps                              查询 KLineGap 表
GET  /api/v1/ga/sharpebank/stats                    SharpeBank 累积情况（前端展示 DSR 可信度）

# 实例与订单
POST /api/v1/instances                              创建实例
GET  /api/v1/instances/:id                          实例详情
POST /api/v1/instances/:id/start
POST /api/v1/instances/:id/stop
GET  /api/v1/instances/:id/trades                   订单流水
```

---

## Phase 10 — 系统入口（cmd 层）

```
请实现 cmd/saas/main.go、cmd/agent/main.go、cmd/lab/main.go（可与 saas 共享 binary 通过 app_role 切换）。

启动时校验：
1. TimescaleDB 扩展已启用
2. 关键表（klines / kline_gaps / sharpe_bank / champion_history）存在
3. 近 24 小时内 KLineGap 表无新发现缺口（warn 不 fatal）
4. 配置 schema_version="v5.3.3" / fitness_version="v1-raw-std" / fingerprint_version="fp-v1" 与代码常量一致
5. app_role=saas 时拒绝注册 lab 专属路由（POST /api/v1/evolution/tasks 等）；app_role=lab 时拒绝注册实盘订单路由
6. 优雅停机：收到 SIGTERM 时先停止新接受任务，等待在跑 Epoch 完成或超时后写状态快照
```

---

## Phase 11 — 测试与验证(11 必落 + 12 扩展,v3.2 修订)

> **v3.1 修订**:严格区分"**必落 11 条**(对齐框架文档 v5.4 §10.1,原型阶段必须 PASS)"与"**扩展测试**(强烈建议但不阻塞原型交付)"。所有涉及排序的测试**必须使用 `sort.SliceStable`,禁止 `sort.Slice`**,以保证 Fingerprint collision 场景的稳定性。

### 一、必落 11 条测试(对齐框架文档 §10.1)

```
请为以下 11 条测试编写最小实现:

1. TestEvaluateDeterministic
   相同输入两次 Evaluate 结果在容差内一致(ScoreTotal 差异 ≤ 1e-6)

2. TestEvaluateOrderInvariance
   100 个 gene 正序 vs 倒序评估,结果集合在容差内一致(顺序无关性)

3. TestAdapterResetIsolation(v3.1 强化)
   构造两个差异较大的 Gene A 和 B,断言 [A, B] 顺序评估的结果与 [B, A]
   顺序在容差内一致。
   反向断言:人为破坏 Reset()(不清空持仓态),结果必须不一致(验证测试本身有效)。
   反向断言:人为保留依赖 Gene 内容的中间值,结果必须不一致。

4. TestClampValidateContract
   随机生成越界 Gene,Clamp 后 Validate 必须通过

5. TestSegmentsCoverage
   Segments() 覆盖所有维度,无遗漏无重复;
   len(Dimensions) == len(QuantizationStep) == len(GeneStep) 三者长度一致

6. TestCrossoverBlockFidelity
   随机抽 100 个 child,每个 Segment 完整等于 p1 或 p2,不存在维度级混合

7. TestReplayWithinTolerance
   同 EpochSeed、同 data_version、同 plan_hash、同 bars_hash,两次 RunEpoch
   产出的 challenger ChromosomeJSON 完全相同;ScoreTotal 差异 ≤ 1e-6

8. TestGapHandlingNoFakeTrades
   构造含 KLineGap 的 plan,断言:IsGap=true 时无虚假成交,indicator 跳过该
   bar;ImportSymbol 不做插值

9. TestMutationScaleLinearity
   scale=2.0 时平均变异量约为 scale=1.0 的 2 倍(统计学意义上,N=1000 个体)

10. TestCascadeShortCircuit
    构造 6m 窗口必定 Fatal 的 Gene,断言:
    - SliceScore[6m].Fatal == true
    - SliceScore[2y/5y/10y].SkippedBy != nil 且值为 "cascaded_from_6m"
    - SliceScore[2y/5y/10y].Value == nil
    - SliceScore[2y/5y/10y].BarsEvaluated == 0
    - ScoreTotal.Fatal == true, ScoreTotal.Value == nil
    额外验证:2y Fatal 时跳过 5y/10y(SkippedBy="cascaded_from_2y");
    5y Fatal 时跳过 10y(SkippedBy="cascaded_from_5y")。
    评估顺序必须为 6m → 2y → 5y → 10y。

11. TestCompareFitnessNilSafe
    构造混合 Fatal/Normal 个体(含双 Fatal 情形),调用 sort.SliceStable
    (v3.1 修订:禁止 sort.Slice) 配合 CompareFitness:
    - 不 Panic
    - 所有 Normal 个体排在所有 Fatal 个体之前
    - Normal 内部按 Value 降序
    - 双 Fatal 时按 Fingerprint 字典序稳定排序
```

### 二、扩展测试(强烈建议,v3.2:12 条)

> 不阻塞原型交付,但在 PR 合并前应全部 PASS。v3.2 新增 3 条(22/23/24)对应 M14/M15/M16 的契约约束。

```
12. TestFatalAuditSampling
    构造 fatal 率 100% 的种群,FatalAuditSampleRate=0.05;断言
    diagnostics.fatal_audit_samples 大小约为 PopSize × 0.05(±20% 波动)

13. TestSchemaVersionAlignment(v3.1 强化)
    JSON round-trip:构造 ChallengerResultPackage,序列化后反序列化,所有字
    段保持;schema_version / fitness_version / fingerprint_version 必须
    从 internal/resultpkg/versions.go 常量读取并与代码常量完全一致
    (验证 M08 的版本号常量包约束)

14. TestSliceScoreThreeStateExclusive
    遍历 SliceScore × CrucibleResult 的所有可能状态组合,CrucibleResult.Validate()
    仅在三态之一时通过(Normal: Fatal=false, SkippedBy=nil, Value!=nil;
    Cascaded: Fatal=false, SkippedBy!=nil, Value=nil;
    SelfFatal: Fatal=true, SkippedBy=nil, Value=nil)

15. TestPlanHashStability
    同一个 EvaluablePlan 在不同进程、不同时间序列化两次,plan_hash 完全相同

16. TestTestModeSnapshotEffectiveValues(v3.2 修订,原 TestTestModeNotPromotable)
    test_mode=true 跑完 Epoch,断言(v3.2 M16 强化):
    - challenger.core.ga_config.test_mode == true
    - challenger.core.ga_config.taker_fee_bps == 0 (v3.2:GAConfigSnapshot 存生效值)
    - challenger.core.ga_config.slippage_bps == 0
    - challenger.evaluation.friction_actual.taker_fee_bps == 0
    - challenger.evaluation.friction_actual.slippage_bps == 0
    - ga_config.taker_fee_bps == evaluation.friction_actual.taker_fee_bps (数值一致,消除自相矛盾)
    - 对应 EvolutionTask.RequestedTakerFeeBps == 10 (原始请求意图保留在 task 表)
    - 调用 Promote API 返回 400(test_mode 产物不可 Promote)
    - 结果包中**不存在** friction_disabled 独立字段(v3.1 删除)
    - 结果包中**不存在** taker_fee_bps_actual / slippage_bps_actual 字段(v3.2 删除)

17. TestSbbBlockLength
    输入 AR(1) phi=0.5 n=10000 合成序列;期望 OptimalBlockLength 在理论
    最优块长 ±30% 区间内

18. TestDSRReliabilityThreshold(v3.1 强化)
    - SharpeBank n=4 时 ComputeDSR 返回 NaN
    - SharpeBank n=10 时返回 [0.5, 0.95] 内的有限值
    - DSR 结果**写入 verification.dsr_summary**(v3.1:验证 M11 字段归属)

19. TestGhostDCAFrictionParity
    Monthly 和 Weekly 使用相同 FrictionParams,分别跑摩擦 vs 无摩擦,最终
    权益差异符合摩擦扣减的解析预期

20. TestCompareFitnessFingerprintCollision(v3.1 新增)
    人为构造两个 Fingerprint 完全相同的 Fatal 个体(例如直接覆盖
    Fingerprint 字段为相同字符串):
    - 调用 sort.SliceStable 配合 CompareFitness
    - 排序前的相对顺序在排序后保持(稳定性)
    - 多次重复调用结果一致
    - 不 Panic
    该测试覆盖 hash collision 的极低概率场景,以及双 Fatal 的退化排序行为。

21. WebSocket 协议测试 hub_test.go
    - 鉴权超时强制断连
    - delta_report 按 user_id 路由到对应实例

22. TestRawEvaluateResultNoScoreTotal(v3.2 M14 新增)
    通过 Go reflect 反射 RawEvaluateResult 类型,断言:
    - 不含字段名为 "ScoreTotal" 的字段(任意大小写)
    - 字段集合 == {Windows, FrictionActual}
    该测试验证类型级强约束 —— 策略物理上不可能写 ScoreTotal。

23. TestDecisionStatusPromotedRename(v3.2 M15 新增)
    JSON round-trip:
    - 序列化 PromoteLayer{DecisionStatus: "promoted"} 成功
    - 序列化 PromoteLayer{DecisionStatus: "approved"} 失败(Validate 拒绝)
    - 反序列化历史含 "approved" 的 JSON 字符串触发 migration warning(可选)

24. TestGAConfigSnapshotEffectiveValues(v3.2 M16 新增)
    用 test_mode=false + taker_fee_bps=10 + slippage_bps=5 跑 Epoch,断言:
    - GAConfigSnapshot.TakerFeeBps == 10
    - GAConfigSnapshot.SlippageBps == 5
    - EvolutionTask.RequestedTakerFeeBps == 10
    用 test_mode=true + 同上 跑 Epoch,断言:
    - GAConfigSnapshot.TakerFeeBps == 0 (生效值)
    - GAConfigSnapshot.SlippageBps == 0
    - EvolutionTask.RequestedTakerFeeBps == 10 (请求意图保留)
```

### 三、运行

```
go test ./... -race -timeout 600s

要求 race detector 通过,11 条必落测试全部 PASS;扩展测试在 PR 合并前
应全部 PASS。
```

---

## Phase 12 — 基础观测（精简版）

> **完整 Prometheus 指标矩阵、Web 前端展示、Promote 决策面板、磁盘空间监控均移入 Part III-1。** 本 Phase 仅落地最小观测能力。

### Prompt

```
一、结构化日志规范 internal/saas/logger/logger.go

所有日志必须包含：
- timestamp (RFC3339)
- level
- module (cron / engine / ws / instance / datafeed)
- 业务上下文（user_id / instance_id / strategy_id / epoch_id / challenger_id 视情况）

GA 关键事件统一事件名：
- ga.epoch.start / ga.epoch.complete / ga.epoch.fatal
- ga.generation.complete
- ga.challenger.created
- ga.compare_fitness.panic_attempted（理论上永远不应触发，触发即测试失败）
- ga.cascade_short_circuit.triggered
- ga.mutation_ramp.triggered
- ga.early_stop

二、最小 Prometheus 指标 internal/saas/metrics/metrics.go

仅暴露以下 6 个 metric，更多指标见 Part III-1：

- ga_epoch_total{strategy, status}                Epoch 完成总数
- ga_epoch_duration_seconds{strategy}             Epoch 耗时直方图
- ga_challenger_score{strategy, pair}             最近 challenger 评分
- instance_tick_duration_seconds{instance}        Tick 耗时
- agent_connection_status{user_id}                1=在线 / 0=离线
- klines_coverage_days{symbol, interval}          数据覆盖天数

暴露端点：GET /metrics（仅 internal 网络 / lab 模式开放）

三、Agent 心跳监控 internal/saas/ws/heartbeat_monitor.go

后台 goroutine 每 30 秒扫描所有 Agent 连接，若超过 90 秒未收到心跳：
- 标记 Agent 状态为 STALE
- 该用户的所有 RUNNING 实例临时不下发新订单
- 触发 audit_log 记录

四、回放与可复现性测试 internal/engine/replay_test.go

固定 EpochSeed + 固定历史数据快照（同 plan_hash + 同 bars_hash），跑两次 RunEpoch；
断言两次产出的 challenger ChromosomeJSON 完全相同 + ScoreTotal 容差内一致。
```

---

## Phase 13 — Docker 部署配置

```
请创建生产部署所需的全部配置文件。

saas.Dockerfile / agent.Dockerfile：多阶段构建，最终镜像 alpine + binary。

docker-compose.yml 要点：
- postgres 镜像：timescale/timescaledb:latest-pg16（内置 TimescaleDB）
- saas 服务的 healthcheck 增加：POST /health/db（含 timescaledb 扩展存在性检查）
- 新增 datafeeder 一次性容器（profile=tools），用于批量数据导入
- prometheus 服务可选（profile=monitoring）
- 显式声明 schema_version / fitness_version / fingerprint_version 作为环境变量，启动时与代码常量校验

.gitignore 必须包含：
config.agent.yaml
*.env
*.exe
bin/
migrations/atlas.sum   # 如果启用 Atlas 版本化迁移
```

---

# II-5. 完整验收检查清单

## 架构铁律（grep 验证）

```bash
# 策略包内无 isBacktest 分支
grep -rn "isBacktest" internal/strategies/                              # 期望无结果

# 策略包内无墙钟读取（铁律 2）
grep -rnE "time\.Now|time\.Since" internal/strategies/                  # 期望无结果

# SaaS 侧无 API Key 字段（铁律 3）
grep -rn "api_key\|secret_key\|passphrase" internal/saas/               # 期望无结果

# 策略内核不依赖 Bar
grep -rn "quant\.Bar" internal/strategies/                              # 期望无结果

# 摩擦不出现在策略内
grep -rnE "TakerFee|Slippage" internal/strategies/                      # 期望无结果

# 全局 rand 调用只允许在 rng.go 内
grep -rn "rand\.Seed\|rand\.New(rand\.NewSource" internal/ | grep -v "internal/engine/rng.go"
# 期望无结果

# decision_status 不含 retired
grep -rn 'DecisionStatusRetired\|"retired"' internal/resultpkg/ internal/api/    # 期望无结果

# 9-verb 残留检查（v2 → v3 升级残留）
grep -rn "CrossoverSegments\|Verify(ctx" internal/                       # 期望无结果（应为 Segments / ReviewBacktest）

# v3.1 新增:friction_disabled 字段残留检查(M10)
grep -rn "friction_disabled\|FrictionDisabled" internal/                 # 期望无结果(改为 test_mode 推导 + FrictionActual)

# v3.1 新增:版本号硬编码检查(M08)
grep -rn '"v5.3.3"\|"v1-raw-std"\|"fp-v1"' internal/ | grep -v "internal/resultpkg/versions.go"
# 期望无结果(除测试 fixture 外,版本号必须从 versions.go 常量读取)

# v3.1 新增:sort.Slice 禁用检查(M12)
grep -rn "sort\.Slice(" internal/engine/ internal/fitness/               # 期望无结果(必须使用 sort.SliceStable)

# SliceScore.Value 解引用必须在 nil 检查之后
grep -rn '\*.*\.Value' internal/engine/ | grep -v 'nil\|CompareFitness'  # 人工审查

# v3.2 新增:RawEvaluateResult 不应含 ScoreTotal(M14 类型级保证)
grep -rn "type RawEvaluateResult" internal/strategy/                     # 应找到定义
grep -rn "RawEvaluateResult.*ScoreTotal\|ScoreTotal.*RawEvaluateResult" internal/strategy/   # 期望无结果

# v3.2 新增:策略侧禁止出现 ScoreTotal 写入(M14)
grep -rn "ScoreTotal\s*[:=]" internal/strategies/ internal/saas/ga/      # 期望无结果(只允许引擎层写)
grep -rn "AggregateScoreTotal" internal/strategies/ internal/saas/ga/    # 期望无结果(策略不应调用聚合函数)

# v3.2 新增:DecisionStatus 不含 approved(M15)
grep -rn '"approved"\|DecisionStatusApproved' internal/                  # 期望无结果(已重命名为 promoted)

# v3.2 新增:GeneRecord 不应含 TakerFeeBpsActual / SlippageBpsActual 冗余字段(M16)
grep -rn "TakerFeeBpsActual\|SlippageBpsActual" internal/                # 期望无结果

# v3.2 新增:EvolutionTask 必须含 RequestedTakerFeeBps / RequestedSlippageBps(M16)
grep -rn "RequestedTakerFeeBps\|RequestedSlippageBps" internal/saas/store/  # 应找到字段定义
```

## 功能验证

- `go build ./...` 无错误
- `go test ./... -race` 全部通过
- 11 条必落测试全部 PASS;12 条扩展测试在 PR 合并前 PASS
- 相同 EpochSeed + 相同 plan_hash + 相同 bars_hash + 相同摩擦参数,回测两次输出字段完全一致
- GA test_mode(Pop=10, Gen=3) 能完整运行并写入 challenger 记录(v3.2 M16:`core.ga_config.test_mode=true`、`core.ga_config.taker_fee_bps=0`、`core.ga_config.slippage_bps=0`、`evaluation.friction_actual={0,0}`;数值上无矛盾);对应 `EvolutionTask.RequestedTakerFeeBps` 保留原始请求值;Promote API 返回 400;结果包中**不存在** `friction_disabled` 与 `taker_fee_bps_actual` / `slippage_bps_actual` 字段
- v3.2 M14:`RawEvaluateResult` struct 物理上无 `ScoreTotal` 字段(反射验证通过);所有 ScoreTotal 计算路径都经过 `internal/fitness/aggregate.go::AggregateScoreTotal`
- v3.2 M15:`PromoteLayer.DecisionStatus` 只接受 `pending / promoted / rejected`,拒绝旧值 `approved`
- SharpeBank 累积到 ≥5 条目后,新 challenger 报告中 DSR 字段非 NaN
- SBB 块长估计在 BTC 1m 收益序列上返回值落在 [100, 1440]
- 任一窗口 Fatal 时,ScoreTotal.Fatal=true 且 ScoreTotal.Value=nil(不是 ScoreRaw,详见 I-3.9 修订)
- 6m Fatal 时,2y/5y/10y 三个 CrucibleResult.SkippedBy="cascaded_from_6m" 且 Value=nil
- ChallengerResultPackage.Validate() 通过:五层非空,三件套版本号(schema=v5.3.3 / fitness=v1-raw-std / fingerprint=fp-v1)一致

## 数据完整性验证

- `datafeeder verify --symbol BTCUSDT --interval 1m` 输出的缺口数与 Binance 官方维护记录吻合
- KLine 表对 BTC/USDT 9 年 1m 数据，启用 TimescaleDB 压缩后磁盘占用 < 300 MB

## 安全检查

- `config.agent.yaml` 在 `.gitignore` 中
- 未鉴权 WebSocket 连接 10 秒内断开
- `app_role=saas` 时访问 lab 专属路由返回 403
- `app_role=lab` 时拒绝下发真实交易指令

---

---

# ============================================
# Part III — 未来版本迭代
# ============================================

> 对齐《进化系统_程序框架规划_v5_4_1》§12 三阶段实施建议。**原型阶段（Part II）只完成"可运行、可重放、可诊断、可 Promote"的服务闭环**；以下内容均不阻塞原型交付。

---

# III-1. 第二阶段：验证与报告增强

目标：把原型阶段为简化而省略的验证与可视化能力补齐，但仍不引入跨阶段架构变更。

## III-1.1 完整 Prometheus 指标矩阵

### 完整 GA 指标

- `ga_evaluation_total{strategy, fatal}`                 个体评估次数
- `ga_fingerprint_cache_hit_ratio{strategy}`             指纹缓存命中率
- `ga_diversity_rescue_total{strategy}`                  Diversity Rescue 触发次数
- `ga_sharpebank_size{strategy, pair}`                   DSR 累积样本数
- `ga_cascade_short_circuit_ratio{strategy, window}`     级联短路触发占比
- `ga_fatal_audit_sample_count{strategy}`                Fatal 抽样审计样本数

### 完整实例指标

- `instance_orders_total{instance, action, status}`
- `agent_message_lag_seconds{user_id}`

### 完整数据指标

- `klines_gap_count{symbol, interval}`
- `klines_compression_ratio{symbol}`

### 系统指标

- 磁盘空间监控：zap logger 启动时检查 `/var/lib/postgresql` 所在磁盘剩余空间，< 10% 时启动日志告警

## III-1.2 Web 前端（Promote 决策面板）

- 实例管理面板（v2 已规划，留到此阶段）
- Promote 决策面板：展示
  - OOS / DSR 颜色门槛（`green / yellow / red / gray`，对应 I-4.4）
  - `SharpeBank.size < 5` 时 DSR 显示"积累中"灰色态
  - `oos_alpha_monthly` / `oos_alpha_weekly` 的 mean ± std
  - 摩擦参数与 `core.ga_config.test_mode == true` 推导的"摩擦禁用"标识(v3.1:不再有独立 friction_disabled 字段);`evaluation.friction_actual` 展示实际生效摩擦值
  - `fitness_version` 标签（跨版本不直接比较时给出提示）
- challenger 报告页：五层结构可视化（core / evaluation / verification / diagnostics / promote）

## III-1.3 Fatal 抽样审计落地（5%）

原型阶段写入 `diagnostics.fatal_audit_samples` 字段但前端不消费；二期开始：

- 提供 audit 样本浏览页面
- Fatal 原因分类（`fatal_reason`）的统计仪表盘
- Fatal 触发时点（`fatal_at_bar_ts`）的时间分布分析

## III-1.4 Diversity Rescue 第二层

原型阶段在 Phase 5 只落地变异斜坡（层一）；二期开始落地层二（多样性救援），按 Part I §I-3.12 实现：

- 贪心 max-min diversity 算法选 Top-5 核心精英
- 60% 完全随机 / 25% 强变异 / 15% 杂交后代重填
- 救援后冻结 3 代不再触发

## III-1.5 fitness_version 升级到 v2-zscore

- 实现 z-score 标准化方案，`λ_cons = 0.02`
- 写入 `fitness_version = "v2-zscore"`
- 前端展示跨版本警告

## III-1.6 ChampionHistory 完整面板

- 历任 Champion 时间线
- Champion 退役原因分类
- Champion 之间的 fingerprint 距离矩阵

---

# III-2. 第三阶段：审计与历史回顾

目标：引入"Promote 后回顾"与"长期审计"能力。这些能力需要新的数据流（不仅是结果包）。

## III-2.1 ReviewBacktest 实现

原型阶段 `EvolvableStrategy.ReviewBacktest()` 返回 `nil` 占位。三期落地：

- Promote 后由用户显式触发
- 数据范围：IS + OOS + Sacred 全量
- **仅展示，不影响决策**（与 OOS 区分）
- 结果写入 `verification.review_summary`
- 提供 `POST /api/v1/champions/:id/review_backtest` 触发接口

## III-2.2 Sacred Holdout 双层 OOS

- 在 Anchored OOS Holdout 之上再切出一段"神圣窗口"，对系统级元参数搜索保密
- 仅在大版本升级时使用
- `EvaluablePlan` 新增 `SacredOosWindow` 字段（原型阶段不实现）

## III-2.3 邻域稳定性测试（§6.9）

- SpawnPoint 扰动敏感度
- 参数邻域脆性检测（高斯邻域 N=30 采样 + IsCritical Segment 的 OAT 扰动）
- `IsCritical=true` 的 Segment 单独做 OAT 测试

## III-2.4 Audit API 与 Re-evaluation History

- `GET /api/v1/audit/replay/:challenger_id` 触发重放，断言容差内一致
- `GET /api/v1/audit/re_evaluations/:champion_id` 历史月度再评估记录
- Promote 后月度再评估调度

## III-2.5 种群快照与长期归档

- 每代 Top-K 个体快照（含完整 Gene + ScoreTotal）
- 跨 Epoch 的种群多样性长期趋势
- 归档存储到对象存储（S3 兼容）

## III-2.6 Champion 实盘追踪

- Promote 后实盘 alpha 实时跟踪
- 月度再评估对比 backtest 预期 vs 实盘表现
- 偏差超阈值时自动告警

---

# III-3. Optional：AI 多维信号层（v2 Phase 14）

保持 v2 设计：

- 在 cron tick 外圈调用 LLM 获取多维信号向量
- 结果写入 `StrategyInput.AISignalVector`
- `Step()` 内部不读墙钟、不调用 LLM（铁律 2）
- 与现有 Sigmoid 框架兼容（`AISignalVector` 作为 `Signal` 的一个输入因子）

落地优先级：低（属于策略增强，不属于框架能力）。

---

# III-4. 多标的与多策略扩展

- `pair` 字段升级为 `TradingPair` 类型（待决问题 Q1）
- 多策略并行评估（每个策略独立 SharpeBank）
- 跨标的相关性分析
- 投资组合级 Promote 决策

---

# III-5. 数据契约第二阶段收紧

按《进化系统_v5_4_Go-only_JSON_Schema_v533》§11.2 的待收紧字段：

- `alpha_breakdown` → 明确结构体
- `dsr_summary` → 明确结构体
- `stress_summary` → 明确结构体
- `turnover_metrics` → 明确结构体
- `clamp_modifications` → 明确结构体
- `diversity_rescue_log` → 明确结构体
- `mutation_ramp_log` → 明确结构体
- `slice_score.reason` / `fatal_reason` → 枚举化（`mdd_exceeded` / `invalid_path` / `insufficient_bars`）

---

---

# ============================================
# 附录
# ============================================

# 附录 A:v1 → v2 → v3 → v3.1 → v3.2 主要变化速查

## A.0 v3.1 → v3.2 修订(基线文档同步升级)

| 编号 | 章节 | 修订要点 | 修订原因 |
|---|---|---|---|
| M14 | II-3.4 + II-3.5 + Phase 5A/5B | **拆分两阶段 result**:策略产 `RawEvaluateResult`(无 ScoreTotal),引擎组装 `EvaluationLayer`(含 ScoreTotal)。`Adapter.Evaluate` / `EvolvableStrategy.Evaluate` 返回 `*RawEvaluateResult` | 类型级强保证 vs v3.1 的注释软约束;支持未来 NSGA-II / Pareto 多目标演进无策略层改动 |
| M15 | II-3.6 + Phase 2 + Phase 5D + 全文 | `DecisionStatus` 枚举 `approved` → `promoted`,三态变为 `pending / promoted / rejected` | 状态机真实终态语义:审批通过 = 已晋升为 Champion |
| M16 | II-3.3 + Phase 2 + Phase 5D | `GAConfigSnapshot` 字段语义改为**生效值**;`test_mode=true` 时 `taker_fee_bps`/`slippage_bps` 直接为 0;删除 v3.1 GeneRecord 中 `TakerFeeBpsActual`/`SlippageBpsActual` 冗余字段;`EvolutionTask` 表新增 `RequestedTakerFeeBps`/`RequestedSlippageBps` 记录请求意图 | 消除快照内自相矛盾;请求 vs 生效解耦 |
| M17 | II-3.4 + II-3.5 注释 | 基线注释加固:框架文档 §5.1/§5.6 明确两个 Evaluate 语义边界;Go struct 草案对 Bar 标注"`IsGap`/`GapType` 不参与 bars_hash" | 文档级澄清,无 struct 修订 |
| M18 | II-3.7 + Phase 0 + Phase 10 | `schema_version` 升级 `v5.3.2` → `v5.3.3`;`versions.go` 同步;Phase 10 启动校验三件套与基线一致 | 基线 schema 升级同步 |

## A.1 v3 → v3.1 修订(基于搭档审阅意见)

| 编号 | 章节 | 修订要点 | 修订原因 |
|---|---|---|---|
| M01 | II-3.4 | `Evaluate` 返回 `*EvaluateResult`(替代 `[]CrucibleResult`);新增 `internal/fitness/aggregate.go` 共享聚合函数 | 对齐 Go struct 草案 §4.3 |
| M02 | I-3.6/3.7/3.9 + Phase 5B/5C | **窗口权重固定不重新归一化**;两类窗口缺失语义分开(plan 期不构建 vs 评估期 `Fatal=true, reason="insufficient_bars"`) | "权重不可调"原则 + 数据长度不同时分数可比性 |
| M03 | I-3.7 + 结果包 | 分量写入 `CrucibleResult.AlphaBreakdown`(json.RawMessage,二期收紧) | 字段归属明确 |
| M04 | I-3.8 + Phase 5B | 评估循环**必须**按 6m → 2y → 5y → 10y 固定顺序 | `SkippedBy` 枚举的前提 |
| M05 | I-3.13 | `bars_hash` 序列化范围固化为**完整 OHLCV + OpenTime** | 关闭附录 C Q4 待决 |
| M06 | I-4.1/4.4 | `DecisionColor` 明确为 `VerificationLayer.OOSResult.DecisionColor` 子字段 | 字段归属对齐草案 §4.11 |
| M07 | II-3.5 + Phase 5A | Reset 保留缓存**必须与 Gene 无关**;Test 覆盖 | 隐蔽状态泄露风险 |
| M08 | II-3.7 | 版本号集中到 `/internal/resultpkg/versions.go`;`FitnessCalculator` 接口抽象 | 版本治理 + 升级路径 |
| M09 | Phase 2 | GeneRecord.DecisionStatus 严格三态镜像,`retired` 永不写入 | 语义边界清晰 |
| M10 | 全文 | 删除 `friction_disabled` 独立字段,通过 `test_mode` 推导;`FrictionActual` 记录实际值 | 与基线契约一致,避免字段冗余 |
| M11 | Phase 5B + I-4.2 | DSR 写入 `VerificationLayer.DSRSummary`,不放 PromoteLayer | 对齐草案 §4.11 |
| M12 | Phase 11 | 11 必落 + 9 扩展分层;补 `TestCompareFitnessFingerprintCollision`;强制 `sort.SliceStable` | 优先级清晰 + collision 稳定性 |
| M13 | 附录 A | 新增本表 | 变更追溯 |

## A.2 v1 → v2 → v3 主要变化(继承自 v3)

| 类别 | v1 | v2 | v3 | 原因 |
|---|---|---|---|---|
| **文档结构** | 单一时间线 | 单一时间线 | **Part I/II/III 三段** | 数学/算法/系统分离 |
| **接口动词数** | 8 | 9 | **14** | 框架文档 v5.4 §5.1 完整化 |
| **Verify 方法** | 存在 | 存在 | **重命名为 `ReviewBacktest`** | 角色边界清晰（不影响决策）|
| **四窗口** | `6m/2y/5y/full` | `6m/2y/5y/full` | **`6m/2y/5y/10y` 枚举** | 与 schema 冻结值一致 |
| **OOS 模式** | 缺失 | `oos_days + embargo_days` | **Anchored Holdout，仅 `oos_days`** | 简化字段，无需 embargo |
| **Adapter 接口** | 缺失 | 缺失 | **`NewAdapter/Reset/Close`** | 状态隔离统一由引擎保证 |
| **CompareFitness** | 直接比较 | 直接比较 | **强制封装，禁止 nil 解引用** | 防 Panic，禁止哨兵数值 |
| **级联短路** | 缺失 | 缺失 | **6m→2y→5y→10y 短路** | 节省 70% 计算量 |
| **`fatal_audit_sample_rate`** | 缺失 | 缺失 | **默认 0.05** | 短路不丢诊断 |
| **`plan_hash`/`bars_hash`** | 缺失 | 缺失 | **SHA256(canonical JSON)** | 复现验证锚点 |
| **结果包结构** | 自由 | 自由 | **五层冻结** | 与 schema v5.3.2 对齐 |
| **`decision_status`** | 自由字符串 | 自由字符串 | 三态 `pending/approved/rejected`(`retired` 移到 ChampionHistory);**v3.2 M15 重命名为 `pending/promoted/rejected`** | 状态机真实终态语义 |
| **`spawn_mode`** | 自由字符串 | 自由字符串 | **枚举 `inherit/random_once/manual`** | schema 冻结 |
| **`test_mode`** | 字符串 | 字符串 | **`bool`** | 消除 "True" vs "true" 错误 |
| **`schema_version`** | 缺失 | 缺失 | **`v5.3.3`** | 版本治理 |
| **`fitness_version`** | 缺失 | 显式 | **`v1-raw-std`** | 跨版本不直接比较 |
| **`fingerprint_version`** | 缺失 | 显式 | **`fp-v1`** | 量化算法变更升级 |
| **铁律 2** | 禁止 `time.Now()` | 禁止读墙钟，注入 `NowMs` | 不变 | 实现需"当前时间"逻辑 |
| **铁律 4** | 永不写 SQL migration | AutoMigrate + Atlas 双轨 | 不变 | AutoMigrate 缺陷 |
| **铁律 6** | 单一 Postgres | + TimescaleDB | 不变 | 时序数据规模需要 |
| **Phase 1.5** | 缺失 | 新增数据导入 | 不变 | 没数据跑不了 GA |
| **Phase 5.5** | 缺失 | DSR/SBB/RNG/摩擦 | **+ canonical_json/plan_hash** | 复现性必备 |
| **Phase 9.5 完整观测** | 缺失 | 完整 Prometheus | **精简到 Phase 12** | 完整版移入 Part III-1 |
| **Web 前端** | Phase 12 | Phase 12 | **移入 Part III-1** | 原型阶段不阻塞 |
| **AI 信号层** | Phase 14 | Optional Phase 14 | **移入 Part III-3** | 属策略增强 |
| **GA `EliteCount`** | 硬编码 8 | 比例 `EliteRatio=0.05` | 不变 | v4 §2.6 |
| **`FatalMDD`** | 0.88 | 0.70 | 0.70 | v4 §3.1 |
| **适应度公式** | Alpha - 1.5×DD差分 | + 绝对 MDD + 一致性惩罚（fatal 隔离） | 不变 | v4 §3.1 |
| **Ghost DCA** | 仅月度 | 月度 + 周度 | 不变 | v4 §3.2 |
| **Crossover** | Uniform | 块级正交 | **`Segments()` 返回 `[]SegmentInfo`** | 保留量化精度 / GeneStep / IsCritical |
| **Fingerprint** | 统一 1e-6 | 按维度量化 | 不变 | v4 §2.8 |
| **Mutate `eliteStats`** | 缺失 | 缺失 | **明确移除** | 避免接口污染 |

---

# 附录 B：与基线文档的对应关系

## B.1 与《进化系统_程序框架规划_v5_4_1》

| 框架文档 章节 | 本编码计划 章节 |
|---|---|
| §1.2 Go-only server prototype | II-2.2 |
| §2.1 Gene/Chromosome | I-2.7（设计空间）|
| §2.2 Segment | I-2.7 + II-3.4 |
| §2.3 SpawnPoint | I-3.2 |
| §2.4 EvaluablePlan | II-3.4 字段表 |
| §2.5 CrucibleWindow | I-3.6 |
| §2.6 SliceScore/ScoreTotal + CompareFitness | I-3.8 + II-3 |
| §2.7 ChallengerResultPackage | II-3.2 |
| §3 运行时生命周期 | I-3.1 |
| §4 数据与窗口 | I-3.6 + Phase 1.5 |
| §5.1 14-verb 接口 | II-3.4 |
| §5.6 Adapter | II-3.5 |
| §6.5 Fatal + 级联短路 | I-3.8 |
| §6.6 一致性惩罚（v1-raw-std） | I-3.9 |
| §7 并发/确定性/缓存 | Phase 5/5.5 |
| §8 结果包/报告/落表 | II-3.2 + Phase 2 |
| §9 HTTP API | II-3.1 + Phase 9 |
| §10.1 11 条必落测试 | Phase 11 |
| §11 参数治理与版本管理 | II-3.7 |
| §12.1 第一阶段 | Part II 全部 |
| §12.2 第二阶段 | Part III-1 |
| §12.3 第三阶段 | Part III-2 |

## B.2 与《进化系统_v5_4_Go-only_JSON_Schema_v533》（v5.3.2）

| Schema 章节 | 本编码计划 章节 |
|---|---|
| §2.1-2.3 命名/枚举/版本常量 | II-3.6 + II-3.7 |
| §2.4 Fatal 排序语义 | I-3.8 + II-3 |
| §4.1 CreateEvolutionTaskRequest | II-3.1 |
| §4.2 EvolutionTaskStatusResponse | II-3.1 |
| §4.3 PromoteChallengerRequest | II-3.1 |
| §4.4 RetireChampionRequest | II-3.1 |
| §5 结果包主结构 | II-3.2 |
| §6 评估层 | II-3.2 + I-3.7 |
| §7 验证层 | II-3.2 + I-4 |
| §8 诊断/决策层 | II-3.2 |
| §9 元数据/持久化 | II-3.3 |
| §10 Go struct 优先落地 | Phase 2 |
| §11 实施建议 | Phase 5/5.5 |
| 附录 A 修订列表 | 附录 A |

## B.3 与《Go_struct 冻结版定义草案 v3》

| 草案章节 | 本编码计划 章节 |
|---|---|
| §1 包划分建议 | II-2.1 |
| §2 枚举冻结版 | II-3.6 + Phase 2 |
| §3 api/types.go 草案 | II-3.1 + Phase 2 |
| §4 resultpkg/types.go 草案 | II-3.2 + Phase 2 |
| §5 校验函数建议 | Phase 2 + Phase 11 |
| §6 最小落地顺序 | Phase 2 内部顺序 |
| §7 待决问题 | 附录 C |

---

# 附录 C：待决问题清单（不阻塞原型）

继承自三份基线文档：

| # | 问题 | 建议处理时机 |
|---|---|---|
| Q1 | `pair` 是否收敛为专用类型 `TradingPair` | 第二阶段多标的支持时 |
| Q2 | `slice_score.reason` / `fatal_reason` 是否最终枚举化（候选：`mdd_exceeded` / `invalid_path` / `insufficient_bars`） | 第二阶段 |
| Q3 | ~~`score_raw` 是否固定为"一致性惩罚前总分"~~ | **原型期已关闭**:固定为 `Σ weight·score`(一致性惩罚前加权总分，不归一化)，见 `fitness/aggregate.go` `AggregateScoreTotal`(FitnessVersion `v1-raw-std`) |
| Q4 | ~~`bars_hash` 序列化范围~~ | **v3.1 已关闭**:固定为"完整 OHLCV + OpenTime",写入 `canonical_json.go` 顶部注释(见 I-3.13 / M05) |
| Q5 | `hardware_signature` 的标准格式是否需要额外验证 | 可选，不影响主链路 |
| Q6 | `champion_gene.payload` 是否改为统一数组编码或 base64 二进制 | 第三阶段 |
| Q7 | `spawn_point.risk_bounds` 是否升级为明确结构体 | 策略文档确定后 |
| Q8 | `alpha_breakdown`、`dsr_summary`、`stress_summary` 何时收紧为正式结构体 | 第二阶段（见 Part III-5）|
| Q9 | `OosDays` 是否应有工程最小值（如 30） | 第一阶段末尾确认 |
| Q10 | `spawn_point.meta` 是否拆掉避免变成兜底字段 | 第二阶段 |

---

**文档完。**

> **使用建议**:
>
> - 新成员入职:先读 Part I 理解数学,再读 Part II 知道怎么用 Go 实现,最后扫 Part III 知道什么暂时不做
> - 策略设计者:只读 Part I(特别是 I-2 设计空间)
> - 软件工程师 / Cursor 协作:读 Part II,按 Phase 顺序提交 Prompt
> - 产品 / 技术负责人:读附录 A + Part III,做版本路线决策
> - **v3 → v3.1 升级者**:对照文档头部的 M01–M13 修订表逐项确认本仓库代码是否需要 patch;重点检查
>   - `Evaluate` 签名是否已返回 `*EvaluateResult`(M01,**v3.2 进一步修订**,见下)
>   - 窗口加权聚合是否仍在归一化(M02)
>   - `friction_disabled` 字段是否还有残留(M10,grep 验收清单已加)
>   - 是否还在用 `sort.Slice` 而非 `sort.SliceStable`(M12,grep 验收清单已加)
>   - 版本号是否仍有硬编码字符串字面量(M08,grep 验收清单已加)
> - **v3.1 → v3.2 升级者**:由于基线文档同步升级,这是最实质的一次修订。对照 M14–M18 修订表:
>   - `Adapter.Evaluate` / `EvolvableStrategy.Evaluate` 签名改为返回 `*RawEvaluateResult`(M14),原 `*EvaluateResult` 拆分为策略侧 `RawEvaluateResult` + 引擎侧 `EvaluationLayer`
>   - `DecisionStatus` 枚举从 `approved` 改为 `promoted`(M15);所有 API、enum 常量、测试 fixture 同步
>   - `GAConfigSnapshot.TakerFeeBps` / `SlippageBps` 改为生效值(M16);v3.1 GeneRecord 中 `TakerFeeBpsActual` / `SlippageBpsActual` 字段**删除**;`EvolutionTask` 表新增 `RequestedTakerFeeBps` / `RequestedSlippageBps`
>   - `schema_version` 常量从 `v5.3.2` 升级到 `v5.3.3`(M18)
>   - 三份基线文档应同步升级:Go struct 草案 v2 → v3,JSON Schema v5.3.2 → v5.3.3,框架文档 v5.4 → v5.4.1(详见同目录的"基线文档修订 patch list"与"v3.1 → v3.2 迁移路径文档")
