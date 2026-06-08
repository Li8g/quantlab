# GA / RL / Bayesian 预研报告

状态：预研工作备忘录
日期：2026-06-08（初稿）；2026-06-08（本次会话补充）
作者：Li8g + Claude

本文整理了关于 QuantLab 三类优化方法的研究讨论：遗传算法（GA）、强化学习（RL）、贝叶斯优化（BO）。
不替代正式的引擎规格文档，作为未来路线图决策的工作备忘录使用。

---

## 1. 项目背景

QuantLab 当前的产品循环：

```
历史数据 → GA 进化 → 健壮候选筛选 → 小资金实盘实验 → Promote / Retire / Freeze
```

系统设计上是单交易员平台：

- 一名人类交易员
- 同时只有一个交易所账户处于活跃控制中
- 小资金生产实验
- 确定性 GA / 回放证据
- fail-closed 实盘订单行为
- 强审计能力

这对方法选择很重要。核心问题不是"哪种算法更先进"，而是"哪种算法能改善这个循环，同时不让实盘交易边界更难审计"。

---

## 2. 高层结论

对 QuantLab v1，GA 应继续作为主要策略候选生成器。

RL 和贝叶斯方法有价值，但定位是辅助角色：

- **GA**：搜索可解释的候选基因 / 策略参数
- **贝叶斯方法**：改善昂贵搜索效率、不确定性报告、Promote 证据
- **RL 思路**：改善约束决策控制（仓位大小、执行、冠军分配、观察期、冻结、退役）

不要把 QuantLab v1 改造成端到端 RL 交易器。那会在实盘订单正确性边界还没完全关闭之前，用一个更难调试的 environment/reward 问题替换掉相对可审计的搜索流水线。

---

## 3. GA vs RL

### 3.1 为什么 GA 仍然适合 QuantLab

QuantLab 的策略优化问题是黑箱搜索：

```
score = backtest(strategy_parameters, historical_bars, costs, windows)
```

目标函数不光滑、不可微。小的参数变化可能创建或删除交易，进而不连续地移动回撤和 Sharpe。GA 是务实的匹配，因为它只需要候选基因之间的 fitness 比较。

GA 还与 QuantLab 的工程边界匹配：

- 基因可序列化、可回放
- 确定性种子和数据哈希可以记录
- OOS、review、stress、DSR/PBO 类诊断可以保留在 GA 热循环之外
- 人工 Promote 可以检查具体的候选包

GA 主要风险不是训练难度，而是**过拟合**：GA 可能发现历史噪声、模拟器 bug、费率/滑点盲区，以及不能在 OOS 或实盘中存活的参数孤岛。

### 3.2 GA vs RL 的本质区别

| 任务类型 | 适合方法 |
|---|---|
| 参数优化：给定策略框架，找最优参数组合 | GA、贝叶斯优化、网格搜索 |
| 策略发现：端到端学出"何时买/卖/持"的决策函数 | Deep RL（PPO、SAC、TD3）|

QuantLab 用 GA 做的是**参数优化**（基因 = 参数向量），不是让 GA 直接输出交易决策。RL 通常被用于后者。两者并非完全对等竞争。

### 3.3 RL 的真实劣势

**1. 样本效率极低**
这是 RL 应用于金融的最大硬伤。市场状态空间大、信噪比低，model-free RL 的采样效率问题在金融场景格外严重。金融数据量（每日/每小时 K 线）相比 Atari/围棋训练所需的交互次数差了数量级。

**2. Policy 不稳定**
RL 的 reward 信号在金融中极度嘈杂（价格随机游走），训练过程容易震荡甚至发散。

**3. Overfitting 更隐蔽**
GA 的过拟合通常能通过 OOS 测试暴露出来。RL 的过拟合更隐蔽：模型可能在历史轨迹上学出在回测中"看起来有效"但完全不具泛化性的路径依赖。

**4. 超参数更多**
PPO 就有：clip ratio、GAE λ、entropy coeff、learning rate schedule、mini-batch size……每一个都影响收敛。GA 的调参工作量确实更小。

### 3.4 RL 的真正优势

RL 最强的场景是当前动作会改变未来状态：

- 仓位大小
- 执行时机
- 订单类型选择
- 组合再平衡
- 库存控制
- 策略/冠军分配
- 在回撤下的冻结/降险决策

这些都是控制问题，不是参数搜索问题。

对 QuantLab，RL 应首先被视为围绕 GA 发现的冠军的控制器：

```
状态：
  当前冠军、当前持仓、现金、回撤、波动率、
  近期滑点、拒单、OOS/实盘表现、市场状态标签

动作：
  持有、减仓、冻结、淘汰候选、切换冠军、调整最大资金桶

奖励：
  风险调整后实盘超额收益
  - 回撤惩罚
  - 换手/滑点惩罚
  - 订单状态异常惩罚
  - 冻结/恢复惩罚
```

这比让 RL 从价格历史直接产生原始买卖决策安全得多。

---

## 4. QuantLab 中已内置的 RL 思路

以下是 QuantLab 现有设计与 RL 经典概念的对应关系——许多 RL 核心思路已经在系统中，只是没有用 RL 的语言命名：

| QuantLab 现有设计 | 对应的 RL 概念 |
|---|---|
| DCA 双基线 → 算超额收益 | **Baseline / Advantage function**：RL 用 V(s) 基线消除 reward 方差，DCA 做同样的事 |
| 四窗口 cascade 短路 | **Shaped reward**：中间窗口就给信号，等价于 dense reward |
| `mutation_ramp_log` | **Entropy scheduling**：RL 训练后期降低 entropy（探索），变异衰减做类似的事 |
| SharpeBank 存历史精英 | **Experience replay buffer** 的雏形 |
| OOS holdout 验证 | **Train/eval split**：RL 的 offline evaluation，防止过拟合 |
| 一致性惩罚（λ_cons=0.3） | **Reward shaping**：多目标 reward 设计 |

### 4.1 值得借鉴的高价值 RL 思路

**SharpeBank 升级为跨 Epoch 精英重放池**

类比 Prioritized Experience Replay：不只存 top-1，存 top-K 历史优质基因，按 fitness 加权采样，注入新 epoch 初始种群。

**Curriculum Learning → 窗口渐进训练**

早期 epoch 只用 6m 窗口（简单），逐步解锁 2y → 5y → 10y。防止早期种群被 10y 高门槛大量 Fatal 掉，搜索效率大幅提升。当前的 cascade 短路是"节约计算"的逻辑，curriculum 是"引导搜索"的逻辑，目的不同。

**种群 Fitness 归一化 → 等价于 Advantage Function**

在选择阶段用 `fitness_i - mean(population_fitness)` 归一化，让选择压力反映的是"相对种群的优势"而非绝对分数。一行改动，零代价。

---

## 5. GA 过拟合防范

### 5.1 GA 过拟合的特殊性

GA 的过拟合和普通 ML 不同，根源是"搜索次数"本身：

```
普通 ML 过拟合 → 模型参数太多，拟合了训练集噪声
GA 过拟合      → trial 次数太多，在巨大搜索空间里
                总能凑巧找到"历史上看起来最好"的参数
```

本质上是**多重检验问题（Multiple Testing）**。测试了 10,000 个基因，总有几个纯靠运气在历史数据上表现出色。种群越大、代数越多、参数越多，风险越高。

### 5.2 健壮目标函数（而非单一 Sharpe）

不要只优化收益率或 Sharpe。使用奖励表现并惩罚脆弱性的复合分数：

```
fitness =
  alpha_over_dca（超过 DCA 基线的超额收益）
  + statistical_confidence（统计置信度）
  + consistency_across_windows（跨窗口一致性）
  - drawdown_penalty
  - turnover_penalty
  - slippage_sensitivity_penalty
  - parameter_instability_penalty
```

**GT-Score（MDPI 2026）** 直接在目标函数里惩罚 IS/OOS 表现的差距：

```
GT-Score = f(IS_performance) - λ · |IS_performance - OOS_performance|
```

实测在 Walk-Forward 框架下，使用 GT-Score 的策略泛化率比普通目标函数**提升 98%**。对 GA 特别有价值：把"不过拟合"直接写进 fitness，让进化本身就偏向泛化性好的基因。

QuantLab 的一致性惩罚（`λ_cons = 0.3`）本质上已经是这个思路——惩罚"某个窗口特别好但其他窗口很差"的基因。GT-Score 可以进一步加入 IS vs OOS 差距项。

### 5.3 Walk-Forward Optimization (WFO)

最主流的工业界做法。滑动窗口：在第 1-5 年训练，在第 6 年验证；然后在 1-6 年训练，在第 7 年验证……每段的验证结果拼接成一条"真实可信"的权益曲线。

关键：**训练集永远在验证集之前**，保持时序，不泄漏未来信息。

候选选择应偏向在所有 fold 中都可接受的基因，而不是主导某一历史窗口的基因。

QuantLab 现状：四窗口都是固定历史切片（不是滚动 WFO），这是当前的主要缺口之一。

### 5.4 CPCV（Combinatorial Purged Cross-Validation）

Bailey & López de Prado 提出，目前被认为是金融时序数据上**最严格**的验证方法。

核心机制：
1. **Purging**：删除训练集中与测试集时间相邻的样本，防止信息渗漏
2. **Embargo**：测试集前后各加一段隔离期
3. **Combinatorial**：穷举所有可能的训练/测试分组，生成多条回测路径的**分布**

输出不是单点估计（"Sharpe=1.5"），而是一个分布——从这个分布可以直接估算**过拟合概率（PBO, Probability of Backtest Overfitting）**。PBO > 50% 说明策略大概率过拟合。

ScienceDirect 2024 年对比研究显示 CPCV 显著优于 K-Fold、Purged K-Fold 和 Walk-Forward。

### 5.5 参数邻域稳定性

只在某一精确参数点有效的基因是可疑的。对每个冠军候选，扰动附近参数，验证表现不会崩溃：

```
gene
gene + small noise
gene - small noise
gene with one segment slightly shifted
```

这借用了 GP 过拟合研究中的"平坦最小值"（flat minimum）/ sharpness-aware 思想，映射到量化交易中就是"参数局部性"。

### 5.6 种群多样性保护

**Niching / Fitness Sharing**

防止 GA 种群过早收敛到单一局部最优（这种"单一最优"往往是对历史噪声的精确拟合）。

做法：计算种群内基因之间的距离，对相似基因的 fitness 打折，强迫种群保持多样性。

**Island Model（隔离种群）**

多个并行子种群，各自在稍微不同的历史窗口/参数初始化下进化，定期交换精英基因。好处：每个岛过拟合的是不同的噪声，合并后能互相抵消。

### 5.7 多重检验元数据

每个候选包应保留足够的元数据以判断好结果是否可能是运气：

- 种群大小、代数、已评估基因总数
- 种子数量、策略家族尝试次数、优化器 trial 数
- IS/OOS 窗口、数据哈希、fitness 版本

这是 DSR、PBO、CPCV 或任何选择偏差感知诊断的必要条件。

### 5.8 QuantLab 防过拟合现状对照

| 防范手段 | QuantLab 现状 | 缺口 |
|---|---|---|
| 多窗口 IS 评估 | 已有（6m/2y/5y/10y）| 四窗口是固定切片，不是滚动 WFO |
| OOS holdout | 已有（RunOOS，≥90天）| 只有一段固定 OOS，不是 CPCV 多路径分布 |
| DSR | 已有 | 完整 |
| 一致性惩罚 | 已有（λ_cons=0.3）| 完整 |
| Clamp/Validate | 已有 | 完整 |
| SBB 压力测试 | 已有 | 完整 |
| GT-Score 类 IS/OOS 差距惩罚 | 无 | 可加入 fitness，代价低 |
| 种群多样性保护（Niching）| 无 | 中等实现代价 |
| PBO 估算 | 无 | 需要多路径回测 |
| WFO 滚动验证 | 无 | 改造代价较大 |

---

## 6. 贝叶斯优化集成方案

### 6.1 BO 和 GA 的分工定位

```
贝叶斯优化 (BO)  → 高效采样、代理建模、全局指引
遗传算法   (GA)  → 种群多样性、交叉/变异探索、约束处理

BO 的优势区：低维（< ~30 参数）、评估极贵、需要样本效率
GA 的优势区：高维混合空间、离散约束、种群并行
```

两者是互补而非替代关系。QuantLab 有三个天然的 BO 接入点：

### 6.2 接入点 A：Optuna 元优化 GA 超参数（最低代价）

**问题**：GA 本身有一套超参数（种群大小、变异率、交叉率、代数、`λ_cons`、`FatalMDD`），目前靠手动经验设置。

**做法**：

```
Optuna Trial (Python)
        │
        ▼  建议一组 GA 超参数
POST /api/v1/evolution/tasks   ← 现有 HTTP API，零改动
        │
        ▼  等待 epoch 完成
GET  /api/v1/evolution/tasks/:id
        │
        ▼  取 best_fitness / 收敛代数 / top-K 均值
Optuna 更新代理模型 → 建议下一组超参数
```

**代理模型选型**：TPE（Tree-structured Parzen Estimator）——Optuna 默认，天然处理混合类型（int 的 population_size、float 的 mutation_rate、分类的 selection_strategy）。

**PostgreSQL 持久化**：

```python
study = optuna.create_study(
    storage="postgresql://quantlab:xxx@localhost/quantlab",
    study_name="ga_hparam_search",
    direction="maximize",
    load_if_exists=True  # 跨次运行累积，历史不浪费
)
```

Optuna 直接写进同一个 PostgreSQL 库，每次运行的 trial 累积——第 10 次启动时已经有前 9 次的先验知识。

**优化目标示例**：

```python
def objective(trial):
    config = {
        "population_size": trial.suggest_int("pop", 50, 500),
        "mutation_rate_init": trial.suggest_float("mut_init", 0.01, 0.3),
        "lambda_cons": trial.suggest_float("lambda_cons", 0.1, 0.5),
        "fatal_mdd": trial.suggest_float("fatal_mdd", 0.25, 0.5),
    }
    task_id = post_evolution_task(config)
    result = wait_and_get_result(task_id)
    return result["best_score_total"]
```

**关键优势**：Go 代码零改动。Python 脚本放 `research/`，调用现有 HTTP API 即可。

### 6.3 接入点 B：多保真度 BO 预热初始种群（中等代价）

**核心洞察**：四窗口 cascade 本身就是天然的**多保真度结构**：

```
6m   → 低保真度，快，成本 ≈ 1x
2y   → 中保真度，成本 ≈ 4x
5y   → 高保真度，成本 ≈ 10x
10y  → 最高保真度，成本 ≈ 20x
```

Multi-Fidelity BO 的做法：用低保真度廉价评估大量候选，代理模型学会哪些区域低保真度好、低→高保真度能保持，然后把高保真评估聚焦在最有潜力的区域。

**实现方式**：

```
Phase 1 — BO 预热（Python，仅 6m 窗口）
  用 Optuna + CMA-ES sampler 采样 100 个基因
  建立"6m_score → 参数区域"的代理模型
  找出高期望改进的参数区域

Phase 2 — 种子注入 GA（现有 API 扩展）
  从 BO 结果取 Top-20 基因 → 作为初始种群的一部分
  剩余初始种群仍随机生成（保持多样性）

Phase 3 — GA 正常运行（Go 层零改动）
  初代种群质量更高 → 收敛更快
```

需要在 `CreateEvolutionTaskRequest` 增加可选的 `seed_genes` 字段。

### 6.4 接入点 C：新策略可行性预筛（研发阶段）

**场景**：开发 `sigmoid_v2` 或探索全新策略思路时，不知道参数空间里有没有"可进化的区域"。

**做法**：GA 之前先跑一次 BO 可行性探针：

```
BO 用 6m 单窗口，采样 50-80 个基因
        ↓
绘制 fitness landscape 热图（参数对 × 6m_score）
        ↓
判断：
  A) 有明显高分区域 → 用这些种子跑 GA，收敛快
  B) 全域平坦/随机  → 策略本身没有 edge，停止投入
  C) 多个孤立高分区 → 分别跑多个 GA epoch，找最优
```

**工具**：`research/bo_probe.py`，直接读 klines 表，调用轻量版评估（不走 HTTP API，直接 Python 实现 6m 评估逻辑）。

### 6.5 接入点 D：Optuna Pruner ↔ Cascade 短路映射

Optuna 的 Pruner（中途剪枝）与 QuantLab 的 cascade 短路存在天然对应：

| QuantLab 机制 | Optuna 对应机制 |
|---|---|
| Fatal（MDD 超阈值 → 终止评估）| HyperbandPruner |
| Cascade 短路（6m Fatal → 跳过后续窗口）| `trial.report(intermediate, step)` + `trial.should_prune()` |
| OOS 验证 | 独立 evaluation set |

每个"窗口"对应一个 `step`，Optuna 在中间步骤就能剪掉明显差的 trial，和 cascade 短路等价但增加了统计判断层（"不只是 Fatal，是否统计上低于中位数？"）。

### 6.6 工具选型

| 工具 | 用途 | 理由 |
|---|---|---|
| **Optuna + TPE** | GA 超参数元优化 | 混合类型支持好，PostgreSQL 原生，累积学习 |
| **Optuna + CMA-ES sampler** | 连续参数空间的 BO 预热 | 比 TPE 更适合高维连续参数 |
| **Optuna + NSGAIISampler** | 多目标（Sharpe + 一致性）Pareto 探索 | 内置多目标支持，直接输出 Pareto 前沿 |
| **BoTorch（可选，进阶）** | 精确 GP 代理模型 + 多保真度 | 学术最强，但工程集成成本高 |

### 6.7 贝叶斯证据用于 Promote 决策

Promote 决策应逐步加入概率类证据：

```
P(OOS alpha > 0 after costs)
P(max_drawdown > threshold)
P(fee/slippage stress survives)
expected alpha with credible interval
```

这比单一 `score_total` 更好。首期实现可以简单而经验化：用 fold/seed/stress run 作为样本，估算比例和置信区间，存储在 result package 的 review layer 中。完整贝叶斯建模可以延后。

### 6.8 贝叶斯更新用于实盘观察期

小资金实盘交易应更新置信度，但不污染训练数据：

```
先验证据：
  IS、OOS、stress、review、replay、DSR/PBO

实盘证据：
  真实成交、滑点、拒单、回撤、已实现超额收益

后验决策：
  继续试探、扩大资金、冻结、退役
```

实盘结果不应自动重新进入 GA 训练集或 OOS 窗口，属于独立的观察期证据流。

### 6.9 实施优先级

```
第一步（1-2天）：research/ 目录加 optuna_ga_meta.py
  → 调现有 HTTP API，Optuna 优化 GA 超参数
  → Go 代码零改动，立即有价值

第二步（3-5天）：加 bo_probe.py 做策略可行性探针
  → 开发新策略时先跑，防止在无 edge 策略上浪费 GA 计算

第三步（1-2周）：CreateEvolutionTaskRequest 加 seed_genes 字段
  → BO 预热结果注入 → GA 冷启动提速
```

---

## 7. RL 思路借鉴

### 7.1 Reward 设计用于 GA Fitness

即使在实现 RL agent 之前，也可以借用 RL 的 reward 设计思路。在 GA fitness 中加入：

```
score =
  alpha_over_dca
  - max_drawdown_penalty
  - turnover_penalty
  - slippage_penalty
  - order_frequency_penalty
```

### 7.2 Contextual Bandit 作为 RL 的安全第一步

比完整 RL 更安全的第一步：

```
上下文：
  市场状态、波动率、冠军验证档案、实盘观察期统计

动作选择：
  冠军 A、冠军 B、冻结、减仓、调整最大资金桶

奖励：
  扣除成本后的已实现超额收益（有回撤上限）
```

这避免了最难的 RL 问题：用有限市场数据建模长期状态转移。

### 7.3 多冠军资金分配控制器

如果 QuantLab 将来有多个冠军，RL/Bandit 逻辑可以分配资金：

```
趋势跟踪冠军
均值回归冠军
低波动率冠军
高波动率冠军
现金 / 冻结
```

控制器应在硬约束下运行：

- 每个冠军最大资金上限
- 最大账户敞口
- 最大回撤前冻结
- 不自动覆盖 kill switch
- 不在没有人工 Promote 的情况下自动操作

### 7.4 执行时机原型

RL 执行原型最终可以决定：

- 立即市价单 vs 延迟
- 订单大小分桶
- 重试时机
- 当前价差/滑点是否可接受

需要远比现在更丰富的执行数据，应在实盘成交持久化、滑点测量、幂等边界完全可靠之后再考虑。

---

## 8. 推荐路线图

### Phase A：正确性优先于新智能

先完成当前正确性阻塞项：

- fail-closed 原始 adapter 输出验证
- 幂等读错误处理
- terminal 交易状态单调性
- `spot_executions` DB 去重保障
- v1 每个交易所账户只有一个活跃实盘控制器

任何 RL 或贝叶斯层都不应补偿弱的订单状态正确性。

### Phase B：健壮性证据层

为候选包增加更丰富的健壮性输出：

- Walk-Forward fold 结果
- 市场状态切片
- 费率/滑点扰动检查
- 参数邻域稳定性
- 多重检验元数据
- DSR/PBO/CPCV 就绪的 trial 计数

### Phase C：贝叶斯搜索控制器

增加一个可选的研究级优化器，调优 GA 配置：

```
输入：允许的 GAConfig / fitness-weight 搜索空间
目标：Walk-Forward 健壮性分数
输出：建议的 GAConfig 预设
```

保持在实盘交易之外。

### Phase D：贝叶斯 Promote 证据

将 review 输出扩展到概率类字段：

```
prob_oos_alpha_positive
prob_drawdown_breach
prob_slippage_stress_survival
expected_alpha_interval
```

最初是决策支持字段，不是自动 Promote 规则。

### Phase E：小资金观察期 Bandit 分配

只有在实盘正确性够强之后：

- 在 `hold`、`reduce`、`freeze`、小资金桶之间选择
- 永不绕过硬风险约束
- 永不绕过人工 Promote
- 每次分配决策保留完整审计日志

### Phase F：窄范围 RL 执行研究

只有在足够的真实成交/滑点/订单簿数据存在之后：

- 构建离线 Gym 类执行环境
- 使用历史成交和真实滑点
- 与确定性执行基线比较
- 扣除成本后持续优于简单规则之前保持 paper-only

---

## 9. v1 的非目标

不要在 v1 中添加：

- 直接发出原始买卖信号的端到端 RL 交易器
- 基于 RL/贝叶斯分数自动实盘 Promote
- 实盘数据自动混回 GA 训练集
- 贝叶斯优化器直接选择最终冠军
- 没有明确资金所有权模型的多策略共享账户实盘分配
- 正确性门控关闭之前的大规模架构重组

---

## 10. 开放问题

1. 在当前数据量下，DSR/PBO 类诊断有意义需要最少多少个 Walk-Forward fold 和每个 fold 的交易次数？
2. QuantLab 应该存储所有已评估基因，还是只存每代摘要加精英候选，用于多重检验诊断？
3. 第一个健壮性分数应该是简单手动加权分数，还是带校准分量的 GT-Score 启发式复合分数？
4. 实盘观察期是否应该更新附在冠军历史上的贝叶斯置信记录，还是仅作为独立的 review 制品？
5. Bandit 控制器的第一个安全动作空间是什么：只有 freeze/reduce，还是也包括小仓位分桶？
6. ε 容忍度（fitness_version bump 的触发阈值）何时完成校准？校准完成之前应如何保守对待评分变更？

---

## 11. 工作建议

下一个有价值的智能升级不是 RL，而是围绕 GA 的更强证据层：

```
GA 候选生成
→ Walk-Forward / OOS / stress / 邻域验证
→ 贝叶斯风格置信度摘要
→ 小资金观察期
→ 人工 Promote / Freeze / Retire
```

RL 应在这个层建立之后才引入，且只作为围绕已验证 GA 冠军的约束控制组件。

---

## 参考文献

**遗传算法与量化交易**
- [Springer 2025: GA for multi-threshold trading strategies (Artificial Intelligence Review)](https://link.springer.com/article/10.1007/s10462-025-11419-z)
- [Springer 2025: Multi-objective genetic programming for algorithmic trading](https://link.springer.com/article/10.1007/s10462-025-11390-9)
- [Adaptive Multi-Asset GA with Walk-Forward Robustness Analysis](https://www.researchgate.net/publication/400541386_Adaptive_Multi-Asset_Trading_Strategy_Optimization_via_Genetic_Algorithms_with_Walk-Forward_Robustness_Analysis)
- [arxiv 2510.07943: Agent-Based GA for Crypto Trading Strategy Optimization](https://arxiv.org/html/2510.07943v1)

**强化学习与量化金融**
- [arxiv 2408.10932: The Evolution of RL in Quantitative Finance: A Survey (2024)](https://arxiv.org/pdf/2408.10932)
- [arxiv 2512.10913: RL in Financial Decision Making: Systematic Review (2025)](https://arxiv.org/html/2512.10913v1)
- [OpenAI: Evolution Strategies as scalable alternative to RL](https://openai.com/index/evolution-strategies/)
- [Springer 2024: Proximal Evolutionary Strategy — GA + RL hybrid](https://link.springer.com/article/10.1007/s12293-024-00419-1)

**过拟合防范**
- [GT-Score: Robust Objective Function for Reducing Overfitting (MDPI 2026)](https://www.mdpi.com/1911-8074/19/1/60)
- [Backtest Overfitting in ML Era: CPCV vs WFO comparison (ScienceDirect 2024)](https://www.sciencedirect.com/article/abs/pii/S0950705124011110)
- [Purged Cross-Validation (Wikipedia)](https://en.wikipedia.org/wiki/Purged_cross-validation)
- [CPCV: Combinatorial Purged Cross-Validation (paperswithbacktest)](https://paperswithbacktest.com/course/combinatorial-purged-cross-validation-cpcv)
- [Interpretable Walk-Forward Validation Framework (arxiv 2512.12924)](https://arxiv.org/html/2512.12924v1)

**贝叶斯优化**
- [Optuna: Next-generation Hyperparameter Optimization Framework](https://optuna.org/)
- [Multi-Fidelity Methods for Optimization: A Survey (arxiv 2402.09638)](https://arxiv.org/html/2402.09638v1)
- [Constrained Multi-Fidelity Bayesian Optimization (arxiv 2503.01126)](https://arxiv.org/pdf/2503.01126)
- [Recent Advances in Bayesian Optimization (arxiv 2206.03301)](https://arxiv.org/pdf/2206.03301)
