# Prod Schema 迁移方案（draft，已决策 / 待实现）

> 状态：**方向已决策（2026-06-04），尚未实现**。本文最初的问题——
> **生产库（`app_role=saas`）的 schema 迁移用不用 Atlas、有没有更合适的替代？**——已有结论。
> 实现落地后另出 `-v1.md` 冻结版（沿用 `phase-5d-oos-draft.md → -v1.md` 的约定）。

## 决策摘要（DECIDED 2026-06-04）

| 层 | 决定 | 备注 |
|---|---|---|
| **Migration Framework** | **Goose** | 版本化 SQL；声明式被本仓 TimescaleDB/raw-DDL 现实中和（§3.1） |
| **Drift Guard** | **Go 集成测试**（`pg_dump` 比对 goose 库 vs AutoMigrate 库，非空即 fail） | 一等项；零依赖；既做 baseline 保真、又做持续 drift 闸门。承重墙：防双轨漂移 |
| **Apply 护栏（下游）** | `lock_timeout=3s`（设全局）；`statement_timeout` **不设紧全局值，按需 `SET LOCAL` 覆盖**；review checklist | checklist 显式认领 Squawk 盲区（hypertable chunk 广播 / 改名走 expand-contract / DROP 需工单） |
| **Migration Lint（上游）** | **Squawk —— 与 Goose 同批落地，不作未来扩展项** | 引入成本趋近零，拦一次错误迁移价值远超维护成本 |
| **Rejected** | Atlas Declarative、Atlas Lint、Eugene（暂缓） | Atlas 全程排除；Eugene 待真出锁事故再议 |

**落地耦合**：Squawk 立即启用 ⇒ 跑它的最小 CI 也随之落地。baseline + drift 测试 + Squawk 三者进**同一个 PR、同一道 CI gate**。

---

## 1. 问题陈述

### 1.1 现状

整个项目的 schema 至今**只靠 GORM `AutoMigrate`**（`internal/saas/store/db.go:72`）。`db.go` 的注释（铁律 4，第 26–29 行）写明：

- **dev / lab**：`AutoMigrate(AllModels()...)` —— 启动时反射 20 个 struct 自动建表/加列；
- **prod（saas）**：「the deploy must run Atlas migrations and AutoMigrate becomes a sanity check」。

但**这条 prod 路径从未落地**：仓库里没有 `atlas.hcl`、没有任何 `.sql` 迁移文件、`go.mod` 无 atlas 依赖、无 CI、无 Makefile migrate 目标。`internal/migrate/` 不是 schema 迁移，是结果包 JSON 的应用层数据回填骨架。

所以「prod 用 Atlas」目前是一句**未兑现的设计意图**。

### 1.2 触发本提案的具体缺口

②③① 账本修复（PR #3，merge `cac6b5e`）往三张表加了三列，dev 靠 AutoMigrate 已自动有，但 prod 无迁移脚本：

| 列 | 表 | 类型 | 作用（一句话） |
|---|---|---|---|
| `funded_at_ms` | `strategy_instances` | `*int64` | 创世注资时间戳；NULL 期跳过对账避免 $0 账本误判全部持仓为 drift |
| `last_applied_exec_id` | `portfolio_states` | `uint` | 账本回写水位线；每笔成交只折算一次 |
| `trade_id` | `spot_executions` | `int64`(index) | 交易所全局唯一成交 ID；market 多档 sweep 共享 ms，去重靠它 |

更早还欠 `funded_at_ms` 同批的 reconcile-scoping、以及任何未来 schema 改动（④⑤⑥ backlog 大概率还会动表）。**根因不是"少三条 ALTER"，而是"prod 根本没有迁移机制"。**

---

## 2. prod 迁移机制必须满足什么（需求，先于选型）

| # | 需求 | 为什么（AutoMigrate 的盲区） |
|---|---|---|
| R1 | **版本化、有序** | AutoMigrate 无版本概念，无法保证"按顺序、且只跑一次" |
| R2 | **可回滚 / 可审计** | 生产改 schema 必须能评审 diff、能回退、留痕 |
| R3 | **支持 GORM 表达不了的 DDL** | hypertable、partial unique index、extension —— db.go 现已手写 raw SQL |
| R4 | **不破坏既有数据** | 加 NOT NULL 列要带默认/回填；改类型要安全；AutoMigrate 从不回填 |
| R5 | **并发/重入安全** | 已踩过 `ALTER ADD trade_id` 并行 AutoMigrate 竞态（集成测试被迫 `-p 1`） |
| R6 | **与 dev 的 AutoMigrate 共存** | dev/lab 继续用 AutoMigrate 快迭代，不能二选一 |
| R7 | **Go 生态 / 低运维** | 原型期单人维护，不引重型基础设施 |

---

## 3. 核心判断：声明式 vs 版本化 SQL

工具大致分两类，**先选范式再选工具**：

- **声明式（declarative）**：你描述"目标 schema 长什么样"，工具 diff 当前库自动生成迁移。代表：**Atlas**、ariga、部分 ORM。卖点是"基本不用手写迁移"。
- **版本化 SQL（versioned）**：你手写有序的 `NNN_xxx.up.sql / .down.sql`，工具只负责"按版本号有序跑、记到一张 version 表"。代表：**golang-migrate、goose、dbmate**。

### 3.1 为什么本仓的现实削弱了 Atlas 的核心卖点

Atlas 最大价值是"声明式 diff 省去手写迁移"。但本仓的 schema **不是纯 GORM struct 能描述的**：

```
db.go 在 AutoMigrate 之后还命令式 Exec 了：
  - CREATE EXTENSION timescaledb
  - create_hypertable('klines', ...)            ← 声明式工具推导不出
  - create_hypertable('portfolio_states', ...)  ← 同上
  - 3 个 partial unique index (WHERE status != 'retired' 等) ← GORM tag 表达不了
```

声明式工具要么（a）需要你**额外手维护一份 HCL/SQL 的 desired-state**与 struct 并行（双份真相源），要么（b）每次 diff 都想"删掉"它看不懂的 hypertable 配置 → 反而危险。

**结论倾向**：本仓"难的部分本来就是手写命令式 DDL"，这恰好是**版本化 SQL 的主场**，而非声明式的主场。Atlas 的超能力在我们这儿被 TimescaleDB + 手写 DDL 中和掉了，只剩它较重的那一面（额外二进制依赖、HCL 配置、diff 用的影子库）。

> 注：Atlas 也支持"手写 SQL 的 versioned 模式"（不止声明式），但那样就只是"更重的 golang-migrate"，性价比存疑。

---

## 4. 候选工具对比

| 维度 | **Atlas** | **golang-migrate** | **goose** | 纯 SQL + 自建 version 表 |
|---|---|---|---|---|
| 范式 | 声明式（也支持 versioned） | 版本化 SQL | 版本化 SQL（+ 可 Go 迁移） | 版本化 SQL（手搓） |
| 引依赖 | 独立二进制 / Go SDK | 单库，CLI+lib 一体 | 单库，CLI+lib 一体 | 无 |
| TimescaleDB/raw DDL | diff 不理解 hypertable，需 HCL 兜底 | 原样跑 .sql，无障碍 | 原样跑 .sql，无障碍 | 无障碍 |
| 回滚 | 支持 | up/down 文件 | up/down（同文件分段） | 自己写 |
| version 账本 | 自带（含 hash 校验、lint） | `schema_migrations` 表 | `goose_db_version` 表 | 自建 |
| 嵌入二进制 | 较难 | `//go:embed` + iofs 源 | `//go:embed` 原生支持 | 自己写 |
| 与 AutoMigrate 共存 | 可（dev 用 AM，prod 用 Atlas） | 可 | 可 | 可 |
| 运维心智 | 最重（影子库/HCL/CLI） | 轻 | 最轻（API 最小） | 看似最轻，实则要自己实现 R1/R2/R5 |
| 生态/成熟度 | 高，增长快 | 最广泛 | 高，Go 项目常见 | N/A |

### 4.1 各方案一句话裁决

- **Atlas**：声明式优势被本仓 TimescaleDB/raw-DDL 现实中和；引入它=承担重运维换取我们用不上的超能力。**不推荐作首选**。
- **纯 SQL + 自建 version 表**：看着最省，实则要自己实现 R1/R2/R5（有序、账本、并发锁），等于重造半个 goose，**不推荐**。
- **golang-migrate**：稳妥、最广泛，up/down 双文件清晰。可选。
- **goose**：原生 `//go:embed` 友好、API 最小、单文件 up/down 用 `-- +goose Up/Down` 注释分段，**对"嵌进 cmd/saas 二进制、启动时按 app_role 决定跑不跑"这个用法最顺手**。

---

## 5. 已决策方案

**Goose（版本化 SQL）+ Squawk（lint）+ Go drift 测试 + 下游护栏；dev/lab 保留 AutoMigrate。** golang-migrate 曾为同级备选，定 goose；Atlas 全程排除。

理由小结：
1. 本仓难点是命令式 TimescaleDB/partial-index DDL，版本化 SQL 直跑无障碍，声明式反而要兜底（§3.1）。
2. goose 的 `//go:embed` 让迁移 SQL 随 `cmd/saas` 二进制走，部署不依赖外部 CLI，契合 R7。
3. dev/lab 保留 AutoMigrate 快迭代（R6）；prod 走 goose-only，**AutoMigrate 彻底退出 prod 启动路径**（不保留"sanity check"残留——AutoMigrate 无只读模式，在 prod 跑它=偷偷改 schema）。真正的 sanity check 移到 CI 的 drift 测试。

### 5.0 AutoMigrate 退场的精确范围与"双源"关系

- **DDL 权威 = 迁移文件；ORM 映射权威 = GORM struct；CI drift 测试把两者钉死对齐。** 不是"一个 source of truth"，是"两个 + 一道粘合闸门"——这正是 drift guard 为何是必需品而非可选项。
- AutoMigrate 仅豁免给"允许漂、随时重建"的环境（开发者笔记本）。**凡是必须和 prod 一致的环境（paper-trading、回测集群）都跑 goose，不用 AutoMigrate。**

### 5.1 落地形态（已决策，待实现，三者同一个 PR）

```
internal/saas/store/migrations/
  00001_baseline.sql            ← 当前 20 表的完整 schema（从 AutoMigrate 现状导出）
  00002_add_ledger_columns.sql  ← 本次三列 + 索引
  README.md                     ← review checklist + Squawk 盲区清单（hypertable/改名/DROP）
  ...
internal/saas/store/migrate.go       ← //go:embed migrations/*.sql + RunMigrations(db, appRole)
internal/saas/store/migrate_drift_test.go ← //go:build integration：goose 库 vs AutoMigrate 库 pg_dump diff
cmd/saas/main.go                ← saas: SET lock_timeout='3s' → goose up（不跑 AutoMigrate）; dev/lab: AutoMigrate
.github/workflows/ci.yml        ← 最小 CI：go test（含 drift） + squawk lint migrations/
.squawk.toml                    ← per-file ignore：00001_baseline.sql（空库从零建表，非并发 index 安全）
```

- **下游护栏在 runner 里**：prod 路径执行 `goose up` 前 `SET lock_timeout='3s'`；**不设紧的全局 `statement_timeout`**（会半截砍掉合法的慢迁移如大表 backfill / hypertable 跨 chunk ALTER），需要时在单条迁移内 `SET LOCAL statement_timeout`。`lock_timeout` 限的是"等锁"不是"持锁/运行时长"——护栏重心是它 + `CONCURRENTLY`。
- **Squawk 对 baseline 的误报**：`00001` 全是"从零建表"DDL（非并发 `CREATE INDEX`、`create_hypertable`、partial index），在**空库上安全**但会触发 Squawk 的 populated-table 规则 → 用 `.squawk.toml` per-file ignore 或 `-- squawk-ignore` 处理。增量迁移（00002+）不豁免，全量 lint。
- **baseline（00001）怎么生成 + 保真**：从一个干净库跑完 AutoMigrate + db.go 的 raw SQL，`pg_dump --schema-only` 导出，人工校对成 goose 文件。**保真验证（最高风险步骤）**：起两个空库，一个 goose 一个 AutoMigrate，`pg_dump --schema-only` 对 diff 必须为空——这同一段代码即 §决策摘要的 Drift Guard，一次性做 baseline 保真、CI 里持续做 drift 闸门。
- **00002 本次三列**（示意，非最终）：
  ```sql
  -- +goose Up
  ALTER TABLE strategy_instances ADD COLUMN IF NOT EXISTS funded_at_ms bigint;
  CREATE INDEX IF NOT EXISTS idx_strategy_instances_funded_at_ms ON strategy_instances (funded_at_ms);
  ALTER TABLE portfolio_states ADD COLUMN IF NOT EXISTS last_applied_exec_id bigint NOT NULL DEFAULT 0;
  ALTER TABLE spot_executions ADD COLUMN IF NOT EXISTS trade_id bigint NOT NULL DEFAULT 0;
  CREATE INDEX IF NOT EXISTS idx_spot_executions_trade_id ON spot_executions (trade_id);
  -- +goose Down
  ...
  ```
  注意 `last_applied_exec_id`/`trade_id` 在已有行上要 `DEFAULT 0`（与 struct 语义一致：0=回退到 ms 键 / 未 apply）；`portfolio_states` 是 hypertable，`ALTER` 会广播到所有 chunk。

---

## 6. 时机问题（独立于工具选型）

两个现实因素影响"现在做到什么程度"：

1. **prod 是否已有真生产库？** 若 `app_role=saas` 还没真上线（原型迭代期大概率没有），则**没有历史数据要保护**，baseline 可以"现在就锚定为第 0 版"，成本最低。一旦真上线再补 baseline，要先 `pg_dump` 既有库对齐，麻烦得多。→ **倾向：趁没上线，现在就把机制搭起来。**
2. **是否等 ④⑤⑥ backlog 的 schema 改动一起做？** 机制（goose + baseline）只需搭一次；之后每次 schema 改动加一个 `NNNNN_*.sql` 是边际小成本。**机制先搭，三列作为 00002 顺手带上**，后续改动各自追加文件即可——不必攒批。

---

## 7. 决策点（已定）

- **D1 范式**：✅ 版本化 SQL（声明式 Atlas 排除——§3.1）
- **D2 工具**：✅ goose
- **D3 时机**：✅ 现在搭机制 + baseline（趁 prod 未上线，无历史数据要保护，成本最低）
- **D4 baseline 来源**：✅ `pg_dump` 现状导出 + 保真验证
- **D5 CI**：✅ 随 Squawk 同批落地最小 GitHub Action（不再后置——Squawk「立即启用」连带 CI 立即落地）
- **D6 lint**：✅ Squawk，与 goose 同批（非未来扩展项）；Atlas Lint / Eugene 排除（Eugene 暂缓）
- **D7 drift guard**：✅ Go 集成测试为一等项（防 dev-AutoMigrate / prod-goose 双轨漂移）
- **D8 AutoMigrate 退场**：✅ prod 路径彻底移除（不留 sanity-check 残留）；仅开发者笔记本豁免，paper-trading/回测集群走 goose

## 8. 实现顺序（待实现）

1. 引入 goose + 写 `00001_baseline` + **保真验证**（等价性 diff 为空才算成立）
2. `00002` 带上本次三列（`DEFAULT 0` / hypertable 广播注意）
3. prod 启动切 goose-only，AutoMigrate 退出 prod 路径
4. drift 测试 + Squawk + 最小 CI 同批落地（承重墙 + lint gate）
5. 可选后续：paper-trading/回测集群切 goose；dev 笔记本保留 AutoMigrate 豁免

---

## 9. 关联

- `internal/saas/store/db.go`（铁律 4 + 现有 raw DDL）
- `docs/saas-tier2-schema-v1.md`（Tier2 表定义源）
- `[[ws-protocol-freeze]]` / `[[reconcile-freeze-scoping]]`（三列各自修复的 bug 背景）
