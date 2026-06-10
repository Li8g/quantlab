# 距实盘测试的已知缺口清单（前端 + 后端）

Status: **工作清单（live inventory）** — 供逐项排期，不是规格文档
Date: 2026-06-10
用途: 安全机制代码已全部到位（kill_switch Option 3、对账自动冻结、env 一致性断言、③账本吸收成交均已 shipped）。本清单收拢「从当前 dev/原型可跑」到「mainnet 实盘安全」之间**已知**的前后端缺口，明天逐项过。

相关源文档（细节不在本页复制，按需跳转）:
- `docs/mainnet-runbook.md` — 部署步骤 + 运维 + 已知运营局限（§G datafeeder）
- `docs/backlog-6-price-source-divergence.md` — 价格源分歧分析（per-order 守卫 A 延后 / agent-side sizing C 否决 / env 断言 v1 已 ship）
- `docs/decision-analysis-page-productionization.md` — 分析页 5 缺口（含本页 G1–G3）
- 记忆 `killswitch-option3` / `live-trading-gaps` / `ws-protocol-freeze`

---

## 0. 一页速览

| # | 缺口 | 层 | 严重度 | 阻塞 mainnet? | 状态 |
|---|---|---|---|---|---|
| **B1** | kill 无服务端持久 latch（选项 B） | 后端 | 🔴 安全 | ~~是~~ | ✅ DONE 2026-06-10（feat/manual-kill-button） |
| B2 | 无 limit order 价格保护路径 | 后端 | 🟡 中 | 否（market+③+reconcile 兜底） | OPEN，未规格化 |
| B3 | per-order 价格分歧守卫（⑥ 选项 A） | 后端/agent | 🟢 低 | 否 | 延后（等真盘数据调阈值） |
| B4 | datafeeder 中段空洞无 heal | 后端 | 🟢 低 | 否（概率极低） | 已文档化局限 |
| B5 | datafeeder cron 运营局限 | 运维 | 🟢 低 | 否 | 已文档化（runbook §G） |
| **G1** | 分析页 dashboard 不持久（手工 nohup） | 运维 | 🟡 中 | 部分（重启即 502） | OPEN（P1） |
| **G2** | 分析页快照不自动刷新 | 运维 | 🟢 低 | 否 | OPEN（P1） |
| **G3** | 分析页 :8088 零鉴权裸暴露 + URL 硬编码 | 前端+运维 | 🔴 安全(mainnet)/🟢(dev) | ~~是~~ | ✅ DONE 2026-06-10（A′：localhost+SSH 隧道+URL 外置） |
| F1 | 前端 eslint 遗留债 | 前端 | 🟢 低 | 否 | 已知，不 gate build |

> ~~真正在 mainnet 前必须处理的只有 **B1**（安全 latch）与 **G3**（暴露面）。~~ **B1 与 G3 均已于 2026-06-10 完成**（见下）。mainnet 前已无安全级硬阻塞；其余为中/低优先或已延后/已文档化。

---

## 1. 后端缺口

### B1 — kill 服务端持久 latch（选项 B）✅ DONE 2026-06-10

**已实现**（复用 audit_logs 作 latch 真相源，零新表/零迁移）:
- **协议**: `wire.AuthOK` 加 `Frozen bool`（additive，§5.4）。Agent 握手收到 `auth_ok` 即 `frozen.Store(ok.Frozen)` → 重启/重连凭握手恢复 HALTED；离线期被 resume 则清回。
- **server 握手**: 新 hook `wshub.Config.OnFrozenLookup` 在发 `auth_ok` 前查 `AuditRepo.IsAccountFrozen`（= 最近一条 kill vs resume），回填 `auth_ok.Frozen`。**查表出错 fail-closed=frozen**（瞬时 store 错误绝不能静默解冻一个被 kill 的 agent）。
- **kill/resume 路径**: 先把 latch（kill/resume 审计行）持久化为 **load-bearing 写**（失败即 500），再 best-effort WS 推送。agent 离线**不再 409**，而是「已 latch，重连即冻」。
- **auto-freeze**: `maybeAutoFreeze` 同样先落 latch 再推送（消除「漂移冻结+离线→重连解冻」同类 bug）；latch 写失败则保持 armed 重试，不推送未持久化的冻结。

**测试**: agent 端 `auth_ok.Frozen=true` → trade_command 被拒（无 kill_switch push）；wshub 握手 `auth_ok.Frozen` 反映 lookup（含 fail-closed/nil-hook）；auto-freeze 推送失败仍 latch、latch 失败保持 armed。全 -race 绿。

**未做（选项 B 的进一步硬化，非本次范围）**: latch 仍是 audit「最近事件」语义而非独立 enforcement 表；多操作员/多副本竞态非当前单操作员架构问题。

**验证侧坑**（保留备查）: mock agent 持仓默认 0，已有 instance baseline 非 0 → delta_report drift=10000bps，连 2 份(debounce=2)就 auto-freeze，会抢在手动 kill 前冻结。验手动 Kill happy-path 要么 seed mock 持仓对齐 baseline，要么用全新无 baseline instance。

---

### B2 — 无 limit order 价格保护路径（🟡 中）

**现状**: 下单全走 market order。Backlog ⑥ §4 的结论把"flash crash 价格保护"明确指向 **limit order（带策略选定的价格带）**，但 limit-order 路径**尚未实现**——策略只产 market 意图。

**影响**: 高波动瞬间，market order 按当时盘口成交（③ 按真实价吸收，账本不错），但没有"价格超出带就不成交"的保护。原型期可接受；真盘想要价格保护时这是前置。

**注意**: 这不是用 per-order 守卫(B3)硬阻 market 单——backlog-6 已论证那是把正确性风险换成可用性风险。正解是 limit order，需策略层 + wire + agent 三处协同，**未规格化**。

---

### B3 — per-order 价格分歧守卫（⑥ 选项 A，🟢 低，已延后）

**现状**: SaaS 按 kline close 定 qty，agent 按盘口成交，两价分歧。**已延后**——misconfig 半边已被 env 一致性断言（⑥ v1，已 ship）覆盖；flash 半边交给 limit order(B2)。

**为何延后**: testnet-hostile（分歧永远巨大→拒每单）；每 market 单加一次盘口往返；阈值是 `[INVENTED v1]` 无真盘数据可调。等真盘分歧分布数据再决定是否做。详见 `backlog-6 §4–6`。

---

### B4 — datafeeder 中段空洞无 heal（🟢 低，已文档化）

**现状**: `datafeeder_cron.sh` 只补尾部（`last-bar` → today）。若历史中段有空洞，无 `heal` 子命令回填。原型期概率极低，作为已知局限。

---

### B5 — datafeeder cron 运营局限（🟢 低，已文档化于 runbook §G）

只补尾部 / 非原子 / DB 不可达时 fallback 到 30 天前（慢）/ `go run` 开销（生产建议 `DATAFEEDER_BIN` 指向编译产物）。

---

## 2. 分析页缺口（运维 + 前端，源自 `decision-analysis-page-productionization.md`）

> P0（#1 traces 重导 + #5 requirements.txt）已于 2026-06-10 完成。下列为剩余 P1/P2。

### G1 — dashboard 不持久（🟡 中，P1）

**现状**: dashboard + 导出都是手工 `nohup` 起的临时进程，机器重启即丢；runbook 通篇不提 :8088。
**修法**: systemd unit `quantlab-optuna-dashboard.service`（开机自启 + 崩溃重拉）+ runbook 补"分析页（可选组件）"一节。定性 = 可选诊断组件（:8088 挂不影响交易/进化）。

### G2 — 快照不自动刷新（🟢 低，P1）

**现状**: 导出是 on-demand wipe-rebuild，新 GA 任务跑完页面仍旧快照。
**修法**: cron 定时 `--mode traces` 重导（15–30 min）；导出脚本写临时文件再原子 `mv` 覆盖，消掉 wipe 期瞬时 502；可附 `user_attrs.exported_at` 标时效。与 G1 同批落 runbook。

### G3 — :8088 零鉴权裸暴露 + 前端 URL 硬编码 ✅ DONE 2026-06-10（走 A′，非反代）

**修法（A′：localhost bind + SSH 隧道 + URL 外置）**——反代方案（B）经实施前复核被否：optuna-dashboard 0.20.0 无子路径支持 + 现有 Bearer-JWT 不覆盖浏览器导航，「同源反代复用 auth」两堵墙；localhost bind 让 optuna **网络不可达**，安全等价且更强（无暴露面），零后端代码。详见 `decision-analysis-page-productionization.md` 的「Q4 复核」。

**已实现**:
- **前端**: `App.tsx` `OPTUNA_URL` 外置为 `import.meta.env.VITE_OPTUNA_URL`（默认 `http://localhost:8088/`）+ `web/.env.example` + `web/src/vite-env.d.ts` 类型增强。`npm run build` 绿。
- **安全/运维**: mainnet 一律 **绑 localhost**（默认 `127.0.0.1`，不传 `--host 0.0.0.0`），SSH 隧道访问。runbook 新增「分析页（可选诊断组件）」节（bind + 隧道 + `VITE_OPTUNA_URL` 构建说明 + venv requirements）。
- **dev 不变**: VM 上仍可 `--host 0.0.0.0` + `VITE_OPTUNA_URL=http://192.168.67.129:8088/`（dev 🟢 低危，决策文档已定）。

---

## 3. 前端缺口

### F1 — 前端 eslint 遗留债（🟢 低，不 gate）

`eslint` 报 `web/src/auth/AuthContext.tsx:85` `useAuth` 导出触发 `react-refresh/only-export-components`。**与功能无关**，`npm run build`=tsc+vite 不 gate lint。卫生项，可顺手清。

> 前端 F2 live monitor（Tier M 监控 + F2.3 干预 + Tier L 对账/error 流 + 手动 Kill 按钮）已全完结，无功能缺口。手动 Kill 按钮 = PR #17（base main，2026-06-10）。

---

## 4. 明确延后 / 否决（别明天重新发现）

| 项 | 处置 | 原因 |
|---|---|---|
| Redis | **已移除** | 单用户架构无多副本需求（[[project-redis-removed]]） |
| agent-side USD sizing（⑥ 选项 C） | **否决** | 反转冻结协议 §5.8 + 损 determinism/replay + 把资金 sizing 移到安全关键边 |
| Phase 5 `agentmsg.go` 提取 | **延后** | 等无实盘交易窗口 |
| Phase 6 durable reconnect replay | **延后** | 产品规格未定 |

---

## 5. 明天的建议顺序

1. ~~**B1（kill 服务端 latch）**~~ — ✅ DONE 2026-06-10（复用 audit_logs + `auth_ok.frozen` 握手下发）。
2. **G3 后端半边（反代 + auth）** — 现唯一安全级 mainnet 阻塞项；前端半边（VITE_OPTUNA_URL）顺带。
3. **G1 + G2（systemd + cron）** — 同一节 runbook，工程量小，与未提交的 `requirements.txt` 打成一个 analysis-page 提交。
4. **B2（limit order）** — 中优先，但需先规格化（策略层 + wire + agent），单独立项。
5. B3 / B4 / B5 / F1 — 低优先或已文档化，按需。
