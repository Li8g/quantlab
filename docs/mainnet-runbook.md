# QuantLab Mainnet 部署手册

## ⚠️ 上真实盘前的关键提示（必读）

**与 testnet 的关键差异：**

| 项目 | testnet | mainnet |
|---|---|---|
| `app_role` | `dev` | `saas` |
| Schema 迁移 | AutoMigrate（启动时自动） | Goose（启动时自动，prod schema） |
| `live.expected_environment` | 空或 `testnet` | `mainnet`（必填；handshake 硬断言） |
| `jwt.secret` | 任意字符串 | `openssl rand -hex 32` 真随机，≥32 字节 |
| Binance `base_url` | `https://testnet.binance.vision` | `https://api.binance.com` |
| 资金 | 测试代币，定期重置 | **真实资金，不可逆** |
| 参考手册 | `docs/ws-phase2-runbook.md` | 本文档 |

**不可逆操作清单（操作前三思）：**
- agent 下单：真实交易，不可撤销已成交部分
- `POST /instances/:id/start`：开始 cron tick，可能立刻下单
- `POST /champions/:id/retire`：退役后需重新 promote 才能部署

---

## 速览：步骤全图

| 步骤 | 一次性? | 做什么 |
|---|---|---|
| A | ✓ | 生成并保管 Binance 主网 API Key |
| B | ✓ | 配置 `config.yaml`（saas 关键字段） |
| C | ✓ | 配置 `config.agent.yaml`（主网） |
| D | ✓ | 初始历史 K 线导入 |
| E | ✓ | Seed 管理员用户 |
| F | ✓ | Seed agent token |
| G | ✓ | 配置 datafeeder 定时导入（cron） |
| 1 | 每次 | 启动 SaaS（Goose 自动迁移） |
| 2 | 每次 | 登录拿 admin JWT + 建 StrategyInstance |
| 3 | 每次 | 启动 agent → 验证 L1 |
| 4 | 按需 | Deploy champion + start → 验证 L2 |

**完成标准：**
- **L1**：`agent_session_ready` + 每 60s `delta_report` 在 SaaS 端可见
- **L2**：一笔成交在 `trade_records` / `spot_executions` 落库

---

## 前置检查

- [ ] Postgres 在跑，且 `config.yaml` 的 `database:` 指向它
- [ ] Binance 主网账户已完成 KYC，持有真实 USDT 余额
- [ ] API key 已生成（见步骤 A）
- [ ] Champion 已 promoted（无 champion 无法 start instance）
- [ ] 仓库已编译或可 `go run`
- [ ] 时区确认：系统时间为 UTC（K 线日期计算依赖 UTC）

---

## 一次性配置

### 步骤 A — 生成 Binance 主网 API Key

1. 登录 https://www.binance.com → 账户 → API 管理 → 创建 API
2. **权限勾选：仅勾选 "现货与杠杆交易"，不勾选提现**
3. 强烈建议绑定服务器 IP 白名单（防 key 泄露后被滥用）
4. 复制 API Key 和 Secret Key（Secret **只显示一次**，立刻保存）

> ⚠️ API Key 和 Secret Key 是真实资金的凭证，不要提交到 git，不要明文写在共享文档。

---

### 步骤 B — 配置 `config.yaml`（完整 mainnet 配置）

将以下内容保存为 `config.yaml`，替换所有 `<...>` 占位符：

```yaml
# QuantLab mainnet 生产配置
# 生成命令：cp config.example.yaml config.yaml，再按本文档逐项修改

# app_role 是全局行为开关。saas 的连锁效果：
#   - 启动时自动执行 goose up（prod schema，幂等，已最新则跳过）
#   - jwt.secret 长度强制 ≥32 字节，不足直接拒绝启动
#   - database.migration_mode=automigrate 被铁律 4 封禁，误填会报错退出
#   - live.expected_environment 不匹配时 handshake 硬断言（不像 dev 仅警告）
app_role: saas

database:
  host: localhost
  port: 5432
  user: quantlab
  password: <数据库密码>
  database: quantlab
  # ssl_mode: 生产环境建议 require 或 verify-full；同机部署 disable 可接受
  ssl_mode: disable
  max_open_conns: 50
  max_idle_conns: 10
  # migration_mode 留空 → app_role=saas 自动推导为 goose。
  # 禁止填 automigrate（铁律 4：prod schema 由 goose 独占管理）。
  migration_mode: ""

jwt:
  # MUST ≥32 字节（HS256 / RFC 7518）。app_role=saas 下长度不足直接拒绝启动。
  # 生成命令：openssl rand -hex 32
  secret: <openssl rand -hex 32 的输出，64 个十六进制字符>
  # ttl：viewer / operator token 有效期。24h 是合理的默认值。
  ttl: 24h
  # admin_ttl：sudo-style 管理员 token 有效期。短 TTL 缩小误操作窗口
  # （promote / retire / kill 等高风险操作需 admin token）。过期重登即可。
  admin_ttl: 10m

server:
  http_listen: ":8080"
  ws_listen: ":8081"
  metrics_listen: ":9090"
  shutdown_timeout: 30s

# 策略评估时使用的默认摩擦系数。Binance 主网 VIP0 挂单约 10bps，吃单约 10bps。
# 按实际费率等级调整；GA 评估用这套值，影响 champion 的 ScoreTotal 比较基准。
friction:
  taker_fee_bps: 10
  slippage_bps: 5

# GA 引擎全局默认值。可通过 POST /api/v1/evolution/tasks 的请求体逐任务覆盖。
ga:
  pop_size: 200
  max_generations: 30
  elite_ratio: 0.05
  fatal_mdd: 0.70        # MDD ≥ 70% → Fatal，该 gene 直接淘汰
  oos_days: 60           # OOS holdout 窗口长度（天）
  fatal_audit_sample_rate: 0.05
  sbb_block_len_fallback: 12

# 实盘自动冻结（kill_switch Option 3）。
# agent 被冻结的条件：managed 资产的对账漂移连续 freeze_debounce_reports 次
# 超过 freeze_tolerance_bps。
#
# 调参依据：观察 delta_report_reconcile_summary 日志的 max_managed_drift_bps 正常峰值。
# 冻结线应高于正常 in-flight fill 引起的瞬时漂移（一笔未确认 fill 的量级）。
# 低频策略（每小时 < 1 笔）默认 200bps / 2 次足够；高频策略需相应上调。
reconcile:
  freeze_tolerance_bps: 200
  freeze_debounce_reports: 2

data_feed:
  binance_archive_base_url: https://data.binance.vision
  binance_api_base_url: https://api.binance.com
  api_rate_interval: 150ms
  default_symbol: BTCUSDT
  default_interval: 1m
  # max_bar_staleness：Tick 时刻允许最新 1m K 线的最大"陈旧度"。
  # 超出则跳过本次 Tick 并记录 scheduler_tick_skipped_stale_data——
  # 用陈旧收盘价定价会被交易所 LOT_SIZE / notional 拒单，
  # 继而触发对账漂移自动冻结，提前跳过更安全。
  # 建议设为 cron 运行间隔的 1.5 倍（cron 每小时 → 90m），留出执行延迟余量。
  max_bar_staleness: 90m

live:
  # expected_environment：强制要求 agent 连接的交易所环境（mainnet / testnet / mock）。
  # app_role=saas 下不匹配 → auth_fail{environment_mismatch}，agent 拒绝握手。
  # 空值 = 断言禁用（不推荐生产环境留空）。
  expected_environment: mainnet
```

---

### 步骤 C — 配置 `config.agent.yaml`（主网）

```yaml
agent_id: "agent-1"
account_id: "main"
saas_url: "ws://<服务器IP>:8081/api/v1/ws/agent"
saas_token: "<步骤 F 拿到的 agt_... token>"
exchange:
  name: "binance_spot"
  api_key: "<步骤 A 的 API Key>"
  api_secret: "<步骤 A 的 Secret Key>"
  base_url: "https://api.binance.com"    # ← 主网，不是 testnet
idempotency:
  db_path: "agent.db"
```

**`account_id` 三处必须完全一致**（同 testnet 手册要求）：
1. `config.agent.yaml` 的 `account_id`
2. 步骤 F `--seed-agent-token` 的参数
3. 步骤 2 建 StrategyInstance 的 `account_id`

---

### 步骤 D — 初始历史 K 线导入（一次性）

策略评估需要足够的历史 K 线（最少 MinEvalBars，覆盖四窗口 = 约 10 年）。

```bash
# 示例：导入 BTCUSDT 1m 从 2015-01-01 到今天（耗时较长）
go run ./cmd/datafeeder import \
  --symbol BTCUSDT --interval 1m \
  --from 2015-01-01 --to $(date +%Y-%m-%d)
```

导入是幂等的，中断后重跑会跳过已有数据。完成后验证：

```bash
go run ./cmd/datafeeder stats
go run ./cmd/datafeeder verify --symbol BTCUSDT --interval 1m
```

---

### 步骤 E — Seed 管理员用户（一次性）

```bash
go run ./cmd/saas --config config.yaml \
  --seed-user-email admin@local --seed-user-password 'CHANGE_ME'
```

---

### 步骤 F — Seed agent token（一次性）

```bash
go run ./cmd/saas --config config.yaml --seed-agent-token main
```

输出一行 `agt_<ULID>_<secret>`，**明文只显示一次**，立刻填入 `config.agent.yaml` 的 `saas_token`。

---

### 步骤 G — 配置 datafeeder 定时导入（cron）

**为什么必须配置：** `max_bar_staleness: 15m` 守卫要求 DB 里最新 1m K 线在 Tick 时刻不超过 15 分钟。Binance Vision 归档最快 T+1 发布，当月数据走 REST API fallback。不配 cron → 数据陈旧 → 实例静默跳过 Tick → 不下单且无告警。

**推荐方案：`last-bar` 动态起点 + wrapper 脚本**

脚本 `scripts/datafeeder_cron.sh` 已就绪：
1. 调 `datafeeder last-bar` 查 DB 最新 bar 日期
2. 以该日期为 `--from`，今天为 `--to`，执行增量导入
3. `last-bar` 失败时（DB 不可达或无数据）fallback 到 30 天前

**配置步骤：**

```bash
# 确认脚本可执行
chmod +x scripts/datafeeder_cron.sh

# 编辑 crontab（重要：cron 里 % 需转义，wrapper 脚本绕开了这个问题）
crontab -e
```

添加一行（每小时整点运行）：

```
0 * * * * /path/to/quantlab/scripts/datafeeder_cron.sh >> /var/log/datafeeder.log 2>&1
```

可选环境变量（在 crontab 行前设置）：

```
DATAFEEDER_SYMBOL=BTCUSDT
DATAFEEDER_INTERVAL=1m
DATAFEEDER_FALLBACK_DAYS=30
# DATAFEEDER_BIN=/path/to/compiled/datafeeder   # 推荐生产环境用编译产物代替 go run
```

`max_bar_staleness` 建议设为 cron 间隔的 1.5 倍（cron 每小时 → `max_bar_staleness: 90m`），留出 cron 执行延迟的余量。

**验证 cron 在跑：**

```bash
tail -f /var/log/datafeeder.log
# 预期：每小时出现 [2026-06-10T01:00:00Z] last-bar=2026-06-10, importing 2026-06-10 → 2026-06-10
```

#### 已知局限性（`last-bar` 方案）

> 以下问题在原型期概率极低，但上线前应知晓，后续可按需演进。

1. **只补尾部，不处理中段空洞**
   如果历史数据中间某段从未导入（如步骤 D 初始导入意外跳过了某月），`last-bar` 无法发现。中段空洞需手动：
   ```bash
   go run ./cmd/datafeeder verify --symbol BTCUSDT --interval 1m
   # 查看输出的 gaps，再手动 import --from <gap_start> --to <gap_end>
   ```
   长期演进方向：`datafeeder heal` 子命令（读 `kline_gaps` 表自动补全）。

2. **`last-bar` 与 `import` 非原子**
   两次调用之间若有并发手动导入，理论上 `--from` 会略早于实际最新 bar，重复导入已有数据（幂等，无副作用）。单操作员场景可忽略。

3. **`last-bar` 不可达时 fallback 窗口较宽**
   DB 不可达时脚本 fallback 到 `DATAFEEDER_FALLBACK_DAYS`（默认 30 天）。导入幂等，但重新下载 30 天数据比正常增量慢。若 cron 频繁触发 fallback，说明 DB 有更严重的问题需排查。

4. **生产环境应使用编译产物，不用 `go run`**
   `go run` 每次重新编译（约 3-10 秒），在高频 cron 下浪费时间。部署时：
   ```bash
   go build -o /usr/local/bin/datafeeder ./cmd/datafeeder
   # 然后设置 DATAFEEDER_BIN=/usr/local/bin/datafeeder
   ```

---

## 每次启动

### 步骤 1 — 启动 SaaS

前端 SPA 通过 `go:embed`（`web/embed.go`）编译进 saas 二进制，**编译前必须先构建前端**，否则只嵌入占位文件、网页不可用（API 仍正常）：

```bash
cd web && npm ci && npm run build && cd ..   # 产出 web/dist，被 go:embed 嵌入
go run ./cmd/saas --config config.yaml        # 或先 go build -o saas ./cmd/saas 再 ./saas
```

启动后**网页 UI 与 API 同源**，直接访问 `http://<服务器IP>:8080/` 即可登录（promote/retire、live 监控、start/stop/deploy/resume 都在网页里）。下面各步的 curl 是等价的脚本化路径，按需选用。无需 nginx 或 vite。

`app_role=saas` 启动时自动执行 `goose up`（幂等，已是最新则跳过）。

预期日志：
```
goose: no migrations to run. current version: 3
saas: listening on :8080 (app_role=saas, strategies=[sigmoid_v1])
saas: ws listening on :8081
```

若 Goose 报错（迁移失败），排查：
```bash
# 查看当前迁移状态
go run ./cmd/saas --config config.yaml goose status   # 若支持
# 或直接查 DB
psql -c "SELECT version, is_applied FROM goose_db_version ORDER BY version;"
```

### 步骤 2 — 登录拿 admin JWT + 建 StrategyInstance

```bash
TOKEN=$(curl -s -X POST http://<服务器IP>:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"CHANGE_ME","role":"admin"}' | jq -r .token)

INST=$(curl -s -X POST http://<服务器IP>:8080/api/v1/instances \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"strategy_id":"sigmoid_v1","pair":"BTCUSDT","account_id":"main"}' | jq -r .instance_id)
echo "INST=$INST"
```

admin token TTL 为 10 分钟，过期重新登录。

### 步骤 3 — 启动 agent，验证 L1

```bash
go run ./cmd/agent --config config.agent.yaml
```

**预期（L1 通过标志）：**
- agent 端：`agent_session_ready`
- SaaS 端：`ws_agent_ready account_id=main`
- 每 60s，SaaS 端出现 `ws_agent_msg ... type=delta_report`

> **主网与 testnet 的关键差异**：`live.expected_environment: mainnet` 是硬断言。`app_role=saas` 下环境不匹配直接返回 `auth_fail{environment_mismatch}`，agent 拒绝连接（不像 dev 只警告）。检查 `config.agent.yaml` 的 `base_url` 确认是主网地址。

genesis 注资逻辑与 testnet 相同：首个 `delta_report` 用交易所真实持仓初始化 baseline，第二份起开始对账。

### 步骤 4 — Deploy champion + start（L2）

> **⚠️ 首次 L2 提醒：start 会立刻开始 cron tick，可能在下一个 tick 就下真实订单。**
> 建议 start 前确认以下两点，避免第一笔单量超出预期：
>
> 1. **账户余额与策略仓位量级匹配**：`/instances/$INST/live` 的 `portfolio` 字段显示
>    genesis 注资后的 baseline（USDT + BTC）。champion gene 对应的信号强度决定
>    OrderIntent 的仓位比例，确认该比例下的绝对金额在可接受范围内。
> 2. **kill switch 随时可用**：start 后若发现首笔单量异常，立刻：
>    ```bash
>    curl -s -X POST http://<服务器IP>:8080/api/v1/instances/$INST/kill \
>      -H "Authorization: Bearer $TOKEN"
>    ```
>    kill 后 agent 拒绝后续 trade_command，已成交部分不可撤销。

```bash
# 列出已 promoted 的 champion
curl -s http://<服务器IP>:8080/api/v1/champions/history \
  -H "Authorization: Bearer $TOKEN" | jq .

# 部署
curl -s -X POST http://<服务器IP>:8080/api/v1/instances/$INST/deploy-champion \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"challenger_id":"<champion_id>"}'

# 启动（cron 开始 tick，可能立刻下单）
curl -s -X POST http://<服务器IP>:8080/api/v1/instances/$INST/start \
  -H "Authorization: Bearer $TOKEN"
```

**预期（L2）：** `trade_records` + `spot_executions` 有行；`/instances/$INST/live` 的 `recent_trades` 出现成交。

---

## 运维操作

### 轮换 agent token

```bash
# 生成新 token
go run ./cmd/saas --config config.yaml --seed-agent-token main
# 将新 token 填入 config.agent.yaml 的 saas_token，重启 agent
```

### Kill switch 手动触发与解冻

```bash
# 手动 kill
curl -s -X POST http://<服务器IP>:8080/api/v1/instances/$INST/kill \
  -H "Authorization: Bearer $TOKEN"

# 解冻（admin）
curl -s -X POST http://<服务器IP>:8080/api/v1/instances/$INST/resume \
  -H "Authorization: Bearer $TOKEN"
```

自动冻结触发条件：连续 `freeze_debounce_reports` 个 delta_report 对账漂移超 `freeze_tolerance_bps`。解冻后 auto-freeze 计数器清零、重新武装。

### 更换 champion

1. 先 retire instance（防止 champion 仍在部署中被 retire 拦截）：
   ```bash
   curl -s -X POST http://<服务器IP>:8080/api/v1/instances/$INST/stop \
     -H "Authorization: Bearer $TOKEN"
   ```
2. Retire 旧 champion（admin）
3. Promote 新 challenger（admin）
4. Deploy + start instance

### 停机与重启（顺序很重要）

**停机顺序：agent 先 → SaaS 后**
- 先停 agent（Ctrl+C 或 SIGTERM），等 agent 日志出现断连
- 再停 SaaS

反向顺序（SaaS 先停）会导致 agent 疯狂重连直到超时，日志噪音多且有未处理 fill 风险。

---

## 分析页（可选诊断组件，optuna-dashboard）

**定位**：`Analysis ↗` 是指向 optuna-dashboard 的只读参数空间探索页（`:8088`）。它是**可选诊断组件**——挂掉不影响交易/进化（saas + agent 照常运行），SLA 低于核心服务。

**⚠️ 安全（G3，mainnet 必须）**：optuna-dashboard **自身零鉴权**。`--host 0.0.0.0` 裸暴露 = 任何能摸到端口的人可读**完整策略参数空间 + champion 历史**。mainnet 一律**只绑 localhost**（默认即 `127.0.0.1`，**不要**传 `--host 0.0.0.0`），通过 SSH 隧道访问：

```bash
# 服务器上启动（绑 localhost，仅本机可达）
cd research/optuna_toy
.venv/bin/optuna-dashboard sqlite:///quantlab_phase1.db --port 8088   # 默认 127.0.0.1

# 运维机上开隧道，然后浏览器开 http://localhost:8088/
ssh -L 8088:127.0.0.1:8088 <user>@<mainnet-server>
```

**前端深链（G3）**：`web/src/App.tsx` 的 `Analysis ↗` 目标已外置为构建期变量 `VITE_OPTUNA_URL`（不再硬编码 IP）。mainnet 走隧道 → 默认 `http://localhost:8088/` 即可（不设也是这个 fallback）。构建前按 `web/.env.example` 设置：

```bash
# mainnet（隧道，默认值，可省略）
echo 'VITE_OPTUNA_URL=http://localhost:8088/' > web/.env
cd web && npm run build   # 产物经 go:embed 进 saas 二进制
```

**环境依赖（一次性）**：`python -m venv research/optuna_toy/.venv && research/optuna_toy/.venv/bin/pip install -r research/optuna_toy/requirements.txt`（pin 版本见该文件）。

**数据刷新**：导出是 on-demand `python quantlab_to_optuna.py --mode traces`（wipe-rebuild）。开机自启（systemd）+ 定时重导（cron）是后续 P1 项（G1/G2，见 `docs/pre-live-trading-gaps.md`）；当前为手工拉起。

---

## 故障排查

| 现象 | 原因 / 处理 |
|---|---|
| SaaS 启动报 `jwt.secret must be at least 32 bytes` | `config.yaml` 里 `jwt.secret` 太短；`openssl rand -hex 32` 重新生成 |
| SaaS 启动报 `app_role=saas cannot use automigrate` | `migration_mode` 误填为 `automigrate`；改为空或删除该行 |
| Goose 迁移失败 | 查 `goose_db_version` 表确认版本；手动 `goose -dir migrations postgres $DSN status` |
| agent `auth_fail: environment_mismatch` | `config.agent.yaml` 的 `base_url` 是 testnet 地址，或 `config.yaml` 的 `live.expected_environment` 填错 |
| agent `auth_fail: invalid_token` | `saas_token` 填错或 `account_id` 不一致 |
| 实例 Tick 后不下单，日志 `scheduler_tick_skipped_stale_data` | datafeeder cron 没跑或跑失败；检查 `/var/log/datafeeder.log`；手动跑 `datafeeder stats` 确认最新 bar 时间 |
| delta_report 对账超阈值，自动 kill | 查 `delta_report_reconcile_summary` 日志里 `max_managed_drift_bps`；正常 in-flight fill 引起的瞬时漂移可调高 `freeze_tolerance_bps` |
| `/instances/$INST/start` 返回 422 | 可能无 deployed champion；先 deploy-champion |

---

## 附：与 testnet 手册的对照差异

| 差异点 | testnet (`ws-phase2-runbook.md`) | mainnet（本文档） |
|---|---|---|
| `app_role` | `dev` | `saas` |
| Schema | AutoMigrate | Goose 自动运行 |
| `jwt.secret` | 任意字符串 | ≥32 字节真随机 |
| `live.expected_environment` | 空（断言禁用） | `mainnet`（硬断言） |
| `exchange.base_url` | `https://testnet.binance.vision` | `https://api.binance.com` |
| datafeeder | 手动按需 | cron 自动（步骤 G） |
| 资金 | 测试代币 | 真实资金 |
| environment mismatch 处理 | warn-only，继续运行 | `auth_fail`，拒绝连接 |
