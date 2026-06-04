# Schema 迁移入门:我们为什么要上 Goose,到底迁的是什么

> 这是一篇**给将来接手"prod schema 迁移"开发任务的搭档**的背景讲解。
> 规范版(决策表、实现顺序、文件清单)在 [`../saas-schema-migration-draft.md`](../saas-schema-migration-draft.md);
> 本文只讲**前因后果**——为什么需要它、迁的是哪个库、为什么是 goose 而不是别的。

读完你能回答:**我接的这个任务到底在解决什么问题,为什么现在的做法不够,goose 进来之后世界变成什么样。**

---

## 0. 一句话背景

> 这个项目所有数据库表,**至今全靠 GORM `AutoMigrate` 在程序启动时自动建/改**。
> 这在开发机上很爽,但**生产环境(`app_role=saas`)不能这么干**。
> 你的任务,就是给生产环境补上一套**正规的、可控的、可回滚的**改表机制——这套机制叫 migration,我们选的工具叫 **goose**。

下面把这句话拆开讲。

---

## 1. 先认识现状:AutoMigrate 是什么,它怎么工作

打开 `internal/saas/store/db.go`,核心就一行:

```go
db.AutoMigrate(AllModels()...)   // AllModels() 返回 20 个 GORM struct
```

GORM 的 `AutoMigrate` 会**反射**你的 Go struct(比如 `StrategyInstance`、`SpotExecution`),
看数据库里对应的表长什么样,然后**自动补差**:表不存在就 `CREATE TABLE`,
struct 比表多个字段就 `ADD COLUMN`。**你改 struct、重启程序,列就自动有了。**

对开发机来说这是天堂:加个字段不用写任何 SQL,跑起来就行。

但请记住它的工作方式——**它永远只"加",而且它的"真相源"是 Go struct**。这两点后面全是坑。

> 一个容易混淆的点:`internal/migrate/` 这个目录**不是** schema 迁移。
> 它是结果包 JSON 的应用层数据回填骨架(Filter+Transform),改的是行里的内容,不动表结构。别被名字骗了。

---

## 2. 为什么开发机能用、生产不能用

`db.go` 顶部有一条注释叫"铁律 4",大意是:**AutoMigrate 只配 dev/lab 用;生产必须跑正规迁移。**
这条规矩**写了,但从来没实现**——仓库里没有任何迁移文件、没有迁移工具、没有 CI。

为什么生产不能用 AutoMigrate?因为它有几个对生产致命的盲区:

| AutoMigrate 的行为 | 在生产为什么是灾难 |
|---|---|
| **只加列,从不改类型/删列/回填** | 任何"改"和"删"它都做不了;加 NOT NULL 列不会给老数据填默认值 |
| **没有版本概念** | 无法保证"按顺序、且只跑一次";无法知道"这个库跑到第几版了" |
| **不可回滚、不可评审** | 生产改表必须能 review diff、能回退、留痕——AutoMigrate 全没有 |
| **真相源是 struct,不是受控脚本** | 改表的"权力"散落在每个改 struct 的人手里,没有闸门 |
| **并发不安全** | 本项目真踩过:多个进程同时 AutoMigrate 撞 `ALTER ADD trade_id` 竞态 |

一句话:**AutoMigrate 适合"随便漂、随时能重建"的环境;生产数据库是"碰一下都要小心、不能丢数据"的环境。** 两者诉求相反。

---

## 3. 那"正规迁移"是个什么东西

一套 migration 机制,本质就三件事:

1. **一串有编号、有顺序的 SQL 文件**,每个文件描述"这一步把库改成什么样":
   ```
   00001_baseline.sql            ← 第 0 版:把当前所有表一次性建出来
   00002_add_ledger_columns.sql  ← 第 1 步改动:加三列
   00003_xxx.sql                 ← 以后每次改表,追加一个文件
   ```
2. **一张记账表**(goose 叫 `goose_db_version`),记录"这个库已经跑到第几号文件了"。
   工具每次启动只跑"还没跑过的"文件,**有序、且每个只跑一次**。
3. **可选的回滚**:每个文件可以写一段 `Down`,需要时往回退一步。

对比一下你就懂区别了:

| | AutoMigrate | Migration(goose) |
|---|---|---|
| 真相源 | Go struct | 受控的 SQL 文件 |
| 顺序保证 | 无 | 有(编号 + 记账表) |
| 回滚 | 不能 | 能 |
| 评审 | 改 struct 谁都能改 | 改表=提一个 .sql,可 review |
| 适合 | 开发机 | 生产 / 任何要和生产一致的环境 |

---

## 4. 我们到底迁的是哪个库、哪些东西

**库**:一个 PostgreSQL,但装了 **TimescaleDB 扩展**(时序数据库)。这个细节后面是选型的关键。

**要管的表**:`AllModels()` 里的 20 张,分两层——

- **Tier 1(进化引擎)**:`evolution_tasks`、`gene_records`、`klines`、`champion_histories`…
- **Tier 2(实盘/SaaS)**:`strategy_instances`、`portfolio_states`、`spot_executions`、`trade_records`…

**直接触发这个任务的"债"**:最近的账本修复(PR #3)往三张表加了三列,
开发机靠 AutoMigrate 自动有了,但**生产没有任何脚本能把这三列加上去**:

| 列 | 表 | 干什么用的 |
|---|---|---|
| `funded_at_ms` | `strategy_instances` | 创世注资时间戳;在它为 NULL 期间跳过对账,免得 $0 账本把真实持仓全误判成漂移 |
| `last_applied_exec_id` | `portfolio_states` | 账本回写水位线;保证每笔成交只折算进余额一次 |
| `trade_id` | `spot_executions` | 交易所全局唯一成交 ID;一笔市价单多档成交共享时间戳,去重只能靠它 |

但注意:**这三列只是导火索,不是任务本身。** 任务本身是"生产根本没有迁移机制",
补好机制之后,这三列只是顺手写成第一个增量迁移。

**还有一个隐藏难点**——`db.go` 里除了 AutoMigrate,还**手写了一堆 SQL**:

```
CREATE EXTENSION timescaledb
create_hypertable('klines', ...)            ← 把普通表变成时序"超表"
create_hypertable('portfolio_states', ...)
3 个 partial unique index (WHERE status != 'retired' 之类)
```

> **什么是 hypertable?** TimescaleDB 把一张大表按时间自动切成很多"块(chunk)"分开存,
> 查询和写入只碰相关的块,大数据量下更快。代价是:对它做 `ALTER` 会**广播到所有块**,
> 块多时会慢、会持锁。这点迁移时要特别小心。

这些是 **GORM 的 struct tag 表达不了的**,所以现在它们以"命令式 SQL"的形式硬写在 db.go 里。
**记住这个事实——它是下一节"为什么选 goose 不选 Atlas"的全部理由。**

---

## 5. 为什么是 goose,而不是 Atlas

迁移工具分两大流派:

- **声明式(Atlas 为代表)**:你写一份"我想要的最终 schema 长这样",工具自己 diff 当前库、**自动生成**迁移。卖点是"几乎不用手写 SQL"。
- **版本化 SQL(goose / golang-migrate 为代表)**:你**手写**那串有编号的 .sql,工具只负责"按号有序跑、记账"。

Atlas 听起来更高级——能自动生成,谁不想要?但**它的杀手锏在我们这个项目里失效了**,原因就是上一节那个事实:

> 我们的 schema **不是纯 GORM struct 能描述的**——它是 "struct + 一堆手写的 TimescaleDB/partial-index SQL"。
> 声明式工具**推导不出** hypertable、partial index 这些东西。
> 结果:用 Atlas 要么得**额外维护一份它能读懂的 schema 描述(HCL)**,跟 struct 并行——于是你有了**两份真相源**,正是要避免的;
> 要么 Atlas 每次 diff 都想"删掉"它看不懂的 hypertable 配置——**危险**。

换句话说:**我们项目"最难的部分"恰恰是手写命令式 SQL,这正是版本化 SQL 流派的主场,而不是声明式的主场。** Atlas 的超能力被中和了,只剩它较重的那一面(额外二进制、HCL 配置、diff 用的影子库)。

goose 在版本化阵营里又比 golang-migrate 略胜一筹,因为它能用 Go 的 `//go:embed`
把迁移 SQL **打包进 `cmd/saas` 二进制**,部署时不依赖外部命令行工具——一个二进制就自带了所有迁移。

> 教训:**选工具先看自己的现实约束,别看工具的宣传亮点。**
> "能自动生成迁移"很诱人,但当你的 schema 有一半是工具看不懂的 TimescaleDB DDL 时,这个亮点对你毫无意义,反而拖你下水。

**Atlas 被完全排除**(连"只用它做 CI 检查"也不要——同样的理由:它做检查也需要一份它能读懂的 schema 描述,又把双真相源请回来了)。

---

## 6. 配角:还有三样东西和 goose 一起来

光有 goose 不够。生产改表是高风险动作,我们配了三道护栏,理解它们各自防什么:

### 6.1 Drift 测试(承重墙)——防"忘了写迁移"

这是最重要的配角。我们的安排是:**开发机继续用 AutoMigrate(快),生产用 goose(稳)。**
但这是"双轨",有个致命陷阱:

> 开发者加了个 struct 字段 → 本地 AutoMigrate 自动给列 → 测试全绿、心安理得提交 →
> **但他忘了写对应的 goose 迁移** → 生产上线即炸。

所以我们加一个 Go 集成测试当闸门,逻辑很朴素:

```
起两个空库 →
  库 A: 跑 goose 的所有迁移
  库 B: 跑 AutoMigrate(structs) + db.go 的手写 SQL
→ pg_dump 导出两边的 schema,diff 必须为空。非空 = 有人改了 struct 没写迁移 = CI 红。
```

它不需要 Atlas、零新依赖,而且一箭双雕:既验证"第 0 版 baseline 写得对不对",又持续盯防双轨漂移。

### 6.2 Squawk(lint)——防"迁移本身写得危险"

Squawk 是个 Postgres 专用的 SQL linter,在 CI 里**静态检查**迁移文件,抓经典的危险写法:
非并发建索引会锁表、不带默认值加 NOT NULL 会重写大表、`DROP COLUMN` 丢数据……
**它在 PR 阶段就拦下来,而不是等部署到生产才发现。** 引入成本几乎为零,拦一次锁表事故的价值就回本了,所以它和 goose 同批落地、不当"以后再说"。

> 注意 Squawk 不懂 TimescaleDB(把 hypertable 当普通表),所以"块广播成本"这类它看不见——那部分靠人工 checklist 兜。

### 6.3 `lock_timeout`(执行护栏)——防"迁移堵死交易流量"

生产跑迁移前设 `lock_timeout=3s`:迁移**等锁**等不到就快速失败,而不是堵在那里、顺带把实盘交易也堵死。
注意它限的是"等锁",不是"运行多久";所以我们**不**给迁移设一个紧的 `statement_timeout`(那会把合法的慢迁移砍到半截)。

---

## 7. 你需要记住的核心心智模型

接手开发前,把这张图刻进脑子:

```
        DDL 的权威                     ORM 映射的权威
     ┌──────────────┐               ┌──────────────┐
     │ goose 迁移文件 │               │  GORM struct  │
     └───────┬──────┘               └──────┬───────┘
             │                              │
             └──────────► Drift 测试 ◄───────┘
                      (CI 里把两者钉死对齐)
```

- **不是"一个 source of truth",是"两个 + 一道粘合闸门"**:迁移文件管 DDL,struct 管应用怎么读写,drift 测试保证两者一致。这就是为什么 drift 测试是必需品不是可选项。
- **AutoMigrate 从生产路径彻底退场**(不留"sanity check"残留——它没有只读模式,在生产跑它=偷偷改 schema)。**只有"允许漂、随时能重建"的开发者笔记本豁免**;paper-trading、回测集群这些"必须和生产一致"的环境,也走 goose。

---

## 8. 任务地图(你大概会按这个顺序做)

1. **引入 goose + 写 `00001_baseline`(把现状固化成第 0 版)+ 保真验证**——这一步最有风险,baseline 一旦撒谎后面全错,务必用 §6.1 那个 diff 验证等价。
2. **写 `00002`,把那三列 + 索引补上**(注意 `last_applied_exec_id`/`trade_id` 要给老行 `DEFAULT 0`)。
3. **生产启动路径切成 goose-only**,把 AutoMigrate 从生产移除。
4. **drift 测试 + Squawk + 最小 CI 同批落地**(本仓目前没有 CI,这一步会顺手建起来)。

细节、文件清单、决策依据见规范版 [`../saas-schema-migration-draft.md`](../saas-schema-migration-draft.md)。

---

## 9. 一句话总结

> 我们不是"嫌 AutoMigrate 不好用",而是**生产数据库需要一套可控、可回滚、可评审、和 struct 对得上的改表机制**。
> goose 提供这套机制;Atlas 因为读不懂我们的 TimescaleDB DDL 被排除;
> drift 测试防止"忘写迁移",Squawk 防止"写了危险迁移",`lock_timeout` 防止"迁移堵死交易"。
> 这四样一起,才让"以后做回测集群、paper-trading、生产同步"风险最低。
