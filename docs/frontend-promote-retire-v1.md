# Frontend F0 + F1 — Promote/Retire 工作流 v1(可执行计划)

`[v1 计划冻结 2026-05-29 — 待实现]`

QuantLab 原生前端的第一片。**意图 (a)**:原生 UI 只补 Optuna 盖不到的场景;分析场景继续全靠 optuna-dashboard(导航里一个外链指过去,不重建)。本计划覆盖 **F0(垂直切片地基)+ F1(promote/retire 工作流,MLflow 对照)**。live monitor(场景②)是后续独立战役,不在本计划。

---

## 0. 已定决策

| 维度 | 决策 | 出处 |
|---|---|---|
| 范围 | (a) 只补 promote/retire + live monitor;分析靠 Optuna | 用户 2026-05-29 |
| 技术栈 | React SPA(Vite + React + TypeScript) | 用户 2026-05-29 |
| 代码位置 | monorepo `quantlab/web/`(`[默认]` 未否决推荐) | 推荐 |
| 首个场景 | promote/retire(自包含、零新后端、纯前端) | 用户 2026-05-29 |

---

## 1. 技术栈

- **Vite + React + TS** —— SPA 脚手架
- **TanStack Query** —— REST 缓存/重取/loading-error 态(promote/retire 全是 REST)
- **React Router** —— Champions / Tasks / Challenger 详情 路由
- **UI**:`[INVENTED v1]` Tailwind CSS(单人 ops 工具,快;不引重型组件库)
- **图表**:F1 **不需要**(promote/retire 无图);challenger 的回测可视化短期走 QuantStats HTML tearsheet iframe,不自建

---

## 2. 跨域 / 部署(无 CORS 中间件 → 绕开它)

后端**没有 CORS 中间件**(已核实)。决策:**全程同源,不引入 CORS**。

- **dev**:Vite dev server 配 proxy,`/api` → `http://localhost:8080`、`/api/v1/ws/*` 不涉及(F1 无 WS)。浏览器视角同源。
- **prod**:`go:embed` 把 `web/dist/` 嵌进 saas binary,在 `/` 下 serve 静态资源,API 仍在 `/api/v1`。同源,零 CORS。
- **这是 F0 唯一的一处后端改动**:saas 加一个 static file handler(embed)+ SPA fallback(非 `/api` 路径回 `index.html`)。约 ~25 LoC。

---

## 3. 鉴权模型(sudo-style 的 UI 兑现)

登录契约:`POST /auth/login {email, password, role?}` → `{token, role, expires_at(ms)}`。默认 `viewer`;传 `role:"admin"` 升级,服务端按 DB role 封顶,**admin token short-TTL(~10min)**。

UI 流程:
- **常态**:viewer token(login 不带 role 或 `viewer`),存 `sessionStorage`,注入 `Authorization: Bearer`。
- **promote/retire(admin-only)**:点按钮 → 弹 **SudoModal**(email+password,role 固定 admin)→ 拿 short-TTL admin token → **立刻**执行该操作 → 操作完**丢弃 admin token**,回到 viewer。
- `expires_at` 用于:admin token 临期提示 / viewer 过期自动跳登录。
- 见 [[auth-login-sudo-style]]:UI 必须尊重"admin 操作要 prompt 重登",不长期持有 admin token。

---

## 4. F0 — 垂直切片(0.5–1 天)

**目标**:浏览器点到真数据 + 跑通 auth + 跑通同源部署环路。一切后续都站在这个地基上。

| 步 | 内容 |
|---|---|
| F0.1 | `web/` 脚手架:Vite+React+TS、Tailwind、Router、TanStack Query、`vite.config` dev proxy |
| F0.2 | `apiClient`:fetch 封装,注入 token,统一 401(跳登录)/403/4xx→`{error}` 解析 |
| F0.3 | `AuthContext` + `LoginPage`(viewer 登录),token 存 sessionStorage |
| F0.4 | 一个真实读视图:**Champion History 表**(`GET /champions/history`)—— 最贴 F1 |
| F0.5 | `AppShell`:顶部导航 Champions / Tasks / **[分析 → Optuna 外链]** |
| F0.6 | 后端:saas `go:embed web/dist` + SPA fallback(同源 serve) |

**验收**:`npm run dev` → 登录(viewer)→ 看到 champion history 真数据;`npm run build && go run ./cmd/saas` → 同源 `:8080/` 访问到同一界面。

---

## 5. F1 — Promote/Retire 工作流(~3–5 天)

challenger **没有 list 端点**,发现路径只能经 tasks 或 champion history。所以入口是 tasks 列表。

| 步 | 内容 | 端点 |
|---|---|---|
| F1.1 | **Tasks 列表**(challenger 发现入口):每行 task → 其 winner `challenger_id` | `GET /evolution/tasks` |
| F1.2 | **Champion 视图**:history 时间线 + 当前 active champion(score) | `GET /champions/history`、`GET /genome/champion` |
| F1.3 | **Challenger 评审页**:摘要 + 全量 package 渲染 | `GET /challengers/:id`、`GET /challengers/:id/package` |
| F1.4 | **PackageView 子组件**:四窗口 window_scores、OOS 三色(green/yellow/red/gray)、DSR、friction、consistency_penalty | (来自 package) |
| F1.5 | **Promote 操作** + SudoModal,`decision_note` 输入,`reviewed_by` 取自 token claims | `POST /challengers/:id/promote` |
| F1.6 | **Retire 操作** + SudoModal | `POST /champions/:id/retire` |
| F1.7 | **状态/错误规则**(见 §6) | — |

请求体:`{reviewed_by, decision_note?}`(promote 与 retire 同形)。

**分步验收**:
- F1.1–1.3:能从 task 列表钻到一个 challenger 的完整 package 渲染。
- F1.5:viewer 点 Promote → 弹 SudoModal → admin 重登 → 200 → decision_status 变 `promoted`。
- F1.6:同上 retire,champion_history 出现 `retired_at/retired_by/retire_note`。

---

## 6. 业务规则(UI 必须兑现)

- `decision_status ∈ {pending, promoted, rejected}` —— 已非 pending 的 challenger,Promote 按钮置灰/隐藏。
- `test_mode=true` 的 challenger **不可 promote** —— 显式 badge + 置灰按钮(后端也会拒,但 UI 先挡,给清晰理由)。
- promote/retire 是 **admin-only**:viewer 也能看到按钮,但点击触发 SudoModal;若用户 DB role < admin,登录返 400 → UI 显示"你没有权限"。
- `409`(如 promote 时该 pair 已有 active champion 冲突)→ 明确错误提示,不静默。
- 时间戳一律 `_ms`(epoch 毫秒),前端统一格式化。

---

## 7. 组件清单(F0+F1)

```
AppShell                 nav + AuthContext + token 过期处理
  LoginPage              viewer 登录
  SudoModal              admin 重登(promote/retire 前置),用完即弃 token
  apiClient / queryHooks fetch 封装 + TanStack Query hooks
  TasksListPage          GET /evolution/tasks(challenger 发现入口)
  ChampionsPage
    ChampionHistoryTable GET /champions/history
    ActiveChampionCard   GET /genome/champion
  ChallengerReviewPage   GET /challengers/:id (+/package)
    PackageView
      WindowScores       四窗口 cascade
      OosBadge           三色 + gray
      DsrPanel / FrictionPanel
    PromoteButton        → SudoModal → POST .../promote
  RetireButton           → SudoModal → POST .../retire
```

---

## 8. 落地规模估计

| 模块 | 估 |
|---|---|
| F0 脚手架 + auth + apiClient + 1 视图 | ~0.5–1 天 |
| 后端 go:embed serve | ~25 LoC |
| F1 tasks/champions/challenger 三页 + PackageView | ~2–3 天 |
| SudoModal + promote/retire 操作 + 规则/错误 | ~1 天 |
| **合计** | **~3.5–5 天** |

---

## 9. 不在本计划

- **Live Monitor(场景②)** —— 独立战役;已知拖出 3 个后端前置:缺 `GET /instances` list 端点、无浏览器实时推送通道(`/ws/agent` 是 SaaS↔Agent 非 SaaS↔浏览器)、Ack/OrderUpdate 未持久化(`connection.go:280` 仅 log)。单独规划。
- **分析场景原生化** —— 永久留给 Optuna,只外链。
- **`/data/import` UI** —— 见 [[phase9-data-import-v1]],绑定更后期。
- **图表自建 / 替换 QuantStats** —— 短期 iframe。

---

## 10. 待确认(动手前)

1. UI 库:Tailwind(推荐)还是 plain CSS modules?
2. prod serve:`go:embed` 同源(推荐)还是前端独立静态托管 + 加 CORS 中间件?
3. `web/` 的 lint/format/CI:接进现有流程还是前端自带(Biome/ESLint)?`[INVENTED v1]` 先 Biome,轻。

---

## 11. 实现位置(待填)

实现后回填 `web/` 结构与 saas embed 的 `file:line`,header 从"待实现"改"已实现"。

---

## Related

[[frontend-design-refs]] —— 三参考与 Phase 1/1.5(Optuna 当 UI)的来龙去脉;本计划是其 Phase 2(MLflow 那一片)的落地。
[[auth-login-sudo-style]] —— SudoModal 的依据。
[[phase9-rest-api]] —— 本计划消费的 REST 端点全表。
[[ws-protocol-freeze]] —— live monitor(下一片)依赖的 WS,F1 不用。
