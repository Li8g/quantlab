# mra_ab — MRA filter bank vs sigmoid_v1 特征集 A/B 原型（设计稿）

> 状态:**两阶段全部跑完。终局判决 = 判决表 row 4(介于其间):不立项 mra_v1、
> 不弃案,等 mainnet 实盘偏差数据后复议;证据偏 H0(§12)。复议前置的
> CPCV/PBO 加固件已落地并跑完(§13):三臂选拔均落噪声区间,第三角度印证。**
> 来源:2026-06-11 "用小波/傅里叶替代均线作 GA 维度"
> 评估的落地件。结论链(为什么是 EMA bank 而不是正交小波、三宗罪反泄漏背景)
> 见该次评估;执行排序挂在 LEARN-ga-rl-bayesian §12.4 框架下,与 item 4(CPCV/PBO)
> 共享反泄漏协议。
>
> 仓库教义:research/ 永不进 server path;本实验只读 `klines`,零 Go 改动;
> 结论只决定"是否立项 `mra_v1` 策略",不直接产出代码。

---

## 1. 预注册假设(先写判决标准,后跑实验)

**H1(表达力):** 把价格特征从"2 个进化周期采样的 3 特征"换成"固定 dyadic
EMA filter bank 的 6 band 权重",在**等额优化预算**下能取得更高的 OOS alpha
(对 DCA 基准的年化超额,镜像 `verification.RunOOS` 的 alpha 定义)。

**H0(零假设/证伪形态):** B 臂的最优解塌缩回嵌套配置(各细 band 权重近似相等
≈ 复现 A 臂的 priceDeviation),或 OOS alpha 无显著优势 → 额外容量只带来过拟合,
想法证伪,不立项 mra_v1。

**判决规则 [INVENTED v1]:**

| 结果 | 判定 |
|---|---|
| B 的 OOS alpha 中位数(跨 fold) − A 的 ≥ +1%/yr,且 DSR(B) ≥ DSR(A) 于 ≥3/5 fold | **H1 成立** → 立项 mra_v1 |
| ridge 闭式基线(§6)与 Optuna 都找不到正 OOS alpha 的谱形 | **强证伪** → 弃案 |
| B 最优 w 与嵌套配置余弦相似度 > 0.95(跨 fold 中位) | **容量无用** → 弃案 |
| 介于其间 | 收集为证据,不立项不弃案,等 mainnet 实盘偏差数据后复议 |

判决规则在跑实验前冻结于此;事后只许解读、不许改阈值。

## 2. 双臂定义

两臂共享除"价格特征块"外的一切;差异被隔离到单一变量。

**共享部分(两臂相同):**
- 信号 → 仓位:`targetWeight = 1/(1+exp(β·signal + γ·invBias))`,β γ **冻结**
  为现役 champion 7550b6 的基因值 [INVENTED v1: 取冻结而非搜索,消去交互项];
- 波动率项:`A3·(volRatio−1)`,MAV short/long 周期**两臂都搜索**(同维同界,
  mirror chromosome.go §4.1 bounds 5-50 / 30-250);
- 模拟器、窗口、费用、预算、种子协议全部相同(§4/§5)。

**Arm A(对照 = sigmoid_v1 价格特征):**
```
signal_price = A1·(close − EMA_L)/EMA_L + A2·ln(close/close[−m])
搜索维度: A1, A2 ∈ [−1,1]; L ∈ [50,300]; (m 复用 MAVShort, 同 Go 实现)
→ 价格块 3 维 + 共享块 3 维(A3, MAVShort, MAVLong) = 6 维
```

**Arm B(处理 = MRA filter bank):**
```
spans S = [6, 12, 24, 48, 96, 192]   (1h bar; 192h ≈ 8 天) [INVENTED v1]
d_0 = (close − EMA_6)/close
d_k = (EMA_{S[k−1]} − EMA_{S[k]})/close,  k = 1..5
signal_price = Σ_{k=0..5} w_k · d_k
搜索维度: w_0..w_5 ∈ [−1,1] (无周期维 — span 固定)
→ 价格块 6 维 + 共享块 3 维 = 9 维
```

**Arm C(嵌套对照,廉价加跑):** B 的结构但 6 个 w 绑成 1 个标量 w_all
(数学上 ≈ A 的 priceDeviation 项,L≈192)。用途:把 B−A 的差分解为
"表示形式之差"(C−A,应≈0)与"容量之差"(B−C,真正的效应量)。

**预算不对称声明:** B 比 A 多 3 维,等额 trial 预算下高维方天然吃亏——该偏置
**对 B 不利**,因此 B 胜出时结论更可信(保守设计,不做维度补偿)。

## 3. 数据与窗口

- 来源:`klines` 表,BTCUSDT 1h,2017-08-17 → 2026-05-01(76,165 bars),只读;
- bars 载入后做与 Go 同语义的 gap 检查(`kline_gaps`),gap bar 不造假交易
  (mirror TestGapHandlingNoFakeTrades 语义:gap 处冻结仓位、不产生信号);
- warmup:每个评估段前置 max(span)+1 = 385 bars 不计分,镜像 MinEvalBars 思想。

**Walk-forward folds(anchored,镜像 OOS Anchored Holdout 哲学)[INVENTED v1]:**

| fold | IS(优化) | OOS(评分) |
|---|---|---|
| 1 | 2017-08 → 2021-05 | 2021-06 → 2022-05 |
| 2 | 2017-08 → 2022-05 | 2022-06 → 2023-05 |
| 3 | 2017-08 → 2023-05 | 2023-06 → 2024-05 |
| 4 | 2017-08 → 2024-05 | 2024-06 → 2025-05 |
| 5 | 2017-08 → 2025-05 | 2025-06 → 2026-04 |

- IS/OOS 之间设 **purge+embargo = 385 bars**(≥ 最大滤波器记忆),防止 IS 末端
  指标状态把 OOS 开头的信息倒灌进优化目标;
- 5 个 OOS 段覆盖牛/熊/震荡(2022 熊、2024 牛),单 regime 运气被摊薄。

## 4. 简化模拟器规格(两臂共享)

**刻意不复刻 simulator.go 全语义** — A/B 的效度来自两臂共享同一下游,
绝对保真度不是目标(那是立项后 Go 侧四窗 crucible 的事)。保留/舍弃清单:

| 保留(影响两臂排序) | 舍弃(对两臂等价的复杂度) |
|---|---|
| close-only 成交 + taker_fee_bps + slippage_bps(取 config 现值) | DeadBTC/FloatBTC 双池、macro 月度注入 |
| wedge no-trade band(消高频抖动,路径依赖的主源) | MicroReservePct 现金约束 |
| DCA 双基准 + 超额年化 alpha(镜像 RunOOS 定义) | 月度再平衡日历细节 |
| MDD 与 Fatal 阈值(只记不截断 — 单窗无 cascade) | SharpeBank/consistency penalty |

实现注记:wedge band 造成路径依赖 → 无法全向量化;预归 **numba** `@njit`
per-bar 循环(76k bars × ~500 trials × 5 folds × 3 arms ≈ 6×10⁸ bar-step,
numba 下分钟级;纯 Python 不可行)。指标(EMA/MAV)用 numpy 向量化预计算——
B 臂 span 固定,d_k 矩阵**每 fold 只算一次**,全 trial 共享(这本身就是
filter bank 的 CPU 红利的预演)。

## 5. 优化器与预算

- **Optuna TPE**(复用 `optuna_toy` venv + PG storage + :8088 dashboard 约定);
- study 命名 `mra_ab_{arm}_{fold}`,storage 进现有 PG,dashboard 直接可视化;
- 预算 [INVENTED v1]:**每 arm × fold 500 trials**,TPE seed = hash(arm, fold)
  固定,numpy seed 同源 — 全程确定性可复跑;
- 优化目标 = IS 段 alpha − λ·max(0, MDD − FatalMDD)(软惩罚替代 cascade,
  λ [INVENTED v1] = 10,只为把 Fatal 区逐出最优解,不参与 OOS 评分);
- **NTrials 纪律:** 500 × 5 fold 全部计入 DSR 的 trials 计数(与刚 ship 的
  SearchStats 元数据同一哲学)— 不许只报最好 fold。

## 6. ridge 闭式基线(一天证伪通道)

信号对 w 线性 + sigmoid 单调 → 固定 β γ 下整条管线 ≈ 广义线性模型。在跑
Optuna 前先做:

```
对每个 fold IS 段: ridge 回归  next_k_bar_return ~ d_0..d_5   (k ∈ {24, 72})
→ 用回归 ŵ 直接当 Arm B 权重跑 OOS
```

- 若**凸方法都找不到**正 OOS alpha 的谱形,TPE 大概率也不会无中生有 →
  提前触发判决表第 2 行(强证伪),省掉全部算力;
- 若 ridge ŵ 已有正 OOS alpha,其谱形(哪些 band 载荷大)是 Optuna 结果的
  交叉验证 — 两者谱形矛盾 = 过拟合警报。

## 7. 产出物

```
research/mra_ab/
├── README.md          (本设计,跑完后追加"结论"节 — 判决表逐行勾)
├── mra_ab.py          (单脚本: load → folds → ridge 基线 → optuna → 汇总)
├── requirements.txt   (numpy, numba, optuna, psycopg, pyyaml)
└── out/
    ├── summary.csv    (arm × fold: IS/OOS alpha, MDD, Sharpe, DSR, best params)
    └── spectra.csv    (B/C 臂最优 w 谱形 + 对嵌套配置的余弦相似度)
```

加上 PG 里的 optuna studies(:8088 可视化 fitness landscape 切片 —
顺带肉眼检验"w 空间比 period 空间平滑"的论断,A 臂 L 维 vs B 臂 w 维的
parallel-coordinate 图就是证据)。

## 8. Non-goals(本实验不回答的问题)

1. **GA landscape 平滑性/crossover 语义**(权重空间对 block crossover 更友好)
   — 那是 phase 2:本实验若 H1 成立,再用 toy GA(同预算 GA vs TPE)单测;
2. **simulator.go 全保真复刻** — 立项后由 Go 侧 mra_v1 + 四窗 crucible 回答;
3. **跨粒度结论** — 全实验锁 1h(§13 铁律:基因不跨粒度),日线/分钟线另案;
4. **波动率特征 bank 化**(MAV 通道同手法)— 留 v2,本轮 MAV 维持原样以
   隔离变量。

## 9. 风险与已知偏差

- **单资产单 regime 序列**:BTCUSDT 8.7 年含两轮周期,但 fold 间高度相关
  (anchored IS 重叠)→ 5 fold 不是 5 个独立样本,判决表的"≥3/5"是序惯证据
  而非独立检验;PBO/CPCV(§12.4 item 4)是后续加固件,框架可直接复用本脚本;
- **简化模拟器与 Go 语义的缝隙**:双池/macro 注入被舍弃,若 mra_v1 立项后
  四窗结果与本实验矛盾,先查这条缝;
- **TPE ≠ GA**:TPE 对 B 臂线性空间可能比 GA 更高效,B 的优势量可能高估
  GA 场景的真实增益 — 效应量打折读,方向性结论(立项与否)不受影响。

---

## 10. 结论 — ridge 基线(2026-06-12 跑完)

运行:`mra_ab.py --stage ridge`(优选 venv:`../optuna_toy/.venv`),
BTCUSDT 1h 76,165 bars / 29 gaps,5 折 × 2 horizon,确定性闭式解。
输出 `out/`(实费率 10+5bps)与 `out_nofee/`(零摩擦对照)。

**判决:判决表 row 2(强证伪)未触发 → Optuna 阶段放行,但带 §10.3 设计修正。**

### 10.1 数字

| 口径 | OOS alpha(10 个 fold×horizon 格) |
|---|---|
| 实费率(10+5bps) | **10/10 全负**(−16% … −68%/yr) |
| 零摩擦对照 | **7/10 正**(+0.6% … +31%/yr;fold2 2022 熊与 fold3 各 1-2 格负) |

归因:失败模式不是"信号为空"而是**信号弱 + 换手摩擦主导**——σ 标定后策略
每小时在 ~0.3-0.7 仓位间摆动,15bps 单边摩擦年化吃掉 30-50 个点。
IS R² ~0.1-0.3%(1h 收益的典型量级),val R² 多为负,λ 几乎全选 100(重收缩):
信号真实但很弱,毛 alpha 经不起 1h 频率的全幅再平衡。

### 10.2 意外发现:谱形跨折稳定

ŵ 谱形(out_nofee/ridge_spectra.csv)在 5 折 × 2 horizon 上高度一致:
`w1(12-24h band) ≈ +1.0, w2(24-48h) ≈ −0.9, w0(<6h) ≈ −0.3, w4(96-192h) ≈ +0.3,
w5 ≈ 0`,且 cos_nested ≈ 0(全部 |cos| ≤ 0.05)。

解读:ridge 找到的方向**不是** priceDeviation(嵌套配置),而是一个
"12-24h 动量 × 24-48h 反转"的带间对比结构——这正是 filter bank 表达得了、
sigmoid_v1 的单一 deviation 项表达不了的对象。这是 H1(表达力)方向的
**正面间接证据**,也是 Optuna 结果未来的交叉验证锚(谱形矛盾 = 过拟合警报)。
注意:anchored 折 IS 重叠,稳定性部分来自数据共享,打折读。

**追加测量(2026-06-12):权重空间正交 ≠ 信号不相关。** cos_nested≈0 是
权重空间命题;信号时间序列相关 `corr(ŵ·d, n·d) = ŵᵀΣn/√(ŵᵀΣŵ·nᵀΣn)`,
仅当 Σ=cov(d)∝I 时两者等价——而 band 高度共线(§9 一阶 IIR 滚降慢)+方差差
一个量级,Σ 远非对角。OOS 段实测:h=24 corr ∈ [−0.24, −0.12](逆 deviation,
反转主导),h=72 corr ∈ [+0.36, +0.53](顺 deviation,动量成分多,与 w3 在
h=72 转正一致)。即对 priceDeviation 回归 R² ≤ 0.28 → **ridge 信号 ≥ 72%
的方差是旧特征表达空间之外的新成分**。"有意义"的论证依据是三件事而非
不相关:①不可表达性(旧模型价格信号空间是一维 {a·(1,…,1)·d},任何 A1 缩放
都只能全 band 同号等权;ridge 解要求相邻 band 反号,在其张成之外);
②跨折稳定;③正毛 alpha。**可变现性(净摩擦 alpha)待 Optuna 阶段验证。**

### 10.3 设计修正 [AMENDED 2026-06-12,Optuna 阶段生效]

基线跑出来的接线级教训,两臂对称、不动判决表:

1. **幅度维必须可搜**:d_k 量纲是 bps 级,w ∈ [−1,1] × β=0.88 会让 sigmoid
   饱和在 0.5 平坦区(基线第一次跑即此假象:策略退化为恒定 50/50)。B 臂加
   1 个 gain 维(或等价地放宽 w 界);A 臂的 priceDeviation 天然 ±20% 量纲,
   A1 已承担幅度——两臂各自有幅度自由度后对称。
2. **换手控制进搜索空间**:10.1 显示摩擦主导,wedge 阈值(|ΔW| 死区宽度)
   应作为两臂共享的搜索维(同维同界),否则 Optuna 只是在重复"信号弱于摩擦"
   的结论而非比较表示形式。
3. ridge 的符号/σ 标定接线(预测收益为正 ⇒ signal 取负;signal/σ_IS)已在
   mra_ab.py docstring step 4 固化,Optuna 阶段模拟器直接复用。

## 11. Optuna 阶段实现注记 [INVENTED v2,2026-06-12,跑前冻结]

§10.3 修正的接线级落地,全量跑之前写定;判决表(§1)不动。

**搜索空间(共享维三臂同名同界):**

| 维 | 界 | 来源 |
|---|---|---|
| A3, A1, A2, w_0..w_5, w_all | [−1, 1] | chromosome.go §4.1 镜像 / §2 |
| EMA_L | int [50, 300] | 同上 |
| MAVShort / MAVLong | int [5,50] / [30,250];long ≤ short 时取 short+1(镜像 Go Clamp short<long) | 同上 |
| **sig_std**(B/C 幅度维,§10.3-1) | log [0.02, 2.0] | β=0.88 ⇒ exponent σ ∈ [0.018, 1.77],覆盖"贴 A 幅度"到"近满幅择时" |
| **wedge**(共享,§10.3-2) | log [0.001, 0.2] | 下界≈ridge 实效区,上界=强换手抑制 |

**关键接线:**
- **sig_std 重参数化**:`signal_price = sig_std·(D·w)/σ_IS(D·w)`,σ 只取 IS 段
  (因果);w 的范数被归一吸收 ⇒ 有效自由度 = 方向 + 幅度,与 ridge 的 1/σ
  接线(§10.3-3)同构。副作用:C 臂 w_all 只剩符号作用(幅度由 sig_std 承担)。
- **wedge 语义收紧**:`|ΔW| ≥ wedge 且 |ΔUSD| ≥ $5` 才成交(AND)。ridge 阶段
  是 OR——$5 下限在 1e4 本金下等效无死区,正是 §10.1 摩擦主导的接线根源。
  三臂对称,不影响 A/B 效度;但 turnover 口径与 ridge 结果不可直比。
- **共享简化**(三臂同,隔离单一变量):无 market-state beta 调制
  (QuietThreshold 不进搜索空间)、β γ 冻结 champion 7550b6、无双池/macro。
- **DSR 接线**(判决表 row1):逐行移植 `internal/verification/dsr.go`
  (Bailey-Prado 闭式 + Acklam Φ⁻¹)。每臂 pooled:`Var(Sharpe)` = 该臂全部
  trial 的 IS bar-level Sharpe 方差,`N` = 该臂总 trial 数(500×5,§5 NTrials
  纪律);`SR_obs` = 该折 best-trial 的 **OOS** bar-level Sharpe,`T` = OOS
  bars,skew/kurt 取 OOS per-bar returns。
- **目标函数**:IS alpha − 10·max(0, MDD − 0.70)(§5;FatalMDD=0.70 取
  config.yaml 现值)。
- **storage**:同 PG 实例独立库 `optuna_mra_ab`(自动建库)——满足 §5"进现有
  PG/dashboard 可视化",同时保住 header 的"quantlab 库只读"教义。
  TPE seed = crc32(study 名),全新跑确定性;`--storage` 可覆盖(sqlite)。

## 12. 结论 — Optuna 阶段(2026-06-12 跑完,7500 trials)

运行:`mra_ab.py --stage optuna`(实费率 10+5bps,3 臂 × 5 折 × 500 trials,
studies 在 PG 库 `optuna_mra_ab`,数字在 `out/optuna_summary.csv` / `optuna_spectra.csv`)。

**终局判决:row 4(介于其间)→ 不立项 mra_v1,不弃案;等 mainnet 实盘偏差
数据后复议。证据天平偏 H0。**(通俗版解读 + 后续方向排序:
`docs/learn/LEARN-mra-filter-bank-research.md` §5.2)

### 12.1 判决表逐行勾(§1,预注册,只解读不改阈值)

| 行 | 条件 | 结果 |
|---|---|---|
| row 1 (H1) | B−A 跨折 median OOS alpha ≥ +1%/yr **且** DSR(B)≥DSR(A) 于 ≥3/5 折 | 条件① +1.23% 名义达标**但是伪影**(见 12.2);条件② **0/5 折** ✗ → **H1 不成立** |
| row 2 (强证伪) | ridge 阶段已判 | 未触发(§10) |
| row 3 (容量无用) | B 最优 w 对嵌套余弦 > 0.95 | median cos = −0.377,B 没塌缩回嵌套 → 未触发 |
| **row 4** | 介于其间 | **✓ 收集为证据,复议待 mainnet 数据** |

### 12.2 数字与四条关键证据

OOS alpha(实费率)× DSR,按折:

| fold | A: alpha / DSR | B: alpha / DSR | C: alpha / DSR |
|---|---|---|---|
| 1 | +16.7% / 0.29 | +7.0% / 0.08 | +23.7% / 0.27 |
| 2 | −23.5% / 0.39 | −26.7% / 0.19 | −20.5% / 0.33 |
| 3 | −9.8% / 0.95 | −6.6% / 0.86 | −15.0% / 0.87 |
| 4 | −7.8% / 0.70 | −26.9% / 0.29 | −17.2% / 0.49 |
| 5 | +3.9% / 0.12 | +0.3% / 0.03 | +6.1% / 0.12 |
| **median** | **−7.8%** | **−6.6%** | **−15.0%** |

1. **median 差 +1.23% 是伪影**:逐折比较 B>A 仅 fold3(1/5);两臂的 median
   恰好落在不同折上造成名义达标。判决表条件②(DSR ≥3/5)正是为捕捉这种
   伪影而预注册的,起效了——B 的 OOS bar-Sharpe 在 **5/5 折**都 ≤ A。
2. **IS→OOS 不迁移(H0 的形态)**:B 的 IS objective 是 A 的 2-3 倍
   (0.22-0.40 vs 0.05-0.18),OOS 反而更差 → 额外容量主要被 IS 过拟合吸收;
   但不是"塌缩回嵌套"分支(cos=−0.38,B 确实在用带间结构——只是用在了
   记 IS 噪声上)。
3. **谱形交叉验证:§10.2 预设的过拟合警报触发**。换算到预测空间
   (sim 空间 w 取负),w0(<6h 反转)、w1(12-24h 动量)跨折复现 ridge 锚 ✓;
   **w2(24-48h)与 w5(96-192h)跨折与 ridge 反号** ✗。
4. **净摩擦 alpha 三臂全军覆没**(median −7.8 / −6.6 / −15.0%):wedge 死区
   进搜索空间(§10.3-2)也没把净 alpha 救回正区——§10.1"信号弱+摩擦主导"
   在换手控制修正后依然成立。**Caveat**:A 臂是简化镜像而非完整 sigmoid_v1
   (无双池/macro/市场状态调制,β γ 冻结),此结果**不构成**对现役 champion
   四窗成绩的否证。

### 12.3 设计反思(写给复议时的自己)

- **C−A = −7.2% ≠ 0(预期 ≈0)**:分解被结构污染——A 多一个 A2·logReturn
  动量项而 C 没有,故 B−C = +8.4% **高估**纯容量效应,§2 的分解读法降权。
- 复议时的加固件:CPCV/PBO(LEARN-ga-rl-bayesian §12.4 item 4,框架可直接
  复用本脚本)+ anchored 折间相关的独立性问题(§9)依旧在。
  → **已落地并跑完,见 §13。**
- 复议触发条件(预注册于 row 4):mainnet 实盘偏差数据可用之后。

## 13. CPCV/PBO 审计(复议前置加固件,2026-06-12 跑完)

工具:`research/cpcv_pbo/cscv.py`(Bailey-Prado 2017 CSCV,纯函数,算法注记
见其 docstring;通俗版讲解 `docs/learn/LEARN-cpcv-pbo.md`);入口 `--stage pbo`。对 15 个 arm×fold study **各自真实发生
过的选择事件**做审计:该 study 全部 500 个 trial 在该折 IS 段(优化器排名用的
那段数据)重模拟成日级收益矩阵,S=16 块 → 12870 条 IS/OOS 路径,问"按 IS
Sharpe 选出的冠军在 OOS 半段保住名次的概率"。数字:`out/pbo_summary.csv`。

**工具校准**(cscv.py 自检,确定性):纯噪声候选池 → PBO=0.58;植入一个
真优势候选 → PBO=0.000。即:有真东西时该工具会亮绿灯。

### 13.1 结果

| arm | PBO 按折(1→5) | median PBO | degradation slope 范围 |
|---|---|---|---|
| A | 55%, 50%, 40%, 24%, 30% | **40%** | −0.94 … −0.98 |
| B | 46%, 26%, 50%, 39%, 35% | **39%** | −0.51 … −0.79 |
| C | 42%, 49%, 43%, 47%, 72% | **47%** | −0.47 … −0.88 |

median p(IS 冠军 OOS Sharpe < 0) ≈ 3%(三臂同)。

### 13.2 读法

1. **三臂的选择动作都落在噪声区间**(噪声基线 ≈50%,真优势 → ~0):按 IS
   挑最优,OOS 名次保住与否接近抛硬币。这从第三个独立角度(继 DSR 条款、
   谱形反号之后)印证 §12 的 row 4/H0 判决——且对 A 臂同样成立:**连现状
   形态的小时级调参,其"选拔"本身也几乎不传递信息**,与 §12.2-4(净摩擦
   下三臂全负)互相咬合。
2. **slope 全负是比 PBO 更刺眼的信号**:候选池内 IS Sharpe 对 OOS Sharpe
   的回归斜率全部 < 0——IS 排名越靠前 OOS 越差,"选拔在主动奖励噪声"的
   教科书形态(slope≈1 才是 IS 优势可迁移)。
3. **p_oos_neg ≈ 3% 的正确读法**:候选们在 OOS 半段 Sharpe 多为正,是因为
   样本期偏牛、beta 抬底;PBO 审计的是**选拔的排名传递**,不是"会不会亏钱"
   ——输的是名次优势,不是绝对生存。
4. **方法边界**:CSCV 的 OOS 半段 ⊂ 该折 IS 段(审计选拔的教科书设置),
   与 §3 折表的真 OOS 段无重叠使用;块长(~100-170 天)≫ 持仓记忆,块界
   不 purge 的近似成立(cscv.py 注记)。

### 13.3 对复议的含义

- 前置加固件**已就位**:复议 MRA(或任何新特征线)时,PBO 应进判决表
  (具体阈值届时预注册,本次数字只作基线参照 [INVENTED v1:候选阈值
  PBO < 25% 且 slope > 0])。
- §12.4 item 4 的原题下半场("PBO 会不会推翻历史 Promote")需要引擎级
  收益序列回放,两条路线及裁决待办:`research/cpcv_pbo/README.md`。
