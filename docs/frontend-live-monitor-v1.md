# Frontend F2 — Live Monitor v1(as-built)

`[v1 已实现 + verified 2026-05-29 — commits f0368bf(Tier M) / 130432f+27f0330(admin_capable) / 74fc365(F2.3)]`

QuantLab 原生前端的**场景②(live monitor)**。是 [[frontend-design-refs]] 三场景里 Freqtrade FreqUI 对照的那一片 —— "一屏看全 instance + 点按钮干预"。本文是 **as-built**:落档晚于实现(F2.3 先 ship、文档后补),记录已冻结的决策与实现位置,不是待办计划。

意图 (a) 不变:原生 UI 只补 Optuna 盖不到的场景;分析永远走 optuna-dashboard 外链。

---

## 0. 已定决策

| 维度 | 决策 | 出处 |
|---|---|---|
| 实时性 | **轮询 MVP**(react-query `refetchInterval`),不上 WS/SSE 推送 | 见 §2 |
| 轮询节奏 | list 15s、详情 3s | `InstancesPage.tsx:20`、`InstanceLivePage.tsx:43` |
| 看的鉴权 | viewer+;owner-scoped,admin 看全(经 `admin_capable` claim,非 step-up) | §3、[[frontend-design-refs]] |
| 干预的鉴权 | operator+,点击时 **SudoModal step-up** 拿 operator token、用完即弃 | §3 |
| 后端改动 | **零新端点**:list/live/start/stop/deploy 早在 `handlers.go:289-316` | §4 |
| 分析场景 | 永久留给 Optuna,不在本片 | 意图 (a) |

---

## 1. 范围

**本片覆盖**:
- **Tier M — 监控(读)**:live instance 总览列表 + 单 instance live 快照(equity/holdings/connection/recent_trades)。
- **F2.3 — 干预(写)**:start / stop / deploy-champion 三动作。

**不在本片**(见 §10):Tier L(`delta_report` 持仓对账 + agent error stream)→ Phase 8;实时推送(WS/SSE)→ 延后且不改契约。

---

## 2. 实时性 —— 为什么是轮询而非推送

**决策:轮询 MVP,不上 WS/SSE 推送。**

理由:

- **数据自身新鲜度就是 1min 粒度** —— scheduler 是 1min cron tick(`scheduler_started interval=1m`),portfolio/trade 状态本就按分钟推进。亚秒级推送对"分钟级才变"的数据无意义。
- **轮询零后端** —— react-query `refetchInterval` 纯前端;WS 推送要 Hub→浏览器的新通道 + 重连/鉴权/背压处理,成本远大于收益。
- **契约不锁死** —— 将来若真要推送,SSE/WS 是叠加层,不改现有 REST 契约;轮询先行不挖坑。

节奏:list 15s(只在 cron tick 或 promote/retire 时变,更密无益);详情 3s(唯一值得密一点的地方,仍快于数据自身 1min 新鲜度)。

---

## 3. 鉴权分层(看 vs 干预)

两档,刻意不同:

**看 = viewer+(标准 session,无 step-up)**
- list/live owner-scoped:viewer/operator 只看自己拥有的 instance,admin 看全。
- admin 的"看全"靠 **`admin_capable` claim**(commit `130432f`):登录按 DB role 盖章 `admin_capable`(与 issued role 解耦),admin 用默认 viewer 登录拿 24h session 即可监控全 fleet,**不需要** step-up。`canViewInstance`(`handlers_phase9.go:302`)对 admin role 或 `admin_capable` 放行。
- **要点:监控(读)≠ 特权写。** 见 §7。

**干预 = operator+(SudoModal step-up)**
- start/stop/deploy-champion 走 `RequireOperator`(`handlers.go:297-300`)。
- 标准 session 是 viewer → 点干预按钮弹 **SudoModal**,以 `role:"operator"` 重登拿 token、立刻执行、用完即弃。
- `SudoModal` 从 F1 的硬编码 admin **泛化成 `role` prop(operator|admin)**:干预请求 operator、promote/retire 仍 admin。
- 见 [[auth-login-sudo-style]]:UI 尊重 sudo-style,不长期持有 elevated token。

---

## 4. 后端契约(全部已存在)

| 方法 | 路径 | 角色 | 请求体 | 响应 |
|---|---|---|---|---|
| GET | `/instances` | viewer+(owner-scoped) | — | `{items[], count}` |
| GET | `/instances/:id/live` | viewer+(canView) | — | `{instance, portfolio?, connection?, recent_trades[]}` |
| POST | `/instances/:id/start` | operator+ | — | `{instance_id, status, noop?}` |
| POST | `/instances/:id/stop` | operator+ | — | `{instance_id, status, noop?}` |
| POST | `/instances/:id/deploy-champion` | operator+ | `{challenger_id}` | `{instance_id, active_champion_id}` |

注:
- `/live` 的 `portfolio`/`connection` 缺数据时**省略**(nil-skippable collaborator);`recent_trades` 恒在。equity = `(dead+float)*mark + usdt`,**cold_sealed 排除**;仅当有 mark price 时才填 equity/mark_price。
- **start = → live,stop = → paused**(非终态 stopped);`retired` 拒绝任何 transition(`422`)。
- 实现:`ListInstances`(`live_handlers.go:82`)、`GetInstanceLive`(`live_handlers.go:101`)、`transitionInstance`/`DeployChampion`(`handlers.go`)。

---

## 5. Tier M — 监控(shipped `f0368bf`)

- **`/instances` 总览**(`InstancesPage.tsx`,15s 轮询):strategy/pair/status 徽章/last-tick 新鲜度;行点击钻入详情。
- **`/instances/:id` live 快照**(`InstanceLivePage.tsx`,3s 轮询):
  - Equity card —— marked-to-market,带 mark-price + staleness age(`507d ago` 之类)。
  - Holdings —— float/dead/cold-sealed BTC + USDT(8dp BTC / 2dp USD)。
  - Connection 徽章 —— `connected` true/false;collaborator 缺失(无状态副本)时优雅降级 "connection: n/a"。
  - Recent trades —— fills 折叠成子行(fill qty/price/fee/slip)。
  - 404 优雅降级 "Instance not found."。
- 组件:`StatusBadge.tsx`(InstanceStatusBadge/ConnectionBadge)、`format.ts`(number/age 格式化)、`types.ts`(契约镜像)。
- nav "Live" → `/instances`(`App.tsx:66/111-114`)。

---

## 6. F2.3 — 干预(shipped `74fc365`)

`InstanceLivePage` 加 **Controls card**:

- **Start(→live)/ Pause(→paused)**:状态感知启停 —— `live` 时 Start 置灰、非 `live` 时 Pause 置灰;`retired` 显 "no actions available"(后端本就 422)。
- **Deploy champion**:`challenger_id` input → `SetActiveChampion`;active champion 显在 controls 下方。
- 每个动作 → SudoModal(`role="operator"`)→ 命中端点 → invalidate live query 即时反馈。
- 按钮对所有人可见;真正的闸门是点击时的 operator step-up(DB role < operator → 登录 400 → modal 显 "lacks operator permission")。

---

## 7. `admin_capable` claim(read-elevation,`130432f` + 测试 `27f0330`)

verify Tier M 时暴露:登录页 viewer-only + list owner-scoped,导致 admin 经 UI 看不到他人 instance。修法 = option C:

- `Claims.AdminCapable`,`IssueToken(userID, role, adminCapable)`;`Login` 按 DB role 设 `adminCapable = (u.Role==admin)`。
- **监控(读)≠ 特权写**:`RequireRole` 仍查 issued `claims.Role`,promote/retire 仍要 10min admin step-up。单测 `TestRequireRole_AdminCapableViewerStillRejected` 钉死"viewer+admin_capable token 写仍 403",防提权回归。

---

## 8. step-up 401 fix(`74fc365` 顺带)

verify F2.3 时发现的 **pre-existing bug(F1 promote/retire 也中招)**:step-up 输错密码的 401 经 `apiFetch` 全局 `onUnauthorized` 把整个 session 登出、弹回 `/login`,使 SudoModal 自己的 "Wrong password." 重试成死代码。

修法:`apiFetch` 加 `skipUnauthorizedHandler` opt(`api.ts`),login + step-up 两处 credential-check 调用都设 → 401 浮给 modal,不杀 session。

---

## 9. 业务规则(UI 兑现)

- list/live **owner-scoped**:非 admin 只见己有;admin(role 或 admin_capable)见全。
- 干预 **operator+**:viewer 也见按钮,点击触发 operator step-up;权限不足 → 400 → "lacks operator permission"。
- `retired` instance:Controls 显 "no actions available";start/stop 后端 422。
- start/stop 的 `noop`(已在目标态)→ 200,UI 照常 refetch。
- 时间戳一律 `_ms`(epoch 毫秒),前端统一格式化。

---

## 10. 不在本片

- **Tier L** —— `delta_report` 持仓对账 + agent error stream → **Phase 8**(`delta_report` 目前仅 log,见 [[ws-protocol-freeze]])。
- **实时推送(WS/SSE)** —— 延后;叠加层,不改 REST 契约(§2)。
- **分析场景** —— 永久 Optuna 外链。

---

## 11. 遗留

- **落档晚于实现** —— 本文 as-built,F2.3 已先 ship。
- **pre-existing lint 债(未动)** —— `eslint` 报 `AuthContext.tsx:85` `useAuth` 导出触发 `react-refresh/only-export-components`;与本片无关,`npm run build`=tsc+vite 不 gate lint。

---

## 12. 实现位置

```
web/src/
  App.tsx                      nav "Live" + /instances、/instances/:id 路由
  pages/InstancesPage.tsx      Tier M 总览(15s 轮询)
  pages/InstanceLivePage.tsx   Tier M 快照 + F2.3 Controls card(3s 轮询)
  auth/SudoModal.tsx           role prop(operator|admin)泛化 + skipUnauthorizedHandler
  auth/AuthContext.tsx         login skipUnauthorizedHandler
  lib/api.ts                   apiFetch + skipUnauthorizedHandler opt
  lib/types.ts                 Instance*/Portfolio*/Trade* 契约镜像
  components/StatusBadge.tsx   InstanceStatusBadge / ConnectionBadge
  lib/format.ts                number / age 格式化

internal/api/
  live_handlers.go             ListInstances:82、GetInstanceLive:101
  handlers.go                  路由 289-316;transitionInstance / DeployChampion
  handlers_phase9.go           canViewInstance:302(admin_capable 放行)
  handlers_auth.go             Login 设 adminCapable
  middleware/auth.go           RequireRole(查 issued Role,不认 admin_capable)
internal/saas/auth/service.go  Claims.AdminCapable、IssueToken(.,.,adminCapable)
```

---

## Related

[[frontend-design-refs]] —— 三场景来龙去脉;本片是 Freqtrade FreqUI 对照那一片的落地,F2 段记总进度。
[[frontend-promote-retire-v1.md]] —— 姐妹片(F0+F1 promote/retire);SudoModal 在那里诞生、本片泛化。
[[auth-login-sudo-style]] —— SudoModal step-up 与 viewer/operator/admin TTL 的依据。
[[ws-protocol-freeze]] —— Tier L 依赖的 WS / `delta_report`(Phase 8)。
[[phase9-rest-api]] —— 本片消费的 instance 端点出处。
