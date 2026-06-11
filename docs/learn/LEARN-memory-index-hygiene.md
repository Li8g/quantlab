# LEARN — 记忆索引漂移 & 上下文臃肿的治理手册

> 类型:方法论 explainer(非规范文档)。
> 来源:2026-06-11 一次针对 `MEMORY.md` + `CLAUDE.md` 的 token 治理实战,沉淀为可复用流程。
> 适用:任何"每轮对话都常驻"的上下文文件 —— Claude Code 的 `CLAUDE.md`(项目/全局)、文件式记忆的 `MEMORY.md` 索引、以及未来任何 always-loaded 的清单。

---

## 0. 一句话

**常驻上下文要么是"代码/文档推不出的不变量",要么是"打开详情的钩子"——其它一切都是税。**
治理不是删除信息,而是把信息搬回它该在的地方(详情文件 / 代码 / docs),让常驻层只剩信号。

---

## 1. 两种病征

### 病征 A:记忆索引漂移(index drift / 索引臃肿)

`MEMORY.md` 的设计契约是**一行一条记忆,只给钩子**(title + 一句"何时该打开它")。
但每次有东西 ship,人/agent figure 往行尾**追加**状态(PR 号、commit hash、日期、子项进度),日积月累,一行"索引"膨胀成一整篇变更日志。

> 实测最坏一行 **2,220 字 ≈ 700 token**,而它对应的详情文件里这些信息**本来就有、而且更全**。

**判别式**:索引行里出现 commit hash / PR 号 / "✅ DONE 日期" / 多级子项枚举 → 它已经不是钩子,是日志。

### 病征 B:CLAUDE.md 臃肿(spec bloat)

项目 `CLAUDE.md` 每条消息都加载、永不卸载,是你**唯一完全可控**的最贵文件。它会吸入两类不该在的内容:

1. **可从代码推出的复刻** —— HTTP 路由表、DB 表名枚举、接口 verb 列表、struct 树。这些会和代码漂移,且 grep 一下就有。
2. **通用而非项目专属的内容** —— 泛化的编码纪律,属于全局配置不属于某个项目。

**经验基准**:结构良好的 `CLAUDE.md` 约 300–600 token;超过 ~2,000 token 多半在存"任务状态/可推导文档"。

---

## 2. 为什么贵(两重成本)

1. **常驻税**:always-loaded 内容占满上下文窗口,每条消息都计入。即便有 prefix cache 省钱,窗口占用与首消息 cache-write 成本仍在。
2. **命中率稀释**(更隐蔽):当每行索引都是 700-token 的日志,相关性匹配在噪声里游泳。**瘦身既省 token 又提命中率**,二者同向。

---

## 3. 核心原则

| 内容类型 | 归宿 |
|---|---|
| 代码/意图**推不出**的不变量(复现性红线、nil 安全、角色门控、效价语义) | **留**在 CLAUDE.md |
| 代码里 grep 得到的(路由、表名、接口签名) | **换指针**:"在哪 + 非显然注释" |
| docs/ 里已有的设计规格 | **换指针**指向那篇 doc |
| **通用**行为纪律(非本项目专属) | 移到**全局** CLAUDE.md(单源),项目留一行指针 |
| 某条记忆的当前状态(PR/commit/日期/结论) | **只进详情文件**,索引行只留主题钩子 |

> 权威落点判据见记忆 `feedback_where_to_put_behavioral_rules`:①治召回的元规则必须放永远加载处 ②通用→全局 / 专属→项目 ③单源律(只一处权威副本,别处仅指针)。

---

## 4. 流程:测量 → 分类 → 安全搬家 → 知止

### 步骤 1 —— 先测量,别先压缩

没有数字就不知道瓶颈在哪,盲目上压缩工具是反模式。

- Claude Code 内:`/context` 精确列出 token 花在 system prompt / tools / memory / skills / history。
- 文件粗估(CJK 约 1 tok/字,latin 约 0.28 tok/字):

```python
import re
def est_tokens(path):
    t = open(path, encoding='utf-8').read()
    cjk = len(re.findall(r'[一-鿿]', t))
    return int(cjk * 1.0 + (len(t) - cjk) * 0.28)
```

- 找膨胀行:把索引里 **> ~250 字** 的行按长度排序,它们就是收益集中地(本次 top-8 行占了 54% 体积)。

### 步骤 2 —— 分类(对 CLAUDE.md)

逐段问一句:**"这段能从代码或某篇 docs 推出来吗?"**

- 能 → 换成"代码在哪 + 只留非显然注释"的指针。
- 不能(是不变量/意图)→ 留,一字不动。
- 拿不准 → 当作"留",保守优先。

### 步骤 3 —— 安全搬家(对 MEMORY.md 索引)—— **本流程的核心**

> 黄金规则:**搬家,不删除。** 信息从索引移走前,必须确认它已在详情文件里。

对每个膨胀的索引行:

1. **读它对应的详情文件**,核对索引行里的每条状态(PR/commit/日期/结论)是否都在详情里。
2. 详情**已覆盖** → 把索引行压成钩子(主题 + 一句"何时打开" + 必要的 load-bearing 信号)。
3. 详情**缺** → **先把缺的回填进详情**,再瘦身索引。
4. **未验证的行一字不动**(批次外的行保持 byte-identical,降低风险面)。

实战中 14/14 个详情文件都已完整覆盖索引行——因为详情文件本就是权威源,索引只是它被反复追加的摘要。但**这要逐个验证才能断言**,不能假设。

### 步骤 4 —— 知止(diminishing returns)

当索引行降到 ~150–200 字、不再含 commit/PR 日志时,就到了合理区间。继续从 200 抠到 150 字只省个位数百 token,边际极低。**最贵的不变量段(复现性红线等)风险高、收益低,保守保留。**

---

## 5. Before / After(真实样本)

### 索引行(MEMORY.md)

```
旧(~700 tok):
- [WS protocol freeze + Phase 7/8 done](project_ws_protocol_freeze.md) — docs/saas-ws-protocol-v1.md
  frozen + Phase 7 + Phase 8 全完结。Redis 已移除…agent 端 60s delta_report…①③+②(over+under)
  全修…PR #3 merge cac6b5e…[2200 字变更日志]

新(~30 tok):
- [WS 协议冻结 + Phase 7/8](project_ws_protocol_freeze.md) — SaaS↔Agent WS 协议契约 + Phase 7/8
  全完结;wire/agentauth/wshub/agent 实现、delta_report 对账、testnet e2e 三层全绿与坑详情见文件
```

钩子告诉你**何时该打开它**,而不是把它整本塞进每条消息。

### 规格段(CLAUDE.md)

```
旧:整张 HTTP 路由表(~450 tok,= router 代码的复刻,会漂移)

新(~120 tok):指向 internal/api + Tier-2 handlers,只保留"代码里不显然"的角色门控:
- promote/retire = admin only(operator 排除)
- data import = AppRole=saas + admin
- fleet: start/stop/deploy = operator;kill = admin
```

载得起的(代码 grep 不到的)留下,代码里有的换指针。

---

## 6. 实战成绩(本次,供量级参考)

| 文件 | 起点 | 治理后 | 省 |
|---|---|---|---|
| MEMORY.md 索引 | ~5,726 tok | ~3,059 | −47% |
| project CLAUDE.md | ~4,925 tok | ~3,956 | −20% |
| **常驻合计/消息** | **~10,958** | **~7,322** | **−33%** |

平均索引行 357 → 185 字;最长行 2,220 → ~235 字;**零有损压缩、零外部依赖、零信息损失**。
附带还修出一处事实漂移(CLAUDE.md 写 "prod uses Atlas",实际代码是 Goose)——**清理时顺手就能抓到陈旧规格**,这是瘦身的副产品价值。

---

## 7. 长期纪律(防再次漂移)

1. **更新明细的同一轮更新索引行**。半更新的索引行(同一行里既"shipped"又"remaining")就是漂移信号 —— 去看明细。(权威副本在全局 `~/.claude/CLAUDE.md`「Memory hygiene」段。)
2. **状态进详情,不进索引**。索引行是稳定的主题钩子(几乎不变);PR/commit/日期/子项进度只进详情文件。
3. **单源律**:任何事实只有一处权威副本,别处仅指针。CLAUDE.md 复述 docs/ 或代码 = 制造未来的漂移源。
4. **bootstrap 陷阱**:治召回/治漂移的**元规则**本身不能只存在靠召回的明细里,否则会和它要修的 bug 一起失效 —— 必须放永远加载的 CLAUDE.md。
5. **报状态先读明细 + 决策文档,别引用索引行**。索引只说"哪条相关",不说它的当前状态;代码答"建没建",决策文档答"还想不想建"(未实现 ≠ 活跃 backlog,可能是已否决)。

---

## 8. 反模式(别做)

- ❌ **没 `/context` 测量就上压缩代理**。先看清谁在吃 token。
- ❌ **对复现性命根子的仓库用有损压缩**碰精确值(hash/ScoreTotal/ε)——见下方关联。最大、最安全的收益在精简常驻上下文,不在外部工具。
- ❌ **盲删索引行**而不先确认详情已覆盖 → 真会丢状态。
- ❌ **把通用纪律塞进每个项目的 CLAUDE.md** → N 份副本一起漂移。移到全局。
- ❌ **过度抠最贵的不变量段**省那几十 token,换来误删红线的风险。

---

## 9. 关联

- 全局 `~/.claude/CLAUDE.md`「Memory hygiene」段 —— 报状态看明细别看索引(权威副本)。
- 记忆 `feedback_where_to_put_behavioral_rules` —— 行为规则落点三轴 + 单源律。
- 记忆 `feedback_status_from_detail_not_index` —— 把已否决项当活跃 backlog 误报的 worked example。
- 为什么不用 headroom 这类**有损**压缩库:本仓 `bars_hash`/`fingerprint`/`ScoreTotal` ε=1e-4 要逐字节精确,有损压缩 tool 输出会污染"是否 bump fitness_version / hash 对没对上"这类闸门判断。无损路线(精简常驻上下文 + 检索类工具)才与本仓约束相容。
