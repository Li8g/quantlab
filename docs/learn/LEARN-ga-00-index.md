# LEARN GA — 看懂本项目 GA 代码(系列总目录)

这是一套**教程式**文档(不是参考手册),目标:一个**理工科本科生**读完后,能看懂本项目用遗传算法(GA)优化量化交易策略参数的代码。

> 它和 `LEARN-ga-source-map.md` 分工不同:那份是"代码在哪、按什么顺序读"的**导航图**(假设你已懂 GA);这套是"从零讲懂 GA 和这个策略**为什么这么写**"的**讲解**。

## 给谁看 / 前置假设

**假设你已经会**(不再讲):微积分、exp/log、sigmoid(logistic)函数、基础概率与统计、线性代数基础;Go 基础(读得懂 struct / interface / slice);**基础现货交易常识**(知道现货、价格、K 线/candle、持仓、USDT 交易对是什么)。

**这套文档教的是你大概率不会的三类东西**:
1. **GA 的机械原理**(gene / 种群 / 选择 / 交叉 / 变异 / 收敛)——从零讲。
2. **指标和指标的金融含义**(EMA 当趋势线、MDD、Sharpe、DCA 基线、NAV……)——你也许会算,但未必知道在交易里**是什么意思、为什么用**。
3. **本项目的设计与代码**(两层边界、14 动词接口、四窗熔断、ScoreTotal、基因布局、可复现性)。

**不教**:本科已覆盖的纯数学基础。看到 `sigmoid(x)=1/(1+e^{-x})` 这类,直接用,不展开。

## 全景(一屏看懂)

本项目是一套**服务端进化系统**:用 GA 自动搜索一个量化交易策略的最优参数。一句话数据流——

```
随机一批"参数候选"(基因)
   → 每个候选放进历史行情里回测,跨 4 个时间窗(6m/2y/5y/10y)打一个分(fitness)
   → 偏向保留高分的,让它们"杂交+变异"出下一代
   → 重复几十代,直到收敛 → 得到一个"挑战者"(challenger)
   → 人工复核后可"晋升"(promote)为正式 champion 上线交易
```

GA 引擎(`internal/engine`)只管"进化的循环";策略本身(`internal/strategies/sigmoid_v1`)定义"基因是什么、怎么交易、怎么打分"。两者用一个 14 动词的接口(`EvolvableStrategy`)隔开——这条**两层边界**是全项目的骨架。

## 详细目录

> 状态:**第 1 章样章 + 第 2 章初稿已成稿**(第 2 章待用户校准手算密度/要点颗粒度后定调);第 3–6 章为下方详纲。
> 定调(2026-06-11 敲定):**脚手架+知识点地图**风格——关键机制亲手带数字走一遍(worked example),其余列要点+知识点+代码/文档位置,让读者配合 AI 自学;代码编排按章混用(短代码内联、长管线给指针)。

### 第 1 章 — 遗传算法是什么(用 toy 策略入门)  ✅ 样章
- 1.1 为什么用 GA 而不是梯度下降/网格搜索(对会微积分的人讲:目标函数是黑箱、不可导、中维度)
- 1.2 词汇表:生物隐喻 → 优化术语 → 本项目代码(逐个对位)
- 1.3 用 `toy` 策略坐实每个概念(2 维基因、已知最优解、显式 fitness)
- 1.4 "一代"是怎么走的(初始化 → 评估 → 选择+繁殖 → 收敛检查)
- 1.5 手动走一代(带真实数字的 worked example)
- 1.6 为什么这样能行(直觉)
- 代码:`internal/strategies/toy/toy.go`、`internal/engine/engine.go`、`internal/domain/types.go`

### 第 2 章 — 本项目把 GA 套在什么上  ✅ 初稿(待校准)
- 2.1 基因 = 策略参数:`sigmoid_v1` 的 13 维 chromosome 逐维讲(A1/A2/A3、EMA/MAV 周期、Beta/Gamma……)
- 2.2 段(Segment)与量化:`Segments()`、为什么交叉/指纹按段做
- 2.3 14 动词接口 `EvolvableStrategy`:引擎只通过它和策略对话,逐个动词的职责
- 2.4 Adapter 与 worker 隔离(为什么每个 worker 一个 Adapter、Reset 的意义)
- 代码:`internal/strategy/evolvable.go`、`internal/strategies/sigmoid_v1/chromosome.go`、`internal/strategies/sigmoid_v1/sigmoid.go`

### 第 3 章 — 策略在做什么交易(sigmoid_v1)
- 3.1 三个输入信号的**金融含义**:priceDeviation(偏离 EMA 趋势)、logReturn(动量)、volRatio(波动放大/收缩)
- 3.2 signal 合成:`signal = A1·priceDeviation + A2·logReturn + A3·(volRatio−1)`
- 3.3 sigmoid 微观再平衡:把 signal 变成"目标仓位权重",再换成下单量
- 3.4 macro 注资 / release 释放 / warmup 预热各是什么、为什么需要
- 代码:`signal.go`、`market_state.go`、`step.go`、`macro.go`、`release.go`

### 第 4 章 — 怎么给一个基因打分(fitness)
- 4.1 回测 → NAV 曲线 → MDD(最大回撤)/ Sharpe 的**意义**
- 4.2 四窗熔断(Crucible):6m/2y/5y/10y、权重、Fatal MDD 级联短路
- 4.3 DCA 双基线:为什么要和"定投"比、怎么比
- 4.4 ScoreTotal 聚合 + 一致性惩罚:把四个窗的分合成一个,且惩罚"忽好忽坏"
- 代码:`internal/fitness/aggregate.go`、`internal/fitness/ghost_dca.go`、`sigmoid_v1/evaluate_window.go`

### 第 5 章 — 引擎怎么把这些跑起来
- 5.1 `RunEpoch` 主循环:一代的生命周期
- 5.2 评估一代:worker pool 为什么能并行、跨代为什么不能
- 5.3 产下一代:精英保送 + 锦标赛选择 + 交叉 + 变异
- 5.4 收敛:变异 ramp、early-stop、为什么需要"没进步就加大变异"
- 代码:`internal/engine/engine.go`、`internal/engine/convergence.go`

### 第 6 章(可选) — 可复现性与版本
- 6.1 为什么 GA 要可复现(determinism、seed)
- 6.2 fitness_version、bars_hash、tolerance gate(引 `docs/decision-ga-reproducibility-constraint.md`)
- 代码:`internal/quant/canonical_json.go`、`internal/resultpkg/versions.go`

### 附录
- A. 术语表(领域向:EMA/MAV/MDD/Sharpe/DCA/NAV/challenger/champion……)
- B. 怎么自己跑一次、看 GA 收敛
- C. 和规范文档的关系(`docs/进化计算引擎.md`、`docs/策略数学引擎.md`、`docs/strategies/sigmoid_v1.md` 是**真源**;本系列是它们的科普化重述,有冲突以规范为准)

## 怎么读

- **顺序读** 1→5(6 可选)。每章建立的词汇下一章会用。
- **赶时间**:先读 1、2、5(纯 GA 机制,不依赖策略细节),就能看懂引擎层 GA 循环;3、4(策略+打分)第二批。
- 想直接跳进代码:配合 `LEARN-ga-source-map.md` 的阅读顺序。
