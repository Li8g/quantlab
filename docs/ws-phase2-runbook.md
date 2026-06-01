# WS Phase 2 真 testnet 实跑手册

## 5 分钟速览

**目标**: 在 Binance testnet 上跑通 `SaaS ⇄ Agent ⇄ Binance` 实盘链路,**不碰真钱**。

**你需要**: 一个 GitHub 账号(登 testnet)· Postgres 在跑 · 能 `go run` 本仓库。
**耗时**: 一次性配置约 20 分钟;之后每次起服务约 1 分钟。

| 步骤 | 一次性? | 做什么 |
|---|---|---|
| 准备 A | ✓ | Binance testnet:GitHub 登录 → 拿 api_key/secret → 确认余额 |
| 准备 B | 读一下 | `account_id` = 你自己起的逻辑名(本手册统一用 `main`) |
| 0 | ✓ | 修 `config.agent.yaml`(去缩进 + 填 account_id / saas_token / saas_url) |
| 1 | ✓ | seed admin 用户 |
| 2 | ✓ | seed agent token → 填进 `config.agent.yaml` 的 `saas_token` |
| 3 | 每次 | 起 SaaS(`dev` 自动起 WS:8081 + cron) |
| 4 | 每次 | 登录拿 admin JWT + 建 StrategyInstance |
| 5 | 每次 | 起 agent → ✅ **L1 达成**(WS 连上 + 60s delta_report) |
| 6 | 可选 | deploy champion + start → 下单链路(L2) |
| 7 | 可选 | kill_switch 实跑(L3) |

**三层验证目标**(详见下方):**L1** WS 链路通(核心)· **L2** 一笔 testnet 成交落库 · **L3** kill→拒单+frozen banner。

> **只想验证 WS 链路**:做到**步骤 5** 即可 —— L1 就是 Phase 2 的核心目标,L2/L3 是加分项。

---

把实盘链路在 Binance **testnet** 上真实跑通:
`SaaS(WS Hub + cron) ⇄ Agent ⇄ Binance Spot testnet`。

验证目标分两层:
- **L1(最小,核心目标)**: agent 连上 SaaS、handshake ready、每 60s 发 `delta_report`(含 testnet 真持仓)、SaaS 收到并对账。这验证 WS 链路 + delta_report sender(Phase 1)+ 持仓对账(Phase 8)。
- **L2(完整)**: 建 live instance + cron tick → `trade_command` → agent 在 testnet 下单 → fill → `order_update`。
- **L3(可选)**: 验证 kill_switch(Option 3)实跑(手动 kill / drift 自动冻结 / frozen banner)。

> 命令里的 IP `192.168.67.129` 是这台 VM 的地址(见 memory `env_vm_host_ip`)。若 agent 与 SaaS 同机,可用 `localhost`/`127.0.0.1`。SaaS 监听 `:8080`(HTTP)+`:8081`(WS),都绑所有接口,**不需要额外的 --host flag**。

---

## 前置检查

- [ ] Postgres 在跑,且 `config.yaml` 的 `database:` 指向它(默认 `quantlab@localhost:5432/quantlab`)。
- [ ] `config.yaml` 的 `jwt.secret` 已填(≥32 字节)、`app_role: dev`(dev 会起 WS Hub + cron,正是要的)。
- [ ] Binance **testnet** API key/secret(你已填进 `config.agent.yaml`)。从 https://testnet.binance.vision 用 GitHub 登录生成。
- [ ] testnet 账户**有余额**(同站点领取测试 USDT/BTC),否则 agent 拉到的 positions 为空。
- [ ] 已 build 出 `--seed-agent-token` CLI(commit `63f958e`)。

---

## 准备 A — Binance Spot Testnet(注册 / API key / 余额)

> ⚠️ 用 **Spot** Testnet(`testnet.binance.vision`),**不是** Futures Testnet
> (`testnet.binancefuture.com`)。Testnet 完全独立于真实 Binance、**不碰真钱**、
> 无需 KYC。注意账户与余额会被官方**定期重置**(约每月),重置后通常要**重新生成
> API key**。下面 UI 步骤以 2026 年初为准,可能变化,**以官网为准**。

1. **登录**: 打开 https://testnet.binance.vision/ → 点 "Log In with GitHub"。
   Testnet 用 GitHub OAuth 登录,不需要真实 Binance 账户、不需要实名。
2. **生成 API Key**: 登录后页面有 **"Generate HMAC_SHA256 Key"**。填一个 label
   (如 `quantlab-agent`)→ 生成。得到:
   - **API Key**(公开标识)
   - **Secret Key** —— ⚠️ **只显示这一次**,立刻复制保存。
   - 这两个就是 `config.agent.yaml` 里的 `exchange.api_key` / `exchange.api_secret`
     (你说已填的就是它们)。
3. **余额**: Spot Testnet 账户**预置**了测试资金(各币种一批,含 USDT / BTC),
   一般无需手动充值,登录页可看余额。若为 0(刚被重置):在 testnet 页面找领取
   入口(faucet),或等下个重置周期。
   - 验证: agent 起来后第一个 `delta_report` 的 positions 会显示真实 testnet
     持仓;为空 = 余额为 0。
4. **base_url**: 固定 `https://testnet.binance.vision`(模板里已是)。REST endpoint
   形如 `https://testnet.binance.vision/api/v3/order`。

> testnet 与真实盘的差别**只在 base_url + 凭证**;`binance_spot` adapter 同一套代码
> 两边都跑(见 [[ws-protocol-freeze]] commit f968c16)。将来上真实盘 = 换
> `base_url: https://api.binance.com` + 真实 key —— **那是真钱,务必先在 testnet 跑通。**

## 准备 B — `account_id` 到底是什么

`account_id` **是你自己起的一个逻辑名字**,不是从 Binance 拿来的、也不对应任何
交易所字段。它在 QuantLab 内部把三样东西绑在一起:

```
 一个 agent 进程  ⇄  一套交易所凭证(api_key/secret)  ⇄  一个/多个 StrategyInstance
                  都靠同一个 account_id 关联
```

- **长什么样**: 任意字符串,≤64 字符。例如 `main`、`binance-testnet-1`、`btc-spot`。
  本手册统一用 `main`。
- **不是什么**: 不是 Binance API key(那是 `exchange.api_key`)、不是 Binance 的
  account number、不是邮箱/用户名。Binance 只认 api_key/secret;`account_id` 纯粹是
  QuantLab 这边的命名。
- **约束**: 同一 owner 下 `(strategy_id, pair, account_id)` 组合唯一(DB partial
  unique index);≤64 字符。
- **三处必须完全一致**(见步骤 0): `config.agent.yaml` 的 `account_id`
  ↔ `--seed-agent-token <这里>` ↔ 建 instance 时的 `account_id`。
- **怎么起名**: 单账户实验直接用 `main`。将来一个人跑多个交易所子账户(每套独立
  api_key/secret)时,每个子账户给一个不同的 `account_id`,各自跑一个 agent 进程
  (协议规定 agent ↔ account 是 1:1)。

---

## 步骤 0 — 修 `config.agent.yaml`

⚠️ **当前文件顶层字段有 2 空格缩进** —— YAML 顶层 key 不能缩进,否则解析后字段全空,agent 启动报 `agent: agent_id empty`。改成下面这样(顶层 0 缩进,`exchange:`/`idempotency:` 的子字段 2 缩进):

```yaml
agent_id: "agent-1"
account_id: "main"                                      # ← 见下方"account_id 一致性"
saas_url: "ws://192.168.67.129:8081/api/v1/ws/agent"    # ← 完整路径,别用占位的 ...
saas_token: "<步骤 2 拿到的 agt_... token>"
exchange:
  name: "binance_spot"
  api_key: "<保持你已填的>"
  api_secret: "<保持你已填的>"
  base_url: "https://testnet.binance.vision"
idempotency:
  db_path: "agent.db"
```

**account_id 一致性(关键)**: 下面三处的 `account_id` 必须**完全相同**(本手册统一用 `main`):
1. `config.agent.yaml` 的 `account_id`
2. 步骤 2 `--seed-agent-token` 的参数
3. 步骤 4 建 StrategyInstance 时的 `account_id`

不一致会导致:token 验证不过(agent↔token)、或 delta_report 对账找不到 baseline(agent↔instance)。

---

## 步骤 1 — seed admin 用户(一次性)

```bash
go run ./cmd/saas --config config.yaml \
  --seed-user-email admin@local --seed-user-password 'CHANGE_ME'
```
建一个 `role=admin` 的 User 后退出(这是 v1 唯一的建用户路径,无注册端点)。

## 步骤 2 — seed agent token(一次性)

```bash
go run ./cmd/saas --config config.yaml --seed-agent-token main
```
打印一行 `agt_<ULID>_<secret>`。**复制它填进 `config.agent.yaml` 的 `saas_token`** —— 明文只此一次可见(库里只存 bcrypt)。

## 步骤 3 — 启动 SaaS

```bash
go run ./cmd/saas --config config.yaml
```
看到:
```
saas: listening on :8080 (app_role=dev, strategies=[sigmoid_v1])
saas: ws listening on :8081
```
**保持运行**,另开终端做后续。

## 步骤 4 — 登录拿 admin JWT + 建 instance

```bash
# 登录(role:"admin" 显式请求 admin token,10min TTL,过期重登)
TOKEN=$(curl -s -X POST http://192.168.67.129:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"CHANGE_ME","role":"admin"}' | jq -r .token)

# 建 StrategyInstance(account_id 必须 = main)
INST=$(curl -s -X POST http://192.168.67.129:8080/api/v1/instances \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"strategy_id":"sigmoid_v1","pair":"BTCUSDT","account_id":"main"}' | jq -r .instance_id)
echo "INST=$INST"
```

## 步骤 5 — 启动 agent,验证 L1

```bash
go run ./cmd/agent --config config.agent.yaml
```
**预期(L1 通过的标志):**
- agent 端: `agent_session_ready`
- SaaS 端: `ws_agent_ready account_id=main`
- 每 60s,SaaS 端出现 `ws_agent_msg ... type=delta_report`

✅ 到此 **L1 达成** —— WS 链路 + delta_report 收发跑通。

> 对账落库要 baseline:instance 至少 tick 过一次(有 portfolio 行)才会 reconcile,否则日志 `delta_report_reconcile_skipped_no_baseline`(正常,不是错)。baseline 在 L2 start 后产生。

---

## 步骤 6 —(L2)下单链路: deploy champion + start

```bash
# 列 champion,挑一个 challenger_id(promoted gene)
curl -s http://192.168.67.129:8080/api/v1/champions/history \
  -H "Authorization: Bearer $TOKEN" | jq .

# 部署 + 启动 → instance 变 live → cron 开始 tick
curl -s -X POST http://192.168.67.129:8080/api/v1/instances/$INST/deploy-champion \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"challenger_id":"<上一步挑的 champion id>"}'

curl -s -X POST http://192.168.67.129:8080/api/v1/instances/$INST/start \
  -H "Authorization: Bearer $TOKEN"
```
cron 每个 tick 跑 `strategy.Step`;产生 `OrderIntent` 时 → `trade_command` → agent → binance testnet 下单 → fill → `order_update`。

**预期(L2):** agent 端 binance 下单日志;SaaS 端 `ws_agent_msg type=ack` / `type=order_update`;DB `trade_records` + `spot_executions` 有行。`/instances/$INST/live` 的 `recent_trades` 出现成交。

---

## 步骤 7 —(L3,可选)验证 kill_switch

```bash
# 手动 kill → agent frozen,后续 trade_command 被拒
curl -s -X POST http://192.168.67.129:8080/api/v1/instances/$INST/kill \
  -H "Authorization: Bearer $TOKEN"

# /live 看 frozen banner(kill_status 字段)
curl -s http://192.168.67.129:8080/api/v1/instances/$INST/live \
  -H "Authorization: Bearer $TOKEN" | jq .kill_status
```
- 预期: agent 端后续 tick 的 trade_command 回 `rejected "agent frozen by kill_switch"`;`kill_status.reason=manual_admin_action`。
- **自动 kill**: 在 testnet 手动下单制造持仓与 SaaS 账本的 drift → 连续 2 个 delta_report 超 200bps → SaaS 自动发 kill(`reason=discrepancy_detected`,operator=system)。
- **解冻(v1)**: 重启 agent 进程(`frozen` 是 Client 级硬闩锁,重连不解;只有进程重启清除)。

---

## 故障排查

| 现象 | 原因 / 处理 |
|---|---|
| agent `agent: agent_id empty` | `config.agent.yaml` 缩进没去干净(步骤 0) |
| agent `auth_fail: invalid_token` | `saas_token` 没填对(用步骤 2 的)/token 已 revoke / account_id 不匹配 |
| agent 连不上 / 一直 backoff 重连 | IP/port/path 错;确认 SaaS 打印了 `ws listening on :8081`;防火墙;`saas_url` 路径必须是 `/api/v1/ws/agent` |
| `delta_report_reconcile_skipped_no_baseline` | 正常 —— instance 还没 tick 过。L2 start 后等一个 tick |
| testnet positions 空 | testnet 账户无余额,去 testnet.binance.vision 领 |
| 登录 401 / instance 创建 403 | JWT 过期(admin 10min)重登;或 login 没带 `"role":"admin"`(默认给 viewer,建 instance 要 operator+) |

---

## 完成标准

- **L1**: agent ready + 60s `delta_report` 在 SaaS 端可见 → **Phase 2 核心目标达成**。
- **L2**: 一笔 testnet 成交在 `trade_records`/`spot_executions` 落库。
- **L3**: kill → agent 拒单 + `/live` frozen banner。

L1 是 [[ws-protocol-freeze]] memory 里标的 "Phase 2 未做" 的实质;跑通即可更新该 memory。
