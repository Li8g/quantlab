# SaaS Tier 2 Schema v1 — 设计草案

> **状态（2026-05-20）**：
> - **22/30 决策已冻结**（跨表 CC1-3、组 A 5 项、组 B 5 项、组 C 4 项、组 E 5 项）。详见 §9 待审清单。
> - **8 项待审 = 组 D（Lot + Trade + Execution）全部**，显式阻塞于 Phase 7 `OrderIntent` / `TradeCommand` 协议冻结（`docs/系统总体拓扑结构.md` §5 + `internal/strategy/contract.go:54` + `docs/策略数学引擎.md` §I-2.8）。
> - 已冻结部分可独立实施（见 §8 迁移路径），退役 `internal/saas/store/models.go` 中 8/11 个代码标记（D 组涉及的 SpotLot/TradeRecord/SpotExecution 3 个保留 `[INVENTED v1 — sync with TradeCommand v1]`）。

## 0. 文档元信息

| 项目 | 取值 |
|---|---|
| 文档作者 | claude (代笔) |
| 创建日期 | 2026-05-20 |
| 当前阶段 | Phase 5 已完成（commit `b5372af`）；Phase 6 未开 |
| 上游来源 | `docs/系统总体拓扑结构.md` §4 / `docs/Coding-plan-dev-phases-prompts_v3_2_2.md` Phase 6/7 |
| 下游影响 | `internal/saas/store/models.go` Tier 2 9 个 placeholder model 的字段冻结 |
| 退役标记数 | 11 个代码标记 → 本轮可退役 8 个（A/B/C/E 涉及表）；剩 3 个（D 组 SpotLot/TradeRecord/SpotExecution）待 Phase 7 TradeCommand 冻结 |

## 1. 范围与边界

本文档**只**约束以下九张表的 Go struct / JSON 字段 / GORM 约束 / 索引：

```
User · StrategyTemplate · StrategyInstance · PortfolioState · RuntimeState
SpotLot · TradeRecord · SpotExecution · AuditLog
```

**不**约束（这些是其他冻结线的范畴）：

- WebSocket TradeCommand 协议（`docs/系统总体拓扑结构.md` §5 全章 `[INVENTED v1]`） → 本稿对 D 组的字段引用该处的草稿，标 `[INVENTED v1 — sync with TradeCommand v1]`
- OrderIntent Go struct（`internal/strategy/contract.go:54` `[INVENTED v1]`） → 同上
- 策略数学引擎 §I-2.8 Lot/Order 字段细节（`docs/策略数学引擎.md` §8.3 `[INVENTED v1]`） → 同上

这三处一旦冻结，本稿 D 组的字段需要重对齐；其他组（A/B/C/E）不受影响。

## 2. 跨表通用决策

### 2.1 软删除策略 `[v1 — frozen: no soft-delete anywhere]`

**所有 9 张表都不使用 `gorm.Model` 的软删除机制**（不引入 `deleted_at` 字段）。"失活 / 不再活跃 / 退役"的语义统一用业务字段表达。

理由：v0 草案曾按"4 张软删 + 5 张不删"分配，但内部一致性差——`StrategyInstance` 既有 `status='retired'` 终态又走 `gorm.Model` 软删，两套机制干同一件事。配合 `email`/`strategy_id`/`instance_id` 唯一索引时，GORM 默认软删 + 普通 `uniqueIndex` 会冲突，必须改 partial index `WHERE deleted_at IS NULL`（已在 `repository/challenger_integration_test.go` 踩过同形 bug）。**统一不用软删** 抹掉这整类 bug 来源。

| 表 | "失活" 表达方式 | 备注 |
|---|---|---|
| `User` | `Active bool` 字段，`false` ⇔ 禁用 | 真要"删除用户"走 AuditLog 留痕 |
| `StrategyTemplate` | `Active bool` 字段 | Registry 是 SoT；DB 内 row 仅作 catalog；硬删也可 |
| `StrategyInstance` | `Status` enum 含 `retired` 终态（§4.2） | 单一机制，与生命周期同源 |
| `PortfolioState` | 不需要 | 追加历史，从来不删 |
| `RuntimeState` | 不需要 | 当前态 UPSERT；instance 退役时可硬删 row |
| `SpotLot` | `CloseMs != nil` ⇔ 已平仓 | 业务时间字段表达终态 |
| `TradeRecord` | `Status` enum 含 `cancelled`/`rejected` 终态 | 同上 |
| `SpotExecution` | 不需要 | 永远 insert-only |
| `AuditLog` | 不需要 | 永不删，后续归档 |

**所有表都丢弃 `gorm.Model`**，改为显式：

```go
ID        uint      `gorm:"primaryKey"`
CreatedAt time.Time `gorm:"index"`     // GORM 按字段名 auto-populate
UpdatedAt time.Time                    // 仅在"会更新行"的表上保留
```

`UpdatedAt` 保留的表：`User` / `StrategyTemplate` / `StrategyInstance` / `RuntimeState` / `SpotLot` / `TradeRecord`。
`UpdatedAt` 不要的表（纯 insert-only）：`PortfolioState` / `SpotExecution` / `AuditLog`。

**备选方案与影响**：

| 选项 | 影响 |
|---|---|
| **本稿 (b) 全部不软删** | 一套机制（status/active/CloseMs/Status enum）；唯一索引零 partial；GORM `Unscoped()` 心智成本归零；查询要养成 `WHERE active = true`（middleware 可包装） |
| (a) 原 v0：4 软删 + 5 不删 | 两种"失活"机制并存，与 StrategyInstance.status 重复；partial index 复杂度散布在 3-4 张表 |
| (c) 全部硬删，连 active 字段也不要 | 删用户 / 删模板就真删；失去"曾经存在"线索；不可接受 |
| (d) 全部统一软删（含 PortfolioState 等） | 时序表行数爆，软删无意义；写性能受拖累 |

### 2.2 NowMs vs CreatedAt 双时间 `[v1 — frozen: 双时间防御性深度，按表挑选业务时间字段名]`

`NowMs` = 策略时钟（`StrategyInput.NowMs`，铁律 2 唯一合法时间源）。
`CreatedAt` = 服务侧 wall clock（GORM 自动管理）。

**关键事实**：Tier 2 表只被 **Phase 6 live tick + Phase 7 Agent 回报**填充，**回测路径不写 Tier 2**（回测只写 challenger_result_packages / gene_records / sharpe_bank）。所以实践中 `NowMs ≈ CreatedAt`，差异在亚秒到秒级。**双时间为防御性深度保留**——若实盘出现两者大幅偏差，即时钟漂移 / cron 卡死的告警信号。

| 表 | 策略时间字段 | `CreatedAt` (GORM) | `UpdatedAt` (GORM) | 备注 |
|---|---|---|---|---|
| User | — | ✓ | ✓ | 业务实体；无策略时间概念 |
| StrategyTemplate | — | ✓ | ✓ | 同上 |
| StrategyInstance | `LastTickWallTime` (wall) | ✓ | ✓ | 上次 cron 触发的 wall 时间（ops 监控）；策略时间从 PortfolioState 最新行查 |
| PortfolioState | `NowMs` (必填) | ✓ | — | 追加历史；按 `(InstanceID, NowMs)` 索引 |
| RuntimeState | `NowMs` (必填) | ✓ | ✓ | 当前态 UPSERT |
| SpotLot | `OpenMs` / `CloseMs` (交易所时间) | ✓ | ✓ | 取自 entry/close trade 首个 `SpotExecution.FilledAtExchangeMs` |
| TradeRecord | `NowMsAtSaaS` (策略 NowMs at emit) | ✓ | ✓ | 发单时的策略时间；与 SpotExecution.FilledAtExchangeMs 比较得发单→成交延迟 |
| SpotExecution | `FilledAtExchangeMs` (交易所时间) | ✓ | — | 交易所回报；与 CreatedAt 差 = 网络/Agent 延迟 |
| AuditLog | `NowMs` (nullable) | ✓ | — | v1 阶段 11 个 Action 全部填 nil；预留未来策略时间事件 |

**备选方案与影响**：

| 选项 | 影响 |
|---|---|
| **双时间（本稿）** | 多 16 字节/行（可忽略）；CreatedAt 由 GORM 零代码成本管理；NowMs vs CreatedAt 偏差可作时钟漂移/cron 卡死告警；查询时需明确按谁排序 |
| 只存 NowMs | 失去 GORM auto-CreatedAt；需要手工 timestamp 索引；写入时机不可独立追踪 |
| 只存 CreatedAt | Tick 重放无法对齐策略时间；PortfolioState 时序语义崩溃（按落表时间排序而非策略时间） |

### 2.3 ID 体系 `[v1 — frozen: Tier 1 hex 不动, Tier 2 内部统一 ULID]`

- **内部主键**：显式 `ID uint`（`gorm:"primaryKey"`），CC1 已决不用 `gorm.Model`。便于关联和性能（B-tree 友好）。
- **业务可读 ID（对外 HTTP 暴露）**：所有面向 HTTP 的实体用 **ULID**（26 字符，单调递增，URL 安全）。
- **不迁移 Tier 1**：已有的 `TaskID`/`ChallengerID`/`ChampionID` 用 `crypto/rand` 16-byte hex（32 字符），保持现状；迁移代价 > 收益（需要数据迁移 + URL 兼容 + 测试改）。两套 ID 体系并存，但**新表（Tier 2）内部一致使用 ULID**。
- **实施依赖**：`github.com/oklog/ulid/v2`（Go 生态事实标准，~600 行，无间接依赖；用 `MonotonicEntropy` 模式开箱即用单调递增）。

| 表 | 业务 ID 字段 | 生成方式 |
|---|---|---|
| User | `user_id` (ULID) | 创建时生成；URL 暴露字段，避免 uint PK 泄露行数 |
| StrategyTemplate | `strategy_id` (string) | 等于代码 `epoch.Registry` key，e.g. `sigmoid_v1`；非 ULID |
| StrategyInstance | `instance_id` (ULID) | 创建时生成 |
| PortfolioState | uint PK (无业务 ID) | — |
| RuntimeState | uint PK + (instance_id) unique | — |
| SpotLot | `lot_id` (ULID) | 开仓时生成；ULID 单调递增对高频开仓表是真实收益 |
| TradeRecord | `client_order_id` (ULID) | 发单时生成；同步给 Agent 作幂等键 |
| SpotExecution | uint PK + `exchange_order_id` (string, index) | 关联 TradeRecord 由 client_order_id；多账户上线前应改复合 unique (instance_id, exchange_order_id) |
| AuditLog | uint PK | 无业务 ID |

**备选方案与影响**：

| 选项 | 影响 |
|---|---|
| **(a) 本稿**：Tier 1 hex / Tier 2 ULID | 两套机制并存；Tier 2 内部统一；ULID 单调性兑现在 TradeRecord/SpotLot 高频写入 |
| (b) 全 hex（删 ULID 提议） | 单一机制；放弃 TradeRecord 索引插入性能；新依赖归零 |
| (c) 全迁 ULID（Tier 1 也改） | 最一致；但破坏性迁移（DB 数据 + URL 兼容 + 测试改），原型期不值 |
| (d) 全 UUID v4 | 36 字符、随机非单调、btree 页分裂；除了"看起来标准"无优势 |
| (e) 全 uint 自增 | URL 暴露行数；前端能枚举 `/instances/1` `/instances/2`...；不可接受 |
| (f) UUID v7 (gofrs/uuid) | 与 ULID 等价（单调），但 36 字符更长，hyphens 影响 URL 紧凑性 |

## 3. 组 A: 身份 — User

### 3.1 Go struct v1

```go
type User struct {
    ID           uint       `gorm:"primaryKey"                       json:"id"`
    UserID       string     `gorm:"type:varchar(32);uniqueIndex"     json:"user_id"`  // ULID, 对外 URL 暴露
    CreatedAt    time.Time  `gorm:"index"                            json:"created_at"`
    UpdatedAt    time.Time                                            `json:"updated_at"`
    Email        string     `gorm:"type:varchar(255);uniqueIndex"    json:"email"`
    PasswordHash string     `gorm:"type:varchar(255);not null"       json:"-"`
    Role         UserRole   `gorm:"type:varchar(16);index;not null"  json:"role"`
    DisplayName  string     `gorm:"type:varchar(128)"                json:"display_name"`
    Active       bool       `gorm:"index;default:true;not null"      json:"active"`
    LastLoginAt  *time.Time                                           `json:"last_login_at,omitempty"`
}

type UserRole string

const (
    UserRoleAdmin    UserRole = "admin"     // 全权
    UserRoleOperator UserRole = "operator"  // 可创建/管理 Instance，不能 Promote
    UserRoleViewer   UserRole = "viewer"    // 只读
)
```

### 3.2 `[v1 — frozen 2026-05-20]` 决策

1. **Auth 方案：JWT HS256**。`config.yaml` 已有 `jwt.secret` 字段，方向已定。
   - **算法 HS256**：单体 SaaS 进程内验签，最简单。未来拆服务时再考虑迁 RS256。
   - **TTL 24h**：沿用 config.yaml 现值；**不**引入 refresh token 机制（原型期降复杂度）。
   - **Claims 包含 `role`**：鉴权零 DB 查询。代价：role 变更需要等 token 过期或主动 re-login 才生效（变更罕见，可接受）。
   - **禁用滞后已知限制**：用户被禁用（Active=false）后，已签发 JWT 在最长 24h 内仍有效。原型期接受该窗口；Phase 9 REST API 硬化期再考虑短 TTL+refresh 或 Redis 黑名单。
2. **角色：3 态闭枚举**（admin/operator/viewer）。**不**做完整 RBAC。
   - **admin**：全权，含 Promote/Retire 与全局配置
   - **operator**：可创建/管理自有 Instance；**可读全部 Instance**（单租户假设下不做用户隔离）；不能 Promote
   - **viewer**：只读
3. **单租户**：不引入 `tenant_id` 字段。多租户化的迁移成本（所有表加列 + 所有查询加 scope）原型期不值。
4. **密码哈希：bcrypt cost=12**。`golang.org/x/crypto/bcrypt` 标准；OWASP 推荐默认；登录 ~250ms 在可接受区间。Argon2id 是更现代选项，原型期收益不显著。
5. **`Active bool` 表达禁用** — 不走软删，无 partial unique index 需求。禁用用户保留 row 用于历史关联（StrategyInstance.OwnerUserID）。配合 A1 的 24h JWT TTL，禁用是异步生效。

### 3.3 备选方案与影响

| 决策 | 备选 | 影响 |
|---|---|---|
| Auth | OAuth2/SSO | 适合企业场景，但需 IdP，原型期过重 |
| Auth | API key | 无登录态、易调试，但浏览器前端难承载 |
| Role | 完整 RBAC | 灵活，但需 permission/role_permission 两张关联表 + 中间件大改 |
| Role | 单一 `is_admin bool` | 最简但前端无法表达"只能看不能改" |
| 租户 | 加 `tenant_id` | 现在加便宜，未来 retrofit 贵 |
| 租户 | 不加（本稿） | 多租户化时需要 schema migration + 所有外键加列 |

---

## 4. 组 B: 策略部署 — StrategyTemplate + StrategyInstance

### 4.1 StrategyTemplate Go struct v1

```go
type StrategyTemplate struct {
    ID           uint      `gorm:"primaryKey"                       json:"id"`
    CreatedAt    time.Time                                          `json:"created_at"`
    UpdatedAt    time.Time                                          `json:"updated_at"`
    StrategyID   string    `gorm:"type:varchar(64);uniqueIndex"     json:"strategy_id"`
    DisplayName  string    `gorm:"type:varchar(128);not null"       json:"display_name"`
    Version      string    `gorm:"type:varchar(32);not null"        json:"version"`
    Description  string    `gorm:"type:text"                        json:"description"`
    Active       bool      `gorm:"index;default:true;not null"      json:"active"`

    // ChromosomeSchemaJSON 是策略基因维度的元描述（segment names, ranges,
    // mutation scales），供前端表单和 lab 工具消费。由策略自身定义，启动期
    // 由代码注册同步进 DB。
    ChromosomeSchemaJSON []byte `gorm:"type:jsonb" json:"chromosome_schema_json,omitempty"`
}
```

### 4.2 StrategyInstance Go struct v1

```go
type StrategyInstance struct {
    ID               uint           `gorm:"primaryKey"                          json:"id"`
    CreatedAt        time.Time                                                  `json:"created_at"`
    UpdatedAt        time.Time                                                  `json:"updated_at"`
    InstanceID       string         `gorm:"type:varchar(32);uniqueIndex"        json:"instance_id"` // ULID
    StrategyID       string         `gorm:"type:varchar(64);index;not null"     json:"strategy_id"`
    Pair             string         `gorm:"type:varchar(32);index;not null"     json:"pair"`
    AccountID        string         `gorm:"type:varchar(64);index;not null"     json:"account_id"` // 字符串标签；语义上 scoped by OwnerUserID
    OwnerUserID      uint           `gorm:"index;not null"                       json:"owner_user_id"`
    Status           InstanceStatus `gorm:"type:varchar(16);default:'idle';index" json:"status"`
    ActiveChampID    *string        `gorm:"type:varchar(64);index"               json:"active_champion_id,omitempty"`
    LastTickWallTime *time.Time                                                  `json:"last_tick_wall_time,omitempty"`  // wall clock, ops 监控用；策略时间从 PortfolioState 最新行查
}

type InstanceStatus string

const (
    InstanceStatusIdle    InstanceStatus = "idle"     // 已创建未启动
    InstanceStatusLive    InstanceStatus = "live"     // Cron Tick 中
    InstanceStatusPaused  InstanceStatus = "paused"   // 手动暂停（保留状态）
    InstanceStatusRetired InstanceStatus = "retired"  // 终态
)
```

**复合唯一约束**（GORM struct tag 无法表达 partial unique，需 migration 显式建）：

```sql
CREATE UNIQUE INDEX idx_inst_unique_active
  ON strategy_instances (owner_user_id, strategy_id, pair, account_id)
  WHERE status != 'retired';
```

含义：同一用户在同一 (strategy, pair, account) 组合上**只能有一个活跃实例**（idle/live/paused），但允许 retired 实例不阻止重建。

状态转移图（v1）：

```
idle ──start──→ live ⇄ paused
   │              │      │
   └──retire──────┴──────┴──→ retired (终态)
```

任意非 retired 状态都可转 retired。不引入 `error` / `degraded` 状态；异常通过 AuditLog 记录 + 转 paused 表达。

### 4.3 `[v1 — frozen 2026-05-20]` 决策

1. **`AccountID` 抽象账户标签**：scoped by `OwnerUserID`——alice 的 "main" 和 bob 的 "main" 是不同账户。schema 层不在 Instance 表上加 (owner_user_id, account_id) unique（一个账户可对应多 Instance）；语义上 Agent 通过 (owner_user_id, account_id) 映射到具体交易所凭证。未来若需 Account 一等实体可加 `accounts` 表，那时 unique 落在 accounts 上。
2. **`ActiveChampID` 联动**：Promote 操作**不**自动设置 ActiveChampID。原因：Promote 是"祝福这个 challenger 为 Champion"；Deploy 是"把 Champion 上岗到 Instance"，两者解耦才能审计回滚。用户需走单独的 `POST /api/v1/instances/:id/deploy-champion` 动作。
3. **`StrategyTemplate` 与 `epoch.Registry` 关系**：Registry 是代码内强校验（`DefaultRegistry()` 启动期注册），是 SoT；Template 是 DB 内 metadata 镜像（前端展示用）。启动期由代码同步 `registry.IDs()` → upsert Template 行。
   - **同步规则 1（移除）**：Registry 不再含某 StrategyID，对应 Template 行设 `Active=false`，不硬删（历史 GeneRecord 仍能联表显示）。
   - **同步规则 2（更新）**：代码字段（DisplayName/Version/Description/ChromosomeSchemaJSON）总是 Overwrite——代码是 SoT，admin 想改显示名应改代码。`Active boolean` 例外保留（admin 在 DB 手动禁用应保留）。
4. **状态 4 态**：idle/live/paused/retired。**任意非 retired 状态都可转 retired**（含 idle→retired，paused→retired）。不引入 `error` / `degraded` 状态——异常通过 AuditLog (`agent.heartbeat_stale` / `task.fail` 等) + 转 paused 表达。
5. **复合唯一约束（partial unique）**：`(owner_user_id, strategy_id, pair, account_id) WHERE status != 'retired'`。同用户、同 (strategy, pair, account) 只能有一个活跃实例（防重复部署事故）；retired 实例不阻止重建。`pair` 在 Instance 上是本质属性（Template 不持 pair），不是冗余。

### 4.4 备选方案与影响

| 决策 | 备选 | 影响 |
|---|---|---|
| AccountID scoped by user (本稿) | 全局唯一 AccountID | 多用户冲突（"main" 名字撞）；强制前缀解决但约定脆 |
| AccountID scoped by user (本稿) | 加 Accounts 一等表 | 更规范；原型期未触发需求 |
| `ActiveChampID` Never auto-deploy (本稿) | Auto-deploy when null（冷启动场景） | 减一步操作；规则两段式（null vs not-null 不同行为）|
| `ActiveChampID` Never auto-deploy (本稿) | Always auto-deploy 到匹配实例 | 一步到位；Promote 变成有副作用，难审计 |
| Template 同步：Active=false 不删 (本稿) | 硬删 row | 历史 GeneRecord 联表破裂 |
| Template 同步：代码 Overwrite (本稿) | Preserve, 仅补全空字段 | admin 可在 DB 改 DisplayName；代码改名后历史不同步 |
| Template+Registry 共存 (本稿) | 仅保留 Registry（删 Template 表） | -1 张表；前端无法显示模板元数据 |
| Template+Registry 共存 (本稿) | 仅保留 Template（hot-load 策略） | 需要 plugin/wasm 机制；原型期 over-engineered |
| 状态 4 态 (本稿) | 加 `error` 状态 | 出错时实例锁死；恢复路径要明确 |
| 状态 4 态 (本稿) | 加 `degraded`（agent 心跳超时） | §5.7 心跳协议落地后可加；本稿先不加 |
| Instance 多 ActiveChamp | A/B 测试同时挂俩 | 摩擦记账复杂度爆炸；不在原型期 |
| 复合 partial unique (本稿) | 不加 unique，允许重复 | 允许同 (strategy, pair, account) 多实例（变体）；订单可能在同 account 上混 |
| 复合 partial unique (本稿) | 加全状态 unique（含 retired） | retired 实例阻止重建——退役后无法在同配置重新部署 |

---

## 5. 组 C: 实时状态 — PortfolioState + RuntimeState

### 5.1 PortfolioState v1（**追加历史**）

```go
type PortfolioState struct {
    ID         uint   `gorm:"primaryKey" json:"id"`
    CreatedAt  time.Time
    InstanceID string  `gorm:"type:varchar(32);index:idx_ps_inst_now,priority:1;not null" json:"instance_id"`
    NowMs      int64   `gorm:"index:idx_ps_inst_now,priority:2;not null"                   json:"now_ms"`

    DeadBTC       float64 `json:"dead_btc"`
    FloatBTC      float64 `json:"float_btc"`
    ColdSealedBTC float64 `json:"cold_sealed_btc"`
    USDT          float64 `json:"usdt"`

    LastProcessedBarTime int64 `json:"last_processed_bar_time"`
}
```

- **每 Tick 必写一行**（即使 portfolio 数值未变）。理由：提供"实例还在跑"的心跳信号；前端能由最新行的 `NowMs` vs wall now 判断实例健康；Tick 函数代码无条件 INSERT，简化。
- **复合索引方向 ASC**：`(instance_id, now_ms ASC)`。PG B-tree 双向扫描，"最新行"查询 (`ORDER BY now_ms DESC LIMIT 1`) 反向叶子链扫描成本与正向 DESC 索引一致；选 ASC 是因为它与"时间向前"心智一致，调试/手查时方向不易踩错。
- 不用 `gorm.Model`（无软删，无 UpdatedAt）。
- **TimescaleDB hypertable 现在就开**：与 klines 同模式，`chunk_time_interval = 30d`。一行 SQL 成本；实盘期上线后才迁移 hypertable 需要停服重建，现在开零代价。

```sql
SELECT create_hypertable(
  'portfolio_states',
  'now_ms',
  if_not_exists => TRUE,
  chunk_time_interval => 30 * 24 * 60 * 60 * 1000  -- 30 天，毫秒
);
```

### 5.2 RuntimeState v1（**当前态替换**）

```go
type RuntimeState struct {
    ID         uint            `gorm:"primaryKey"                    json:"id"`
    CreatedAt  time.Time                                             `json:"created_at"`
    UpdatedAt  time.Time                                             `json:"updated_at"`
    InstanceID string          `gorm:"type:varchar(32);uniqueIndex"  json:"instance_id"`
    NowMs      int64           `gorm:"not null"                       json:"now_ms"`
    StateJSON  json.RawMessage `gorm:"type:jsonb;not null"           json:"state_json"`
}
```

- **每 Tick UPSERT 一行**（`ON CONFLICT (instance_id) DO UPDATE`）。
- 旧值丢失；策略自己负责在 `StateJSON` 内编码必要的历史尾巴。
- **StateJSON ≤ 64KB 软约束**：超过 jsonb 单 page (8KB) 走 TOAST 表外存储；64KB 是经验上限，超过应在 RuntimeState 之外另存。原型期不强制 trigger 校验；emitter 自觉。
- **实例 retire 后行保留**：InstanceID 是 ULID 不重用，保留 row 是廉价防御；不引入 retire→delete 的 hook。

### 5.3 `[v1 — frozen 2026-05-20]` 决策

1. **PortfolioState = 追加历史 / RuntimeState = 当前态**。两表语义不同：
   - PortfolioState 引擎可解释（4 个数值字段），有时序审计价值
   - RuntimeState 对引擎不透明（opaque blob），保存历史无审计价值
2. **PortfolioState 每 Tick 必写**（不做"未变跳过"优化）：提供心跳；简化 Tick 代码；525k 行/年/实例对 PG 完全可接受。
3. **PortfolioState 复合索引方向 ASC**：`(instance_id, now_ms ASC)` 单索引；PG 双向扫描，DESC 查询性能等价；ASC 与时间向前的心智一致。
4. **TimescaleDB hypertable 现在就开**：`portfolio_states` 走 hypertable，`chunk_time_interval = 30d`。与 klines 同模式；实盘后迁移需要停服重建。
5. **`LastProcessedBarTime` 放 PortfolioState 而不是 RuntimeState**：它是引擎需要的字段（Tick 函数读取），不属于策略私有。
6. **RuntimeState 实例 retire 后保留行**：InstanceID 是 ULID 不重用，保留 row 是廉价防御。
7. **`StateJSON ≤ 64KB` 软约束**：超过 jsonb page 走 TOAST；emitter 自觉，不引入 trigger 校验。

### 5.4 备选方案与影响

| 决策 | 备选 | 影响 |
|---|---|---|
| PortfolioState 历史 | 当前态 1 行 UPDATE | 表只剩 ~实例数行；丢历史，故障时无法重建 |
| PortfolioState 历史 | 两表并存（current + history） | 一致性同步复杂；prototype 阶段不值 |
| 每 Tick 必写 | 仅在 portfolio 变化时写 | 行数大幅下降；失去心跳信号；上次 Tick 时间需查 StrategyInstance.LastTickWallTime |
| 索引方向 ASC（本稿） | `(instance_id, now_ms DESC)` 索引 | PG B-tree 双向扫描，性能等价；DESC 让"最新行"查询省 `DESC` 关键字 4 字符 |
| 索引方向 ASC（本稿） | 两个方向都建 | 多 ~50% 索引空间；写代价更高；现阶段无需 |
| RuntimeState 历史 | 也用追加历史 | 写量 2x；opaque blob 大小未知，可能爆 |
| RuntimeState 历史 | last-N 环形缓冲 | 复杂度上升，价值未证明 |
| Hypertable 现在开（本稿） | 推迟到 Phase 6.5+ | 推迟则迁移需停服重建已有数据；现在开成本一行 SQL |
| StateJSON ≤64KB 软约束（本稿） | GORM 硬限制 / trigger 校验 | jsonb 无 size GORM tag；需 trigger 或 app 层 |
| RuntimeState retire 后保留 | 硬删 | 干净；但若同名重部署需冷启动 — ULID 不重用前提下不会发生 |

---

## 6. 组 D: 交易生命周期 — SpotLot + TradeRecord + SpotExecution

> ⚠️ **本组与 OrderIntent / TradeCommand 协议强耦合**。`docs/系统总体拓扑结构.md` §5、`internal/strategy/contract.go:54`、`docs/策略数学引擎.md` §8.3 三处冻结之前，本组**字段不算冻结**，仅作为"如果 TradeCommand 按现有草案落地"的对齐版本。每个跨域字段额外标 `[INVENTED v1 — sync with TradeCommand v1]`。

### 6.1 SpotLot v1

```go
type SpotLot struct {
    ID            uint    `gorm:"primaryKey"                      json:"id"`
    CreatedAt     time.Time                                     `json:"created_at"`
    UpdatedAt     time.Time                                     `json:"updated_at"`
    LotID         string  `gorm:"type:varchar(32);uniqueIndex"     json:"lot_id"` // ULID
    InstanceID    string  `gorm:"type:varchar(32);index;not null"  json:"instance_id"`
    Symbol        string  `gorm:"type:varchar(16);index;not null"  json:"symbol"`
    Kind          LotKind `gorm:"type:varchar(8);index;not null"   json:"kind"`
    OpenMs        int64   `gorm:"not null"                          json:"open_ms"`
    CloseMs       *int64                                         `json:"close_ms,omitempty"`
    Quantity      float64 `gorm:"not null"                          json:"quantity"`
    EntryPrice    float64 `gorm:"not null"                          json:"entry_price"`
    EntryTradeID  string  `gorm:"type:varchar(32);index"           json:"entry_trade_id"` // client_order_id
}

type LotKind string

const (
    LotKindMacro LotKind = "macro" // 宏观引擎建立（长期持仓）
    LotKindMicro LotKind = "micro" // 微观引擎建立（短期 swing）
    LotKindCold  LotKind = "cold"  // 已转 ColdSealedBTC，不再交易
)
```

- **维护态**：策略自己决定开/平/减仓规则，引擎不强制 FIFO。
- `Quantity` 随部分卖出减少；归零时同步设 `CloseMs`。
- `Kind` 三态对应策略数学引擎的双引擎 + 冷封存。
- **`OpenMs` / `CloseMs` 时钟源**：取 entry/close trade 的首个 `SpotExecution.FilledAtExchangeMs`（交易所时间）。Lot 的"存在"以交易所成交为准，不用策略 NowMs 或 SaaS CreatedAt——这两者在异步链路上都早于实际持仓建立。

### 6.2 TradeRecord v1

```go
type TradeRecord struct {
    ID            uint        `gorm:"primaryKey" json:"id"`
    CreatedAt     time.Time                      `json:"created_at"`
    UpdatedAt     time.Time                      `json:"updated_at"`
    ClientOrderID string      `gorm:"type:varchar(32);uniqueIndex" json:"client_order_id"`        // [INVENTED v1 — sync with TradeCommand v1]
    InstanceID    string      `gorm:"type:varchar(32);index;not null" json:"instance_id"`
    Symbol        string      `gorm:"type:varchar(16);index;not null" json:"symbol"`
    Side          string      `gorm:"type:varchar(8);not null" json:"side"`                       // [INVENTED v1 — sync with OrderIntent.OrderSide]
    OrderType     string      `gorm:"type:varchar(16);not null" json:"order_type"`                // [INVENTED v1 — sync with OrderIntent.OrderType]
    QuantityUSD   float64     `gorm:"not null" json:"quantity_usd"`
    LimitPrice    *float64                                                      `json:"limit_price,omitempty"`
    NowMsAtSaaS   int64       `gorm:"not null" json:"now_ms_at_saas"`
    ValidUntilMs  int64       `gorm:"not null" json:"valid_until_ms"`                              // [INVENTED v1 — sync with TradeCommand v1]
    Status        TradeStatus `gorm:"type:varchar(16);index;default:'pending'" json:"status"`
    LotID         *string     `gorm:"type:varchar(32);index" json:"lot_id,omitempty"`              // 命中/创建的 SpotLot
}

type TradeStatus string

const (
    TradeStatusPending      TradeStatus = "pending"        // SaaS 已写入，未送达 Agent
    TradeStatusAcked        TradeStatus = "acked"          // Agent ACK，待成交
    TradeStatusFilled       TradeStatus = "filled"         // 完全成交
    TradeStatusPartialFilled TradeStatus = "partial_filled" // 部分成交（仍在挂）
    TradeStatusCancelled    TradeStatus = "cancelled"      // 主动撤单或超时
    TradeStatusRejected     TradeStatus = "rejected"       // 交易所拒绝
)
```

### 6.3 SpotExecution v1

```go
type SpotExecution struct {
    ID                 uint   `gorm:"primaryKey" json:"id"`
    CreatedAt          time.Time
    ClientOrderID      string  `gorm:"type:varchar(32);index;not null" json:"client_order_id"` // FK to TradeRecord
    ExchangeOrderID    string  `gorm:"type:varchar(64);index;not null" json:"exchange_order_id"`
    FillQuantity       float64 `gorm:"not null" json:"fill_quantity"`
    FillPrice          float64 `gorm:"not null" json:"fill_price"`
    FillFeeAsset       string  `gorm:"type:varchar(16);not null" json:"fill_fee_asset"`
    FillFeeAmount      float64 `gorm:"not null" json:"fill_fee_amount"`
    FilledAtExchangeMs int64   `gorm:"not null" json:"filled_at_exchange_ms"`

    // ActualSlippageBPS 由 Agent 计算（实际成交价 vs LimitPrice / 期望价）。
    // Phase 7 prompt L1853 明确要求。负值 = 滑点对买方有利。
    ActualSlippageBPS float64 `json:"actual_slippage_bps"`

    // ExchangeOrderID 来自交易所（Binance 返回），在 (account, symbol) 维度
    // 唯一，全局可能重复。原型期单账户用 index 已够；多账户上线前应改为
    // 复合 unique (instance_id, exchange_order_id) — 见 CC3。
}
```

### 6.4 `[INVENTED v1]` 决策

1. **SpotLot 是维护态**（策略写、引擎读），不是从 TradeRecord 聚合派生。
2. **TradeRecord.LotID** 是软链接（可为 nil，表示"还未匹配/已撤单"）。
3. **TradeRecord ↔ SpotExecution 一对多**（部分成交场景）。
4. **`Side` / `OrderType` 用 string 而非 enum**：v1 阶段先按字符串，等 OrderIntent 冻结后改 typed alias。
5. **`ValidUntilMs` 字段**：从 TradeCommand 草案带过来，到期未成交由 Agent 主动撤单。
6. **`Quantity` 单位是 USDT 计价**：与 OrderIntent.QuantityUSD 对齐。Agent 端转换为 BTC 数量。
7. **`SpotExecution.FillFeeAsset`**：可能不是 USDT（Binance 默认用 BNB 抵扣）；保留 asset 字段。

### 6.5 备选方案与影响

| 决策 | 备选 | 影响 |
|---|---|---|
| SpotLot 维护 vs 派生 | 派生（视图） | 简单；但 FIFO/LIFO 等匹配规则成为系统级硬编码 |
| SpotLot 维护 vs 派生 | 全派生 + 策略私有 RuntimeState 编码 | 把 lot 簿记下放策略；引擎对持仓无感知，无法做仓位风控 |
| 是否拆 SpotLot | 不要这张表 | 简化 schema；策略和 lab 都失去仓位审计视角 |
| TradeRecord.LotID | 改为必填（开仓即配） | 撤单/拒绝场景无 lot 可配，需要 sentinel `lot_id="—"` |
| TradeRecord/Execution 1:N | 1:1 合并 | 部分成交无法表达；改回 1:N 时 schema 不兼容 |
| 数量单位 | 改 BTC 而非 USDT | 与 Strategy.OrderIntent 不一致；摩擦计算复杂 |
| FillFeeAsset | 假设永远 USDT | Binance BNB 抵扣常态；记账误差累积 |

---

## 7. 组 E: 审计 — AuditLog

### 7.1 Go struct v1

```go
type AuditLog struct {
    ID        uint            `gorm:"primaryKey"                                json:"id"`
    CreatedAt time.Time       `gorm:"index"                                     json:"created_at"`
    NowMs     *int64          `json:"now_ms,omitempty"`                          // 与策略时间相关时填；v1 阶段 §7.1 列出的 19 个 Action 全部填 nil，字段保留给未来策略时间驱动事件（release_intent.applied / auto_lot_close / tick_failed 等）
    Actor     string          `gorm:"type:varchar(64);index;not null"           json:"actor"`     // user:<user_id ULID> / agent:<agent_id> / system
    Action    AuditAction     `gorm:"type:varchar(48);index;not null"           json:"action"`
    Subject   string          `gorm:"type:varchar(128);index;not null"          json:"subject"`   // 动作的"直接受体"，如 challenger:<id> / champion:<id> / instance:<id> / user:<user_id>；多 subject 事件次要 subject 放 DataJSON
    DataJSON  json.RawMessage `gorm:"type:jsonb"                                json:"data_json,omitempty"`
}

type AuditAction string

const (
    // 决策
    AuditActionChallengerPromote      AuditAction = "challenger.promote"
    AuditActionChampionRetire         AuditAction = "champion.retire"
    // 进化任务
    AuditActionTaskCreate             AuditAction = "task.create"
    AuditActionTaskSucceed            AuditAction = "task.succeed"
    AuditActionTaskFail               AuditAction = "task.fail"
    // 实例生命周期
    AuditActionInstanceCreate         AuditAction = "instance.create"
    AuditActionInstanceStart          AuditAction = "instance.start"
    AuditActionInstanceStop           AuditAction = "instance.stop"
    AuditActionInstanceDeployChampion AuditAction = "instance.deploy_champion"
    // Agent
    AuditActionAgentConnect           AuditAction = "agent.connect"
    AuditActionAgentDisconnect        AuditAction = "agent.disconnect"
    AuditActionAgentHeartbeatStale    AuditAction = "agent.heartbeat_stale"
    // 用户 / 认证
    AuditActionAuthLogin              AuditAction = "auth.login"
    AuditActionAuthLoginFailure       AuditAction = "auth.login_failure"
    AuditActionUserCreate             AuditAction = "user.create"
    AuditActionUserDisable            AuditAction = "user.disable"
    AuditActionUserRoleChange         AuditAction = "user.role_change"
    // 手工
    AuditActionManualPortfolioAdjust  AuditAction = "manual.portfolio_adjust"
    AuditActionManualLotClose         AuditAction = "manual.lot_close"
)
```

### 7.2 `[v1 — frozen 2026-05-20]` 决策

1. **`Action` 闭枚举 19 项**（§7.1 表）。命名一致采用 `<namespace>.<verb>` 格式（含 `challenger.promote` / `champion.retire`，避免无命名空间例外）。新增事件需走 schema 提议；GORM `varchar(48)` 容许 enum 增项无 migration。
2. **`Subject` 字符串**：约定 `<type>:<id>` 格式但不强校验。**主 Subject = 动作的直接受体**（Promote→challenger，Retire→champion，DeployChampion→instance）；次要受体放 `DataJSON`。
3. **`Actor` 字符串**：`user:<user_id ULID>` / `agent:<agent_id>` / `system`。**用 user_id 而非 email**，避免改 email 后 audit log 回溯断裂。
4. **保留期**：永不删；后续可按 `created_at` 滚动归档到冷存储（不在本稿范围）。
5. **`DataJSON` 自由 schema + §7.4 字段约定附录**：emitter 不强制；约定附录给出每个 Action 的推荐字段，便于前端预期。

### 7.3 备选方案与影响

| 决策 | 备选 | 影响 |
|---|---|---|
| Action 闭枚举 (19 项) | 自由字符串 | 添加事件零成本；但易产生 `promotedChallenger` / `challenger_promoted` 拼写漂移 |
| Action 闭枚举 (19 项) | v1 只保 11 项缺事件用例触发时再加 | 列表短；后期补漏行；趁此次冻结一次性梳理更稳 |
| 一张大表 | 按 Action 拆多表（PromoteAudit / TradeAudit / ...） | 类型安全 + 索引贴合；管理复杂度高 |
| 不删 | 30 天滚动删除 | 节省存储；但失去长期审计能力 |
| DataJSON 自由 + 附录 | 每个 Action 一个 typed Go struct | 类型安全；代码量上升；alteration 需小心 |
| Actor = user_id | Actor = email | 人眼直接可读；email 改名时历史 actor 字符串变陌生 |
| 主 Subject + 次放 DataJSON | Subjects []string（数组列） | 平等表达多受体；查询语义复杂，jsonb GIN 索引 |

### 7.4 DataJSON 字段约定附录

约定**不强制**；emitter 可塞额外字段。约定的价值是"前端能预期看到什么"。

| Action | 推荐 DataJSON 字段 |
|---|---|
| `challenger.promote` | `decision_note`, `reviewed_by` |
| `champion.retire` | `decision_note`, `reviewed_by`, `previous_champion_id` |
| `task.create` | `strategy_id`, `pair`, `interval` |
| `task.succeed` | `challenger_id`, `best_score`, `generations` |
| `task.fail` | `failure_reason` |
| `instance.create` | `strategy_id`, `pair`, `account_id` |
| `instance.start` | （空）|
| `instance.stop` | `reason` |
| `instance.deploy_champion` | `champion_id`, `previous_champion_id` |
| `agent.connect` | `agent_id`, `account_id` |
| `agent.disconnect` | `agent_id`, `reason` |
| `agent.heartbeat_stale` | `last_heartbeat_at_ms`, `seconds_since_last` |
| `auth.login` | `ip`, `user_agent` |
| `auth.login_failure` | `email`, `ip`, `reason` |
| `user.create` | `email`, `role` |
| `user.disable` | `reason` |
| `user.role_change` | `from_role`, `to_role` |
| `manual.portfolio_adjust` | `field`, `from`, `to`, `note` |
| `manual.lot_close` | `lot_id`, `reason` |

---

## 8. 迁移路径

冻结后落地步骤：

1. 把本文档 `[INVENTED v1]` 全部退役（或转 `[v1 — frozen 2026-XX-XX]`）。
2. 用 `internal/saas/store/models.go` 现有 9 个 placeholder 当骨架，按本稿增补字段、加 typed enum、调 GORM tag。
3. 跑一遍 `go test -tags=integration ./internal/saas/store/`，确认 `AutoMigrate(AllModels()...)` 成功跑通新 schema。
4. 删除 placeholder 时代留下的 `[INVENTED v1]` 标记，更新 `internal/saas/store/models.go` package doc。
5. **不**手动写 SQL migration —— prototype 阶段 GORM AutoMigrate 直接跑（破坏性变更可接受）。Phase 9 切到 prod 数据前再考虑 atlas / migrate 流程。

预计 ~400-500 行 Go 改动 + ~50 行 typed enum + ~30 行约束补全。

## 9. 待审阅决策清单（聚合）

每行一条 `[INVENTED v1]`，方便扫读后逐条 ✓/✗/改。

### 跨表

- [x] **CC1**: 全部不软删，统一用 `Active bool` / `Status` enum / `CloseMs` / 业务时间字段表达失活。所有表丢弃 `gorm.Model`，改显式 ID + CreatedAt (+ UpdatedAt where applicable)。【已审，2026-05-20】
- [x] **CC2**: 双时间为防御性深度（实践中 NowMs ≈ CreatedAt）；按表挑选业务时间字段（PortfolioState/RuntimeState 用 NowMs；SpotLot/SpotExecution 用交易所时间；TradeRecord 用 NowMsAtSaaS；StrategyInstance.LastTickWallTime 改用 wall clock；AuditLog.NowMs nullable，v1 阶段全填 nil 预留未来事件）。【已审，2026-05-20】
- [x] **CC3**: Tier 1 (hex 32) 保持不迁移；Tier 2 内部统一 ULID（`github.com/oklog/ulid/v2`，`MonotonicEntropy`）。User 加 `user_id` ULID 用于 URL，避免 uint PK 泄露行数。StrategyTemplate.strategy_id 仍是字符串（=Registry key）。SpotExecution.exchange_order_id 单账户期用 index，多账户前换复合 unique。【已审，2026-05-20】

### User

- [x] **A1**: Auth = JWT HS256, TTL=24h, claims 含 role, 无 refresh token；**禁用滞后已知限制**：JWT 在最长 24h 内仍有效。Phase 9 硬化期再考虑短 TTL+refresh 或 Redis 黑名单。【已审，2026-05-20】
- [x] **A2**: Role 3 态闭枚举 (admin/operator/viewer)；operator 可读全部 Instance（单租户假设下不做用户隔离），不能 Promote/Retire。【已审，2026-05-20】
- [x] **A3**: 单租户，不引入 `tenant_id`。【已审，2026-05-20】
- [x] **A4**: bcrypt cost=12（OWASP 默认，~250ms/login）。【已审，2026-05-20】
- [x] **A5**: 用户禁用走 `Active bool` 字段，不软删（随 CC1 已决）。【已审，2026-05-20】

### StrategyTemplate + StrategyInstance

- [x] **B1**: AccountID 是抽象字符串标签，scoped by OwnerUserID（alice 的 "main" 和 bob 的 "main" 是不同账户）；schema 层不强制 (owner, account) unique（一账户可对应多 Instance）；未来 Accounts 一等表化时 unique 落在 accounts 表上。【已审，2026-05-20】
- [x] **B2**: Promote 不自动 deploy；走单独 `POST /api/v1/instances/:id/deploy-champion` 接口。两个决策解耦才能审计回滚。【已审，2026-05-20】
- [x] **B3**: Template + Registry 共存；启动期同步规则：(1) 代码移除策略 → Template `Active=false` 不删；(2) 代码字段更新 → Overwrite（Active boolean 例外保留）。【已审，2026-05-20】
- [x] **B4**: 4 态 (idle/live/paused/retired)；任意非 retired → retired 都允许；不引入 error/degraded 状态（异常用 AuditLog + paused 表达）。【已审，2026-05-20】
- [x] **B5**: 修正原文表述错误：`pair` 在 Instance 上不是冗余（Template 不持 pair）。加 partial unique `(owner_user_id, strategy_id, pair, account_id) WHERE status != 'retired'`：同用户、同配置只能一个活跃实例；retired 不阻止重建。【已审，2026-05-20】

### PortfolioState + RuntimeState

- [x] **C1**: PortfolioState = 追加历史，每 Tick 必写（含心跳信号）；复合索引 `(instance_id, now_ms ASC)` 单索引（PG 双向扫描性能等价）；TimescaleDB hypertable **现在就开** chunk=30d（与 klines 一致，避免实盘后停服迁移）。【已审，2026-05-20】
- [x] **C2**: RuntimeState = 当前态 UPSERT；实例 retire 后行保留（ULID 不重用前提下无重复风险）；StateJSON ≤64KB 软约束（jsonb 1 page = 8KB；超过走 TOAST）。【已审，2026-05-20】
- [x] **C3**: `LastProcessedBarTime` 放 PortfolioState 而非 RuntimeState（引擎可读字段，非策略私有）。【已审，2026-05-20】
- [x] **C4**: hypertable 现在就开（与 C1 合并，无需推迟）。【已审，2026-05-20】

### SpotLot + TradeRecord + SpotExecution

> 整组与 §6 标头警告同时冻结/解冻，依赖 TradeCommand / OrderIntent 协议。

- [ ] **D1**: SpotLot = 维护态（策略写引擎读）
- [ ] **D2**: TradeRecord.LotID 软链接（可空）
- [ ] **D3**: TradeRecord ↔ SpotExecution = 1:N
- [ ] **D4**: Side/OrderType 暂用 string，等 OrderIntent 冻结后改 typed
- [ ] **D5**: TradeRecord 含 ValidUntilMs
- [ ] **D6**: 数量单位 USDT 计价
- [ ] **D7**: SpotExecution 保留 FillFeeAsset 字段（非 USDT）
- [ ] **D8**: SpotExecution.ActualSlippageBPS 由 Agent 计算

### AuditLog

- [x] **E1**: Action 19 项闭枚举（§7.1）；统一 `<namespace>.<verb>` 命名（含 `challenger.promote` / `champion.retire`）；补全 task.* / auth.* / user.* 三组事件。【已审，2026-05-20】
- [x] **E2**: 主 Subject = 动作直接受体（challenger/champion/instance/user），次要 subject 放 DataJSON。jsonb 字段后续可加 GIN 索引按需查。【已审，2026-05-20】
- [x] **E3**: `user:<user_id ULID>`（不用 email，避免改名断裂回溯）/ `agent:<agent_id>` / `system`。【已审，2026-05-20】
- [x] **E4**: 永不删，后续可按 `created_at` 归档冷存储。【已审，2026-05-20】
- [x] **E5**: DataJSON 自由 schema + §7.4 字段约定附录（不强制；约定给前端预期）。【已审，2026-05-20】

---

**评审完成后**：把 `[INVENTED v1]` 标签批量替换成 `[v1 — frozen YYYY-MM-DD]`，本稿升级为正式 schema 文档；`internal/saas/store/models.go` 同步实施并退役 11 个代码内标记。
