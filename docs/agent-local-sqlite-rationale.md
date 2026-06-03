# Agent 本地 SQLite 的设计理由

> 一句话:SaaS 服务器用 PostgreSQL 因为它是**中心账本**;LocalAgent 用 SQLite 因为它要的是一张**掉电不丢、断网可读、零依赖**的本地小表。两者各自独立,不是"该统一却没统一"。

## 1. 谁在用它 —— 两个进程、两套库

QuantLab 里有两个完全不同的进程,各自持有自己的存储:

| 进程 | 存储 | 职责 |
|---|---|---|
| SaaS 服务器 `cmd/saas` | PostgreSQL | 全局中心账本:进化任务、challenger、instance、`trade_records`、对账记录等 |
| LocalAgent `cmd/agent` | 本地 SQLite 文件 | 一张幂等表:`client_order_id → exchange_order_id` 映射 |

Agent 的 SQLite **不是** SaaS 服务器在用,它和你机器上装的 PostgreSQL 是两个不同进程、不同职责,根本不在同一个边界里。

幂等表只回答一个问题:**"这一单我是不是已经下过了。"** 表结构见 `docs/saas-ws-protocol-v1.md §8.3`,实现见 `internal/agent/idempotency_sqlite.go`(纯 Go 驱动 `modernc.org/sqlite`,无 CGO,WAL 模式 + `busy_timeout=5s`)。

```
client_order_id (PK) | exchange_order_id | status | market_ref_decimal | submitted_at_ms | last_updated_ms
```

- **写时机**:收到 `trade_command` → INSERT `pending`;交易所 ack → UPDATE `accepted`+`exchange_order_id`;`order_update` → UPDATE 终态。
- **清理**:每次 Agent 启动 `DELETE WHERE last_updated_ms < now - 7d`。

## 2. 架构边界 —— Agent 是独立的边缘进程

LocalAgent 被设计成可以跑在和 SaaS 服务器**不同的机器上**(贴近交易所、或用户自己的交易盒子),两者之间只通过 WebSocket 通信(`docs/saas-ws-protocol-v1.md`),不共享数据库连接。

## 3. 为什么 Agent 不能直接用 SaaS 的 PostgreSQL

幂等表存在的全部意义就是"下单去重必须本地可靠、且不依赖网络"。把它放进远端 PostgreSQL 会同时破坏三点:

1. **去依赖**:如果 Agent 把幂等写进 SaaS 的 PostgreSQL,那它每次下单前都要先访问 SaaS 的库。一旦 Agent↔SaaS 网络抖动/断开,Agent 就既不能判重也不能下单——而这恰恰是 WS 协议里反复处理的核心场景(断线重连、丢单兜底)。本地 SQLite 让 Agent 在**离线时仍能正确判重**。
2. **复活路径**(协议 §8.3):Agent crash 重启后读本地 SQLite,在 `state_sync_response` 里把 7 天内的 `pending`/`filled` 全 push 给 SaaS 对账。这要求幂等态是 Agent 自己**掉电后还在的本地文件**,而不是远端库——若依赖远端库,crash+断网时复活路径直接失效。
3. **进程边界**:Agent 是 single-process、零外部服务依赖的设计(`modernc.org/sqlite` 纯 Go、无 CGO,`cmd/agent` 在任何 Go 目标平台都能直接编)。要求它额外连一个 PostgreSQL,等于给"轻量边缘 Agent"强加一个重运维依赖,违背拓扑。

## 4. 当 QuantLab 整体上云、用户只用 Web 界面时,为什么这个设计反而更可靠

设想最终形态:SaaS 服务器 + PostgreSQL 全部跑在云端,用户只通过浏览器做监视和操控。此时**让 LocalAgent 保留独立 SQLite 进程,是更可靠的方案,而不是历史包袱**。原因:

### 4.1 故障域隔离 (blast radius)

云端服务器、网络链路、用户浏览器都可能不可用。把下单去重的"最后一道闸"放在 Agent 本地,意味着**云端整体宕机时,已在途的订单仍不会被重复下单**。如果幂等依赖云端 DB,云端一挂,Agent 要么停摆、要么裸奔重复下单——这正是实盘最不能接受的。本地 SQLite 把"资金安全相关的判重"和"云端可用性"解耦。

### 4.2 网络是常态故障,不是异常

Agent 贴着交易所跑,和云端之间隔着公网。公网抖动、跨区延迟、连接重置是**常态**而非异常。下单是有时效、且涉及真金白银的操作,不能让它卡在一次跨公网的远程 DB 往返上。本地读 SQLite 是微秒级、且永远在线。

### 4.3 Web 界面只做监视/操控,不进下单热路径

用户的 Web 操作(start/stop/deploy/kill)经由 SaaS → WS → Agent 下发,是**控制平面**。而"这单下没下过"是**数据平面**里 Agent 必须自洽回答的问题。控制平面可以容忍延迟和重试,数据平面的幂等不能。两者分层:云端 PostgreSQL 服务控制平面与对账,本地 SQLite 服务下单幂等。

### 4.4 单一可信源仍然成立

有人会担心"两套库会不会导致数据不一致"。不会——它们是**不同层的不同事实**:
- SQLite 是 Agent 对"我向交易所发了什么"的本地真相(进程私有,短生命周期,7 天滚动)。
- PostgreSQL 是系统对全局账本的中心真相。
- 二者通过 `state_sync_response` 对账:Agent 复活/重连时把本地态 push 给云端,由云端裁决并落 `reconciliation_discrepancies`。

中心账本仍唯一,本地表只是它在边缘的、用于断网自洽的派生缓存。

## 5. 小结

| | SaaS PostgreSQL | Agent SQLite |
|---|---|---|
| 进程 | `cmd/saas`(云端) | `cmd/agent`(边缘) |
| 角色 | 中心账本 / 控制平面 | 下单幂等 / 数据平面 |
| 可用性要求 | 高,但允许运维介入 | 必须本地自洽,断网可读 |
| 故障域 | 与云端同生死 | 与 Agent 同生死,隔离于云端 |
| 依赖 | 重(独立 DB 服务) | 零(纯 Go 内嵌文件) |

把幂等下沉到边缘的本地 SQLite,不是"省事没接进中心库",而是一个有意的可靠性设计:**让资金安全相关的判重独立于云端可用性与公网链路**。上云之后,这条边界只会更值钱,不会更碍事。
