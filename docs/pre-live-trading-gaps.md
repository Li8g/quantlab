# 距实盘测试的已知缺口清单（前端 + 后端）

Status: **工作清单（live inventory）** — 供逐项排期，不是规格文档
Date: 2026-06-10（更新：两个 mainnet 安全级阻塞 B1+G3 已闭环并合并 main）
用途: 安全机制代码已全部到位（kill_switch Option 3、对账自动冻结、env 一致性断言、③账本吸收成交均已 shipped）。本清单收拢「从当前 dev/原型可跑」到「mainnet 实盘安全」之间**已知**的前后端缺口。**2026-06-10 已闭环并合并 main：B1（PR #18）、G3+G1+G2（PR #20）、手动 Kill 按钮（PR #17）——mainnet 前已无安全级硬阻塞。** 剩余均为中/低优先或已延后/文档化。

相关源文档（细节不在本页复制，按需跳转）:
- `docs/mainnet-runbook.md` — 部署步骤 + 运维 + 已知运营局限（§G datafeeder）
- `docs/backlog-6-price-source-divergence.md` — 价格源分歧分析（per-order 守卫 A 延后 / agent-side sizing C 否决 / env 断言 v1 已 ship）
- `docs/decision-analysis-page-productionization.md` — 分析页 5 缺口（含本页 G1–G3）
- 记忆 `killswitch-option3` / `live-trading-gaps` / `ws-protocol-freeze`

---

## 0. 一页速览

| # | 缺口 | 层 | 严重度 | 阻塞 mainnet? | 状态 |
|---|---|---|---|---|---|
| **B1** | kill 无服务端持久 latch（选项 B） | 后端 | 🔴 安全 | ~~是~~ | ✅ MERGED main（PR #18, f4537a0） |
| B2 | limit order 价格保护（marketable-limit IOC cap） | 后端 | 🟡 中 | 否（market+③+reconcile 兜底） | ✅ 实现完成（本分支；决策见 decision-b2-limit-order-price-protection.md，待 PR） |
| B3 | per-order 价格分歧守卫（⑥ 选项 A） | 后端/agent | 🟢 低 | 否 | 延后（等真盘数据调阈值） |
| B4 | datafeeder 中段空洞无 heal | 后端 | 🟢 低 | 否（概率极低） | 已文档化局限 |
| B5 | datafeeder cron 运营局限 | 运维 | 🟢 低 | 否 | 已文档化（runbook §G） |
| **G1** | 分析页 dashboard 不持久（手工 nohup） | 运维 | 🟡 中 | 部分（重启即 502） | ✅ MERGED main（PR #20, e2ba58e；systemd unit） |
| **G2** | 分析页快照不自动刷新 | 运维 | 🟢 低 | 否 | ✅ MERGED main（PR #20；原子导出+cron） |
| **G3** | 分析页 :8088 零鉴权裸暴露 + URL 硬编码 | 前端+运维 | 🔴 安全(mainnet)/🟢(dev) | ~~是~~ | ✅ MERGED main（PR #20；A′：localhost+SSH 隧道+URL 外置） |
| F1 | 前端 eslint 遗留债 | 前端 | 🟢 低 | 否 | 已知，不 gate build |

> ~~真正在 mainnet 前必须处理的只有 **B1**（安全 latch）与 **G3**（暴露面）。~~ **B1 与 G3 均已于 2026-06-10 闭环并合并 main**（见下）。**mainnet 前已无安全级硬阻塞。** ~~剩余唯一中优先项是 **B2**（limit order 价格保护，需先规格化）；~~ **B2 已规格化并实现（marketable-limit IOC，决策 D1–D6 全拍板，见 decision-b2-limit-order-price-protection.md，本分支待 PR）。** 其余为低优先或已延后/已文档化。

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

### B2 — limit order 价格保护（🟡 中，✅ 实现完成）

**实现**: SaaS dispatcher 把每个 market 意图改写成 **marketable limit IOC**，限价 = `latestClose×(1±cap/1e4)`（买 +、卖 −）。flash 把价推过 cap → 交易所拒成交（IOC 撤余量，无挂单、无锁仓），而非按盘口任意差价成交。cap 是执行层护栏（`orders.price_cap_bps`，缺省 50bps，0=退回 market），不进 GA、不进回测。决策 D1–D6 全拍板见 `decision-b2-limit-order-price-protection.md`。

**回测中性（关键）**: 因为回测按 `close×(1±slippage)` 成交且不变量 1 保证 `cap ≥ slippage_bps`（deploy-champion 时校验），marketable limit 在回测里与 market **数值恒等** → ScoreTotal 零变化、不 bump `fitness_version`、champion 免重测。保护纯活在实盘路径。

**改动面**: wire 加 additive `time_in_force`（omitempty 缺省 GTC）；dispatcher `buildTradeCommand` 转换；agent `SubmitLimit` 支持 IOC；deploy-champion 校验 cap≥slippage + 审计行。**策略代码与回测 simulator 零改动。** EXPIRED→cancelled 终态映射 UDS 路径本已存在。

**未做（明确）**: 被动 maker 挂价（D1-b，做市另一产品）、回测 intrabar 撮合（D3-b，会触发版本事件）、cap 进基因（"执行层进化"独立大项目）。per-order 价格分歧守卫(B3)仍延后（等真盘数据）。

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

### G1 — dashboard 持久化 ✅ DONE 2026-06-10

systemd unit `scripts/quantlab-optuna-dashboard.service`（开机自启 + 崩溃重拉 `Restart=on-failure`；注释强制绑 localhost，禁 `--host 0.0.0.0`）。runbook「分析页（可选诊断组件）」节加一次性安装步骤（venv → 首次导出 → enable）。定性 = 可选诊断组件（:8088 挂不影响交易/进化）。

### G2 — 快照自动刷新 ✅ DONE 2026-06-10

`quantlab_to_optuna.py` 改 **temp + `os.replace` 原子替换**（重建期不再让在跑 dashboard 读半成品，消 wipe 期 502）+ 打 `study.user_attrs.exported_at` 时效戳。`scripts/optuna_export_cron.sh` 包装「重导 → 重启 dashboard 拉新数据」，runbook 给 `*/20` cron 示例（重启需权限：root crontab 或 sudo 免密）。实跑验证：15256 trials 原子替换、无 `.tmp` 残留、3 study 均带 exported_at。

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

## 5. 进度与剩余顺序

**✅ 2026-06-10 已闭环并合并 main：**
1. ~~**B1（kill 服务端 latch）**~~ — 复用 audit_logs + `auth_ok.frozen` 握手下发 + fail-closed（PR #18, f4537a0）。
2. ~~**G3（:8088 暴露面 + URL）**~~ — 走 A′：optuna 绑 localhost + SSH 隧道 + 前端 `VITE_OPTUNA_URL` 外置（非反代，理由见决策文档 Q4 复核）（PR #20, e2ba58e）。
3. ~~**G1 + G2（systemd + cron）**~~ — systemd unit + 原子导出 + cron 自动刷新（PR #20，同批含 `requirements.txt`）。

**✅ 2026-06-10 实现完成（本分支，待 PR）：**
4. ~~**B2（limit order）**~~ — marketable-limit IOC，dispatcher 转换 + `orders.price_cap_bps` 护栏 + wire `time_in_force` + agent IOC + deploy 校验 cap≥slippage；回测中性不 bump（decision-b2-limit-order-price-protection.md）。**策略与回测零改动。**

**剩余（按需）：**
5. B3 / B4 / B5 / F1 — 低优先或已文档化，按需。
