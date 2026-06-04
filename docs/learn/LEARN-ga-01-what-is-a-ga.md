# 第 1 章 — 遗传算法是什么(用 toy 策略入门)

> 本章目标:从零讲清 GA 的机械原理,并用项目里最小的 `toy` 策略把每个概念坐实。读完你能看懂 `internal/engine/engine.go` 里那个"进化循环"的骨架。
> 前置:会基础概率即可。**不需要**懂回测——`toy` 故意不碰行情数据。

## 1.1 为什么用 GA,而不是梯度下降

我们想解的问题是:**找一组策略参数,使它在历史行情上的回测评分最高。**

你学过的优化方法大多要求目标函数可导(梯度下降、牛顿法)。但这里的目标函数是:

```
评分 = 回测引擎(一组参数, 十年逐根 K 线的交易模拟)
```

它是个**黑箱**:内部是几万根 K 线的逐根模拟、下单、计算净值、算回撤……你**没法对它求导**,甚至连"参数动一点点评分怎么变"都不连续(多一笔成交,评分可能跳一下)。梯度下降这类方法直接出局。

那网格搜索(把每个参数切成若干格,全试一遍)呢?本项目的参数是 **13 维**,每维哪怕只切 10 格就是 10¹³ 种组合——不可能。

**遗传算法(Genetic Algorithm, GA)** 正好补这个位:它**只要求你能比较两个解谁好谁坏**(能算 fitness),不要梯度、不怕黑箱、不怕中等维度,也能在"多个局部最优"的崎岖地形里搜索。代价是它**不保证全局最优**,是一种务实的启发式(heuristic)。

## 1.2 词汇表:生物隐喻 → 优化术语 → 本项目代码

GA 借用了生物进化的词。一一对应如下(最后一列是本项目里它落在哪):

| 生物词 | 优化里的意思 | 本项目代码 |
|---|---|---|
| 基因 / 染色体 gene/chromosome | 一个候选解 = 一组参数 | `domain.Gene`(其实就是 `[]float64`) |
| 种群 population | 一批候选解(同时存在的几十个) | 引擎里的 `[]domain.Gene` |
| 适应度 fitness | 这个解有多好(一个数,越大越好) | 回测打分(toy 里是个简单公式) |
| 一代 generation | 一轮迭代 | `RunEpoch` 主循环的一次 |
| 选择 selection | 偏向保留高 fitness 的解 | `tournamentSelect` |
| 交叉 crossover | 两个父代解"混血"出子代 | 策略的 `Crossover` |
| 变异 mutation | 随机微扰一个解 | 策略的 `Mutate` |
| 精英 elitism | 最好的几个直接保送下一代 | `produceNextGeneration` 开头 |

记住一句话:**GA = "维护一群解,反复地'优胜劣汰 + 杂交 + 微扰',让整群解越来越好"。**

## 1.3 用 `toy` 策略把概念坐实

项目里有个占位策略 `internal/strategies/toy/toy.go`,专门用来测 GA 机制。它**刻意简单**,正好当教具:

- **基因是 2 维**:`gene[0]=alpha ∈ [0,1]`,`gene[1]=beta ∈ [-1,1]`。
- **fitness 是显式公式**(我们**知道**最优解在哪):

  ```
  score = -( |alpha - 0.42| + |beta - (-0.3)| )
  ```

  即"离目标点 (0.42, −0.3) 越近,分越高(最高 0)"。这就是个 L1 距离的相反数。

- 它**不看行情**(plan-independent),所以这章不用懂回测。

看几段真代码,对上 1.2 的词:

**生成一个随机基因(Sample)** —— 这是种群初始化的零件:

```go
func (t *Toy) Sample(rng *rand.Rand) domain.Gene {
    g := domain.Gene{
        minAlpha + rng.Float64()*(maxAlpha-minAlpha), // alpha ∈ [0,1]
        minBeta + rng.Float64()*(maxBeta-minBeta),    // beta  ∈ [-1,1]
    }
    return t.Clamp(g)
}
```

**交叉(Crossover)** —— 对每一"段"(这里 alpha、beta 各一段)抛硬币,整段继承父代 1 或父代 2:

```go
for _, seg := range t.Segments() {
    from := p1
    if rng.Float64() < 0.5 { from = p2 }      // 50/50 选一个父代
    for _, idx := range seg.Dimensions {
        child[idx] = from[idx]                // 这一段整段继承
    }
}
```

**变异(Mutate)** —— 每一维以概率 `prob` 抖一下,抖动幅度是 `正态噪声 × 步长 × scale`:

```go
if rng.Float64() < prob {
    delta := rng.NormFloat64() * seg.GeneStep[localIdx] * scale
    child[geneIdx] += delta
}
```

**适应度(Evaluate)** —— 算那个 L1 距离的相反数:

```go
score := -(math.Abs(g[geneDimAlpha]-targetAlpha) + math.Abs(g[geneDimBeta]-targetBeta))
```

> 注意:真实策略 `sigmoid_v1` 的这几个动词复杂得多(基因 13 维、`Evaluate` 是一整段四窗回测),但**接口和机制和 toy 完全一样**。第 2、3、4 章就是把 toy 的这几格换成真的。

## 1.4 "一代"是怎么走的

把 1.2 的零件拼起来,一代的流程是(伪代码):

```
第 0 代:用 Sample 随机生成 N 个基因                      // 初始种群
循环每一代:
    评估:对每个基因算 fitness(Evaluate)
    按 fitness 排序
    产生下一代:
        ① 精英保送:最好的 K 个原样进下一代               // 保证不退步
        ② 其余 N−K 个:tournamentSelect 选两父代
                       → Crossover 混血 → Mutate 微扰
    收敛检查:连续多代没进步 → 提前停;否则到代数上限停
```

对应到代码(只点名,细节留第 5 章):

- `RunEpoch`(`engine.go`)是整个循环。
- `evaluatePopulation` 评估一代的所有基因。
- `produceNextGeneration` 产下一代,内部就是上面的 ①②:

  ```go
  nElite := int(float64(n) * e.cfg.EliteRatio)   // 默认 EliteRatio=0.05
  if nElite < 1 { nElite = 1 }
  // ① 精英:order 是按 fitness 排好的下标,前 nElite 个原样复制
  for i := 0; i < nElite; i++ { next = append(next, clone(pop[order[i]])) }
  // ② 其余靠选择+交叉+变异填满
  for len(next) < n {
      p1 := e.tournamentSelect(rng, scores, fingerprints)
      p2 := e.tournamentSelect(rng, scores, fingerprints)
      child := e.strat.Crossover(pop[p1], pop[p2], rng)
      child = e.strat.Mutate(child, mutProb, mutScale, rng)
      next = append(next, child)
  }
  ```

- `convergence.go` 管收敛/提前停(默认 `MaxGenerations=25`)。

**锦标赛选择(tournament)** 是这样的:随机抓 `TournamentSize` 个(默认 3)基因,留下其中 fitness 最高的当父代。重复两次得到两个父代。它的好处是:高 fitness 的基因更可能被选中,但低 fitness 的也有机会——既偏向"好",又不至于过早把多样性掐死。

## 1.5 手动走一代(带数字)

设种群 `N=4`,精英 `K=1`,锦标赛 size=2(为讲解简化;真实默认 3)。fitness `= -(|a-0.42| + |b+0.3|)`。

**第 0 代**(随机得到的 4 个基因及其分数):

| 基因 | [alpha, beta] | fitness |
|---|---|---|
| g1 | [0.40, −0.25] | −0.07  ← 最好 |
| g3 | [0.30, −0.50] | −0.32 |
| g4 | [0.65,  0.30] | −0.83 |
| g2 | [0.90,  0.10] | −0.88  ← 最差 |

**① 精英保送**:最好的 g1 = [0.40, −0.25] 直接进下一代。

**② 造一个子代**:
- 锦标赛选父代 1:随机抓 {g2, g3} → 留 g3(−0.32 > −0.88)。
- 锦标赛选父代 2:随机抓 {g1, g4} → 留 g1(−0.07 最好)。
- 父代 = g3=[0.30, −0.50] 和 g1=[0.40, −0.25]。
- **交叉**:alpha 段抛硬币继承 g1 → 0.40;beta 段继承 g3 → −0.50。子代 = [0.40, −0.50],fitness = −(0.02+0.20) = **−0.22**(已比两个父代之一好)。
- **变异**:beta 维抖动 +0.15 → −0.35。子代 = [0.40, −0.35],fitness = −(0.02+0.05) = **−0.07**。

一次交叉+变异,就造出了一个和当前最优并列的子代。把"②"重复 3 次填满 4 个名额,**整群的平均分朝 (0.42, −0.3) 爬升**。几十代后,种群会密集地聚在最优点附近——这就是"进化"。

## 1.6 为什么这样能行(直觉)

- **选择**给高 fitness 的基因更高的"繁殖概率",等于把搜索的算力偏向地形里的高地。
- **交叉**把不同解的"好零件"重新组合——也许 g3 的 alpha 好、g1 的 beta 好,拼一起更好。
- **变异**提供局部探索和"跳出局部最优"的随机性;没有它,种群会很快同质化、卡在一个次优点。
- **精英**保证最好解不会因为运气差的交叉/变异而丢失 → 每代的最高分**单调不降**。

这四个力一起,使整群解在崎岖、不可导的地形上稳定地往高处走,而**全程只用到"比较谁好"这一个能力**。

## 小结 / 下一章

你现在懂了 GA 的机械原理,并在 `toy` 上看到了每个零件的真代码。真实项目里换的是这三格:

1. 基因不是 2 维玩具,是 **13 维真实策略参数** → **第 2 章**。
2. fitness 不是一个公式,是把参数喂进**一整段历史回测、跨四个时间窗打分** → **第 3、4 章**。
3. 这个循环怎么**并行评估、怎么判收敛** → **第 5 章**。

> 动手:`internal/engine` 和 `internal/strategies/toy` 下有针对收敛/确定性的测试(如 `TestReplayWithinTolerance`、toy 的收敛用例),`go test ./internal/engine/ -run Replay -v` 可以看 GA 在 toy 地形上跑出来的结果。
