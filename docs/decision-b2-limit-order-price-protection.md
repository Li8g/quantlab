# 决策讨论 — B2：limit order 价格保护

Status: **已拍板（2026-06-10，D1–D6 全部落定，见 §7 拍板记录）** — 决策文档，非规格。实现落地时把 §4.5 cap 不变量提炼进 CLAUDE.md，本文归档。
Date: 2026-06-10（同日讨论 + 拍板）
Owner: 待定
Related:
- `docs/backlog-6-price-source-divergence.md` §4 — flash 防护指向 limit order 的原始结论
- `docs/pre-live-trading-gaps.md` B2 — 缺口清单条目（**注：原描述"limit-order 路径尚未实现"已被本文证伪**）
- `internal/strategy/contract.go` `OrderIntent` / `internal/wire/tradecommand.go` / `internal/agent/binance/order.go` `SubmitLimit`
- `internal/strategies/sigmoid_v1/simulator.go` — 回测 fill 模型（close ± slippage）
- `docs/decision-ga-reproducibility-constraint.md` — ε=1e-4、fitness_version 事件判据

---

## 0. 一句话

执行管线（下 LIMIT GTC + tick/LOT snap）**已端到端建好**；B2 真正的问题是**让限价单可用于价格保护，且不破坏"回测 ScoreTotal 预测实盘"这一 promote 前提**。难点不在 agent，在策略层与回测的自洽。

---

## 1. 背景：B2 到底是什么（与缺口清单的描述不符）

缺口清单写「无 limit order 价格保护路径……limit-order 路径尚未实现」。**勘探后证伪**——机械执行路径全通且正确：

| 层 | 现状 | 证据 |
|---|---|---|
| 策略意图 | `OrderIntent{OrderType, LimitPrice}` 已定义 | `contract.go:51` |
| SaaS dispatcher | limit → 渲染 `LimitPriceDecimal` | `buildTradeCommand`（wshub） |
| wire | `TradeCommand.LimitPriceDecimal`（omitempty） | `wire/tradecommand.go` |
| agent 解码 | 解 `LimitPriceDecimal` → `OrderRequest{OrderType,LimitPrice}` | `agent/tradecommand.go:82` |
| agent 下单 | `SubmitLimit`：LIMIT **GTC** + price snap PRICE_FILTER + qty snap LOT_SIZE + 正价校验 | `binance/order.go:208` |

**真正没有的三件事**：

- **A. 没有策略发 limit**：sigmoid_v1（唯一真策略）只发 `OrderTypeMarket`（`step.go:222/254`）。
- **B. 回测不建模 limit 成交**：fill 模型是 market-only（见 §2）。
- **C. 无未成交 limit 的生命周期**：无 `CancelOrder`、无 cancel-and-replace、无挂单锁仓对账处理。

---

## 2. 关键事实：回测今天怎么成交（整件事的支点）

`sigmoid_v1/simulator.go` + `quant/friction.go`：

> 每个 market 单**按该 bar 的收盘价 `Close` 成交**，再施加 `slippage_bps`（成交价变差）+ `taker_fee_bps`。**只用 `Close`，完全不看 bar 内 `Low`/`High`。** DCA ghost baseline 同样按 `Close` 成交。

记号：买单回测成交价 = `Close × (1 + slippage_bps/1e4)`，外加 fee。

**这个"只看 close"的事实是后面所有可复现性论证的支点**（见 §3、§5-D3）。

---

## 3. 为什么 limit 是"可复现性问题"而非"执行问题"

QuantLab 的 promote 前提（`decision-ga-reproducibility-constraint.md`）：**champion 的回测 ScoreTotal 预测它的实盘行为**。这要求"回测里怎么成交" ≈ "实盘怎么成交"。

- 现在两边都是 **market**：回测按 close+slippage，实盘按盘口即时成交。两者在正常行情接近（slippage_bps 就是为这个分歧留的摩擦预算）。
- 若实盘改发 **limit** 而回测仍按 market 建模 → 成交动态分叉：limit 只在价格触及才成交、可能不成交、可能部分成交。**回测高估/错估了成交** → champion 的回测分数不再预测实盘 → promote 闸门失真。

所以"加 limit 价格保护"不是改个 order_type 字段就完事——**要么回测同步建模 limit 成交（可能触发 `fitness_version` bump），要么把 limit 设计成"回测中性"（对 ScoreTotal 零影响）**。后者是本文倾向的关键（§5-D1/D3）。

---

## 4. 三个真缺口（决策围绕它们）

- **A（核心，reproducibility-critical）**：策略发 limit + 回测 fill 模型如何对齐。
- **B（生命周期）**：GTC 未成交单怎么办。`ValidUntilMs` 只是 SaaS 指令有效期（提交前去重/过期），**不是交易所过期**——GTC 单会活过它。需要 cancel/replace 策略 + agent 端 `CancelOrder`（现无，且与 kill 的 `CancelAllOpenOrders` 拆出项相关）。
- **C（账本/对账）**：未成交 limit 锁住交易所余额（delta_report 的 `locked`）。reconcile 要不把 locked-by-open-limit 误判 drift（→ 可能误触 auto-freeze）；回测若涉及可用资金也要建模。

---

## 4.5 术语定义 — cap（`price_cap_bps`）`[INVENTED v1 命名]`

为保证后续讨论一致，冻结以下定义。原标 **[待 D-x]** 的属性已随 2026-06-10 拍板（§7）落定，全文按拍板结果收口。

**一句话**：cap 是**单笔订单可接受的最差成交价相对参照价的不利偏离上限**（bps 计）。它是执行层风控护栏：限定"成交价最差到哪"，**不承诺"是否成交"**。

**形式定义**：
- 参照价 `P_ref` ≜ dispatcher 为该订单定价时所用的 kline 收盘价 `latestClose`。注意这与 USD→数量换算用的是**同一个** close（`quantity_decimal = QuantityUSD / latestClose`），数量与价格护栏锚定同一时刻、同一数据源——cap 永远相对"策略决策时看到的价"，不相对盘口。
- 买单：`limit_price = P_ref × (1 + cap/1e4)`（可接受的最高买价）
- 卖单：`limit_price = P_ref × (1 − cap/1e4)`（可接受的最低卖价）
- 交易所保证全部（含部分）成交价不差于 `limit_price` ⇒ **任何成交相对 `P_ref` 的不利偏离 ≤ cap**。

**单位与取值**：bps（1/10⁴）。起步值 `[INVENTED v1]` 50bps。

**不变量**：
1. **`cap ≥ slippage_bps`**（被部署 champion 的 effective 值）。理由：回测假设正常成交价 = `close×(1±slip)`；若 cap < slip，回测自认的"正常成交"会被实盘护栏拒掉——回测与实盘自相矛盾。校验点（D2 已拍板）：deploy-champion 时校验，不满足拒绝部署。
2. **tick-snap 边界**：cap 的硬保证以 snap 到 PRICE_FILTER 网格后的 `limit_price` 为准。当前 `binance.compliantPrice` 为 **nearest 取整** → 名义 cap 最多被放松 **½ tickSize**（BTCUSDT tick=0.01、价位 ~10⁵ 下 ≈10⁻⁵ bps，可忽略）。若未来接入 tick 粗的品种，实现应改向性取整（买 floor / 卖 ceil，snap 只收紧不放松）。
3. **cap 只约束价格，不约束成交概率**：flash 把价推过 cap ⇒ 不成交（或部分成交）——这是护栏的**预期行为**，不是故障。未成交的恢复语义（D4 已拍板 IOC）：交易所即时撤余量，下一 Tick 按新 close 重新决策。

**cap 不是什么**（边界澄清，防口径漂移）：
- **不是 `slippage_bps`**：slippage_bps 是回测的摩擦**假设**（每笔都付、影响 ScoreTotal）；cap 是实盘的**拒绝边界**（正常行情不起作用、不进回测）。两者数值独立，仅受不变量 1 约束。
- **不是 maker 挂价**：cap 不试图拿更优价。marketable limit 正常情况即时按盘口成交，仍是 **taker**，手续费档不变（D6）。
- **不进 ScoreTotal**（D3 已拍板 a）：回测不消费 cap，limit 在回测里与 market 数值恒等。
- **不是交易所有效期**：与 `ValidUntilMs`（SaaS 指令防过期/去重，提交前检查）正交。（D4 已拍板 IOC）订单本身无存活期问题。

**作用域与生效点**（D2 已拍板 config）：SaaS dispatcher 的 OrderIntent→TradeCommand 转换处——**策略代码不可见 cap**。基因路线已否决（§7），若未来重启即"执行层进化"独立项目（连锁触发 D3-b + 双版本 bump）。

**观测校正**：agent 的 `ActualSlippageBPS` 对 limit 单以 limit 价为参照（`tradecommand.go:144`）⇒ marketable limit 下读数相对 `P_ref` 系统性偏小一个常数 cap。分析时按 `真实滑点 ≈ ActualSlippageBPS + cap` 校正（cap 为已知配置，可精确反推）。v1 接受此口径并在此注明，不为此加 wire 字段。

---

## 5. 待拍板的决策（含选项展开 + 倾向）

### D1 — 产品意图：limit 到底用来干嘛？

| 选项 | 含义 | 利 | 弊 |
|---|---|---|---|
| **D1-a 倾向 — marketable limit（滑点天花板）** | limit 价 = 当前 close ± cap（买 `close×(1+cap)`）。正常行情瞬间成交（≈market）；只有 flash 把价推过 cap 才不成交 | 直接交付 backlog-6 §4 的 flash 防护；**对回测扰动最小**（见 D3，可做到零）；最贴近现有 market 行为 | 仍是 taker（吃单），不赚 spread；flash 时变成"不成交"而非"更好价" |
| D1-b — 被动 maker 挂单 | 挂在盘口内侧赚 spread / 等更好价 | 可能拿到更优价、负手续费（maker rebate） | **blast radius 巨大**：成交动态彻底变（可能长期不成交=策略不交易）；回测必须重写为限价撮合；策略要管挂单/撤单/重挂的全套逻辑 |
| D1-c — 维持现状（market）+ 不做价格保护 | 不动 | 零工作 | flash 时按盘口任意差价成交（③ 账本吸收、reconcile 兜底，但无事前保护）；B2 永远 open |

> **倾向 D1-a**：把 limit 当"滑点上限"，是价格保护的最小、最可复现实现，正是 backlog-6 的本意。D1-b 是另一个产品（做市），不在"价格保护"范畴，blast radius 不成比例。
>
> ✅ **DECIDED（2026-06-10，用户确认）：D1 = a（marketable limit）**。被动 maker 留作未来独立方向，不在 B2 范围。

### D2 — cap 放哪、取多少

| 维度 | 选项 | 倾向 |
|---|---|---|
| 存放 | (i) 策略基因（参与进化优化）/ (ii) `GAConfigSnapshot` 配置项（全局，类似 slippage_bps）/ (iii) 实例级 config | **(ii) [INVENTED v1]**：cap 是风控护栏不是 alpha 来源，不该进化；放配置、随 GAConfigSnapshot 快照（保证回测/实盘同值、可审计） |
| 取值 | 30 / 50 / 100 bps | **[INVENTED v1] cap = max(slippage_bps, 50bps)** 起步；约束 **cap ≥ 回测 slippage_bps**（见 D3 为何关键） |
| 关系 | cap 与 slippage_bps 的关系 | cap 是"绝不接受比 close×(1±cap) 更差的成交"；slippage_bps 是"回测假设的正常成交损耗"。cap 应 ≥ slippage_bps，否则回测的正常成交价本身就被 cap 拒掉（自相矛盾） |

> **讨论更新（2026-06-10，倾向修正 (ii)→live config）**：D3-a 下 cap 对回测数值恒等 → 回测**不消费** cap → 放 `GAConfigSnapshot` 的"回测/实盘同值"理由是空话，且实盘若从快照读 cap，改护栏要重跑一轮进化才生效（荒谬）；快照只抄一份则随时与实盘脱节，审计价值归零。另外加字段碰 `resultpkg` 的 v5.3.3 冻结 schema。**修正后倾向：`config.yaml` 加 `orders.price_cap_bps`（per-deployment，与 `freeze_tolerance_bps` 同类），不变量 1 在 deploy-champion 时校验 + 把当时 cap 写进 `instance.deploy_champion` 审计行 DataJSON。**
>
> **新发现的 D2↔D3 强耦合**：cap 进**策略基因** ⇒ 被迫 D3-b——D3-a 下 cap 对 ScoreTotal 零影响，GA 对该基因维度**无选择压力**（纯噪声 DNA），要进化它回测必须感知它（intrabar）→ bump `fitness_version`；加基因维度还改 Fingerprint → bump `fingerprint_version` + 老 champion `DecodeElite` 兼容。基因路线实质是"执行层进化"独立大项目，不是 B2 的解。cap 进 **config** ⇒ 自然配 D3-a。**D2 的真问题：cap 是策略的一部分（进化+进回测），还是执行层护栏（回测不可见）。**
>
> ✅ **DECIDED（2026-06-10，用户拍板）：D2 = live config（`orders.price_cap_bps`）**。用户判定基因路线"没有太大意义，且增加太多意义不大的复杂度"——与上述零选择压力 + 双版本事件级联 + 护栏被优化即虚设三条论据一致。cap 定位为**人持有的执行层护栏**，与 `freeze_tolerance_bps`/`max_bar_staleness` 同族。起步值保持 `[INVENTED v1]` 50bps（部署期可调，非本拍板内容）；test_mode 问题随之消解（cap 纯实盘，test_mode 是回测概念）。

### D3 — 回测 fill 规则（决定要不要 bump `fitness_version`）

这是最关键的技术决策，**§2 的"只看 close"事实让它有一个近乎免费的解**：

| 选项 | 做法 | ScoreTotal 影响 | fitness_version |
|---|---|---|---|
| **D3-a 倾向 — 保持 close-only，cap ≥ slippage_bps ⇒ 回测中性** | 回测仍按 `Close+slippage` 成交。因为 cap ≥ slippage_bps，回测的成交价 `close×(1+slip)` 永远落在 cap 内 → **limit 在回测里和 market 成交完全一致** → ScoreTotal 零变化 | **零**（数值恒等） | **不 bump**。protection 纯活在实盘路径，只挡比 close×(1±cap) 更差的成交——而回测本来就没产生过那种成交 |
| D3-b — 回测建模 intrabar 不成交 | 用 bar 的 `Low`/`High` 判定"价格是否触及 cap"，未触及则不成交/部分成交 | 改变成交序列 → **材料性改 ScoreTotal** | **要 bump fitness_version**（按 CLAUDE.md ε 判据），且要重测所有 champion，blast radius 大 |

> **倾向 D3-a**：这是整个方案优雅的核心——**只要 cap ≥ slippage_bps 且回测保持 close-only，marketable limit 对回测是数值恒等的 no-op，无 fitness_version 事件**。flash 防护是纯实盘行为，挡的是回测从未建模过的尾部滑点。代价：回测略微"乐观"（假设总成交），但这与今天的 market 回测假设完全一致，没有变差。
>
> ✅ **DECIDED（2026-06-10）：D3 = a（close-only 恒等，不 bump）**。由 D2=config 连锁落定——cap 不进基因后，没有任何东西需要回测感知 cap，intrabar 建模失去唯一动机。不 bump `fitness_version`，champion 全部免重测。

### D4 — 未成交 GTC limit 的生命周期

前提：选 D1-a + D3-a 后，"未成交"只在实盘 flash 越过 cap 时罕见发生。

| 选项 | 做法 | 利 | 弊 |
|---|---|---|---|
| **D4-a 倾向 — 下一 Tick cancel-and-replace** | 每 Tick 开头：若上一单未成交（仍 open），先 `CancelOrder` 再按新 close 重下 | 有界、自愈、limit 价始终跟着最新 close；语义干净 | 需在 agent 加 `CancelOrder`（现无）；cancel→place 之间有微小窗口 |
| D4-b — 留 GTC 挂着不管 | 不撤，靠它自己将来成交 | 零新代码 | 锁仓累积、陈旧价挂单、与"每 Tick 重评估"语义冲突；C 缺口恶化 |
| D4-c — 改 IOC/FOK（即时成交否则取消） | timeInForce=IOC | 无挂单残留、无锁仓 | flash 时直接不成交且不留痕，等于"这一 Tick 跳过交易"；且偏离 GTC 现状 |

> ~~**倾向 D4-a**：与"agent 自动交易、定时重评估"的整体节奏一致。附带产出 agent `CancelOrder`，正好补上 kill+flatten 拆出的 `CancelAllOpenOrders` 子缺口。~~
>
> **讨论更新（2026-06-10，倾向修正 a→c′ IOC）**：D4-a 对 D4-c 的指控（"flash 时不成交且不留痕=跳过交易"）站不住——cancel-and-replace 在 flash 情形的最终效果**完全一样**（挂一个 Tick 没成交→撤→按新 close 重下 = 这个 Tick 没交易、下个 Tick 重新决策），IOC 只是即时、原子地做掉同一件事。且 IOC 让 **缺口 B 与 C 同时消失**：交易所自动撤余量 → 无 resting order → 无需 wire cancel 消息/agent `CancelOrder`/SaaS Tick 扫挂单；无 open order → 无锁仓 → reconcile/D5 整条作废。"留痕"也不缺：ack + 终态回报存在，`cancelled` 终态链路已端到端就绪（`wire.OrderStatusCancelled`→`store.TradeStatusCancelled`），Binance IOC 余量撤销的 `EXPIRED` 状态最多一行映射。**修正后倾向：marketable limit + timeInForce=IOC**。成本=wire `TradeCommand` 加 additive `time_in_force` 字段（omitempty，缺省 GTC 向后兼容）+ agent `SubmitLimit` 支持 IOC。代价披露：①Tick 间隔内价格回落到旧 cap 内的"补成交"机会没了（但那是按陈旧价决策，本就不该要）；②原 D4-a 的副产品 `CancelOrder` 没了——kill+flatten 的撤单需求回归独立 backlog（它本来就是 Option 3 拆出项）。
>
> ✅ **DECIDED（2026-06-10）：D4 = IOC**。用户对修正后的 IOC 倾向无异议（D1/D2 确认时一并落定）。缺口 B（生命周期）与 C（锁仓对账）随之消失。

### D5 —（被 D4 牵出）对账/账本含挂单

> ✅ **DECIDED（2026-06-10）：D5 消解**。D4 拍板 IOC → 无 resting order、无锁仓，本节问题不存在。保留正文仅为记录"若将来引入 GTC 挂单（如被动 maker 方向），这些问题会回来"。

- 未成交 limit 锁交易所余额 → delta_report `locked` 非零。
- **必须**：reconcile 的 managed-asset 漂移计算要把 locked-by-open-limit 计入 expected（否则 auto-freeze 误触发——与 testnet faucet 误触发同类坑，见 `cmd/saas/agentmsg.go` 的 managed-asset scoping + reconcile 自动冻结只用 managed 资产的既有处理）。
- 选 D4-a 后挂单存活窗口很短（一个 Tick），但跨 Tick 边界仍可能被一次 delta_report 抓到 → 不能忽略。
- **待定**：是把 locked 计入 expected，还是 cancel 后再对账。倾向：reconcile 时 `expected 持仓 = dead+float+cold + 本账户所有 open 限价单锁定量`。

### D6 — maker/taker 与手续费

- marketable limit 跨价成交 = **taker**（与现状 market 同档手续费）→ `taker_fee_bps` 不变，回测摩擦不变。**无需改动**。
- 仅当走 D1-b（被动 maker）才涉及 maker 费率/rebate → 不在倾向范围。

---

## 6. 拍板后的实现路径（D1-a + D2 live config + D3-a + D4 IOC，2026-06-10 全部落定）

1. **转换点**：SaaS dispatcher（`buildTradeCommand` 或其上游）把 market 意图转成 marketable limit（`OrderType=limit`, `LimitPrice = close×(1±cap)`, `TimeInForce=IOC`）。**策略代码不动、回测 simulator 不动**（D3-a：close-only + cap≥slip ⇒ 恒等）。→ 加 `TestReplayWithinTolerance` 确认 ScoreTotal 不变（应 bit-identical）。
2. **config**：`config.yaml` 加 `orders.price_cap_bps`（per-deployment，缺省 50bps `[INVENTED v1]`；0 = 关闭护栏退回 market，向后兼容）。deploy-champion 时校验不变量 1（cap ≥ champion effective slippage_bps，违例拒部署）+ 把当时 cap 写进 `instance.deploy_champion` 审计行。
3. **wire**：`TradeCommand` 加 additive `time_in_force` 字段（omitempty，缺省 GTC——pre-B2 agent 兼容）。
4. **agent**：`SubmitLimit` 支持 `timeInForce=IOC`；Binance `EXPIRED`（IOC 余量撤销）→ `cancelled` 状态映射。
5. **文档**：§4.5 cap 定义 + "marketable-limit=回测中性"不变量提炼进 CLAUDE.md；本文归档；顺手改正 `pre-live-trading-gaps.md` B2 行的"路径尚未实现"陈旧描述。

**明确不做**：被动 maker 挂单（D1-b）、回测 intrabar 撮合（D3-b）、GTC+cancel-and-replace 与 agent `CancelOrder`（D4-a，kill+flatten 的撤单需求回归独立 backlog）、reconcile 计锁仓（D5，IOC 下无 open order 而消解）、stop/OCO/trailing（contract.go 注释里已 deferred）、"执行层进化"（cap 进基因 = 独立大项目，归 GA/RL/BO 演进 backlog）。

---

## 7. 拍板记录（2026-06-10，原"开放问题"全部关闭）

| # | 决策 | 结论 | 定夺方式 |
|---|---|---|---|
| D1 | 产品意图 | **a — marketable limit（滑点天花板）** | 用户确认；被动 maker 留作未来独立方向 |
| D2 | cap 归属 | **live config（`orders.price_cap_bps`）** | 用户拍板：基因路线"没有太大意义、复杂度不成比例"；与零选择压力/双版本级联/护栏被优化即虚设三论据一致 |
| D3 | 回测规则 | **a — close-only 恒等，不 bump `fitness_version`** | 由 D2 连锁落定（cap 不进基因 → 回测无需感知 cap） |
| D4 | 未成交处理 | **IOC**（additive `time_in_force` wire 字段） | 讨论中修正自 cancel-and-replace，用户无异议；缺口 B/C 消失 |
| D5 | 锁仓对账 | **消解**（IOC 下无 open order） | 随 D4 |
| D6 | 手续费 | **无需改动**（marketable limit 仍是 taker） | 事实性结论 |
| 范围 | B2 边界 | **到"flash 滑点天花板"为止**；做市/更优价、"执行层进化"（cap 进基因）均为独立未来项 | 随 D1/D2 |

仍标 `[INVENTED v1]` 待实现期调整的：cap 起步值 50bps（部署期可调，deploy 校验 `cap ≥ champion slippage_bps` 兜底）。

**下一步**：按 §6 路径出实现草稿（dispatcher 转换 + config + wire TIF 字段 + agent IOC + EXPIRED 映射 + `TestReplayWithinTolerance` 恒等验证）。
