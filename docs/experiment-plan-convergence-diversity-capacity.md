# 实验规划:收敛判定 × 种群多样性 × 容量扩展(2026-06-12)

> 状态:**规划稿,未开工**。来源:optuna_ga_meta 首轮结论(README §7:
> "真约束是 EarlyStop 收敛检测而非 (pop, ratio)")+ MRA 终局判决
> (mra_ab README §12:容量扩展须配更强抗过拟合)+ 用户 2026-06-12 定向。
> 挂靠:LEARN-ga-rl-bayesian §12.4 排序(本计划 = item ③ 的后续 + §12.3-③
> 的落地 + item ④/⑤ 框架的消费)。判决阈值标 [INVENTED v1] 待校准,
> 跑前冻结、事后不改(mra_ab 实践证明这条纪律值钱)。

三条工作流,**执行顺序 W2 → W1 → W3**(依赖与代价递增;W2 的产出是
W1 的机制解释、W3 的风险标尺)。

---

## W2(先做):量化"种群多快变得千人一面"——多样性塌缩测量

**问题**:EarlyStop 提前收工的机制猜想是"种群趋同→无进展"。趋同有多快?
塌缩速度与最终分数什么关系?这决定 Niching/fitness-sharing 值不值得做
(§12.3-③ 预设的观察闸:"先观察,不直接开工")。

**为什么现在做最便宜**:**零 Go 改动,数据已经躺在那里**——
`quantlab_meta`(:5433)里 ga_meta 首轮 50 个 task 的 `evaluation_traces`
(已验字段:TaskID / Generation / IndividualIdx / GeneJSON / ScoreTotal /
Fatal / Fingerprint),50 种 (pop, ratio) 配置 × 逐代全种群基因,正好是
一张现成的"塌缩速度 vs 配置 vs 最终分"观测网。

**设计**:`research/diversity_collapse/`(只读 :5433,纯分析脚本)。
每 (task, generation) 计算:
- **指纹多样性** `u_g` = unique(Fingerprint)/pop(量化去重后的表型多样性);
- **基因空间散布** `d_g` = 各维按 bounds 归一后的平均成对欧氏距离
  (13 维 chromosome,镜像 §4.1 bounds);分 segment 也算(block crossover
  的语义单元,看哪个 segment 先塌);
- **塌缩时刻** T½ = u_g 跌破 0.5·u_0 的代数;
- 与该 task 最终 best ScoreTotal、实耗 evals 的相关。

**预注册判决 [INVENTED v1]**:
| 观测 | 行动 |
|---|---|
| T½ 普遍 < 5 代,且 T½ 与最终分正相关(Spearman ρ > 0.4) | 塌缩是真瓶颈 → Niching/fitness-sharing 立项进引擎 backlog |
| 塌缩慢或与分数无关 | 多样性不是瓶颈 → Niching 降级,W1 的收工规则成为唯一杠杆 |

量级:半天(脚本 + 读数)。

## W1(第二):收工规则(EarlyStop)扫描

**问题**:`EarlyStopPatience=5 / MinDelta=0.001`(engine DefaultConfig)
是不是过早判定"无进展"?多给耐心能否突破 0.83 平台,还是平台就是该预算
下的真实上限?

**前置(本系列第一个 Go 改动,小且 additive)**:`early_stop_patience` /
`early_stop_min_delta` 目前**不在** `CreateEvolutionTaskRequest` 暴露面
(已核对 types.go)。加两个可选指针字段(omitempty,nil→默认,模式同
`warmup_days`),epoch service 透传进 EngineConfig。不改评分语义,
**无 fitness_version 事件**;schema additive。

**设计**:复用 optuna_ga_meta harness(改动只是 body 多两个字段)。
- 固定 pop=300 / ratio=0.05(生产默认,首轮已证平台区);
- 预算抬到 **B=7140**(= 生产默认 300+24×285,补首轮 Caveat 的预算盲区);
- 扫 patience ∈ {3, 5, 10, 20, 40} × min_delta ∈ {1e-4, 1e-3, 1e-2}
  [INVENTED v1],每格 ≥3 重复(首轮噪声 Δ≈0.008~0.026,无重复读不出
  0.0x 级效应);15 格 ×3 ≈ 45 task,每 task ~3-4 min(B=7140)≈ 3h;
- **读数是 Pareto 面不是单分**:best score × 实耗 evals(耐心翻倍若只买
  +0.002 分 ×2 倍算力,结论是"现役规则已最优")。

**预注册判决 [INVENTED v1]**:patience 20 vs 5 的分差 > 2×噪声 σ(重复
trial 实测)→ 收工规则确实过严,改默认值(纯 config,无版本事件);
否则关闭此线,0.83 平台即该预算真实上限。

依赖:W2 先行(若 W2 显示 5 代内已塌缩,patience 再大也只是在重复评估
同质种群——W1 的扫描设计要据此加"塌缩后停"的解读栏)。

## W3(设计先行,执行 gated):容量扩展 × 强抗过拟合

**问题**:给染色体加维度(表达力)在 MRA 实验里被证明"容量被 IS 过拟合
吸收"(mra_ab §12/§13:三臂选拔均落噪声区间)。**结论不是"永不加维",
而是"加维必须配更强的过拟合防线 + 选对频率"。** 本工作流把"防线"产品化,
再谈加维。

**两阶段:**

**W3a — 防线产品化(现在可做,research/ 零 Go 改动)**:
把 mra_ab 实践沉淀成任何新维度实验的**标准判决协议**(写成可复用模板):
1. 预注册判决表,跑前冻结(含 PBO 行:**CSCV PBO < 25% 且 degradation
   slope > 0 才有立项资格** [INVENTED v1,mra §13.3 候选阈值正式化])。
   阈值理由(锚点全部有实测):PBO 刻度上抛硬币=50%(cscv.py 自检纯噪声
   0.58)、真优势→0(自检 0.000)、MRA 已知挑噪声=39~47%;**25% = 至少
   3/4 的换卷路径下冠军守住 OOS 中位以上**,既容忍"弱但真"的信号(不会
   压到 0),又与噪声带拉开清晰距离。slope 是全体候选 OOS~IS Sharpe 的
   回归斜率(1=优势全迁移,0=IS 无信息,<0=选拔奖励噪声;MRA 实测全负),
   **>0 是最低方向性要求**。AND 的原因:PBO 审计"被选中的那一个"的名次
   鲁棒性,slope 审计"整套选拔机制"的信号质量——一个抓状元是幸运儿,
   一个抓机制系统性反向,缺一漏一类失败。资格 ≠ 立项:过线才进判决表
   正式比试(alpha/DSR 行)。
2. 嵌套对照臂(C-arm 模式,容量差与表示差分解;修 mra 的教训:对照臂
   与基线臂的非测试项必须严格同构,否则 C−A≠0 污染分解);
3. anchored 折 + purge/embargo + 谱形跨折交叉验证(两法谱形矛盾=警报);
4. DSR 用诚实 N(全部 trial 计入);
5. 换手/摩擦维进搜索空间(净摩擦 alpha 为准)。

**W3b — 第一个过防线的加维候选(执行 gated)**:
- 候选方向按 mra §12.3 教训选**降频不增密**:日线级 filter bank
  (1h 失败主因=摩擦吃光弱信号;h=72 信号比 h=24 更顺更稳是已有证据;
  §13 铁律"基因不跨粒度"→ 新 strategy 新 interval,engine 零改动);
  次选:MAV/波动率通道 bank 化(mra README §8-4 留的 v2,同手法隔离变量);
- **完整候选池(2026-06-12 公开文献/社区调研)**:
  `docs/learn/LEARN-strategy-directions-survey.md`——日/周线 TSM+vol
  targeting(首选,与上行重合)> funding carry > BTC-ETH 配对 >
  链上 regime 维 > 日内季节性;做市 A-S/Grid 不立项(回测诚实性墙);
- **gate**:①W3a 协议就绪;②mainnet 实盘偏差数据(校准模拟器摩擦假设,
  mra row-4 预注册的复议扳机)。两个 gate 都开才动工;
- 量级:设计 1 天 + 跑数天(mra_ab 同量级)。

## 排序与依赖总览

| 序 | 工作流 | Go 改动 | 量级 | gate |
|---|---|---|---|---|
| 1 | W2 多样性塌缩测量 | 无 | ~半天 | 无(数据已就位) |
| 2 | W1 EarlyStop 扫描 | 小(2 个 additive 请求字段) | ~1 天(含 3h 跑) | W2 读数先出 |
| 3 | W3a 抗过拟合协议模板 | 无 | ~1 天 | 无,可与 W1 并行 |
| 4 | W3b 降频加维实验 | 无(新 strategy 另算) | 数天 | W3a + mainnet 摩擦数据 |

与 §12.4 旧表的关系:item ③ 完结后的自然延伸;item ④ 工具(cscv.py)被
W3a 消费;item ⑤(GT-Score 离线证据)并入 W3a 协议考虑。
基础设施沿用:meta PG(:5433)+ meta saas(:8090)+ optuna_ga_meta harness。
