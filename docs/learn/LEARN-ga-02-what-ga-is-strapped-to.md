# 第 2 章 — 本项目把 GA 套在什么上

> 本章目标:把第 1 章在 `toy`(2 维玩具)上看到的 GA 机制,换成真实策略 `sigmoid_v1` 的**13 维基因**和**14 动词接口**。读完你能看懂:一个基因到底是哪 13 个参数、它们怎么分"段"、四个遗传算子(Clamp/Crossover/Mutate/Fingerprint)在**真 13 维**上具体怎么动数字、引擎只通过哪 14 个动词和策略对话、为什么每个 worker 要有自己的 Adapter。
>
> 前置:读完第 1 章(gene / 段 / 交叉 / 变异 / Fingerprint 这几个词的机械含义)。本章**不讲**这些参数在交易里干什么(那是第 3 章),只讲"基因长什么样、引擎怎么搬它、算子怎么改它"。
>
> 阅读方式:核心机制(2.3 的四个算子)我都**带具体数字在真 13 维上走一遍**;14 动词接口(2.4)逐个给职责 + 它依赖的不变量 + 代码位置。规范真源是 `docs/strategies/sigmoid_v1.md` §4 和 `internal/strategy/evolvable.go` 的 doc comment——本章是它们的科普化重述,冲突以真源为准。

## 2.1 基因 = 13 个策略参数

第 1 章说"基因就是一组参数(`[]float64`)"。`toy` 是 2 维;`sigmoid_v1` 是 **13 维**。

这里有个贯穿全项目的设计:**引擎层永远只看到不透明的 `[]float64`,完全不知道每一维是什么含义**。含义只活在策略包里——策略侧用一组下标常量把这 13 个槽位对位成一个有名字的 struct `Chromosome`(`internal/strategies/sigmoid_v1/chromosome.go`):

```go
const (
    geneDimA1                       = 0   // 信号权重 a1
    geneDimA2                       = 1   // 信号权重 a2
    geneDimA3                       = 2   // 信号权重 a3
    geneDimBeta                     = 3   // sigmoid 陡度 β
    geneDimGamma                    = 4   // 库存偏置 γ
    geneDimEMAShortPeriod           = 5   // EMA 短周期
    geneDimEMALongPeriod            = 6   // EMA 长周期
    geneDimMAVShortPeriod           = 7   // 成交量均值短周期
    geneDimMAVLongPeriod            = 8   // 成交量均值长周期
    geneDimQuietThreshold           = 9   // "安静市场"阈值
    geneDimMicroReservePct          = 10  // 现金保留比例
    geneDimMacroInjectUSD           = 11  // 月度注资金额
    geneDimReleaseDrawdownThreshold = 12  // DeadBTC 释放回撤阈值
    GeneDim = 13
)
```

`DecodeChromosome(gene)` 把 `[]float64` 翻成这个 struct,`EncodeChromosome(c)` 是逆操作。**注意一个细节**:整数维(5–8)在 Decode 时用 `int(math.Round(...))` 取整,而且代码注释明确说"这只在调用方已经 Clamp 过的前提下安全"——也就是说,**取整这件事的真正保证在 `Clamp` 里**(2.3 会手算),Decode 只是信任它。

**逐维速览**(取值范围、默认值是 §4.1 表的真源;这里给"是什么量、什么量纲",交易含义留第 3 章):

| 维 | 名字 | 范围 | 默认 | 类型 | 一句话 |
|---|---|---|---|---|---|
| 0 | A1 | [−1, 1] | 0.5 | float | priceDeviation 信号的权重 |
| 1 | A2 | [−1, 1] | 0.3 | float | logReturn 信号的权重 |
| 2 | A3 | [−1, 1] | 0.2 | float | volRatio 信号的权重(三者 v1 **不归一**) |
| 3 | Beta | [0.5, 5.0] | 2.0 | float | sigmoid 陡度——信号→仓位有多激进 |
| 4 | Gamma | [0, 3.0] | 0.5 | float | 库存偏置——已持仓越多越压制继续买 |
| 5 | EMAShort | [5, 100] | 20 | **int** | 短趋势线周期 |
| 6 | EMALong | [50, 300] | 100 | **int** | 长趋势线周期(硬约束:必须 > 短) |
| 7 | MAVShort | [5, 50] | 10 | **int** | 成交量短均周期 |
| 8 | MAVLong | [30, 250] | 60 | **int** | 成交量长均周期(硬约束:必须 > 短) |
| 9 | QuietThreshold | [0.3, 1.2] | 0.7 | float | 低于它认为市场"安静",降频 |
| 10 | MicroReservePct | [0.05, 0.5] | 0.25 | float | 永远留多少现金不动 |
| 11 | MacroInjectUSD | [10, 1000] | 100 | float | 每月定额注资(模拟持续投入) |
| 12 | ReleaseDrawdownThreshold | [0.1, 0.5] | 0.3 | float | 回撤多深才释放"压箱底"仓位 |

两个**结构性特征**贯穿后面所有手算,先记住:

1. **5、6、7、8 是"整数维"**:基因物理上是 `float64`,但语义是 K 线根数,必须落到整数。
2. **有两条跨维硬约束**:`EMAShort < EMALong`、`MAVShort < MAVLong`(短周期得真比长周期短,否则两条线没有"快慢"之分,趋势判断失去意义)。

GA 的交叉/变异是在连续 `float64` 上自由搅动的——它**根本不知道**"第 6 维得是整数、还得大于第 5 维"。谁来保证搅完仍合法?这正是 `Clamp` 存在的理由,下一节第一个手算。

## 2.2 段(Segment):13 维不是平铺的

13 维捆成 **5 个段(Segment)**,语义相关的维绑在一起(`segmentInfos()`,§4.2 真源):

| # | 段名 | 维 | IsCritical | GeneStep(变异步长) | QuantizationStep(量化粒度) |
|---|---|---|---|---|---|
| 0 | signal_weights | 0,1,2 | ✅ | 0.2 / 0.2 / 0.2 | 0.05 |
| 1 | micro_dynamics | 3,4 | ✅ | 0.3 / 0.2 | 0.1 |
| 2 | feature_periods | 5,6,7,8 | ✅ | 5 / 10 / 5 / 10 | 1 |
| 3 | state_thresholds | 9,10 | — | 0.1 / 0.05 | 0.05 / 0.02 |
| 4 | macro_release | 11,12 | — | 50 / 0.05 | 10 / 0.02 |

段在三个算子里各管一件事,记住这张"段的三重身份":

- **交叉的最小不可拆单位**:`Crossover` 对每个段整体抛硬币(2.3 手算 ②)。意义:把"3 个信号权重"当一个配比整体遗传,不让交叉把 A1 给父代 1、A2 给父代 2 那样拆散——拆散会破坏权重之间的相对关系。
- **变异的步长来源**:每段每维的 `GeneStep` 不同(周期维 5/10,权重维 0.2),因为各维量纲差几个数量级,变异步长必须各自匹配(2.3 手算 ③)。
- **Fingerprint 的量化粒度来源**:每段每维的 `QuantizationStep`(2.3 手算 ④)。

`IsCritical` 标记这个段是不是"核心"(信号权重、微观动力学、特征周期是核心;状态阈值、宏观/释放不是)——它给诊断和报告分层用,不改 GA 机制本身,这里只需知道它存在。

## 2.3 四个遗传算子,在真 13 维上各走一遍数字

这是本章的核心。第 1 章你在 `toy` 2 维上见过这些算子的机械原理;这一节把它们放到真实 13 维 / 5 段上,**每个都带具体数字走一遍**。先准备两个"父代"基因,后面反复用:

```
P1 = [0.6,  0.2,  0.1,   2.5, 0.4,   15, 120,  8,  50,   0.6, 0.30,   200, 0.25]
P2 = [-0.3, 0.5,  0.4,   1.2, 1.5,   40,  80, 20, 200,   0.9, 0.10,   500, 0.40]
      └─signal_w─┘  └micro┘  └─feature_periods─┘  └thresh┘  └macro_rel┘
```

(两者都已是合法基因:周期是整数、short<long、各维在界内。)

### 手算 ①:`Clamp` —— 把一个非法基因修回合法

GA 搅出来的子代经常越界、short≥long、周期带小数。`Clamp` 是**每次 Sample / Crossover / Mutate 之后都要跑**的清洗闸门,三步(`sigmoid.go` 的 `Clamp`)。假设变异把 feature_periods 段(5–8 维)搅成了非法的:

```
搅出来:  EMAShort=103.7   EMALong=48.2   MAVShort=12.4   MAVLong=9.0
```

**Step 1 — 逐维裁到范围内**(`clampOne` = `ClipFloat64`):

| 维 | 原值 | 范围 | 裁后 |
|---|---|---|---|
| EMAShort | 103.7 | [5,100] | **100**(撞上界) |
| EMALong | 48.2 | [50,300] | **50**(撞下界) |
| MAVShort | 12.4 | [5,50] | 12.4(界内不动) |
| MAVLong | 9.0 | [30,250] | **30**(撞下界) |

**Step 2a — 整数维四舍五入**(`math.Round`):`100, 50, round(12.4)=12, 30`

**Step 2b — 强制 short < long**:

- EMA:`100 ≥ 50`?**是** → 把 long 顶到 `short+1 = 101`,再裁回 [50,300] → **101**
- MAV:`12 ≥ 30`?否 → 不动

```
最终(合法):  EMAShort=100   EMALong=101   MAVShort=12   MAVLong=30
```

**直觉**:GA 在连续空间自由探索(允许搅出非法值),`Clamp` 是"连续空间 → 合法离散空间"的**单向投影**。`Validate` 是它的检查版——同样两类约束(界 + short<long),但只报错不修;引擎可以放心假定 `Validate(Clamp(g)) == nil`。

> 边界细节值得拿真代码 + AI 追一下:Step 2b 把 long 顶成 `short+1` 后又裁了上界——若 short **本身已在上界**(EMAShort=100),long=101 仍合法;但想想 MAVShort=50(上界)的情形,`long=51` 也合法。注释提到极端下"可能 short==long,下一轮 Clamp 接住"。问 AI:"什么输入会让 Clamp 一遍出不来 short<long?"是个好练习。

### 手算 ②:`Crossover` —— 5 段抛硬币,拼一张"马赛克"

`Crossover` 对**每个段**独立抛一次硬币,整段继承某个父代(`sigmoid.go`:`src := p1; if rng.Float64() < 0.5 { src = p2 }`——即 `<0.5` 取 P2,`≥0.5` 取 P1)。设这一次 5 次抛硬币的 rng 值依次是:

| 段 | rng | <0.5? | 取自 | 继承的维 |
|---|---|---|---|---|
| signal_weights | 0.62 | 否 | **P1** | 0.6, 0.2, 0.1 |
| micro_dynamics | 0.31 | 是 | **P2** | 1.2, 1.5 |
| feature_periods | 0.88 | 否 | **P1** | 15, 120, 8, 50 |
| state_thresholds | 0.10 | 是 | **P2** | 0.9, 0.10 |
| macro_release | 0.55 | 否 | **P1** | 200, 0.25 |

拼出子代:

```
child = [0.6, 0.2, 0.1,   1.2, 1.5,   15, 120, 8, 50,   0.9, 0.10,   200, 0.25]
         └──P1 信号权重──┘ └P2 micro┘ └──P1 周期──┘    └P2 阈值┘   └P1 macro┘
```

然后 `Clamp`(本例全合法)→ `Validate` 通过(15<120、8<50)。

**直觉**:子代是父代的一张**整段马赛克**——它拿了 P1 的信号权重组合 + P2 的微观动力学 + P1 的周期组合 + P2 的阈值 + P1 的宏观。**关键在"整段"**:A1/A2/A3 这一组权重要么全来自 P1、要么全来自 P2,绝不会出现"A1 来自 P1、A2 来自 P2"的拆分。这保护了段内维之间的配比关系。如果 Clamp 后 `Validate` 仍失败(理论上 Clamp 应已挡住,留作安全网),就退化成克隆某个父代,引擎通过 diagnostics 观察到这个 `crossover_fallback` 事件,而**不是**通过返回 error。

### 手算 ③:`Mutate` —— 逐维 Bernoulli,各段不同步长

`Mutate` 遍历每段每维,以概率 `prob` 决定这一维动不动;命中则加一个高斯扰动 `delta = NormFloat64() * GeneStep[i] * scale`(`sigmoid.go` 的 `Mutate`)。拿手算 ② 的 child,设 `prob=0.3, scale=1.0`,设这一轮 13 次 Bernoulli 只有 3 维命中:

| 命中维 | 所在段 | GeneStep | NormFloat64 抽样 | delta = N·step·scale | 原值 → 新值 |
|---|---|---|---|---|---|
| A1 (0) | signal_weights | 0.2 | −0.8 | −0.8·0.2·1 = **−0.16** | 0.6 → 0.44 |
| EMALong (6) | feature_periods | 10 | +1.3 | +1.3·10·1 = **+13** | 120 → 133 |
| MacroInjectUSD (11) | macro_release | 50 | +2.1 | +2.1·50·1 = **+105** | 200 → 305 |

其余 10 维不动。然后 `Clamp`:A1=0.44∈[−1,1] ✓;EMALong=133∈[50,300],round 仍 133 ✓,且 EMAShort=15<133 ✓;Macro=305∈[10,1000] ✓。

```
mutate 后 = [0.44, 0.2, 0.1,   1.2, 1.5,   15, 133, 8, 50,   0.9, 0.10,   305, 0.25]
```

**三个直觉**(都从这组数字里直接读出来):

1. **变异是稀疏的局部微扰,不是重排**:13 维里只动了 3 维(prob=0.3),大部分基因被原样带下去。GA 是"小步试探",不是每代推倒重来。
2. **步长按段匹配量纲**:同样是 NormFloat64 量级 ~1,权重维只挪了 ±0.16,周期维挪了 ±13,宏观注资挪了 ±105——因为 `GeneStep` 把每维的"自然尺度"编进去了。若所有维共用一个步长,要么权重维抖得失控、要么周期维抖得几乎不动。
3. **`scale` 是全局油门**:它统一乘所有步长。第 5 章会讲引擎在 GA "卡住没进步"时**调大 scale**(变异 ramp),让整群跳出局部最优——`prob`(动几维)和 `scale`(每维动多大)是两个独立旋钮,接口注释特意强调策略必须分别尊重它们。

### 手算 ④:`Fingerprint` —— 量化后哈希,做种群去重

`Fingerprint` 把基因哈希成一个 16 位十六进制串,用于**种群去重**(几乎相同的基因别重复评估,白费一次四窗回测)和双 Fatal 时的 tie-break。机制是**先量化再哈希**:量化 = `round(value/step)*step`,把邻近连续值吸附到同一格点(`sigmoid.go`):

```go
q := math.Round(g[geneIdx]/step) * step   // step = 该维 QuantizationStep
binary.LittleEndian.PutUint64(buf[:], math.Float64bits(q))
h.Write(buf[:])                            // 喂进 FNV-1a-64
```

手算 signal_weights 段(QuantizationStep=0.05),比较两个**原始值不同**的基因:

| | A1 | A2 | A3 |
|---|---|---|---|
| 基因 A 原始 | 0.523 | 0.310 | 0.198 |
| A 量化 | round(0.523/0.05)·0.05=**0.50** | **0.30** | round(0.198/0.05)·0.05=**0.20** |
| 基因 B 原始 | 0.541 | 0.288 | 0.215 |
| B 量化 | **0.55** | **0.30** | **0.20** |

A2、A3 量化后同格;只要 A1 也同格(本例 0.50 vs 0.55 不同格,故这两个基因仍被区分),整串 Fingerprint 才相同、GA 才视为同一个体只评估一次。

**直觉**:`QuantizationStep` 是"多接近才算同一个解"的旋钮——太粗会把本质不同的解误并、丢多样性;太细则几乎不去重、白跑回测。量化之后用 FNV-1a-64 哈希 IEEE-754 小端字节,取低 16 位十六进制。注意它**逐段遍历**(顺序由 `Segments()` 固定),所以同一个基因永远得到同一个指纹——这是去重和 tie-break 能用的前提。

> 四个算子里,Crossover/Mutate 改基因、Clamp/Fingerprint 约束基因。它们全发生在"GA 的连续随机性"与"策略的离散合法性"接缝处,是本章最该亲手走数字的地方。把这四组数字看懂,你就掌握了"引擎搬基因"的全部机械动作;第 3 章开始才是"基因怎么变成交易"。

## 2.4 14 动词接口 `EvolvableStrategy`

这是**全项目的骨架**:引擎层只持有这个接口,永不 reach into 任何具体策略包(CLAUDE.md "两层硬边界"——引擎不许 import 策略内部)。定义在 `internal/strategy/evolvable.go`,每个动词的 doc comment 写了"引擎依赖它的什么性质",那是真源。下面按职责四组逐个过,每个给**职责 + 关键约束**;你已经在 2.3 / 第 1 章见过其中一半的真身。

**第一组 · 基因构造与约束**

- **`Sample(rng) Gene`** —— 从合法空间均匀抽一个基因,返回前**内部已调 Clamp**,所以调用方永远拿不到越界值。这是种群初始化(第 0 代)的零件。
- **`Clamp(gene) Gene`** —— 修非法基因(手算 ①):逐维裁界 + 整数维取整 + short<long 修复。幂等闸门,GA 每个改基因的步骤后都过它。
- **`Validate(gene) error`** —— 检查版的 Clamp:同样两类约束,只报错不修。引擎可依赖 `Validate(Clamp(g)) == nil`。
- **`Segments() []SegmentInfo`** —— 返回 5 段布局,**全生命周期同一个 slice、同一个顺序**。交叉单位、变异步长、量化粒度三件事全靠它(2.2 的"段的三重身份")。顺序若变,Fingerprint 和 SkippedBy 语义全乱。

**第二组 · 遗传算子**

- **`Crossover(p1, p2, rng) Gene`** —— 按段抛硬币的块正交交叉(手算 ②),内部已 Clamp+Validate,失败退化为克隆父代(经 diagnostics 上报 fallback,不返回 error)。
- **`Mutate(gene, prob, scale, rng) Gene`** —— 逐维 Bernoulli(prob),命中则 `NormFloat64()*GeneStep[i]*scale`,结果过 Clamp(手算 ③)。`prob` 与 `scale` 是两个独立旋钮,必须分别尊重。
- **`Fingerprint(gene) string`** —— 量化(按 QuantizationStep)后 FNV-1a-64 哈希取低 16 位十六进制(手算 ④)。同基因必得同指纹,用于去重和双 Fatal tie-break。

**第三组 · 评估**(细节是第 3、4 章正题,这里只立接口边界)

- **`Evaluate(ctx, gene, plan) (*RawEvaluateResult, error)`** —— 跑四窗熔断(6m→2y→5y→10y 固定序,Fatal 即级联短路)。**返回类型 `RawEvaluateResult` 物理上没有 `ScoreTotal` 字段**——见下方"三条不变量"①。
- **`ReviewBacktest(...) (*ReviewSummary, error)`** —— 复核重放的策略侧**薄壳**,策略可直接返回 `(nil, nil)`;真正的可复现重放由引擎层 `verification.RunReview` 驱动(策略若自己做就得 import verification,破坏边界)。引擎从不把复核结果喂回 GA 决策。
- **`MinEvalBars() int`** —— 这个策略要稳定评估至少需要多少根 K 线(最长 EMA 窗 + 统计稳定期)。引擎用它在 plan 给的 K 线不够时**提前 Fatal 一个窗**。sigmoid_v1 按最大周期上界 300 兜底。

**第四组 · 结果序列化与生命周期**

- **`EncodeResult(gene, spawn, repro, gaConfig, eval, verif, diag) (ChallengerResultPackage, error)`** —— 把引擎给的各层缝成五层结果包。**只有策略知道自己基因的 JSON schema**,所以基因编码这一步必须由它做;其余各层原样照搬引擎输入,并把 `decision_status` 初始化为 `pending`。
- **`DecodeElite(blob) (Gene, error)`** —— `EncodeResult` 的逆:把历史 Champion 的序列化基因还原成可运行的 `Gene`(拒绝非 JSON 编码)。
- **`StrategyID() string`** —— 稳定标识符,落在 `EvolutionTask` 和 `GeneRecord` 行上。**改它 = 迁移事件**(历史记录的归属会断)。
- **`NewAdapter(plan) (Adapter, error)`** —— 给一个 worker 造一个评估句柄,见 2.5。

**三条最该记住的接口级不变量**(它们解释了"为什么是这 14 个、为什么这么切"):

1. **`Evaluate` 返回的 `RawEvaluateResult` 类型上没有 `ScoreTotal` 字段。** 策略**物理上写不进**总分——总分是引擎调 `fitness.AggregateScoreTotal(rawResult)` 算出来、再缝进结果包的(第 4 章)。这是用 Go 的类型系统**硬挡**"策略给自己打总分"这种作弊/出错。
2. **`Sample/Clamp/Crossover/Mutate/Fingerprint` 必须是纯函数**——只依赖入参 + 传入的 rng,无全局可变状态、无墙上时钟。这是整个 GA 可复现(同 seed 同结果)的前提(第 6 章)。
3. **`Evaluate` 不许内部起 goroutine,所有浮点累加必须串行。** 并行只发生在"不同基因之间"(引擎的 worker pool),单个基因的评估内部严格串行——否则浮点求和顺序变了,确定性就破了(这也是 `TestEvaluateDeterministic`/`TestEvaluateOrderInvariance` 守的东西)。

> 自学路径:打开 `evolvable.go`,对照上面四组逐个读 doc comment,重点看每条 comment 末尾"引擎依赖它的什么"。把这些性质拿去问 AI"如果某个策略违反了会怎样、哪个测试会红",是理解这条硬边界为什么存在的最快方式。

## 2.5 Adapter 与 worker 隔离

第 5 章会讲引擎用 **worker pool 并行**评估一代里的几十个基因。这里先建立一个关键概念:**每个 worker 拥有自己的 Adapter,互不共享**。接口很小(`evolvable.go`):

```go
type Adapter interface {
    Reset(plan *domain.EvaluablePlan) error          // 每次 Evaluate 前调
    Evaluate(gene domain.Gene) (*RawEvaluateResult, error)
    Close() error                                    // Epoch 拆除时调一次
}
```

引擎对每个基因的调用节奏是**固定的两拍**:`Reset(plan)` → `Evaluate(gene)`。`Reset` 的职责是把**上一个基因留下的所有"基因派生状态"清干净**——持仓 Portfolio、指标缓存、连亏计数器、交易历史……否则上一个基因的尾巴会污染下一个,评估结果就和"基因被评估的顺序"挂钩了,直接破坏 `TestAdapterResetIsolation` 和 `TestEvaluateOrderInvariance`。

`sigmoid_v1` 的 Adapter 现在**刻意不缓存任何基因派生状态**——每次 `evaluateWindow` 都从冷启动的 Portfolio + 空 RuntimeState 重新开始,所以它的 `Reset` 退化成一行(`sigmoid.go`):

```go
type sigmoidAdapter struct {
    strat *Sigmoid                 // 只读,指向父策略
    plan  *domain.EvaluablePlan    // 引擎每次 Reset 刷新
}

func (a *sigmoidAdapter) Reset(plan *domain.EvaluablePlan) error {
    a.plan = plan   // v1 无基因派生缓存可清,只换 plan 指针
    return nil
}
```

**这不是偷懒,是契约的一个合法实现**——但代码注释明确警告:**一旦将来给 Adapter 加了跨 `Evaluate` 的缓存**(比如复用指标 buffer 提速,正是第 5 章 / GA-CPU 优化在做的事),**就必须扩 `Reset` 把它们清掉**,否则隔离契约破裂。"Reset 现在是一行"和"Reset 必须完整"两件事不矛盾:契约要求清空所有基因派生状态,而 v1 恰好一个都没有。

**允许保留的、与当前基因无关的东西**:K 线 buffer、DCA 基线缓存、首次写入前清零的 scratch buffer。区分"基因派生 vs 基因无关"是读任何 `Reset` 实现时的核心问题。`TestAdapterResetIsolation` 用经验手段兜底:同一组基因换两种顺序各跑一遍,结果必须逐位一致——只要 Reset 漏清了什么,这个测试就红。

`Evaluate` 内部就是**四窗熔断**(`AllWindowsInEvalOrder()` 给出 6m→2y→5y→10y,某窗 Fatal 后续窗记 `SkippedBy`、`Value=nil`),那是第 3、4 章的正题。想先看管线骨架,读 `sigmoid.go` 的 `sigmoidAdapter.Evaluate`:它把"按名找窗 → 级联跳过 / 调 `evaluateWindow` / 标 Fatal 触发级联"三件事串起来,并在最后只保留**最长非 Fatal 窗**的 Sharpe 统计(§I-4.2:取最长可用 horizon)。

## 小结 / 下一章

- 基因 = 13 维真实参数,分 5 段;引擎只见不透明 `[]float64`,含义活在策略包。
- 段有"三重身份":交叉的不可拆单位、变异步长来源、量化粒度来源。
- 四个算子在真 13 维上的数字:`Clamp` 三步修非法、`Crossover` 5 段抛硬币拼马赛克、`Mutate` 稀疏微扰且步长按段匹配量纲、`Fingerprint` 量化吸附后哈希去重。
- 14 动词接口是两层硬边界;三条不变量(`RawEvaluateResult` 无 `ScoreTotal` 字段 / 纯函数 / 串行评估)解释了它为什么这么设计。
- 每 worker 一个 Adapter;`Reset` 必须清空基因派生状态——v1 恰好没有,所以是一行,但契约不变。

下一章进入**这些参数到底在做什么交易**:三个输入信号(priceDeviation / logReturn / volRatio)的金融含义、`signal = A1·… + A2·… + A3·…` 怎么合成、sigmoid 怎么把信号变成目标仓位——`sigmoid_v1` 最贵也最易错的部分。
```
