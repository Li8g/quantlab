# 决策备忘录 — Analysis 页面（:8088 optuna-dashboard）生产级完整化

Status: **OPEN — 待一起研究拍板**（本文仅列缺口 + 候选方案 + 倾向，未定稿）
Date: 2026-06-09
Owner: 待定
Related:
- `web/src/App.tsx:19` — `Analysis ↗` 外链（`OPTUNA_URL`）
- `research/optuna_toy/quantlab_to_optuna.py` — Postgres → Optuna sqlite 导出桥
- `research/optuna_toy/README.md` — Phase 1 前端映射约定
- `docs/mainnet-runbook.md` — 生产部署手册（目前**未**覆盖 8088）
- 配置缺口 C6（Optuna URL 硬编码）见上次 distance 报告

---

## 0. 背景与现状（baseline 已跑通）

QuantLab 原生前端（React SPA）**刻意不重建分析场景**（`App.tsx` 注释 intent (a)）：
`Analysis ↗` 是一个指向外部 **optuna-dashboard**（`http://<IP>:8088/`）的深链。
分析视图（History / Hyperparameter Importance / Parallel Coordinate / Slice /
Contour / Trial Detail）全部复用 optuna-dashboard，不在 SPA 内实现。

数据流：

```
QuantLab Postgres ──(quantlab_to_optuna.py, on-demand wipe-rebuild)──> quantlab_phase1.db (sqlite)
                                                                              │
                                                       optuna-dashboard ──────┘  (:8088)
```

**2026-06-09 已完成并验证**（页面当前是活的）：

1. 从 live Postgres 重新导出（旧 db 是 5/27 的 `TESTBTC` 残留快照）：
   `quantlab_to_optuna.py` → `quantlab_phase1.db`，3 studies / 38 trials，
   `sigmoid_v1__BTCUSDT__1h__winners` best=1.70（对上当前 champion）。
2. 启动 dashboard：`optuna-dashboard sqlite:///quantlab_phase1.db --host 0.0.0.0 --port 8088`。
3. 验证：`:8088` 监听中；`/` →302→ `/dashboard` →200；`/api/studies` 正常。

环境事实（写方案时的约束）：

- venv：`research/optuna_toy/.venv`，optuna **4.8.0** / optuna-dashboard **0.20.0** / Python 3.13。
- 导出脚本：默认读 `/home/l9g/quantlab/config.yaml` 的 DSN，默认 `--mode winners`、
  `--output quantlab_phase1.db`，**每次运行 wipe-rebuild 整个 sqlite 文件**，
  study 名带后缀 `__winners` / `__traces`。
- 数据量：`gene_records` 38 行（BTCUSDT 6 / TESTBTC 32）；`evaluation_traces`
  **15256 行 / 24 tasks**。
- gitignore：`research/optuna_toy/.gitignore` 忽略 `.venv/`、`*.db`、`__pycache__/`；
  **无 `requirements.txt`**。

> baseline 定义：「用户能从前端点进 :8088 看到真实 BTCUSDT 的 champion 历史 + 最优 trial」
> —— 已达到。以下 5 项是从 baseline 到「生产级完整」的差距。

---

## 缺口 #1 — 图表数据太稀疏（winners 模式样本不足）

**问题**：默认 winners 模式下，每个 task 只产 1 个 trial（final winner）。
当前 `BTCUSDT__1h__winners` 仅 5 trials、`__1m` 仅 1 trial。Optuna 的
Hyperparameter Importance（fANOVA）/ Parallel Coordinate / Contour / Slice
这些图**需要密集样本**才有统计意义；5 个点画出来的"重要性排名"是噪声。

**影响**：分析页能打开，但最有价值的几个图基本不可读 —— 等于只剩 History 一张表。

**候选方案**：

| 方案 | 做法 | 权衡 |
|---|---|---|
| **A（倾向）** | 切 `--mode traces`：把 GA loop 内每个 `(gen, individual)` 都导成 trial（库里现成 15256 行）| 图立刻有料；但 traces 与 winners **不能共存于同一文件**（wipe-rebuild），需取舍或分文件 |
| B | winners + traces **各导出到不同 `.db`**，dashboard 各开一个端口（如 8088 winners / 8089 traces）| 两个视角都在；但多一个进程/端口，前端深链要再加一个入口 |
| C | 单文件同时塞两种 study：改脚本去掉"wipe 整库"、改成"按 study 名 upsert" | 一个 dashboard 看全；但要改导出脚本的同步语义（当前 wipe-rebuild 是"无双源漂移"的简化前提，破坏它要谨慎） |

**倾向**：A（traces 直接覆盖 phase1.db）。winners 的"promote-grade 一览"价值已被原生
SPA 的 Champions/Challenger Review 页覆盖，optuna 这边主打"参数空间探索"，traces 更对口。

**待决**：winners 视角是否还需要单独保留？若需要 → 走 B。

---

## 缺口 #2 — 不持久（手工进程，重启即丢，runbook 未覆盖）

**问题**：dashboard 和导出当前都是手工 `nohup` 起的临时进程，机器重启就没了。
`docs/mainnet-runbook.md` 通篇不提 8088 / optuna，生产上线流程里这个页面是"隐形"的。

**影响**：生产环境重启后 Analysis 链接 502；新运维照 runbook 走不会知道要拉起它。

**候选方案**：

| 方案 | 做法 | 权衡 |
|---|---|---|
| **A（倾向）** | 加 systemd unit（`quantlab-optuna-dashboard.service`），开机自启 + 崩溃重拉；runbook 补一节 | 标准做法，持久可靠；需写 unit + 文档 |
| B | 仅在 runbook 增加"步骤 X：手工启动 dashboard"，不做 systemd | 改动最小；但仍是手工、易漏、不自愈 |
| C | 用 docker-compose 把 dashboard 跟 saas 一起编排 | 一处编排；但本项目目前 saas 是裸二进制部署（runbook 无 docker），引入 compose 是新依赖 |

**倾向**：A。同时 runbook 补一节"分析页（可选组件）"，含 systemd unit + 导出 cron（见 #3）。

**待决**：分析页算"核心组件"还是"可选诊断组件"？决定它在 runbook 里的位置和 SLA。

---

## 缺口 #3 — 只能按需快照（新任务跑完不自动刷新）

**问题**：导出是 on-demand wipe-rebuild（设计如此，README 明示"无双源/无漂移"）。
新 GA 任务跑完后，sqlite 里还是旧快照，页面"过时"直到有人手工重导。

**影响**：用户点进去看到的可能不是最新一轮进化的结果，且无任何"数据截至时间"提示。

**候选方案**：

| 方案 | 做法 | 权衡 |
|---|---|---|
| **A（倾向）** | cron 定时重导（如每 15 min `quantlab_to_optuna.py --mode traces`）| 简单、与现有 datafeeder cron 同范式；最多 15 min 延迟；wipe 期间有极短不可用窗口 |
| B | 在 epoch 任务完成 hook 里触发导出（事件驱动）| 近实时、零空转；但要在 Go 侧加 shell-out 到 Python 的耦合，跨语言边界 |
| C | 不动同步，只在页面/runbook 标注"快照式，手工 `python quantlab_to_optuna.py` 刷新" | 零工程；但把"记得刷新"的负担留给人 |

**倾向**：A（cron）。事件驱动（B）收益不抵跨语言耦合成本，原型期不值得。
附带：导出脚本可在 study `user_attrs` 写入 `exported_at`，dashboard 能看到数据时效。

**待决**：可接受的最大数据延迟是多少（决定 cron 周期）？wipe 窗口的瞬时不可用是否需要消除（→ 导出到临时文件再原子 `mv` 覆盖）？

---

## 缺口 #4 — 前端 Optuna URL 硬编码（即配置缺口 C6）

**问题**：`web/src/App.tsx:19` 写死 `const OPTUNA_URL = 'http://192.168.67.129:8088/'`，
无环境变量。换 IP / 换机器 / 走域名，Analysis 链接即失效。

**影响**：任何非当前 VM 的部署，分析深链都是坏的。

**候选方案**：

| 方案 | 做法 | 权衡 |
|---|---|---|
| **A（倾向）** | 改 `import.meta.env.VITE_OPTUNA_URL`，构建时注入（`.env` / CI）| Vite 标准做法；需在 runbook 的 `npm run build` 步骤前设好该变量 |
| B | 走相对路径，让 saas 反代 `/analysis` → :8088（同源）| 前端零配置、无跨域；但要在 Go 侧加反向代理（新代码），且 optuna-dashboard 的 `/dashboard` 子路径要处理 base path |
| C | 后端 `/api/v1/config` 下发 optuna_url，前端运行时读取 | 部署期零重建；多一个 config 字段 + 端点 |

**倾向**：A（最小改动，与 B5 go:embed 后的"构建期注入"范式一致）。
若未来想彻底同源、省掉 8088 暴露 → 再考虑 B。

**待决**：分析页是否要藏到 saas 同源之后（安全/暴露面）？若要 → B 值得，否则 A 够。

---

## 缺口 #5 — 部署不可复现（venv/db gitignored，无 requirements.txt）

**问题**：`.venv/` 和 `*.db` 都被 gitignore，且仓库里**没有 `requirements.txt`**。
全新机器 clone 下来，optuna 环境根本不存在，#1~#4 的命令全跑不起来。

脚本实际依赖：`optuna`、`optuna-dashboard`、`psycopg`（v3）、`pyyaml`。

**影响**：换机器 / 重装即"从零考古"venv 该装什么版本，复现成本高、易踩版本不一致。

**候选方案**：

| 方案 | 做法 | 权衡 |
|---|---|---|
| **A（倾向）** | 加 `research/optuna_toy/requirements.txt`（pin 版本：optuna==4.8.0、optuna-dashboard==0.20.0、psycopg[binary]、pyyaml）+ runbook 写 `python -m venv .venv && .venv/bin/pip install -r requirements.txt` | 标准、可复现；需维护版本 pin |
| B | `pyproject.toml` + 锁文件（uv/pip-tools）| 更严格的锁；但给一个 toy/research 目录上工具链，偏重 |
| C | 把 dashboard 跑成 docker 镜像（pin 在 Dockerfile）| 环境完全封装；但同 #2-C，引入 docker 新依赖 |

**倾向**：A。research/ 是离线分析目录，`requirements.txt` 足够，不必上 uv/docker。

**待决**：是否顺手把 `research/` 整体的 Python 依赖收敛到一个 requirements（目前只有 optuna_toy 一个子目录）？

---

## 优先级建议（待一起调整）

| 优先级 | 缺口 | 理由 |
|---|---|---|
| P0 | **#1 traces 重导** | 立刻让分析图"有意义"，纯命令、零代码、收益最大 |
| P0 | **#5 requirements.txt** | 让它能在别的机器拉起来，是其他所有项的前提 |
| P1 | **#2 systemd + runbook** | 生产持久化；与 #3 的 cron 一起落到 runbook 一节 |
| P1 | **#3 cron 自动刷新** | 解决"过时"，与 #2 同批 |
| P2 | **#4 URL 外置** | 单机自用暂不咬人；多机/上域名前必做 |

> 一种省事的打包：**#5 → #1 → (#2+#3 同一节 runbook) → #4**，
> 前两步今天就能做完（命令级），后三步是 runbook + 少量前端/脚本改动。

---

## 待一起拍板的问题清单

1. winners 视角还要不要单独保留？（决定 #1 走 A 还是 B）
2. 分析页定位：**核心组件**还是**可选诊断组件**？（决定 #2 在 runbook 的位置 + 是否上 systemd）
3. 可接受的数据延迟上限？（决定 #3 cron 周期；以及要不要消除 wipe 窗口）
4. 分析页要不要藏到 saas 同源反代之后（安全/暴露面）？（决定 #4 走 A 还是 B）
5. Python 依赖收敛范围：只 optuna_toy 还是整个 research/？（#5 的边界）
