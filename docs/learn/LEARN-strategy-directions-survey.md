# LEARN — 策略方向调研:未来可进 QuantLab 小额实盘的候选线(2026-06)

> 类型:研究评估 + 跟进文档(非规范规格)。
> 来源:2026-06-12 公开文献/社区/开源生态检索,回答"还有哪些不同的策略
> 方向可引入 QuantLab 做实盘小额交易实验"。
> 关系:本文是 `docs/experiment-plan-convergence-diversity-capacity.md`
> **W3b 的候选池**;任何一条立项都必须先过 W3a 预注册判决协议
> (PBO < 25% 资格线 + 净摩擦 alpha)。
> 状态:调研完成;未立项任何一条。

## 0. 筛选标准(四个筛子)

1. **能塞进 `EvolvableStrategy`/GA 基因框架**:信号可参数化、回测确定性;
2. **摩擦面前活得下来**:MRA 实验已证 1h 弱信号被 15bps 单边摩擦吃光
   (mra_ab README §10/§12);
3. **基建现状**:Agent 只有 Binance **现货**客户端,引擎单 pair,
   回测 close-only bar 级;
4. **必须过 W3a 抗过拟合协议**(预注册判决表,跑前冻结)。

## 1. 推荐方向(按优先级)

### 1.1 低频时序动量 + 波动率目标(TSM,日/周线)— 最优先

**证据**:BTC 收益存在 1~8 周持续性、之后部分反转(欠反应→延迟过度反应,
[Springer TSM for Cryptocurrencies](https://link.springer.com/chapter/10.1007/978-981-99-6441-3_17));
日频短回看动量**扣 0.1% 成本后仍显著**,而高频技术规则 OOS Sharpe
0.66→0.06、扣费即死([Momentum & trend following for currencies and
bitcoin](https://assets.super.so/e46b77e7-ee08-445e-b43f-4ffd88ae0a0e/files/9c27aa78-9b14-4419-a53d-bc56fa9d43b2.pdf))
——与 MRA "降频不增密" 教训互为印证。叠加 vol targeting(仓位 ∝ 1/σ̂,
target ~20% 年化)是机构标准件([Grayscale](https://research.grayscale.com/reports/the-trend-is-your-friend-managing-bitcoins-volatility-with-momentum-signals))。

**适配**:零新基建(klines 已有;日线 = 新 strategy 新 interval,
"基因不跨粒度"铁律自然满足);基因维天然(lookback / 入场阈值 /
vol target / 止损)。**= W3b 首选候选的具体化。**

### 1.2 资金费率 carry(现货多 + 永续空,delta 中性)— 新收益源,带冷水

**机制**:永续每 8h 结算 funding,牛市常态 0.01%~0.05%/8h ≈ 10~30% APR,
$5k 起步可行([guide](https://coincryptorank.com/blog/funding-rate-arbitrage))。
收益源与方向性策略**正交**,组合层面真分散。
**冷水**:2025 年综述实测 crypto carry Sharpe 2020-2025=6.45 →
2024 起 4.06 → **2025 转负**([arXiv 2510.14435](https://arxiv.org/pdf/2510.14435))
——拥挤交易在消失。

**适配**:中等代价——Agent 需加永续客户端(保证金/强平 = 新危险面),
需采 funding rate 历史;基因维少(阈值/仓位上限),GA 用武之地有限。
定位:第二实例的稳健腿,非研究主线。

### 1.3 BTC-ETH 配对交易(协整统计套利)— 中期,顺手逼出多资产

**证据**:BTC-ETH 协整对文献 Sharpe 至 2.45,严格 OOS(2022-2024)
1.4~1.5,动态选对后掉至 0.8~1.0([IJSRA](https://ijsra.net/sites/default/files/fulltext_pdf/IJSRA-2026-0283.pdf)、
[Erasmus thesis](https://thesis.eur.nl/pub/67552/Thesis-Pairs-trading-.pdf))
——论文漂亮、落地折半的典型。

**适配**:信号结构完美契合 GA(z-score 入/出阈值、对冲比窗口、半衰期);
**但引擎单 pair**——schema Appendix B 的 deferred `pair` 类型会被这条线
逼出来。双腿低换手,摩擦画像好于 1h 单边。

### 1.4 链上数据作 regime 维度 — 当特征,不当策略

**证据**:MVRV Z-Score 历史定位周期顶至两周内;SOPR>1/<1 度量获利/亏损
抛售;交易所大额净流入偏空([Gate Learn 综述](https://www.gate.com/learn/articles/overview-of-popular-btc-on-chain-indicators/5888)、
[checkonchain 免费图表](https://charts.checkonchain.com/));机构用法 =
多信号合成做 macro 仓位,非独立策略([Neutralis](https://neutralis.finance/zh/insights/on-chain-metrics-hedge-funds))。

**适配**:落点 = 给现有策略加 1~2 个 regime 基因维(模式同 `market_state`
quiet/active)——**W3a 协议的标准客户**。代价:新数据依赖(免费层粒度粗,
长历史回测要花钱或自建);周期级信号样本天然少 → DSR/PBO 会很严格
(这是 feature 不是 bug)。

### 1.5 日内季节性(时区效应)— 便宜的试刀案例

**证据**:"周一亚洲开盘效应" + 21:00-23:00 UTC 收益集中;vol-targeted
日内趋势基准 2018-2025 **毛** Sharpe ~1.6([Concretum](https://concretumgroup.com/seasonality-in-bitcoin-intraday-trend-trading/)、
[QuantPedia](https://quantpedia.com/the-seasonality-of-bitcoin/))。

**适配**:现有 1h klines 当天可离线验证,基因 = 时段 mask;但全是
gross-of-fees 口径,日历效应 = 过拟合重灾区——**只配作 W3a 协议的
第二个试刀案例**,不单独立项。

## 2. 不推荐(近期):做市(Avellaneda-Stoikov)与 Grid

### 2.1 A-S 模型在解什么

做市 = 同时挂双边赚价差,两个天敌:**库存风险**(被动接货后行情反向)
与**逆向选择**(知情流恰在最坏时刻打你的单——成交本身携带坏消息)。
[Avellaneda & Stoikov 2008](https://arxiv.org/pdf/1105.3115) 给出闭式解:

- **保留价**(报价中心被库存推歪,用报价位置代替止损):
  `r = s − q·γ·σ²·(T−t)`(q=库存,γ=风险厌恶)——多头压仓 ⇒ 整套报价
  下移,库存自动均值回归;
- **最优总价差**:`δ_a+δ_b = γσ²(T−t) + (2/γ)·ln(1+γ/κ)`,
  κ 来自成交强度 `λ(δ)=A·e^(−κδ)`——波动大/更怕风险 ⇒ 价差拉宽。

两个旋钮(γ, κ)的闭式解使它成为开源标准件
([Hummingbot 官方实现](https://hummingbot.org/blog/guide-to-the-avellaneda--stoikov-strategy/))。

### 2.2 三道墙(为什么 QuantLab 近期不立项)

1. **回测诚实性(最硬)**:被动挂单的成交取决于订单簿队列位置;QuantLab
   引擎是 close-only bar 级(`simulator.go` 收盘价±slippage 成交),
   从 OHLCV **物理上推不出**"我的限价单成交了吗"。四窗 crucible /
   `bars_hash` / replay 闸门全部站在 bar 级确定性回测上——做市策略会让
   整套审计体系失效。
2. **散户经济学**:利润 = 价差捕获 − 逆向选择 − 手续费。BTCUSDT 现货
   盘口 ~1bp,散户 maker 费 ~10bp 无返佣 ⇒ **费用 ≈ 毛利 ×10**,结构性
   亏损。专业 MM 靠 VIP 返佣/延迟/队列头部活着,小资金一项没有。
   (Hummingbot 社区年成交 $34B 是**量**不是利润,大头在有返佣激励的
   场所与 DEX 做市挖矿。)
3. **运维形态**:秒级订单簿 WS + 持续撤改单 vs Agent 的 1m bar tick
   循环——改造量 ≈ 再写一个系统。

**重启条件**(所以是"近期不推荐"非"永不"):maker 返佣层级 + 逐笔/订单簿
数据采集 + 事件驱动回测引擎——那是新产品线,不是现框架里的新策略。

### 2.3 Grid 的位置

网格 = **不动库存、不看波动率的退化做市**(A-S 取 γ→0、忽略 σ 的特例,
固定间距挂单)——趋势市被库存碾过的死法一模一样,只是更朴素。社区流行
([DCA vs Grid](https://wundertrading.com/journal/en/learn/article/dca-bot-vs-grid-bot))
但信息量低;QuantLab 已有 DCA 基线,这类策略当**对照臂**有价值。

## 3. 排序总表

| 序 | 方向 | 新基建 | GA 契合 | 主要风险 | gate |
|---|---|---|---|---|---|
| 1 | 日/周线 TSM + vol targeting | 无 | 高 | 动量拥挤、单一收益源 | W3a |
| 2 | funding carry(delta 中性) | 永续客户端+funding 数据 | 低 | 2025 carry 压缩、强平 | W3a + 基建 |
| 3 | BTC-ETH 配对 | 多 pair 支持(Appendix B deferred) | 高 | 动态选对衰减 | W3a + 基建 |
| 4 | 链上 regime 维 | 链上数据源 | 中(加维) | 数据费用、样本少 | **W3a 严格适用** |
| 5 | 日内季节性 mask | 无 | 高 | 日历效应=过拟合重灾区 | W3a(试刀) |
| — | 做市 A-S / Grid | 事件驱动回测+订单簿 | 不适配 | 回测不诚实、费率结构 | 不立项 |

## 4. 参考来源

- [Cryptocurrency as an Investable Asset Class (arXiv 2025)](https://arxiv.org/pdf/2510.14435)
- [Cryptocurrency Trading: A Comprehensive Survey (Financial Innovation)](https://link.springer.com/article/10.1186/s40854-021-00321-6)
- [Time Series Momentum Trading Strategy for Cryptocurrencies (Springer)](https://link.springer.com/chapter/10.1007/978-981-99-6441-3_17)
- [Momentum and trend following for currencies and bitcoin](https://assets.super.so/e46b77e7-ee08-445e-b43f-4ffd88ae0a0e/files/9c27aa78-9b14-4419-a53d-bc56fa9d43b2.pdf)
- [Grayscale: The Trend is Your Friend](https://research.grayscale.com/reports/the-trend-is-your-friend-managing-bitcoins-volatility-with-momentum-signals)
- [Funding Rate Arbitrage Complete Guide](https://coincryptorank.com/blog/funding-rate-arbitrage)
- [Statistical Arbitrage Using Cointegration (IJSRA)](https://ijsra.net/sites/default/files/fulltext_pdf/IJSRA-2026-0283.pdf)
- [Pairs Trading in the Cryptocurrency Market (Erasmus thesis)](https://thesis.eur.nl/pub/67552/Thesis-Pairs-trading-.pdf)
- [Gate Learn: BTC 链上指标综述](https://www.gate.com/learn/articles/overview-of-popular-btc-on-chain-indicators/5888)
- [checkonchain](https://charts.checkonchain.com/) / [Neutralis: 链上指标与对冲基金](https://neutralis.finance/zh/insights/on-chain-metrics-hedge-funds)
- [Concretum: Seasonality in Bitcoin Intraday Trend Trading](https://concretumgroup.com/seasonality-in-bitcoin-intraday-trend-trading/)
- [QuantPedia: The Seasonality of Bitcoin](https://quantpedia.com/the-seasonality-of-bitcoin/)
- [Avellaneda & Stoikov 2008 / Guéant-Lehalle-Tapia](https://arxiv.org/pdf/1105.3115)
- [Hummingbot: A&S 策略指南](https://hummingbot.org/blog/guide-to-the-avellaneda--stoikov-strategy/)
- [WunderTrading: DCA vs Grid](https://wundertrading.com/journal/en/learn/article/dca-bot-vs-grid-bot)
