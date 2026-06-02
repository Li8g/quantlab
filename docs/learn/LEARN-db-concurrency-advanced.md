# 数据库并发(进阶):提交缝补不上时,你还有四把工具

> 接 [`LEARN-db-concurrency-invariants.md`](./LEARN-db-concurrency-invariants.md)。
> 科普版的结论是:**单组唯一性,首选部分唯一索引——它在 COMMIT 那刻检查,没有缝。**
> 但现实里有三类问题唯一索引表达不了:配额求和、读改写、跨行一致性。
> 本文把科普版"只点名没展开"的四把工具补全,每把都配本项目的真实落点。

读完你能回答:**手里这个并发问题,该用哪把工具,代价是什么。**

---

## 0. 先把工具谱摆出来

科普版讲的是"把不变式写成一句 DB 约束"。但约束只能表达**静态的、结构性的**规则
(唯一、外键、CHECK)。一旦规则是**动态的**——"这一行现在的值依赖它之前的值"
或"这几行加起来不能超标"——约束就够不着,你得在**运行时**协调并发。

| 工具 | 适合的不变式形状 | 缝在哪关上 | 本项目落点 |
|---|---|---|---|
| **部分唯一索引** (科普版) | 至多/恰好一个满足条件的行 | INSERT/COMMIT 物理检查 | `uq_champion_active` |
| **条件 UPDATE / CAS** | "仅当当前状态是 X 才改成 Y" | `WHERE` 谓词 + `RowsAffected` | `Retire` 的 `RowsAffected==0` |
| **悲观锁** `FOR UPDATE` | 配额求和、读后必写、跨行 | 事务持锁期间别人排队 | (未用——用单 worker 回避) |
| **乐观锁** 版本号 | 读改写,冲突罕见 | 写回时比版本,输家重试 | (未用——`runtime_state` 是反例) |
| **SERIALIZABLE + 重试** | 跨行/聚合,无法单约束表达 | DB 检测序列化冲突,一方回滚 | (未用——成本与适用边界见 §5) |

注意最后三行"未用":这不是疏漏,是**本项目刻意用更便宜的手段绕开了它们**。
进阶的判断力恰恰在于——知道这些工具存在,也知道**什么时候不用它们更好**。

---

## 1. 最便宜的并发控制:不共享(share-nothing)

讲锁之前先讲怎么不用锁。本项目并发度最高的地方是 **engine 的 worker pool**
(GA 每代要评估上百个 gene,跑满 GOMAXPROCS),但它**一把数据库锁都没有**。

为什么?因为每个 worker 拿的是**自己的** `Adapter`:

```
NewAdapter(plan)            // 每个 worker 一个,互不共享
adapter.Reset(plan)         // 每次 Evaluate 前重置到干净态
adapter.Evaluate(gene)      // 纯内存、纯函数、无 I/O、无 goroutine
```

评估过程不碰任何共享可变状态——不读 DB、不写 DB、不共享累加器
(CLAUDE.md 明令:`Evaluate` 内不得起 goroutine,float 累加必须串行)。
**没有共享,就没有竞争,就不需要协调。** 这是最强的并发策略,代价是设计约束:
你得把状态切干净。

> 教训:看到"高并发"先别急着上锁。**先问能不能不共享。**
> 锁是共享不可避免时的退路,不是默认起手式。

那 DB 写在哪发生?在 worker pool **跑完之后**——`RunEpoch` 返回,主 goroutine
串行地把结果落库。把"并行计算"和"串行持久化"分开,是本项目躲开一大类并发 bug
的根本原因。

---

## 2. 条件 UPDATE / CAS:你其实已经在用乐观锁了

科普版的 D-1 讲的是 INSERT 竞态。但 `champion.go` 里还藏着一个**更新**竞态的解法,
而且它就是乐观锁的本质,只是没挂"乐观锁"这个名字。看 `Retire`:

```go
res := tx.Model(&store.ChampionHistory{}).
    Where("id = ?", history.ID).        // 还隐含:这一行 retired_at 仍是 NULL
    Updates(updates)                     // set retired_at = now
if res.RowsAffected == 0 {
    return fmt.Errorf("...update affected 0 rows (race?)")
}
```

两个管理员同时 Retire 同一个 champion:

| 时刻 | T1 | T2 |
|---|---|---|
| t1 | 读到 history.RetiredAt = NULL | 读到 history.RetiredAt = NULL |
| t2 | `UPDATE ... WHERE retired_at IS NULL` → 1 行 | |
| t3 | COMMIT ✅ | `UPDATE ... WHERE retired_at IS NULL` → **0 行** |
| t4 | | `RowsAffected==0` → 报错,不假装成功 |

关键:**把"我读到的前提"塞进 UPDATE 的 `WHERE` 里**,让数据库在写的那一刻
重新验证前提还成不成立。`RowsAffected` 告诉你前提是否被人改掉了。这就是
**Compare-And-Set (CAS)**,也是乐观锁去掉版本号后的最小形态。

> 乐观锁 = CAS 的一般化。把"凭据"从"状态值本身"换成一个**单调递增的 version 列**,
> 就能处理"值可能变回原样"(ABA)的情况。本项目 Retire 的状态是单向的
> (NULL→时间戳,不会变回去),所以不需要 version 列,直接拿 `retired_at IS NULL`
> 当凭据就够。**够用就别加版本号。**

### 反例:`runtime_state` 的 last-write-wins

对照 `runtime_state.go` 的 `Upsert`——它**故意不做** CAS:

```go
r.db.Clauses(clause.OnConflict{ /* DO UPDATE 全字段覆盖 */ }).Create(&row)
```

每个 instance 一行,每个 Tick 无条件覆盖。这里**后写赢是正确的**:策略私有状态
的最新一次写就是真相,没有"丢失更新"问题(同一 instance 不会被两个 goroutine
并发 Tick)。**没有并发写入者时,乐观锁是纯开销。** 选 last-write-wins 是对的。

判断口诀:**会不会"丢失更新"?**——A 读、B 读、A 写、B 写,B 覆盖了 A。
只有当这个序列会导致错误结果时,才需要 CAS / version。

---

## 3. 悲观锁 `FOR UPDATE`:唯一索引表达不了配额求和

唯一索引只能管"行的存在性"。但有一类不变式是**聚合**的:

> "同一账户所有未平仓头寸的名义价值之和 ≤ 风险上限。"

这没法用唯一索引——它不是"至多一行",而是"这些行加起来不超标"。科普版的判断清单
第二条("总和/计数不超过上限")指的就是它。这时候 `SELECT ... FOR UPDATE` 上场:

```sql
BEGIN;
-- 锁住这个账户的所有相关行,直到 COMMIT。别的事务想锁同样的行就排队。
SELECT COALESCE(SUM(notional), 0) FROM positions
  WHERE account_id = $1 FOR UPDATE;
-- 现在"求和→判断→插入"这三步之间没有缝,因为竞争者被挡在锁外。
INSERT INTO positions (...) VALUES (...);
COMMIT;  -- 锁释放,排队的人进来,看到的是含你这笔的新总和。
```

`FOR UPDATE` 把科普版那条"先查后写"的提交缝,用**持锁时长**填上了:从 SELECT 到
COMMIT 期间,锁住的行别人改不了也读不走(读走改版叫 `FOR SHARE`)。

### 为什么本项目没用它:单 worker 是更便宜的串行化

`import_jobs` 的不变式是"同 (symbol,interval) 至多一个进行中导入"。本项目用了**两层**:

1. 部分唯一索引 `WHERE status IN (queued,running)` —— 兜结构性唯一(科普版 §6);
2. **单个 worker 串行消费** —— 同一时刻根本只有一个 goroutine 在处理导入。

第 2 层等于"用架构把并发降到 1",这跟 `FOR UPDATE` 的效果一样(串行化访问),
但**没有锁竞争、没有死锁风险、没有事务持锁拖长**。代价是吞吐:单 worker 不能横向扩。
对"导入历史 K 线"这种低频、可排队的任务,这是完美权衡。

> 选型轴:**`FOR UPDATE` 是"让多个并发者排队访问同一资源";单 worker 是"压根
> 不让它们并发"。** 能接受串行吞吐时,后者运维上简单得多。

### 用 `FOR UPDATE` 必须记住的两件事

1. **死锁来自加锁顺序不一致。** T1 锁行 A 再锁 B,T2 锁 B 再锁 A → 互等死锁,
   Postgres 会 abort 一方(`40P01 deadlock_detected`)。规矩:**所有事务按同一
   确定顺序加锁**(比如永远按主键升序)。
2. **锁的是行,不是"不存在的行"。** `FOR UPDATE` 锁不住还没 INSERT 的行——
   两个事务都想"插入第一笔"时,它们没有共同的行可锁,缝又回来了。这种"插入竞态"
   还得靠唯一索引(回到科普版 §5),`FOR UPDATE` 只解"行已存在、要读后改"的场景。

---

## 4. 重连重试 = 故意的重复执行 → 幂等键是配套刹车

科普版讲的缝是"两个请求插队"。重试制造的是**另一种**重复:**同一个**意图被发了两遍。
本项目 agent 侧把这点暴露得淋漓尽致。

### 重连重试的现实

agent 与 server 的 WS 连接会断。重连用指数退避(`config.go` §4.5:
`500ms→1→2→4→8→16→32→60s` 封顶),断线期间 `deltaBuffer` 跨重连存活,重连后**重发**
缓冲里的报告(at-least-once 投递)。Binance 下单失败也会重试(`binance/client.go`
按 `Retry-After` 退避)。

**at-least-once 的铁律:能重发,就一定会有重复。** 网络超时尤其阴险——你发了下单请求,
没等到响应就超时了,但**交易所可能已经成交**。盲目重试 = 下两次单 = 真金白银的损失。

### 配套刹车:幂等键 + ON CONFLICT

`idempotency_sqlite.go` 就是这把刹车,而且它的实现**正是科普版那套"下沉到 DB 约束"
的思想**——只不过这次约束是 `PRIMARY KEY`:

```sql
CREATE TABLE idempotency (
  client_order_id   TEXT PRIMARY KEY,   -- 幂等键:同一意图永远同一个 id
  exchange_order_id TEXT,
  status            TEXT NOT NULL,
  ...
);
```

```go
// 下单前先 Put,记录"我打算下这一单"。
INSERT INTO idempotency (client_order_id, ...) VALUES (?, ...)
ON CONFLICT(client_order_id) DO UPDATE SET status = excluded.status, ...
```

流程是"**先查幂等键 → 没见过才真正提交 → 记录结果**"。重试打到同一个
`client_order_id` 时,`Get` 命中已有记录,直接返回上次结果,**不重复下单**。
`PRIMARY KEY` 保证即使两个 goroutine 同时 `Put` 同一个 id,也只有一行——
**和科普版唯一索引接住 INSERT 竞态输家,是同一个机制。**

> 把幂等键想成"重试的提交缝补丁":重试本身在"查"和"提交副作用"之间也有缝
> (查的时候没成交、提交的时候成交了)。幂等键让这条缝里即使插进来一次重复,
> 结果也和只执行一次完全一样。**幂等是重试的前提,不是可选项。**

注意它用了 `WAL` + `busy_timeout=5000` + 进程级 `sync.Mutex`(注释 §8.3):
sqlite 单写多读,用超时和锁把偶发的 `SQLITE_BUSY` 吸收掉——这是**单机版**的并发协调,
和 Postgres 那套是同构的,只是工具不同。

---

## 5. SERIALIZABLE + 重试:最后的、最贵的兜底

前面四把工具都没法表达的不变式——典型是"**跨多行的聚合约束 + 复杂判断**",
比如"全组合的风险价值(VaR)在任意时刻满足某联合条件",涉及多张表多行、还做运算——
这时唯一索引表达不了,`FOR UPDATE` 锁的行集都难界定。终极武器是把隔离级别拉到
`SERIALIZABLE`,让数据库保证"并发结果等价于某个串行顺序"。

代价是它**不再默默成功**:Postgres 用 SSI(可串行化快照隔离)检测到事务间的危险依赖,
会**主动 abort** 一方,报 `40001 serialization_failure`。所以用它**必须配重试循环**:

```go
for attempt := 0; attempt < maxRetries; attempt++ {
    err := db.Transaction(func(tx *gorm.DB) error {
        tx.Exec("SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")
        // ... 复杂的读 + 判断 + 写 ...
    })
    if isSerializationFailure(err) { // 40001
        backoff(attempt)             // 退避后整笔重做
        continue
    }
    return err
}
```

三个隐藏要求,少一个就翻车:

1. **整笔事务必须可重做** —— 重试是把整个事务从头再跑。事务里若夹了**外部副作用**
   (发 HTTP、下单、写文件),重做就会重复触发 → 回到 §4 的幂等问题。
   所以重试事务里**只能有 DB 操作**,副作用挪到事务外、并加幂等键。
2. **重试预算有限** —— 高冲突下会反复 abort,得设上限 + 退避,否则活锁。
3. **它惩罚所有走这条路径的事务** —— 科普版 §4 说过,用全局串行化解一个局部唯一性
   是杀鸡用牛刀。**只在"真的需要跨行可串行"时才上 SERIALIZABLE**,
   能用唯一索引 / CAS / `FOR UPDATE` 解决的,都别升到这一级。

---

## 6. 选型决策树

```
要保护的不变式是什么形状?
│
├─ "至多/恰好一个满足条件的行"
│     → 部分唯一索引 (科普版 §5)。插入竞态首选,无缝、无锁、最便宜。
│
├─ "仅当当前状态是 X 才能改成 Y"(单向状态机 / 读改写)
│     → 条件 UPDATE 把前提塞进 WHERE,查 RowsAffected (§2)。
│       值可能 ABA 时,升级为 version 列乐观锁。
│
├─ "这些行加起来不能超标"(配额 / 求和 / 库存)
│     → 能把并发降到 1 吗?能 → 单 worker 串行 (§3,本项目 import_jobs)。
│       必须并发 → SELECT ... FOR UPDATE 锁住相关行,注意加锁顺序防死锁。
│
├─ "同一意图可能被重发"(重连 / 重试 / 双击 / 超时)
│     → 幂等键 + PRIMARY KEY/唯一约束 + ON CONFLICT (§4,本项目 client_order_id)。
│       副作用(下单/发消息)一律走幂等键,别裸重试。
│
├─ "跨多行多表的聚合 + 复杂判断,上面都表达不了"
│     → SERIALIZABLE + 重试循环 (§5)。最贵,事务内禁副作用,设重试上限。
│
└─ "其实没有共享可变状态"
      → 恭喜,share-nothing (§1,本项目 worker pool)。不需要任何上面的工具。
```

---

## 7. 一页纸记忆

- 科普版的话还成立:**只要"检查"和"写入"是两步,中间就有缝。**
- 进阶版补一句:**缝有两种来源——"别人插队"(并发请求)和"自己重发"(重试)。**
  唯一索引/锁/CAS 治前者,幂等键治后者。
- 工具越往下越贵(索引 < CAS < 行锁 < 单 worker 的吞吐代价 < SERIALIZABLE)。
  **永远选能解决问题的最便宜那把。**
- 最便宜的永远是**不共享**——上锁前先问能不能把状态切干净 (§1)。
- 凡是带**外部副作用**的重试,幂等键不是优化,是正确性前提 (§4)。
- 本项目的现状本身就是一份选型范本:
  - champion 插入竞态 → 部分唯一索引
  - champion 退役竞态 → 条件 UPDATE + RowsAffected
  - import 配额唯一 → 单 worker + 部分唯一索引(没上 `FOR UPDATE`)
  - 下单重试去重 → client_order_id 幂等表
  - GA 评估高并发 → share-nothing,零 DB 锁
  - SERIALIZABLE → 至今没用上,因为前面几把已经够。**这是好事,不是缺失。**
