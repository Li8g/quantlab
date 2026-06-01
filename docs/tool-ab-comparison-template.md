# 工具 A/B 对比测试样板(Tool A/B Comparison Template)

用于决定「要不要为现有工作流引入某个新工具」——例如该不该装某个 MCP server、换某个
检索工具。本文先给**方法论模板**(可照填),再附一份**真实 worked example**(codegraph vs
grep,评估是否需要 Serena),最后列**常见陷阱**。

> 核心原则一句话:**先定成功标准,再建独立 ground truth,用真实任务跑,公平对待每一臂,
> 算清成本。** 任何一步省掉,结论就不可信。

---

## 1. 何时用

- 纠结要不要装/换一个工具,而你已有能干类似活的工具(避免重复栈)。
- 想量化「新工具到底比现状强多少」,而不是凭感觉或厂商宣传。

不适用:工具解决的是你**现在完全没有**的能力(那不是 A/B,是补缺,直接评估单点价值即可)。

---

## 2. 方法论模板(照填)

### 2.1 任务(Task)
从**真实工作**里挑一个具体问题,不要造玩具。一句话写清,要可判定。
> 例:「改 X 的某字段,会牵连哪些生产代码?」

### 2.2 成功标准(Success criteria)—— **必须在跑之前写死**
否则会看到结果后再编理由(post-hoc rationalization)。
> 例:「列全所有受影响的非测试文件,漏 1 个算不合格;返回明显误报算扣分。」

### 2.3 Ground truth —— 用**独立于被测工具**的方法建立
通常是 grep/ripgrep + 人工读代码核实。这是评分的标尺,必须先建好。
> 例:`rg -l "Field" --glob '!*_test.go'` 的结果 + 逐个打开确认 → N 个文件。

### 2.4 各臂(Arms)
- Arm A = 被评估的工具。
- Arm B = 基线(通常是已有/内置工具,如 grep、内置 LSP)。
- **拿不到的臂**(如未安装):标记为「分析性评估(analytical)」,按其文档能力推断,
  **明确标注未实跑**,不可冒充实测。

### 2.5 评分维度(Metrics rubric)
| 维度 | 含义 |
|---|---|
| **Recall 召回** | 命中 ground truth 的比例(漏报) |
| **Precision 精确** | 返回里有多少是真相关(误报) |
| **关键准确性** | 有没有「自信地给错」(如假「无 caller」)——比单纯漏报更危险 |
| **Context/token 成本** | 单次返回吞掉多少上下文 |
| **Round-trips 往返** | 拿到完整答案要几次调用 |
| **安装/维护成本** | 是否需常驻进程、重复索引、外部网络/凭据 |
| **何时才赢** | 这工具相对基线的**独占优势场景**是什么 |

### 2.6 公平性要求
每个工具都要在**它的本命场景**和**实际要问的问题**上各试一次。只在对手弱项上测,
结论无效。

### 2.7 判定 + 决策规则(Decision rule)—— 也在跑之前写死
> 例:「若基线已满足成功标准且新工具无独占优势 → 不装;若出现基线明显失手的任务类
> → 才在该任务类上复测新工具。」

### 2.8 样本量
**一个探针是信号,不是证明。** 对你真正在意的**问题类(question class)**跑 2–3 个有
代表性的任务再下结论。一个任务表现差,可能只是不对口。

---

## 3. Worked example:codegraph 够不够,要不要引 Serena

### 3.1 任务
改 `internal/agent/exchange.go` 的 `ExchangeFill` 三个 decimal 字段
(`FillQuantity` / `FillPrice` / `FillFeeAmount`),会牵连哪些**生产代码**?
(背景:这是 quantlab 的 decimal→float64 对账 seam,见 `docs/code-review-plan.md` §3.2。)

### 3.2 成功标准(跑前定)
列全所有触及这些字段 / 构造 `ExchangeFill` 的**非测试**文件;漏 1 个即不合格;
能顺带指出传播链(agent→wire→api→saas/store)加分。

### 3.3 Ground truth(grep + 人工核实)
```
rg -l "FillQuantity|FillPrice|FillFeeAmount" internal/ --glob '!*_test.go'
```
→ **9 个生产文件**:
`agent/exchange.go`、`agent/events.go`、`agent/tradecommand.go`、`agent/client.go`、
`agent/binance/uds_stream.go`、`agent/binance/order.go`、`wire/orderupdate.go`、
`api/live_handlers.go`、`saas/store/models.go`。

### 3.4 各臂
- Arm A = **codegraph**(MCP,已装;index:215 文件 / 3750 节点 / 4727 边)。
- Arm B = **ripgrep**(内置,基线)。
- Arm C = **Serena**(未安装 → analytical,见 §3.6)。

### 3.5 结果

| 探针 | 返回 | Recall(共 9) | 关键准确性 |
|---|---|---|---|
| `codegraph_callers ExchangeFill` | No callers found | 0/9 | struct 无 call 边,符合预期 |
| `codegraph_impact ExchangeFill` | 仅 exchange.go 自身 2 符号 | **1/9** | 无 type-field 引用边 |
| `codegraph_search ExchangeFill` | 4 符号(struct+2 构造器+1 method) | 3/9 文件 | 只找到构造器,漏全部消费者 |
| `codegraph_callers buildFillFromExecution`(函数,codegraph 本命) | **No callers found** | — | ❌ **漏报真实 caller**(实在 `uds_stream.go:476`) |
| `rg -l ...`(Arm B) | 一条命令列 9 文件 | **9/9** | ✅ |

成本对比:

| 维度 | codegraph | ripgrep | Serena(analytical) |
|---|---|---|---|
| Recall(本任务) | ≤3/9 + 一处假「无 caller」 | 9/9 | ~9/9(LSP find-references,type-aware) |
| Round-trips | 4 次调用 | 1 条命令 | 装+索引+2–3 次 RPC |
| 安装/维护 | 已装(但索引需常驻+滞后~0.5s) | 零(内置) | 需装 + 常驻 language server + 重复索引 |
| 独占优势场景 | 理论上 `codegraph_context` 上手/导航——但 §3.8 实测亦失手 | 名字独特的引用查找 | **名字有歧义**的语义引用(grep 会过度匹配处) |

### 3.6 Arm C(Serena)为何不实测也能判定
Serena 基于 gopls 的 LSP `find-references`,对**字段引用是类型感知**的:即便字段名撞名
(如多个类型都有 `Close`),它也能精确解析。**但本任务的 `ExchangeFill` 名字足够独特,
grep 已 100% 召回**,Serena 在此显不出独占优势。Serena 的真正赢面在「标识符有歧义、
grep 过度匹配 / codegraph 欠召」的任务——本次没有这样的任务来暴露它。

### 3.7 判定
- 对「type-field impact」这类问题:**codegraph 严重欠召**(无字段引用边),且在它本命的
  function-caller 上**漏报了真实调用点**——这是比欠召更糟的「自信给错」。
- **ripgrep(内置)已满足成功标准**,且最省。
- 按 §2.7 决策规则:**不引 Serena**。仅当后续遇到「歧义标识符」任务、grep 精确度崩了,
  才在那个任务类上复测 Serena,再决定。
- codegraph 仅剩的可能赢面是 `codegraph_context`(上手陌生区域)——见 §3.8 第二个任务类
  探针(符合 §2.8「一个探针不是证明」,补到 2 个任务类)。

### 3.8 第二个任务类探针:codegraph_context(onboarding 类)
为不冤枉 codegraph,在它**自称的本命场景**再测一个任务类。

- **任务**:理解「四窗 cascade short-circuit 评估 + ScoreTotal 聚合」——入口、Fatal 在哪
  终止级联、ScoreTotal 在哪被引擎填。
- **成功标准(跑前定)**:输出能让人直接上手,至少覆盖 (a) 入口 `AggregateScoreTotal` 且点明
  ScoreTotal 是 engine-filled 边界、(b) cascade Fatal 短路 + `SkippedBy`、(c) 引擎组装/填包
  位置、(d) 关键类型 `CrucibleResult` / `ScoreTotal`。
- **Ground truth**:`AggregateScoreTotal`(fitness/aggregate.go:34,Fatal 短路 43-51)、
  引擎组装 `engine.go:352`、填包 `package.go:139-145`、`CompareFitness`(quant/compare.go:18)、
  convergence Fatal 处理。
- **公平性加强**:把上述核心符号名**直接写进 query** 喂给 `codegraph_context`(给它最好机会)。

结果:

| 成功标准 | codegraph_context 命中? |
|---|---|
| (a) `AggregateScoreTotal` 入口 + engine-filled 边界 | ❌ 未返回(尽管已写进 query) |
| (b) cascade Fatal 短路 + `SkippedBy` | ❌ 未返回 |
| (c) 引擎组装(engine.go:352)/ 填包(package.go) | ❌ 未返回 |
| (d) 关键类型 `CrucibleResult` / `ScoreTotal` | ⚠️ `ScoreTotal` 未返回;`CrucibleResult` 仅在签名露名 |

它实际返回的「entry points」是 `CrucibleWindow`、`CrucibleScoreComponents`,外加一个**前端
TypeScript 类型 `WindowScore`**(与 Go 聚合逻辑无关的噪声),并 dump 了一大段截断的
`evaluateWindow` 源码(吃 token、信噪比低)。Arm B(2 条 grep)则命中全部 landmark + bonus。

**结论**:codegraph_context 也**未通过成功标准**,且即便手喂符号名、在它本命场景下亦然。
两个任务类(impact + context)结果一致 → 对本仓 Go,codegraph 的检索/排序不可靠,**不是
单任务偶然**。§3.7 的「不引 Serena、grep+内置 LSP 已够」判定维持;codegraph 价值存疑,
保留与否取决于是否还有它能赢 grep 的任务类(目前两类都没有)。

---

## 4. 常见陷阱(让对比失效)

1. **成功标准后置**:看到结果再定「合格线」→ 必然偏向。务必跑前写死(§2.2/§2.7)。
2. **没有独立 ground truth**:拿被测工具自己的输出当标准 → 自证。
3. **玩具任务**:挑工具擅长的人造例子 → 结论不迁移到真实工作。
4. **只测对手弱项**:不给每个工具本命场景(§2.6)→ 不公平,结论无效。
5. **单样本下大结论**:一个任务定生死(§2.8)→ 至少 2–3 个代表性任务。
6. **忽略成本**:只比召回、不比安装/维护/token/常驻进程 → 漏掉「能力近似但成本翻倍」。
7. **把 analytical 当实测**:拿不到的臂凭文档推断却不标注 → 误导。

---

## 5. 一句话复用清单

> 真实任务 → 跑前写死成功标准+决策规则 → grep/人工建 ground truth → 每臂测「本命+实际问题」
> → 按 recall/precision/关键准确性/成本/独占场景打分 → 拿不到的臂标 analytical → 2–3 任务
> 再定论。
