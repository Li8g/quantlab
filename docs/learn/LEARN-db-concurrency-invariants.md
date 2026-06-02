    ---
    # 数据库并发科普:事务、不变式与"提交缝"

    > 一句话:**事务管的是"原子",不是"独占";跨行不变式必须靠数据库约束兜底,
    > 应用层的"先查后写"在并发下永远有一条提交缝。**

    本文用本项目真实发生过的一个 bug(code-review D-1,champion 提升竞态)讲清三件
    常被混为一谈的事。读完你能判断:什么时候应用层的 `if` 判断够用,什么时候必须
    让数据库出手。

    ---

    ## 1. 三个被混淆的概念

    | 概念 | 它保证什么 | 它不保证什么 |
    |---|---|---|
    | **原子性 (Atomicity)** | 事务里的多条语句要么全成功、要么全回滚 | 不保证别的事务看不见你 / 你看不见别人 |
    | **隔离性 (Isolation)** | 并发事务"互相干扰的程度",由**隔离级别**决定 | 默认级别下并不等于"排队执行" |
    | **串行化 (Serializable)** | 并发结果等同于某种串行顺序 | 需要**显式**开最高隔离级,默认不给 |

    最常见的误区:**"我包在事务里了,所以并发安全。"**
    错。事务默认只给你原子性;并发安全要看隔离级别,而大多数数据库(含本项目的
    Postgres + GORM)默认是 **READ COMMITTED** —— 它只保证"你读到的都是已提交的数据",
    不保证"别人没在你眼皮底下插队"。

    ---

    ## 2. 什么是"先查后写"(check-then-act)

    本项目的不变式:**每个 `(strategy_id, pair)` 同时至多一个 active champion**
    (active = `retired_at IS NULL`)。

    最初的实现(`internal/repository/champion.go` 的 `Promote`)是这样的:

        ① SELECT count(*) WHERE retired_at IS NULL   → activeOther
        ② if activeOther > 0 { 拒绝 }
        ③ INSERT champion_history (retired_at = NULL)

    "先读一个计数,根据计数决定能不能写" —— 这就是 **check-then-act**。
    单线程下完全正确;并发下有一条致命的缝。

    ---

    ## 3. 提交缝:两个并发请求如何双双"通过检查"

    两个管理员几乎同时提升同一 `(strategy, pair)` 的不同 challenger(A、B),
    而当前还没有 active champion:

    | 时刻 | 事务 T1 (promote A) | 事务 T2 (promote B) |
    |---|---|---|
    | t1 | `count → 0` | |
    | t2 | | `count → 0`(看不见 T1 **未提交**的 INSERT) |
    | t3 | `activeOther=0` → 通过 | `activeOther=0` → 通过 |
    | t4 | `INSERT A (retired_at=NULL)` | `INSERT B (retired_at=NULL)` |
    | t5 | `COMMIT` ✅ | `COMMIT` ✅ |

    两个都认为"我是第一个",都提交成功 → **两个 active champion**。
    不变式被静默打破,**没有任何报错**。

    关键在 t2:READ COMMITTED 下,T2 读不到 T1 在 t4 才写、t5 才提交的行。
    "检查"和"写入"之间的时间窗,就是**提交缝 (commit gap)**。
    任何"先 SELECT 判断、再 INSERT/UPDATE"的模式,在并发下都有这条缝。

    ---

    ## 4. 为什么换隔离级别不是首选

    理论上 `SERIALIZABLE` 能检测到这种冲突并让一方回滚。但它:
    - 要**显式**设置(默认不是);
    - 对所有走这条路径的事务加重负担;
    - 把"一个跨行唯一性"问题用"全局串行化"去解,代价过大。

    对**单行 / 单组的唯一性约束**,有更精准、更便宜的工具:数据库约束本身。

    ---

    ## 5. 正确解法:把不变式下沉到数据库
唯一索引在**物理层**强制唯一性——它在 INSERT/COMMIT 那一刻检查,**没有缝**。
    两个并发 INSERT 撞同一个 key,数据库保证只有一个成功,另一个报
    `23505 duplicate key`。

    本项目的"至多一个 active"是个**条件**唯一性(只对 active 行唯一),
    所以用 Postgres 的**部分唯一索引**:

        CREATE UNIQUE INDEX IF NOT EXISTS uq_champion_active
        ON champion_history (strategy_id, pair)
        WHERE retired_at IS NULL AND deleted_at IS NULL;

    谓词 `WHERE retired_at IS NULL AND deleted_at IS NULL` 必须和应用层"active"的
    定义**完全一致**(注意 GORM 软删除会自动加 `deleted_at IS NULL`),否则数据库的
    "active"和代码的"active"对不上,会出现一边放行一边拦截的诡异现象。

    应用层的 count 检查**不删除**——它降级为"第一道防线",给用户友好的 409;
    数据库唯一索引是"最终防线",接住竞态的输家:

        if err := tx.Create(&history).Error; err != nil {
            if isUniqueViolation(err) {        // 撞 uq_champion_active
                return api.ErrActiveChampionExists
            }
            return fmt.Errorf("create champion_history: %w", err)
        }

    > 两道防线分工:**应用层管"友好提示",数据库管"绝对正确"。**

    ---

    ## 6. 本项目的三处对照

    | 表 | 不变式 | 兜底 |
    |---|---|---|
    | `strategy_instances` | 同 (user,strategy,pair,account) 至多一个非 retired 实例 | 部分唯一索引 `WHERE status !=
    'retired'` |
    | `import_jobs` | 同 (symbol,interval) 至多一个进行中导入 | 部分唯一索引 `WHERE status IN (queued,running)` |
    | `champion_history` | 同 (strategy,pair) 至多一个 active champion | 部分唯一索引 `WHERE retired_at IS NULL AND
    deleted_at IS NULL`(D-1 补) |

    D-1 的本质:前两张表早就这么做了,champion 这张**漏了**。这也提示一个
    review 技巧——**看同类不变式有没有一致地兜底**,落单的那个往往就是 bug。

    ### 附:加唯一索引的运维前置
    `CREATE UNIQUE INDEX` 会因**已存在的重复数据**而失败。所以上线前要先体检:
    独立脚本 `scripts/preflight_champion_dup_check.sql` 查"已经有 >1 active 的组",
    启动代码里 `assertNoDuplicateActiveChampions` 把 Postgres 那句晦涩的索引创建失败
    翻译成"哪个 (strategy,pair) 有几个 active、去把多余的 retire 掉"的可操作报错。

    ---

    ## 7. 判断清单:什么时候必须上 DB 约束

    出现下面任一情况,**别只靠应用层 if**:

    - [ ] 规则形如"**至多/恰好一个**满足条件的行"(唯一性 / 互斥)
    - [ ] 规则形如"**总和 / 计数**不超过上限"(配额、库存)
    - [ ] 实现里出现 **SELECT 判断 → 再 INSERT/UPDATE** 的两步
    - [ ] 这条路径**可能被并发**触发(多用户、多 worker、重试、双击)

    对前两类的"单行/单组唯一",首选**唯一索引 / 部分唯一索引 / EXCLUDE 约束**;
    对"先查后写"且无法用唯一索引表达的(如配额求和),用 `SELECT ... FOR UPDATE`
    行锁、或在更高隔离级 + 重试。

    > 记住那条提交缝:**只要"检查"和"写入"是两步,中间就有别人插队的空间。
    > 把不变式写成数据库一句约束,缝就没了。**

    ---
    补充一句给你判断:这份是科普向,刻意省略了 SELECT FOR UPDATE、乐观锁版本号、
    SERIALIZABLE 重试等进阶手段的细节,只点到名字。如果你想要"进阶版"把这些展开(配本
    项目的 worker pool / 重连重试场景),那是另一篇,可单独再写。

    → 进阶版见 [`LEARN-db-concurrency-advanced.md`](./LEARN-db-concurrency-advanced.md)
