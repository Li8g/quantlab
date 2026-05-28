# Phase 9 — `/data/import` 异步设计 v1

`[v1 设计冻结 2026-05-28 — 实现 defer 到 frontend 阶段]`

这份文档冻结 `POST /data/import` 的 API 契约、表结构、状态机、并发模型与用户决策。**代码尚未实现**——Phase 9 REST 补全的最后一个端点,绑定在 6 月 frontend 阶段拉动(为不存在的前端 ship 是 zero value,ops 当前用 CLI `datafeeder import` 已够)。本文档的目的是先把形态钉死,让 frontend 一开工就有现成契约可对接。

> **What**:把已存在的 `data.Orchestrator.ImportSymbol`(同步、分钟级、幂等)包成异步任务,对齐 `evolution_tasks` 的 `create → poll` 模型。本设计**只做异步外壳**,不碰 orchestrator 内部逻辑。

---

## 1. Scope

### 1.1 在 v1 范围

- `POST /data/import` → 202 + `import_job_id`(双 gate:`!= AppRoleSaaS` 配置层 + `RequireAdmin` 请求层)
- `GET /data/import/:job_id` → 轮询单个 job 状态
- `GET /data/imports?limit=N` → 列出近期 job
- `POST /data/import/:job_id/cancel` → 请求取消(月末边界生效)
- `import_jobs` 表 + repo + 单 worker + orphan sweep
- per-`(symbol,interval)` 并发互斥(DB partial unique index)
- 月级进度(`OnMonth` 回调)

### 1.2 不在 v1 范围

见 §11。

### 1.3 相关阶段 / 既有件

| 件 | 关系 |
|---|---|
| `data.Orchestrator.ImportSymbol` | 已存在,幂等(PK 冲突跳过、gap 行 delete+rewrite);本设计调用它,不改它 |
| `cmd/datafeeder import` | 已存在的 CLI 入口;import 端点是它的异步 HTTP 孪生,二者共用 orchestrator |
| `evolution_tasks`(Phase 5D) | 异步范式来源:202+task_id、`StartedAt/FinishedAt/FailureReason`、`TaskStatus` 枚举 |
| `/data/coverage` `/data/gaps`(Phase 9) | 时间一律毫秒;import 沿用 `start_ms/end_ms` |
| promote/retire(Phase 6 §3.2) | admin-only gate 同档,operator 排除 |

---

## 2. 用户决策(2026-05-28)

五个跨工程边界的选择由用户拍板,固化于此,后续不再重开。

### 2.1 决策 1 — 并发控制 = DB partial unique index(不用 advisory lock)

同一 `(symbol,interval)` 不能并发导入(orchestrator delete+rewrite 该区间 gap 行,并发互相打架)。

```sql
CREATE UNIQUE INDEX uq_import_jobs_active
  ON import_jobs (symbol, interval)
  WHERE status IN ('queued', 'running');
```

- 声明式、代码路径绕不过、重启天然安全、Create 时 INSERT 冲突 → **409**。
- 语义是**拒绝**(非排队):"该 pair 已在导入,见 <running job>"。
- 代价:启动 orphan sweep 从"卫生"升级为 **liveness 必需**(否则崩溃残留的 `running` 行会一直挡新导入)。advisory lock 也逃不掉这个 sweep,故无额外损失。
- 粒度只到 `(symbol,interval)`:它阻塞该 pair 的**所有**并发导入,不区分时间区间是否重叠(比严格必要更保守,但安全)。

### 2.2 决策 2 — 进度粒度 = `months_done / months_total`(不用百分比)

orchestrator 本就按 `monthRange` 逐月循环,月末是回调的天然边界、零阻抗,且"12/89 月"对 lab 用户比 % 更有意义(可估剩余墙钟)。% 是它的子集——前端要进度条就 `done/total`。

- 月内无子进度,不做插值假精度。
- `[INVENTED v1]` 暴露 "month" 这一实现细节是可接受的;将来若改日归档/API 拉取再考虑泛化成 `unit`,现在不抽象。

### 2.3 决策 3 — 配置 gate = `!= AppRoleSaaS`(lab + dev,不含生产 saas)

import 是 research/ops 职能,生产 saas 实例绝不暴露(租户不应 bulk-import K 线)。`AppRoleLab` 已存在(`config.go:24`)。

- 选 `!= AppRoleSaaS` 而非"仅 lab",是为了 **dev 本地能联调**——frontend 6 月开工时本地栈跑 `dev` role,"仅 lab"会让 dev 404,与其他端点不一致。
- 与现有 `cfg.AppRole != AppRoleDev → ReleaseMode` 是同一种"按 role 收紧"惯用法。
- 叠加 `RequireAdmin`(operator 排除,与 promote/retire 同档)。

### 2.4 决策 4 — worker = 全局单 worker 串行(不用分片池)

- 一个 goroutine 轮询 `queued`,跑完取下一个。全量 4.7M 行 bulk upsert + 归档下载很重,串行保持 box 健康。
- 与决策 1 叠加 → **不同 pair 都不并发,正确性 trivially 成立**(无同 pair 碰撞可能)。
- 缺点队头阻塞(大导入挡住小导入):原型期一个用户、低频导入,非问题。
- 迁移路径无痛:转并发只需把捞取 SQL 换 `FOR UPDATE SKIP LOCKED` 并起 N 份,**不动 API 契约**;决策 1 的 unique index 同时兼容串行与并行(只挡同 pair),不锁死未来。

### 2.5 决策 5 — 纳入取消(`cancelled` 状态)

`TaskStatus` 已含 `cancelled`,长任务误启(9 年导入)取消是真需求,且决策 2 的 `OnMonth` 月末边界正好是 cancel 的天然检查点——几乎白送。

- `POST /data/import/:job_id/cancel` 把行标 `cancel_requested=true`。
- worker 每月末在 `OnMonth` 检查该标志,置 `cancelled` 并 return。
- 已导入月份保留(`ImportSymbol` 幂等,重启可续)。
- 只能取消 `queued`/`running`;终态返 409。

---

## 3. 路由契约 `[v1 — frozen]`

```
POST /data/import              → 202 {import_job_id}     | 409 已在进行 | 400 校验
GET  /data/import/:job_id      → 200 ImportJobStatus      | 404
GET  /data/imports?limit=N     → 200 {items, count}
POST /data/import/:job_id/cancel → 202 | 404 | 409 终态不可取消
```

全部 `AppRole != AppRoleSaaS` 才注册 + `RequireAdmin`。

**Create 请求体:**
```json
{ "symbol": "BTCUSDT", "interval": "1h",
  "start_ms": 1483228800000, "end_ms": 1717000000000 }
```
校验:symbol/interval 非空;`start_ms <= end_ms`;interval ∈ 已知集 `[INVENTED v1 待定与 orchestrator 对齐]`。

**Status 响应:**
```json
{
  "import_job_id": "imp_01J...",
  "symbol": "BTCUSDT", "interval": "1h",
  "start_ms": 1483228800000, "end_ms": 1717000000000,
  "status": "running",                              // queued|running|succeeded|failed|cancelled
  "progress": { "months_done": 12, "months_total": 89 },
  "rows_inserted": 0, "gaps_detected": 0,           // 终态填,来自 ImportSummary
  "failure_reason": null,
  "requested_by": "admin-user-id",
  "started_at_ms": 1717000000000, "finished_at_ms": null
}
```

---

## 4. 表 `import_jobs` `[v1 — frozen]`

```go
type ImportJob struct {
    gorm.Model
    JobID    string               `gorm:"type:varchar(64);uniqueIndex"`
    Symbol   string               `gorm:"type:varchar(16);index"`
    Interval string               `gorm:"type:varchar(8);index"`
    StartMs  int64
    EndMs    int64
    Status   resultpkg.TaskStatus `gorm:"type:varchar(16);index"`  // 复用枚举,不新造

    MonthsDone   int
    MonthsTotal  int
    RowsInserted int64
    GapsDetected int

    CancelRequested bool   `gorm:"index"`            // 决策 5:worker 月末检查
    RequestedBy     string `gorm:"type:varchar(64);index"`  // 触发的 admin,审计

    StartedAt     *time.Time
    FinishedAt    *time.Time
    FailureReason *string `gorm:"type:text"`
}
func (ImportJob) TableName() string { return "import_jobs" }
```

加 §2.1 的 partial unique index(automigrate 后用 raw SQL `CREATE UNIQUE INDEX IF NOT EXISTS`,GORM tag 不支持 partial)。

- **状态枚举复用** `resultpkg.TaskStatus`(queued/running/succeeded/failed/cancelled),语义完全一致,不造平行常量。

---

## 5. 状态机 `[v1 — frozen]`

```
                POST                worker 抢到          月循环跑完
   queued ───────────▶ (持久化) ───────────▶ running ──────────────▶ succeeded
     │  INSERT 冲突        │ unique index            │
     │ (同 pair active)    │ 已天然挡住               │ 任一月 err / ctx
     ▼                                              ▼
   409                                            failed (failure_reason 落库)
                                                    │
   cancel 标志置位 ─────────────────────────────────┤ 月末检查 cancel_requested
                                                    ▼
                                                 cancelled (保留已导入月)
```

- `queued → running`:worker 取到、写 `StartedAt`。
- `running → succeeded`:`ImportSymbol` 返回,落 `ImportSummary.{RowsInserted,GapsDetected}` + `FinishedAt`。
- `running → failed`:`ImportSymbol` 返 err。**部分成功**:`ImportSymbol` 注释明说"返回 summary 不管 partial failure"——`[INVENTED v1]` 决策:**有 err 即 failed**,但 summary 的 rows/gaps 仍落库,让用户看到失败前进度。
- `running → cancelled`:月末 `OnMonth` 见 `cancel_requested` → 置 `cancelled` + `FinishedAt`。
- **orphan**:进程崩溃留 `running` 孤儿。启动一次性 sweep:
  `UPDATE import_jobs SET status='failed', failure_reason='interrupted by restart' WHERE status='running'`。
  此 sweep 是 liveness 必需(否则孤儿挡住该 pair 新导入,见 §2.1)。

---

## 6. 进度回调 `OnMonth` `[v1 — frozen]`

给 `Orchestrator` 加一个**可选** hook,落在它已有的月循环边界,orchestrator **不** import `import_jobs`(边界干净):

```go
// orchestrator.go
type Orchestrator struct {
    ...
    OnMonth func(done, total int) (cancel bool)  // nil 时不回调; 返 true 请求中止
}
```

- worker 注入它:每月末 `UPDATE import_jobs SET months_done=?`,并读 `cancel_requested` 决定返回值。
- 返 `cancel=true` 时 orchestrator 在月边界干净中止(不留半个月的脏写——月是 upsert 原子单位)。

---

## 7. 落地清单与规模(实现时)

| 模块 | 文件 | 估 LoC |
|---|---|---|
| 表 + partial index 迁移 | `store/models.go` + automigrate raw SQL | ~40 |
| repo | `repository/import_job.go`(Create/Get/List/MarkRunning/MarkDone/MarkFailed/MarkCancelled/SetCancelRequested/UpdateProgress/SweepOrphans) | ~140 |
| worker | `internal/data/import_worker.go` | ~100 |
| orchestrator hook | `orchestrator.go` 加 `OnMonth` + 月末检查 | ~15 |
| handler + 类型 | `handlers_phase9.go` + `types.go`(4 路由 + 请求/响应) | ~130 |
| wiring + gate | `cmd/saas/main.go`(worker 启动 + sweep + 路由) | ~40 |
| 测试 | handler 202/409/400/gate + worker 状态机 + orphan sweep + cancel 月末生效 | ~180 |

**合计 ≈ 645 LoC**(含 cancel)。

---

## 8. 幂等与重复导入

- `ImportSymbol` 幂等 → 重跑同区间安全。
- §2.1 unique index 只挡 **active** job;已 `succeeded`/`failed`/`cancelled` 的区间可再导(刷新/补数据/续未完成)——合理,归档可能被上游修正。

---

## 9. 边界与错误

- `start_ms > end_ms` → 400(orchestrator 也会拒,但 handler 先挡)。
- 未知 interval → 400 `[INVENTED v1 待与 orchestrator 已知集对齐]`。
- 取消终态 job → 409。
- worker 单实例假设:多副本部署时两个 worker 会争抢 `queued` 行 → **v1 不支持多副本**(见 §11);unique index 仍保证不会同 pair 双跑,但可能两个 worker 各跑一个不同 pair——这其实安全,真正不安全的是同一行被两 worker 领取,需 `SKIP LOCKED`(defer)。

---

## 10. 实现位置(待填)

实现后回填 `file:line`,并把本文档 header 从"实现 defer"改为"frozen + 实现完成"。

---

## 11. 不在 v1 范围

- **多副本 worker / 水平扩展**:单 worker 单实例假设。转并发见 §2.4 迁移路径(`SKIP LOCKED` + N 份)。
- **进度的 WS 推送**:v1 纯轮询(对齐 `evolution_tasks`)。要实时进度条再接 [[ws-protocol-freeze]] 的 WS 通道。
- **time-range 级并发**(同 pair 不重叠区间并行):unique index 只到 pair 级。
- **`data_catalog` 汇总表**:与 `/data/coverage` 的 `[perf]` 足枪同一件事,全量回填前再上。
- **API fallback 拉取近 1-2 日未归档段**:orchestrator 的 `[TODO: api fallback]`,独立于本设计。

---

## 12. Open questions

1. interval 已知集从哪取?(orchestrator 是否已有校验,还是 handler 自带白名单)
2. `job_id` 前缀/格式:`imp_` + ULID?(对齐 instance 的 ULID issuer)
3. `GET /data/imports` 是否要按 symbol/interval/status 过滤?(`[INVENTED v1]` 暂只 limit,与其他 list 端点一致)

---

## Related

[[phase9-rest-api]] — 本端点是 Phase 9 第 8/8(前 7 已 shipped)。
[[sync-vs-async-endpoints]] — import 分钟级 → 必须 async,本设计的根据。
[[frontend-design-refs]] — 实现拉动时机绑定 6 月 frontend。
[[ws-protocol-freeze]] — 进度实时化的未来通道(v1 不用)。
