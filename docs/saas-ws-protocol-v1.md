# SaaS ↔ Agent WebSocket Protocol v1 — 设计草案

> **状态（2026-05-20）**：
> - **22/22 决策已冻结**（编码 / 信封 / 鉴权 / 心跳 / 重连 / 9 类消息字段 / 状态同步 / 幂等 / Agent 在线状态 / 端口 / 退役标记表），详见 §11 待审清单。
> - 已冻结部分可独立实施（Phase 7 LocalAgent + Phase 8 WS Hub），退役 `docs/系统总体拓扑结构.md` §5 + §6.3 + §1.1 + §2 + §4.2 中所有 `[INVENTED v1 — needs architect review]` 标记（共 7 处）；同步退役 `internal/strategy/contract.go:49` + `internal/saas/store/models.go` 中 5 处 `[INVENTED v1 — sync with TradeCommand v1]` 标记。
> - **不**冻结：交易所适配（Binance vs Mock）、Phase 8 Hub 内部并发模型、Phase 7 Agent 进程的具体重连状态机实现。

## 0. 文档元信息

| 项目 | 取值 |
|---|---|
| 文档作者 | claude (代笔) |
| 创建日期 | 2026-05-20 |
| 当前阶段 | Phase 6 已完成（commit `9706327`）；Phase 7 / 8 未开 |
| 上游来源 | `docs/系统总体拓扑结构.md` §5 + §6.3 + §1.1 + §2 + §4.2 |
| 下游影响 | `internal/strategy/contract.go` `OrderIntent`；`internal/saas/store/models.go` D 组（SpotLot/TradeRecord/SpotExecution）；`internal/saas/instance/manager.go` `TradeCommandDispatcher`；新建 `internal/wire/`（共用 wire types）+ `internal/saas/wshub/`（SaaS Hub）+ `cmd/agent/` `internal/agent/`（Agent client） |
| 退役标记数 | 7 处文档 `[INVENTED v1]` + 5 处代码 `[INVENTED v1 — sync with TradeCommand v1]` |

## 1. 范围与边界

本文档**冻结**以下三个面：

```
1. wire 协议层  — 消息类型 / 信封 / 编码 / 字段语义
2. 连接生命周期 — TLS / 握手 / 鉴权 / 状态同步 / 心跳 / 重连 / 优雅停机
3. 资源约束     — 端口 / Redis 在线状态 / Agent token 持久化 / config.agent.yaml schema
```

**不**约束：

- 交易所适配（Binance Spot / Mock / 等）——`internal/agent/exchange/` 子包另行定义。
- Phase 8 Hub 内部并发模型（goroutine-per-conn vs reactor）——实现细节，不进契约。
- Phase 7 Agent 进程退避状态机的具体 Go 表达——只冻退避序列（§6），不冻 struct 形态。
- 多账户 Agent（v1 = 1 Agent : 1 account）——多账户能力为 Part III 议题。
- 衍生品 / 杠杆 / 现货空仓——v1 仅 spot 现货。
- WebSocket 子协议（subprotocols / extensions）——一律不用，纯文本 JSON 帧。

## 2. 跨消息通用决策

### 2.1 编码：JSON `[v1 — frozen: JSON UTF-8 文本帧，禁 binary frame]`

**所有消息都是 WebSocket 文本帧，内容为 UTF-8 编码 JSON。**

| 选项 | 影响 |
|---|---|
| **(a) 本稿 JSON** | 调试零成本（tcpdump/wireshark 可读）；字段演进无 schema 编译；与 schema_version=v5.3.3 同源；序列化开销在 1m tick 节拍下可忽略 |
| (b) protobuf | 字段类型严格；但需要 `.proto` 文件与 Go/Agent 双侧 build；过度工程化 |
| (c) msgpack | 二进制紧凑；但无 schema 校验、无法 tcpdump 读、Agent 侧依赖额外库 |
| (d) JSON + binary frame | 无收益，徒增 mismatch 风险 |

**理由**：原型期协议字段少（< 50 个），1m tick 频率下每秒 < 10 条消息，JSON 序列化成本远低于网络延迟。运维调试成本（看 frame 内容）压倒一切。

### 2.2 数值精度 `[v1 — frozen: 跨边界 string，Agent 内 decimal，SaaS 内 float64]`

铁律（已在 `docs/系统总体拓扑结构.md` §5.4 草案确立）：

- **跨 WS 边界**：所有"金额 / 数量 / 价格"用 string 字面值（保留精度）。字段后缀 `_decimal`。
- **Agent 内部**：用 `github.com/shopspring/decimal` 解析、运算、序列化。
- **SaaS 内部**：用 `float64`（与 `OrderIntent.QuantityUSD` / `EvaluablePlan` 同构）；wire 解析时 `decimal.Decimal.InexactFloat64()`。

**例外字段（基点 / 整数）**：

| 字段 | 类型 | 理由 |
|---|---|---|
| `actual_slippage_bps` | `float64`（直接 JSON number） | 基点本身已是损失精度的衍生量；保留 2 位小数足够 |
| 任何 `*_ms` 时间戳 | `int64` | 毫秒已是最小语义单位 |
| `decision_status` / `intent_kind` 等 enum | `string` | 自然枚举 |

### 2.3 时间戳 `[v1 — frozen: int64 毫秒 UTC，字段后缀 _ms]`

- **所有时间戳一律 int64 毫秒** UTC since epoch。
- 字段名约定：`*_ms` 后缀（`timestamp_ms` / `valid_until_ms` / `now_ms_at_saas` / `filled_at_exchange_ms` / `reported_at_ms`）。
- **Agent 侧禁用本地时钟做关键决策**：`valid_until_ms` 是 SaaS 时钟，Agent 比较时直接用 SaaS 给的 `now_ms_at_saas` 或本地 wall clock 之大者作"当前时间"（容忍 Agent 时钟漂移）。

### 2.4 信封 `[v1 — frozen]`

每条 WS 消息（含握手）都是同一个信封：

```json
{
  "msg_id":         "01H...",            // ULID, 26 字符, 发送方分配
  "type":           "TradeCommand",      // §3 表中枚举
  "schema_version": "v5.3.3",            // 从 internal/resultpkg/versions.go 读
  "timestamp_ms":   1700000000000,       // 发送方 wall clock
  "account_id":     "01H...",            // ULID; 鉴权后必填; 握手前可缺
  "payload":        { ... }              // §5 各类型字段
}
```

**强约束**：

- `msg_id` 用 **ULID**（与 Tier 2 schema CC3 一致）。生成方式：`github.com/oklog/ulid/v2` MonotonicEntropy。
- `schema_version` 必填，**Hub 收到 mismatch → 立即 `Error{code=schema_mismatch}` 并 close**。校验路径：直接字符串等于 `resultpkg.SchemaVersionV533`。
- `account_id` 在 `auth_ok` 之后必填；Hub 用此字段做 routing；Agent 端冗余携带（虽然连接已绑定）以便日志审计。
- `payload` 始终是 object（即使空，写 `{}` 不写 null）。

| 选项 | 影响 |
|---|---|
| **(a) 本稿单一信封 + payload** | type 字段分发清晰；嵌套深度可控；中间件可只解 envelope |
| (b) tagged union（type 与字段平铺） | 单层省解析但 `omitempty` 字段污染整张表 |
| (c) 多 endpoint（每类型一个 WS path） | 与"双向异步同连接"目标冲突 |

### 2.5 幂等键 `[v1 — frozen: client_order_id 是 TradeCommand 的幂等键，msg_id 仅作追溯]`

**核心区分**：

- `msg_id`（envelope 级）：每条消息唯一，用于"这条消息是否已收到"的 transport 幂等。Agent 端缓存最近 N 条 `msg_id`，重复直接丢弃（不重 ack）。
- `client_order_id`（payload 级）：每个**业务订单**唯一，用于"这单是否已下"的 application 幂等。Agent 端按 `client_order_id` 维护"已下单 → exchange_order_id"映射（持久化到本地 sqlite，§9.3）。

**Agent 收到重复 `TradeCommand`（同 client_order_id）的行为**：

1. 如映射表已有 → 返回缓存的 `Ack{exchange_order_id}`，不重新下单。
2. 如订单已 cancelled/rejected → 返回 `Ack{status=duplicate_terminal, reason=...}`。
3. 如订单仍 pending → 返回 `Ack{status=duplicate_pending, exchange_order_id=existing}`。

**SaaS 收到重复 `Ack`（同 msg_id ack 的同一 client_order_id）的行为**：

- 直接忽略（已被持久化）。重复 ack 是 Agent 重试，不是 bug。

### 2.6 schema_version 治理 `[v1 — frozen: 与 v5.3.3 强绑定，单一字面值禁散播]`

- WS 端 `schema_version` **必须等于** `resultpkg.SchemaVersionV533`（"v5.3.3"）。
- Agent 侧也从 wire 包统一常量读取，禁字面量。
- 版本提升（v5.3.3 → v5.3.4）需同时升 `internal/resultpkg/versions.go` 与 Agent 端依赖；不兼容时 Hub 直接拒新版本 Agent（fail-fast）。

## 3. 消息类型总表 `[v1 — frozen: 12 类]`

| 方向 | type 字符串 | 用途 | 期望响应 |
|---|---|---|---|
| Agent → SaaS | `hello` | 连接首条消息：Agent 自我介绍 + 协议版本 | `auth_required` |
| SaaS → Agent | `auth_required` | 要求 Agent 在 10s 内发 `auth` | — |
| Agent → SaaS | `auth` | 携带 token 鉴权 | `auth_ok` / `auth_fail` |
| SaaS → Agent | `auth_ok` | 鉴权通过；附 server_now_ms | — |
| SaaS → Agent | `auth_fail` | 鉴权失败；附 reason；Hub 收到回执前 close | — |
| SaaS → Agent | `state_sync_request` | 重连 / 首连后要求 Agent 上报当前持仓 | `state_sync_response` |
| Agent → SaaS | `state_sync_response` | 完整持仓 + 自上次心跳的 fills | — |
| SaaS → Agent | `trade_command` | 下发订单意图 | `ack` |
| Agent → SaaS | `ack` | 对 trade_command 的接受 / 拒绝 | — |
| Agent → SaaS | `order_update` | 订单生命周期事件（filled/cancelled/rejected/partial） | — |
| Agent → SaaS | `delta_report` | 周期性账户净值 / 持仓差 / 错误流水 | — |
| SaaS → Agent | `ping` | 30s 心跳 | `pong` |
| Agent → SaaS | `pong` | 心跳响应 | — |
| SaaS → Agent | `kill_switch` | 紧急停止该 Agent 全部交易 | `ack` |
| SaaS → Agent | `graceful_shutdown` | SaaS 重启前广播 | — |
| 双向 | `error` | 异常上报；附 code/reason | — |

> **注**：`error` 是双向的"应用错误"消息（不同于 transport-level 错误，比如 TLS 失败）。

**type 字符串 lowercase + snake_case**，与 `OrderIntent.Kind` 枚举字面量风格保持一致。

## 4. 连接生命周期 `[v1 — frozen]`

### 4.1 物理层

| 项 | 值 |
|---|---|
| WS 端口 | **8081**（SaaS 监听；Agent 出站连接） |
| WS Path | `/api/v1/ws/agent` |
| TLS | `app_role=saas` 强制 TLS 1.3；`app_role=dev` 可降级 ws://（仅本地调试） |
| Subprotocol | 无 |
| 帧最大长度 | 64 KB（state_sync_response 含全部持仓，1 KB 量级，64 KB 留 60x 安全空间） |
| Ping/Pong 用 WS 控制帧？ | **否**——用 application-level `ping`/`pong` 消息（带 envelope），便于跨语言一致和 msg_id 追溯 |

### 4.2 握手序列

```
TCP connect
  ↓
TLS handshake (app_role=saas) / 跳过 (dev)
  ↓
HTTP GET /api/v1/ws/agent  → 101 Switching Protocols
  ↓
[t=0]    Agent → SaaS: hello { agent_version, account_id, schema_version }
  ↓
[<100ms] SaaS  → Agent: auth_required {}
  ↓
[<10s]   Agent → SaaS: auth { token: "..." }
  ↓
[<100ms] SaaS  → Agent: auth_ok { server_now_ms } | auth_fail { reason } + close
  ↓
[<100ms] SaaS  → Agent: state_sync_request {}
  ↓
[<1500ms] Agent → SaaS: state_sync_response { positions, since_last_fills }
  ↓
SaaS 用快照覆盖内存 PortfolioSnapshot；mismatch 写 discrepancy_event（§6.3）
  ↓
Ready — 双向消息流开始 / 心跳启动
```

**超时**：

- `auth` 未在 **10s** 内到达 → SaaS 主动 close（code=auth_timeout）。
- `state_sync_response` 未在 **1500ms** 内到达 → Agent 标记 `degraded`，Hub 暂停 `trade_command` 下发，告警；连接保留，下次 ping 触发重试。

### 4.3 鉴权机制 `[v1 — frozen: 长效 token + bcrypt 哈希持久化]`

**不复用** `internal/saas/auth/` 的 user JWT。Agent 是 service-to-service，需独立机制。

新建表 `agent_tokens`（Tier 2 扩展，但不进 Phase 6 `models.go`——挂在 §7.2 Phase 7/8 实施）：

```go
type AgentToken struct {
    ID         uint      `gorm:"primaryKey"`
    CreatedAt  time.Time `gorm:"index"`
    UpdatedAt  time.Time
    AgentID    string    `gorm:"type:varchar(32);uniqueIndex"`  // ULID
    AccountID  string    `gorm:"type:varchar(32);index"`        // ULID; 与 StrategyInstance.OwnerUserID 关联
    TokenHash  string    `gorm:"type:varchar(60);not null"`     // bcrypt cost=12
    Label      string    `gorm:"type:varchar(64)"`              // "MacBook Pro - Binance Spot"
    LastSeenAt *time.Time
    RevokedAt  *time.Time
}
```

**鉴权流程**：

1. SaaS 收到 `auth{token}` → 取 token 前 N 字符作为 `agent_id` 索引（避免全表 bcrypt）。**最简版**：token 形如 `agt_<ULID>_<32-byte-secret-base64>`，`agent_id` 在 token 字符串里明示。
2. SELECT WHERE agent_id = ... AND revoked_at IS NULL。
3. `bcrypt.CompareHashAndPassword(row.TokenHash, secret)` → 通过 = `auth_ok`。
4. 更新 `last_seen_at`（异步，不阻塞 ack）。

**Token 生命**：

- 由管理员手工生成（HTTP API `POST /api/v1/agent_tokens` admin-only），明文只返回一次。
- 无 TTL；显式 `revoked_at` 撤销。
- 一个 `agent_id` 对应一个 token（轮换 = 创建新 + 撤销旧）。

| 选项 | 影响 |
|---|---|
| **(a) 本稿：长效 token + bcrypt** | 部署一次配置；Agent 离线后无需续期；bcrypt 抗暴破；表行数 = Agent 数（< 1000）查询性能足够 |
| (b) JWT（同 user） | 24h TTL 与 Agent 持久部署场景不匹配；过期就断 = 噪声 |
| (c) mTLS（客户端证书） | 安全性最强；但 Agent 端配置 / 轮换 / 调试 cost 远超 token |
| (d) HMAC challenge-response | 不能持久化"撤销"语义；token 表存 secret 反而更难 |

### 4.4 心跳协议 `[v1 — frozen: 30s ping / 5s pong / 3 misses → STALE]`

- SaaS Hub 每 **30s** 发 `ping` 给每个 Agent 连接。
- Agent 必须在 **5s** 内回 `pong`。
- 连续 **3 次** ping 无 pong（≈ 90s 无 pong）→ Hub 标记该连接 `STALE`，主动 close + 暂停下发 `trade_command`。
- Agent 端反向检测：60s 内未收到 ping → 主动 close + 进入 reconnecting。
- `pong.payload.echo_msg_id` 必须等于触发它的 `ping.msg_id`（防止跨周期串扰）。

**为何 30/5/90 而非 10/5/30（拓扑文档 §5.7 原草案）**：

| 项 | 原草案 (§5.7) | 本稿 | 理由 |
|---|---|---|---|
| 心跳间隔 | 10s | 30s | 1m cron tick 节拍下，10s 心跳过密；30s 与 §2 app_role 行为矩阵的 30s 一致（已有的不变值） |
| pong 超时 | 5s | 5s | 不变 |
| 触发 STALE | 3 miss | 3 miss | 不变 |
| 离线判定 | ~30s | ~90s | 一次拥塞峰值 / GC 暂停不应触发误判 |
| 与 §2 矩阵 | — | 对齐 30s 行 | 消除 §2 与 §5.7 的数值不一致 |

### 4.5 重连退避 `[v1 — frozen: 500ms→60s 8 段，超 1h 仍重试]`

退避序列（毫秒，含抖动）：

```
500, 1000, 2000, 4000, 8000, 16000, 32000, 60000
```

- 序号 i 的退避 = `base[i] × (0.8 + 0.4 × rand)`（±20% jitter，避雪崩）。
- 第 8 次后封顶 60s，无限重试。
- 重连成功 → 重置序号到 0。
- 累计断线时长 ≥ 1h 进入 `fatal` 状态：本地告警（systemd/syslog），但**不停止重试**（与拓扑文档 §6.3 一致）。

**为何不采纳拓扑文档 §6.3 的 `500, 1000, 2000, 4000, 8000, 16000, 32000, 60000` 完整序列**：

实际就是采纳了，本稿与拓扑文档 §6.3 数值**完全一致**，仅明确 jitter 与 fatal 行为。

### 4.6 优雅停机

- SaaS 收到 SIGTERM → 向所有 Agent 广播 `graceful_shutdown{reason, retry_in_ms: 5000}`。
- Agent 收到后：
  1. 不再接受新 `trade_command`（实际此时 SaaS 已不发了）。
  2. 完成在途 `ack`/`order_update` 上送（best-effort 2s 窗口）。
  3. 主动 close 连接。
  4. 等 `retry_in_ms`（5s）→ 进入 reconnecting 流程（不走指数退避，直接尝试）。
- SaaS 端 close 所有连接后再退 HTTP server，避免半挂连接被 systemd kill -9。

## 5. 各消息类型字段 `[v1 — frozen]`

> **约定**：所有 payload 字段名 snake_case；可选字段 `omitempty`；`*_decimal` 字段必为 string；`*_ms` 字段必为 int64。

### 5.1 `hello`（Agent → SaaS）

```json
{
  "agent_version": "0.1.0",
  "account_id":    "01H...",
  "schema_version": "v5.3.3",
  "platform":      "linux/amd64",        // 可选 — 诊断用
  "exchange":      "binance_spot",       // 可选 — 多交易所时区分；v1 单一时填 "binance_spot"
  "environment":   "mainnet"             // 可选[additive] — "mainnet" | "testnet" | "mock"；见下
}
```

`account_id` 在 envelope.account_id 与 payload 重复携带：envelope 用于 routing，payload 用于审计 / token mismatch 检测。

`environment`（增量字段，backlog ⑥）：Agent 从 `exchange.base_url` 推导其交易环境。Hub 若配了 `live.expected_environment`，握手时比对：`app_role=saas` 不一致 → `auth_fail{code=environment_mismatch}` 硬拒；dev/lab 仅告警放行（保留 mainnet-klines + testnet-agent 的测试工作流）。字段缺省（pre-⑥ Agent）→ 跳过断言，向后兼容。

### 5.2 `auth_required`（SaaS → Agent）

`payload: {}` — 信封足够；payload 留空 object。

### 5.3 `auth`（Agent → SaaS）

```json
{ "token": "agt_01H..._<base64>" }
```

### 5.4 `auth_ok`（SaaS → Agent）

```json
{
  "server_now_ms": 1700000000000,        // Agent 用此校准时钟漂移（仅日志，不参与决策）
  "agent_id":      "01H..."              // ULID, SaaS 侧绑定的 agent_id（写入审计）
}
```

### 5.5 `auth_fail`（SaaS → Agent）

```json
{
  "code":   "invalid_token" | "revoked" | "schema_mismatch" | "account_mismatch" | "environment_mismatch",
  "reason": "human-readable"
}
```

SaaS 发完此消息立即 close（500ms 内）。Agent 端收到 `invalid_token` / `revoked` 不进入退避循环（无限重试无意义）——直接 fatal 告警。`environment_mismatch`（增量，backlog ⑥）：仅 `app_role=saas` 在 `hello.environment` 与 `live.expected_environment` 不一致时发出（dev/lab 改为告警放行）。

### 5.6 `state_sync_request`（SaaS → Agent）

`payload: {}`。

### 5.7 `state_sync_response`（Agent → SaaS）

```json
{
  "reported_at_ms": 1700000000123,
  "positions": [
    { "symbol": "BTC",  "free_decimal": "0.5000000",  "locked_decimal": "0.0" },
    { "symbol": "USDT", "free_decimal": "1000.00",    "locked_decimal": "0.0" }
  ],
  "open_orders": [
    {
      "client_order_id":   "01H...",
      "exchange_order_id": "12345678",
      "symbol":            "BTCUSDT",
      "side":              "buy",
      "order_type":        "limit",
      "quantity_decimal":  "0.001",
      "filled_quantity_decimal": "0.0003",
      "limit_price_decimal":"65000.00",
      "status":            "partial_filled",
      "placed_at_ms":      1700000000000
    }
  ],
  "since_last_fills": [
    {
      "client_order_id":   "01H...",         // 关联本 Agent 之前下过的单
      "exchange_order_id": "12345677",
      "fill_quantity_decimal": "0.0005",
      "fill_price_decimal":    "64900.00",
      "fill_fee_asset":        "BNB",
      "fill_fee_amount_decimal":"0.00012",
      "filled_at_exchange_ms": 1699999990000,
      "actual_slippage_bps":   3.2
    }
  ],
  "last_seen_msg_id": "01H..."              // 可选 — Agent 上次收到的最后一条 trade_command msg_id，便于 SaaS 重发缺失 since
}
```

**SaaS 处理**：

- positions：直接覆写内存 `PortfolioSnapshot`（ground truth）。
- open_orders：与本地 `TradeRecord` 对账；多出来的（exchange 有 Agent 没传过 client_order_id 的）→ 写 `discrepancy_event`。
- since_last_fills：批量写 `SpotExecution`（去重 by exchange_order_id+filled_at_exchange_ms）。

### 5.8 `trade_command`（SaaS → Agent）

```json
{
  "intent_kind":         "macro",                  // "macro" | "micro"
  "client_order_id":     "01H...",                 // ULID, SaaS 分配
  "instance_id":         "01H...",                 // SaaS 内的 StrategyInstance.InstanceID
  "symbol":              "BTCUSDT",
  "side":                "buy",                    // "buy" | "sell"
  "order_type":          "market",                 // "market" | "limit"
  "quantity_decimal":    "0.00123456",             // 资产单位（不是 USD）
  "limit_price_decimal": "65000.00",               // omitempty — market 单不填
  "valid_until_ms":      1700000060000,            // 过期不下单（Agent 端比 wall clock）
  "now_ms_at_saas":      1700000000000             // SaaS NowMs at emit，审计用
}
```

**SaaS 端 OrderIntent → TradeCommand 转换规则**：

- `intent_kind = OrderIntent.Kind`（macro/micro 直传）
- `client_order_id`：使用 `OrderIntent.ClientOrderID`（必须是 ULID；若 OrderIntent 中为空，SaaS 即时填充）
- `quantity_decimal`：从 `OrderIntent.QuantityUSD / latest_close_price` 换算后转 string（保留 8 位小数适配 BTC；Phase 7 通过 `symbol → quantization_step` 表查精度）
- `limit_price_decimal`：从 `OrderIntent.LimitPrice` float64 转 string（保留 8 位小数）
- `valid_until_ms`：直传 `OrderIntent.ValidUntilMs`
- `now_ms_at_saas`：tick outer `time.Now().UnixMilli()`（cron 唯一时钟读取点）

### 5.9 `ack`（Agent → SaaS）

```json
{
  "client_order_id":     "01H...",
  "status":              "accepted",          // "accepted" | "rejected" | "expired" | "duplicate_pending" | "duplicate_terminal"
  "exchange_order_id":   "12345678",          // omitempty — rejected/expired 时缺
  "exchange_now_ms":     1700000000050,       // Agent 收到 exchange 响应的时间
  "reject_reason":       "insufficient_funds" // omitempty — 仅 rejected/duplicate_terminal 填
}
```

**status 枚举**：

| status | 含义 |
|---|---|
| `accepted` | 已提交给交易所并拿到 exchange_order_id |
| `rejected` | 交易所拒（余额 / 风控 / 参数） |
| `expired` | Agent 看到 valid_until_ms < wall clock，没下单 |
| `duplicate_pending` | 同 client_order_id 已存在 pending 单 |
| `duplicate_terminal` | 同 client_order_id 已存在终态单（cancelled/filled/rejected） |

`exchange_now_ms` 用于诊断 Agent → exchange 延迟。

### 5.10 `order_update`（Agent → SaaS）

```json
{
  "client_order_id":     "01H...",
  "exchange_order_id":   "12345678",
  "status":              "filled",            // "filled" | "partial_filled" | "cancelled" | "rejected"
  "fills": [
    {
      "fill_quantity_decimal":   "0.001",
      "fill_price_decimal":      "65010.00",
      "fill_fee_asset":          "BNB",
      "fill_fee_amount_decimal": "0.00012",
      "filled_at_exchange_ms":   1700000000123,
      "actual_slippage_bps":     1.54        // Agent 计算 = (fill_price - limit_or_market_ref) / ref × 10000
    }
  ],
  "cumulative_filled_quantity_decimal": "0.001"
}
```

- 每条 `order_update` 至少含 1 个 fill（status=partial_filled / filled）或 0 个 fill（status=cancelled / rejected）。
- `actual_slippage_bps` 计算基准：
  - market 单：`(fill_price - market_ref_at_submit) / market_ref_at_submit × 10000`，`market_ref_at_submit` = Agent 提交瞬间的 best bid/ask（Agent 端必须缓存）。
  - limit 单：`(fill_price - limit_price) / limit_price × 10000`。

**SaaS 端处理**：

- 每条 fill 落 `SpotExecution`（去重 by exchange_order_id+filled_at_exchange_ms）。
- `actual_slippage_bps` 持久化到 `SpotExecution.ActualSlippageBPS`（已有字段，§D 组）。
- 累计 `TradeRecord.Status` 更新：partial → partial_filled；first full → filled。

### 5.11 `delta_report`（Agent → SaaS）

低频（每 60s）账户层面差量，用于对账：

```json
{
  "reported_at_ms": 1700000000000,
  "positions": [
    { "symbol": "BTC",  "free_decimal": "0.5", "locked_decimal": "0.0" }
  ],
  "since_last_report": {
    "fills":  [ /* §5.10 fill 结构数组 */ ],
    "errors": [
      { "code": "exchange_rate_limit", "message": "...", "occurred_at_ms": 1700000000000 }
    ]
  }
}
```

`delta_report.since_last_report.fills` 与 `order_update.fills` 是冗余的（同源），SaaS 端按 `client_order_id+filled_at_exchange_ms` 去重。`delta_report` 是兜底通道（防 `order_update` 丢包），`order_update` 是热路径。

### 5.12 `ping` / `pong`

```json
// ping (SaaS → Agent)
{ "server_now_ms": 1700000000000 }

// pong (Agent → SaaS)
{
  "echo_msg_id":        "01H...",          // 触发本 pong 的 ping.msg_id
  "agent_now_ms":       1700000000010,
  "exchange_reachable": true               // Agent 最近一次 exchange API 调用是否 200
}
```

`exchange_reachable=false` 是软告警，SaaS 不立即停发 `trade_command`，但写 audit log。

### 5.13 `kill_switch`（SaaS → Agent）

```json
{
  "reason":          "manual_admin_action" | "discrepancy_detected" | "compliance_freeze",
  "operator_user_id":"01H...",                // ULID，从 user JWT 取
  "scope":           "all" | "symbol",        // v1 frozen: 仅 "all" — symbol 级粒度 Part III
  "symbol":          ""                       // v1 留空字符串
}
```

**Agent 收到后**：

1. 立即 cancel 所有 open orders（best-effort）。
2. 返回 `ack{status=accepted, exchange_order_id=""}`。
3. 转入 `frozen` 状态：拒收任何后续 `trade_command`（回 `ack{status=rejected, reason=killed}`），直到 SaaS 端 push 一条 `kill_switch{reason=...,scope=all,symbol=resume}` 解冻。

**Resume（§5.13 v2，已实现）**：SaaS push `kill_switch{symbol=resume}`（复用本消息，非新类型），Agent 清除 `frozen` 硬闩、ack 接收、恢复接单——无需重启进程（v1 的解冻路径是重启 Agent，仍可用）。服务端 `POST /api/v1/instances/:id/resume`（admin-only）发出 resume，并同步：① 清 `driftStreak` 重新武装 auto-freeze（否则漂移仍在时安全网不会再触发），② 记 `instance.resume` 审计事件——`/live` 红 banner 据「最近一条 kill/resume 谁更新」判定，resume 后自动消失。

### 5.14 `graceful_shutdown`（SaaS → Agent）

```json
{
  "reason":      "saas_restart" | "saas_maintenance",
  "retry_in_ms": 5000
}
```

§4.6 已述。

### 5.15 `error`（双向）

```json
{
  "code":         "schema_mismatch" | "unknown_type" | "decode_failed" | "internal_error" | "invalid_envelope",
  "message":      "human-readable",
  "ref_msg_id":   "01H..."                 // omitempty — 哪条消息触发的本错误
}
```

接收 `error` 的一侧只记日志 + audit log，**不**自动重试。

## 6. 端到端流程图

### 6.1 正常 trade_command 路径

```
SaaS (cron tick)
  └─ Step() 输出 OrderIntent{ClientOrderID="A"}
     └─ Dispatcher 转 TradeCommand → WS Hub
        ├─ Hub 写本地 TradeRecord{Status=pending, client_order_id="A"}
        └─ WS 帧 → Agent
                    ├─ 接收 → 查本地 idempotency 表(A 不存在)
                    ├─ 调 exchange POST /api/v3/order
                    ├─ exchange 返回 order_id=42
                    ├─ 写本地 idempotency{A → 42}
                    └─ ack{client_order_id=A, status=accepted, exchange_order_id=42}
                        ↓
SaaS:
  └─ TradeRecord.Status = acked, ExchangeOrderID=42

[一会儿后 exchange 推 fill]
Agent → SaaS: order_update{client_order_id=A, status=filled, fills=[...]}
SaaS:
  └─ SpotExecution INSERT (含 ActualSlippageBPS)
  └─ TradeRecord.Status = filled
  └─ SpotLot 维护（macro 单建仓；micro 单按对仓规则）
```

### 6.2 重连 + 状态同步

```
Agent: 检测到 conn closed → 状态机 → reconnecting (退避 §4.5)
  └─ 尝试 conn → 失败 → 退避 → 重试 ...
  └─ 成功 conn → §4.2 握手 → state_sync_response 上报快照
SaaS:
  ├─ 用快照覆写 PortfolioSnapshot（ground truth）
  ├─ since_last_fills 批量写 SpotExecution（去重）
  └─ 检测 open_orders 与本地 TradeRecord 差异 → discrepancy_event
```

### 6.3 STALE 检测与 Kill Switch

```
Hub: 30s ping × 3 次无 pong → STALE
  ├─ AuditLog{action=agent.heartbeat_stale, subject=agent:<id>}
  ├─ Redis: agent:{accountID}:status = "stale"（§7.2）
  ├─ 暂停下发 trade_command（Manager 端通过 Resolver 查 status 跳过 dispatch）
  └─ 主动 close conn
Agent 端:
  └─ 进入 reconnecting → §6.2 流程
```

### 6.4 重复 trade_command 幂等（同一 client_order_id 两次）

```
[场景：SaaS 发 trade_command_A → 网络抖动 → ack 没收到 → SaaS 端 Retry 发了第二条 trade_command_A']

SaaS:
  └─ trade_command{client_order_id=A, msg_id=M1}      # 第一条
  └─ [ack 丢失]
  └─ trade_command{client_order_id=A, msg_id=M2}      # 第二条（同 client_order_id）

Agent 收 M1:
  ├─ 调 exchange → exchange_order_id=42
  ├─ 写 idempotency{A → 42}
  └─ ack{client_order_id=A, msg_id=N1, status=accepted, exchange_order_id=42}
     [N1 网络丢]

Agent 收 M2:
  ├─ envelope.msg_id=M2 ≠ M1 → 不走 envelope 级幂等
  ├─ payload.client_order_id=A → 查 idempotency → 命中
  └─ ack{client_order_id=A, msg_id=N2, status=duplicate_pending|accepted, exchange_order_id=42}
     [N2 网络通]

SaaS 收 N2 → TradeRecord.Status=acked, exchange_order_id=42
```

## 7. SaaS 侧资源决策

### 7.1 端口 `[v1 — frozen: 8081]`

退役 `docs/系统总体拓扑结构.md` §1.1 端口分配 `[INVENTED v1]` 标记。已在 `internal/saas/config/config.go:166-167` 默认 `:8081`。

### 7.2 Agent 在线状态 `[v1 — frozen: Redis agent:{accountID}:status, TTL 60s]`

退役 `docs/系统总体拓扑结构.md` §4.2 `[INVENTED v1]` 行。

Redis 键：

```
key:   agent:{accountID}:status
value: JSON { agent_id, connection_state, last_seen_ms, last_msg_id }
TTL:   60s（每次收到 pong 或任何 Agent → SaaS 消息时刷新）
```

`connection_state` enum: `connecting` | `authed` | `ready` | `stale` | `disconnected`。

**TTL 60s 与 §4.4 90s STALE 判定**：TTL 短一些，让"Agent 在线"在心跳停止 60s 后自动消失；90s 是 Hub 显式标记 STALE 的阈值。两个数值并存因为：Redis TTL 用于横向查询（"实例所属 Agent 在线吗？"），STALE 用于纵向决策（"这条连接要不要踢掉？"）。

**回源策略（铁律 6）**：Redis miss → 回源 `agent_tokens.last_seen_at`（最多 30s 前的快照）。

### 7.3 Hub 包结构 `[v1 — frozen: internal/saas/wshub/]`

- 包路径：`internal/saas/wshub/`
- 关键类型：`Hub`（fan-out manager）、`Connection`（per-Agent 连接）、`Registry`（accountID → Connection）。
- 注入到 `instance.Manager`：通过 `WSHubDispatcher` 实现 `TradeCommandDispatcher` 接口，替代 `LogDispatcher`。
- 与 `auth.Service` 关系：Agent 鉴权独立（§4.3），不复用 user JWT；新增 `internal/saas/agentauth/` 子包。

### 7.4 Wire types 包 `[v1 — frozen: internal/wire/]`

- 包路径：`internal/wire/`
- 内容：信封 struct（`Envelope`）+ 所有 12 类 payload struct（每类一个文件，例：`tradecommand.go` / `ack.go`）+ type 字符串常量 + JSON marshal/unmarshal helpers。
- **跨进程共用**：`cmd/saas/` 和 `cmd/agent/` 都 import 此包；这是 Agent 与 SaaS 唯一允许共享的代码。
- **零依赖**：仅 import `encoding/json`、`fmt`、`quantlab/internal/resultpkg`（取 SchemaVersion）。**不**依赖 GORM、Gin、Redis。

## 8. Agent 侧资源决策

### 8.1 `config.agent.yaml` schema `[v1 — frozen]`

```yaml
agent_id:      "01H..."                          # ULID, SaaS 端生成 token 时同步分配
account_id:    "01H..."                          # ULID, 关联 StrategyInstance.OwnerUserID
saas_url:      "wss://saas.example.com:8081"     # 含 schema/host/port
saas_token:    "agt_01H..._<base64>"             # 长效 token, §4.3
exchange:
  name:        "binance_spot"                    # v1 单选
  api_key:     "..."                             # 铁律 3 — 永不上送
  api_secret:  "..."                             # 同上
  base_url:    "https://api.binance.com"         # 可换成 testnet
log:
  level:       "info"
  path:        "/var/log/quantlab-agent.log"
idempotency:
  db_path:     "/var/lib/quantlab-agent/idempotency.sqlite"
  retention_days: 7                              # 仅保留 7d 内 client_order_id 映射
```

**铁律 3**：`exchange.api_key` / `api_secret` 字段**任何代码路径都不进入 WS 消息**。

### 8.2 ActualSlippageBps 观测 `[v1 — frozen: Agent 端必须缓存 best bid/ask at submit]`

- Agent 提交 market 单前，必须从交易所 ticker stream（或最近 REST orderbook snapshot）取最新 best ask（buy）/ best bid（sell），存到 `pending_order.market_ref_at_submit_decimal`。
- 收到 fill 时：`slippage_bps = (fill_price - market_ref) / market_ref × 10000`（buy 正值为 worse-than-expected，sell 反号）。
- 限价单：`slippage_bps = (fill_price - limit_price) / limit_price × 10000`。
- 计算用 `decimal.Decimal`，最终序列化到 wire 时 `.InexactFloat64()` 转 float（已在 §2.2 例外字段）。

退役 `docs/Coding-plan-dev-phases-prompts_v3_2_2.md` Phase 7 第一条要求（"Agent 端必须独立实现摩擦观察"）的待实施状态。

### 8.3 本地幂等表（sqlite）

```sql
CREATE TABLE idempotency (
  client_order_id     TEXT PRIMARY KEY,
  exchange_order_id   TEXT,
  status              TEXT NOT NULL,    -- pending / accepted / filled / cancelled / rejected
  market_ref_decimal  TEXT,             -- best bid/ask at submit
  submitted_at_ms     INTEGER NOT NULL,
  last_updated_ms     INTEGER NOT NULL
);
CREATE INDEX idx_idem_last_updated ON idempotency(last_updated_ms);
```

- 写时机：trade_command 接收 → INSERT pending；exchange ack → UPDATE accepted+exchange_order_id；order_update → UPDATE 终态。
- 清理：每次 Agent 启动 `DELETE WHERE last_updated_ms < now - 7d`。
- 复活路径：Agent crash 后重启 → 读 sqlite → state_sync_response 时把 7d 内的 pending/filled 全 push（让 SaaS 对账）。

## 9. 代码影响 — 标记退役表

| # | 位置 | 当前 marker | 本稿后处置 |
|---|---|---|---|
| 1 | `internal/strategy/contract.go:49` 行注释块 | `[INVENTED v1 — needs architect review before Phase 7 LocalAgent.]` 整段 | 删除注释块，保留 struct；新增 1 行：`// Aligned with docs/saas-ws-protocol-v1.md §5.8.` |
| 2 | `internal/strategy/contract_test.go:8` | `TestOrderIntentRoundTrip verifies the [INVENTED v1] OrderIntent draft` | 注释改为 `verifies the OrderIntent → TradeCommand mapping defined in saas-ws-protocol-v1.md §5.8` |
| 3 | `internal/saas/store/models.go:11` | `marked [INVENTED v1 — sync with TradeCommand v1]` | 改为 `aligned with docs/saas-ws-protocol-v1.md §5.8/§5.9/§5.10` |
| 4 | `internal/saas/store/models.go:314` 注释块 | `[INVENTED v1 — sync with TradeCommand v1]: this group's field set depends on the OrderIntent / WS TradeCommand protocol that lands in Phase 7. Current shape is the design-doc v1 alignment; review once internal/strategy/contract.go:54 OrderIntent freezes.` | 删除整段，替换：`// D group fields aligned with docs/saas-ws-protocol-v1.md §5.8-5.10 (frozen 2026-05-20).` |
| 5 | `internal/saas/store/models.go:361` `ClientOrderID` 行 | `// [INVENTED v1 — sync with TradeCommand v1]` | 删 |
| 6 | `internal/saas/store/models.go:364` `Side` 行 | `// [INVENTED v1 — sync with OrderIntent.OrderSide]` | 删 |
| 7 | `internal/saas/store/models.go:365` `OrderType` 行 | `// [INVENTED v1 — sync with OrderIntent.OrderType]` | 删 |
| 8 | `internal/saas/store/models.go:369` `ValidUntilMs` 行 | `// [INVENTED v1 — sync with TradeCommand v1]` | 删 |
| 9 | `internal/saas/store/models.go:455` `AllModels` 注释 | `// Tier 2 (frozen 2026-05-20; Group D fields tagged [INVENTED v1 — sync with TradeCommand v1] inline)` | 改为 `// Tier 2 (frozen 2026-05-20; Group D wire-aligned per saas-ws-protocol-v1.md)` |
| 10 | `internal/saas/instance/manager.go:73-104` | `LogDispatcher` 作为 stub | 保留（用于 dev/test），新增 `wshub.Dispatcher`（Phase 7/8 实施层） |
| 11 | `docs/系统总体拓扑结构.md` §1.1 | 端口 8081 是 [INVENTED v1] | 在 §548 待审清单中划掉 §1.1 端口 |
| 12 | `docs/系统总体拓扑结构.md` §2 矩阵 | `WS Agent 心跳超时 [INVENTED v1] 30s/n/a/60s` | 注释指向本稿 §4.4（30s ping × 3 = 90s STALE 是真值） |
| 13 | `docs/系统总体拓扑结构.md` §4.2 | `Agent 在线状态 [INVENTED v1] Redis agent:{accountID}:status TTL 60s` | 注释指向本稿 §7.2 |
| 14 | `docs/系统总体拓扑结构.md` §5 全章 | `[INVENTED v1 — needs architect review]` | 章首加 banner：本章已被 saas-ws-protocol-v1.md §3-§5 取代；本章保留作历史归档 |
| 15 | `docs/系统总体拓扑结构.md` §6.3 | `[INVENTED v1]` 重连退避 | 注释指向本稿 §4.5（数值未变，仅增加 jitter + fatal 行为） |
| 16 | `docs/系统总体拓扑结构.md` §548 待审清单 | 6 条 | 全部划掉，注脚指向本稿 |

## 10. 实施顺序（Phase 7 / 8 落地）

**本稿不**强制具体编码节奏；以下是建议路径，供 Phase 7/8 PR 拆分参考：

```
Step 1: 新建 internal/wire/  (无 IO，纯 struct + JSON helpers)
        └─ TestWireRoundTrip × 每条消息类型
        └─ 退役 markers 1-9（contract.go + models.go）
        └─ 不动 dispatcher / hub / agent

Step 2: 新建 internal/saas/agentauth/  (token 表 + bcrypt 验签)
        └─ AutoMigrate AgentToken
        └─ TestAgentAuthHappyPath / TestRevokedTokenRejected

Step 3: 新建 internal/saas/wshub/ (服务端)
        └─ 实现 §4.2 握手 + §4.4 心跳 + §5.* 各消息 dispatch
        └─ 实现 TradeCommandDispatcher 接口 → 取代 LogDispatcher 在 cmd/saas/main.go 的注入
        └─ TestHubHandshake / TestHubHeartbeatStale / TestHubKillSwitch

Step 4: 新建 cmd/agent + internal/agent/  (客户端)
        └─ config.agent.yaml 加载
        └─ exchange.Mock 适配（先 mock，Binance 后做）
        └─ idempotency sqlite
        └─ 握手 + 心跳 + 重连退避
        └─ TestAgentReconnectBackoff / TestAgentIdempotency

Step 5: 接通端到端 (saas + agent in-process test)
        └─ TestEndToEndTradeCommand
```

**关键并行机会**：Step 1 完成后，Step 2/3 与 Step 4 可并行（前者后端，后者客户端，contract by wire package）。

## 11. 待审清单 / Open questions

**全部冻结**：22/22。本稿 §3-§9 所列决策无 `[INVENTED v1]` 残留。

**已知 v1 限制**（不阻塞 Phase 7/8，标记为未来扩展）：

| # | 限制 | 触发后续工作的条件 |
|---|---|---|
| Q1 | KillSwitch 只支持 `scope=all` | Phase 9 多策略并发时需 `scope=symbol` / `scope=instance` |
| Q2 | 1 Agent : 1 account 假设 | 用户连第二个交易所账号时升级（v1 需开第二个 Agent 进程） |
| Q3 | Token 无 TTL，靠手工 revoke | 大规模商业化时需自动轮换 |
| Q4 | `exchange_reachable` 仅软告警，不触发自动 stop | 交易所长时间 down 时需自动 kill_switch（v2 增强） |
| Q5 | Agent crash 后 idempotency.sqlite 损坏的处理 | 损坏 → fail-fast 启动，要求人工介入 / 重置 |
| Q6 | wire 包跨进程版本兼容（旧 Agent + 新 SaaS） | 暂只接受 schema_version 严格等；未来需要灰度时引入 minor version |
| Q7 | 多 fill 一条 order_update vs 每 fill 一条 | 本稿 §5.10 是数组；exchange WS 通常每 fill 一条，Agent 端 batch 1-3 个 → 单条 order_update |

## 附录 A. 与基线文档的引用矩阵

| 本稿章节 | 退役 / 改写 |
|---|---|
| §1 范围 | 取代 `docs/系统总体拓扑结构.md` §5 全章 + §6.3 |
| §2 通用决策 | 取代 §5.1 通信原则 |
| §3 消息类型表 | 取代 §5.2 |
| §4 连接生命周期 | 取代 §5.6 + §5.7 + §6.3 + §6.4 局部 |
| §5 各消息字段 | 取代 §5.3 / §5.4 / §5.5（增 6 类） |
| §6 端到端流程 | 取代 §5.6 状态收敛文字描述 |
| §7 SaaS 侧资源 | 取代 §1.1 端口 + §4.2 在线状态行 + §2 心跳行 |
| §8 Agent 侧资源 | 取代 `docs/Coding-plan-dev-phases-prompts_v3_2_2.md` Phase 7 4 条要求中的前 2 条 |
| §9 标记退役 | 直接对接 `internal/strategy/contract.go` + `internal/saas/store/models.go` |
| §10 实施顺序 | 拆解 `docs/Coding-plan-dev-phases-prompts_v3_2_2.md` Phase 7 / 8 |

## 附录 B. 与 Tier 2 schema v1（`saas-tier2-schema-v1.md`）的衔接

D 组（SpotLot / TradeRecord / SpotExecution）字段已在 Tier 2 schema 冻结，本稿仅**约束 wire 形态**，不修改 GORM struct。具体对应：

| Tier 2 GORM 字段 | wire 消息 / 字段 | 备注 |
|---|---|---|
| `TradeRecord.ClientOrderID` | `trade_command.client_order_id` | ULID, 同源 |
| `TradeRecord.Symbol/Side/OrderType` | `trade_command.symbol/side/order_type` | 1:1 |
| `TradeRecord.QuantityUSD` (float64) | `trade_command.quantity_decimal` (string) | SaaS 端用 latest price 转换；wire 是 asset unit |
| `TradeRecord.LimitPrice` (float64 ptr) | `trade_command.limit_price_decimal` (string omitempty) | 1:1 转换 |
| `TradeRecord.ValidUntilMs` | `trade_command.valid_until_ms` | 1:1 |
| `TradeRecord.NowMsAtSaaS` | `trade_command.now_ms_at_saas` | 1:1 |
| `TradeRecord.Status` | `ack.status` + `order_update.status` 累积 | enum 与 §5.9 一致 |
| `SpotExecution.ClientOrderID` | `order_update.client_order_id` / `delta_report.since_last_fills[].client_order_id` | 1:1 |
| `SpotExecution.ExchangeOrderID` | `ack.exchange_order_id` / `order_update.exchange_order_id` | Agent 提交后填 |
| `SpotExecution.FillQuantity/Price/Fee*` | `order_update.fills[].fill_*_decimal` (string → SaaS 端解 float64) | 跨边界精度协议 |
| `SpotExecution.FilledAtExchangeMs` | `order_update.fills[].filled_at_exchange_ms` | 1:1 |
| `SpotExecution.ActualSlippageBPS` | `order_update.fills[].actual_slippage_bps` (float64) | Agent 端计算（§8.2） |
| `SpotLot.*` | (无 wire 字段) | SaaS 内部账本，由 Lot 维护规则从 SpotExecution 派生 |

**关键不一致**（已在本稿解决）：

- Tier 2 `TradeRecord.QuantityUSD float64` vs wire `quantity_decimal string` → SaaS 端 dispatcher 在转换时同时记 USD（落 TradeRecord）和 asset quantity（下发 wire）。Phase 7 实施步骤 Step 3 中处理。

## 附录 C. 变更记录

| 日期 | 版本 | 变更 | 作者 |
|---|---|---|---|
| 2026-05-20 | v1 (frozen) | 初稿，22/22 决策冻结 | claude（代笔） |
