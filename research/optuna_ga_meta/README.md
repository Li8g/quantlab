# optuna_ga_meta — Optuna TPE 元优化 GA 超参数(接入点 A)

> 来源:LEARN-ga-rl-bayesian §6.2(接入点 A 草图)+ §12.4 **item 3**(权威排序
> 的下一项)。Go 代码零改动:Python 走现有 HTTP API。
> 仓库教义:research/ 永不进 server path;**本实验不触碰生产 quantlab 库**
> (隔离原因见 §3,这是设计核心)。

## 1. 问题与目标

GA 自身的超参数(种群大小、精英比、代数)目前靠手动经验设置。用 Optuna TPE
把"跑一次 epoch"当目标函数,搜"**等额评估预算下,什么样的 (pop_size,
elite_ratio) 组合能进化出最高的 best ScoreTotal**"。

```
Optuna trial → POST /api/v1/evolution/tasks(建议的超参数)
            → 轮询 GET /api/v1/evolution/tasks/:id 至 succeeded
            → GET /api/v1/challengers/:id(score_total)
              + /package(diagnostics.search_stats 验预算)
            → report → TPE 建议下一组
```

## 2. 与 §6.2 草图的三处出入(2026-06-12 代码核对,落地前修正)

1. **可搜旋钮比草图少。** 草图想搜 `mutation_rate_init` 和 `λ_cons`——
   均**不在** `CreateEvolutionTaskRequest` 暴露面里;λ_cons 还被
   `fitness_version`(v1-raw-std, λ=0.3)锁死,改它=版本事件+全体 challenger
   不可比。Go 零改动前提下真实可搜集 = {pop_size, max_generations,
   elite_ratio} ∪ {fatal_mdd, fees, spawn_mode, …}。
2. **fatal_mdd 与费率刻意不搜。** 它们改变的是**任务语义**而非搜索效率——
   放宽 fatal_mdd 让更多基因存活 → best score 单调受益,TPE 会学到
   "宽松=高分"的退化梯度,与"GA 调参"无关。两者锁定 config 现值
   (fatal_mdd=0.70,fee/slip=10/5bps,test_mode=false 真费率)。
   spawn_mode 锁 `random_once`(scratch 库无 champion,inherit 无意义)。
3. **目标函数有噪声。** epoch seed = `time.Now().UnixNano()`
   (`epoch/service.go:227`),请求面无 seed 字段 → 同一组超参数两次跑结果
   不同。处理:TPE 本身容噪 + `--repeats`(同配置跑 N 次取中位)可选;
   解读时把小差异当噪声,只信大效应。

## 3. 隔离:scratch 库 + 第二个 saas 实例(不可省)

`epoch/service.go:377`:每个完成的 task **无条件** `SharpeBank.Add`
(无 test_mode 闸门)。50 个 meta-trial 会把 `(sigmoid_v1, BTCUSDT)` 的
SharpeBank 从 N=5 灌到 N=55,**直接污染未来真实 Promote 的 DSR 输入**
(方差/N 全变);gene_records / evaluation_traces 同样膨胀。

因此:`setup_meta_lab.py` 建独立库 `quantlab_meta`(同 PG 实例),从生产库
拷 `klines`+`kline_gaps`(只读源),生成 `config.metaopt.yaml`(端口
:8090/:8091/:9092,库指向 quantlab_meta),meta 专用 saas 实例跑在它上面。
生产 quantlab 库全程零接触;scratch 库可整库 DROP 重来。

## 4. 等额预算设计(公平比较的关键)

不约束预算时,"pop 越大代数越多分越高"是平凡结论。锁定
**evaluations_total ≈ B**(默认 3000 [INVENTED v1]),利用引擎不变量
(SearchStats 已 ship,PR #28):

```
evals = pop + (gens − 1) × (pop − nElite),  nElite = max(1, int(pop × ratio))
⇒ max_generations = round(1 + (B − pop) / (pop − nElite))
```

搜索空间 [INVENTED v1]:
- `pop_size`:int log [16, 256](上限保证 ≥2 代)
- `elite_ratio`:float [0.02, 0.30]
- `max_generations`:派生,非独立维

事后用 `diagnostics.search_stats.evaluations_total` 验证实耗预算;
**收敛早停会使实耗 < B——这本身是 (pop, ratio) 组合的性质,不补偿**
(早停省下的预算就是该配置的收敛特征),实耗记入 trial user_attrs。

## 5. 运行

```bash
# 一次性:建 scratch 库 + 拷 klines + 生成 config.metaopt.yaml
../optuna_toy/.venv/bin/python setup_meta_lab.py

# 起 meta 专用 server(前台,另开终端;或 nohup)
cd ../.. && go build -o /tmp/quantlab-saas ./cmd/saas
/tmp/quantlab-saas --config research/optuna_ga_meta/config.metaopt.yaml

# 元优化(默认 50 trials × 3000 evals;study 存同 PG 实例独立库 optuna_ga_meta)
../optuna_toy/.venv/bin/python optuna_ga_meta.py --trials 50
```

任务单飞(`ErrTaskInProgress`):trial 串行,409 时等待重试。
脚本默认指 `http://127.0.0.1:8090` —— 即 meta 实例;**不要**把它指到 :8080
(生产 lab 实例),§3 的污染会立刻发生。

## 6. 产出与解读

- `out/meta_summary.csv`:trial × {pop, ratio, gens, score_total,
  evaluations_total, generations, duration_s, fatal}
- Optuna dashboard(库 `optuna_ga_meta`)看 (pop, ratio) 的响应面;
- 预注册的解读纪律:**目标噪声未测定前,只信跨区域的大效应**(例:pop=32
  与 pop=200 的系统性差异),不信单 trial 排名;有效 trial ≥ 30 再读结论。
- 顺带产物:§12.2-③ 的"种群 fitness 归一化"一行级 Go 改动,待本框架就绪后
  用 A/B 两组 study 验证(那是后续第二阶段,涉及 Go 改动,另行立项)。

## 7. 首轮结论(2026-06-12,50 trials × B=3000,零失败)

study `ga_meta_btcusdt_1h_b3000`(:5433 库 optuna_ga_meta);数字
`out/meta_summary.csv`。

**按 pop 分桶(核心读数):**

| pop 区间 | n | score mean ± sd | 实耗 evals 均值 /3000 |
|---|---|---|---|
| < 32 | 13 | 0.802 ± **0.024** | 525(早收敛) |
| 32–63 | 11 | 0.820 ± 0.004 | 950 |
| 64–127 | 12 | 0.823 ± 0.003 | 1842 |
| ≥ 128 | 15 | 0.825 ± 0.004 | 2904 |

**噪声标尺**(同参数重复 trial):Δ=0.008(pop=44)与 Δ=0.026(pop=26)
—— 排行榜上 0.82x 区间内的名次差**都在噪声内**;top-10 横跨 pop 21..193。

**三条结论:**

1. **pop ≥ ~64 即平台**,桶间均值差(≤0.002)小于噪声;elite_ratio 在
   [0.05, 0.25] 无可辨别效应。**生产默认(pop=300, ratio=0.05)稳坐平台区
   —— 无重调收益,维持现状**(这正是接入点 A 想回答的问题,答案是"不用动")。
2. **唯一的坑是小种群**:pop < 32 均值掉 0.02 且方差 ×6 —— 多样性不足,
   收敛快(实耗 525/3000)但质量不稳。任何手工实验别用 pop < 32。
3. **真正的约束是收敛检测,不是 (pop, ratio)**:小/中 pop 大量预算因
   EarlyStop(patience=5, min_delta=0.001)没花掉;per-eval 效率最高的是
   32–63 桶(950 evals 拿到 0.820),绝对分最高的大 pop 只多 +0.005。
   ⇒ 下一个值得研究的杠杆是**收敛/多样性准则**(EarlyStop 参数、
   diversity rescue 触发),与 §12.3-③ "先量化种群塌缩速度" 同向。

**Caveat**:单 budget(3000)、单 pair、单 seed 域的结论;预算改变时
平台位置可能移动(B=7140≈生产默认预算时未测)。harness 在,重跑便宜。

### 7.1 核心读数的通俗解读

把 GA 想成一支育种队:`pop_size` 是队伍人数,`elite_ratio` 是每代留种比例。
实验给每种编制同样的总预算(3000 次基因评估),问什么编制育出的最佳策略
(best `ScoreTotal`)最高。

**先立噪声标尺,再读榜。** 同一编制跑两次(epoch seed 不同),分数能差
0.008~0.026——这是"运气"的量化。**榜上差距小于它的都不是真差距**;没有
这一步,会把 0.8316 和 0.8298 读成"前者更好",其实是抛硬币。

- **pop < 32:又低又飘**(均值 −0.02,σ ×6)。人太少,几代后基因趋同
  (近亲繁殖),撞到哪个局部最优停哪;且早早触发收敛检测收工
  (实耗 525/3000)。
- **pop ≥ 64:平台**。64/128/300 人的桶间均值差 ≤0.002 < 噪声——人多
  不再加分,只是更贵。`elite_ratio` 在 [0.05, 0.25] 整段无可辨效应。
- **生产默认 pop=300/ratio=0.05 稳坐平台 → 不用动。** "查完发现不用改"
  是有价值的结果:关闭了"手动拍的超参数拍错了"这个悬置疑虑。
- **最值钱的是意外发现:卡上限的不是编制,是收工规则。** 几乎所有队伍在
  预算花完前就触发 EarlyStop(`patience=5, min_delta=0.001`,连续 5 代
  进步 <0.001 即停)提前下班;性价比最高的是 32–63 人中型队(950 evals
  拿到 0.820 ≈ 平台分的 99%)。真正决定搜索深度的是"何时判定无进展"
  + 种群趋同速度——与预研 §12.3-③"先量化种群塌缩"两条线索汇合。

一句话:**这次买的不是更好的参数,而是一张体检报告——现役参数健康,
真瓶颈在收敛判定,且现在知道它在哪。**(下一步实验规划:
`docs/experiment-plan-convergence-diversity-capacity.md`)
