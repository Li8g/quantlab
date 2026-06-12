# LEARN — 多尺度分解替代均线作 GA 维度:评估与跟进(MRA filter bank)

> 类型:研究评估 + 跟进文档(非规范规格)。
> 来源:2026-06-11 对"用泰勒逼近或小波/傅里叶变换替代均线(小时/日/周/月线)
> 作为 GA 维度"这一想法的评估对话,整理沉淀。
> 状态:**评估完成;A/B 设计冻结;ridge 基线已跑完(2026-06-12,强证伪未触发,
> 详 §5.1 与 `research/mra_ab/README.md` §10);`--stage optuna` 未写**。
> 前提事实:13 维染色体下 GA 的 CPU 负荷有余量(用户实测),表达力扩容在算力上可行。
> 执行排序:建议挂入 `LEARN-ga-rl-bayesian-preliminary-research.md` §12.4 序列
> (依赖关系上与 item 4 CPCV/PBO 共享反泄漏框架,可并行)。

---

## 0. 一句话结论

**方向有真实价值,但价值排序是 因果小波 > 傅里叶 > 泰勒;且最优落地不是引入小波
机械,而是"固定 dyadic EMA filter bank + 进化权重"——它在数学上就是小波式多分辨
分析(MRA)的 IIR 化身,拿到小波想法约九成收益,付出约一成工程与正确性风险。**
想法的真实价值来自"固定多尺度基 + 进化权重"这个结构转换(把 GA 从"搬滤波器闸门"
改为"调音台推子"),而不是来自变换本身的数学异域性。

## 1. framing 校正:现状已是尺度搜索

sigmoid_v1 只有 3 个特征(priceDeviation / logReturn / volRatio),由 A1/A2/A3
加权。EMA 本身是一阶 IIR 低通滤波器——GA 搜 `EMAShortPeriod`(5-100) 与
`EMALongPeriod`(50-300),**已经是在连续尺度空间里搜 2 个采样点**。所以提案的
准确表述是:

> 把"搜 2 个尺度参数"换成"固定 J 个尺度、进化 J 个权重"。

真正的好处不是表达力,而是 **fitness landscape 平滑化**:权重维连续光滑,
period 维近似阶梯(period 51→52 整条 EMA 轨迹非线性重排),block crossover
在权重段上的语义远比 period 段干净(详 §5)。

## 2. 三候选评估

### 2.1 泰勒逼近 — 弃案

价格路径在 diffusion 极限下处处不可微,对噪声路径估导数 = 高通滤波 = 噪声放大器。
实操中退化为滑窗多项式拟合(Savitzky-Golay):slope ≈ momentum(已被 logReturn
项捕获)、curvature ≈ acceleration(弱 alpha、高噪声)。理论不成立,不投入。

### 2.2 傅里叶 — 用途窄,不做主信号基

全局正弦基假设窗口内平稳 + 周期延拓,crypto 两者都不满足;滑窗 DFT 有 spectral
leakage,且最低频系数(最关心的趋势成分)恰在右边缘方差最大。真实周期成分
(8h funding、亚美时段)只在 intraday 粒度存在。定位:research/ 离线季节性
**检测**工具,不进策略。

### 2.3 小波 — 方向对,有一个一级风险的工程坑

**为什么对:时频局域化。** Fourier 基频率上完美局域、时间上零局域——一次
regime change 会涂抹进所有频率系数。小波基有限长、可平移缩放,系数 W(j,t) 回答
局域问题("时间 t 附近、尺度 2^j 上振荡多大"),时间/频率分辨率按 Heisenberg
不确定性互换。这正好匹配市场的两个本性:非平稳(需要时间局域化)+ 多尺度结构
(不同 horizon 参与者共存,heterogeneous market hypothesis——"小时/日/周/月均线"
直觉即 HAR-RV 模型结构,已实现波动率文献的基准模型)。

**MRA 分解**:`price = A_J(粗尺度趋势) + Σ D_j(逐级变细的波段/噪声)`,能量守恒,
按尺度精确分账。

**坑:边界系数吃未来。** 标准 DWT/MODWT 实现(pywt/MATLAB 默认)滤波器居中 +
边界对称 padding。db4 在 level j 的有效支撑 `(L−1)(2^j−1)+1` bar(level 5 ≈ 218,
level 6 ≈ 442),居中意味着约一半在未来。由此三宗罪
([Quilty & Adamowski 2018, J. Hydrology 563:336-353](https://uwaterloo.ca/scholar/jquilty/publications/addressing-incorrect-usage-wavelet-based-hydrological-and-water-resources),
跨领域但机理通用):

1. **全序列一次分解再切 train/test**(粗暴版):等价于用零相位 filtfilt 做回测
   特征,IS 可以漂亮到任意程度。文献里大量"小波去噪+NN,方向准确率 90%+"死于此,
   复现改逐点因果后优势掉回掷硬币。
2. **滑窗重分解但读右边缘系数**(隐蔽版):每个 t 只用 ≤t 数据,看似因果,但右边缘
   系数靠 padding 的"假未来"算出,会 **repaint**(t+50 时回看同一位置值已变)。
   陷阱:回测用最终版分解(每个历史系数已被未来修正),实盘只能拿点时(point-in-time)
   右边缘值——回测在评估一个实盘不存在的特征。**不是经典 look-ahead 而是
   train/serve 特征不一致,更难被测试抓到。** 本仓 `verification.RunReview` 哈希
   重放闸门对此有现成免疫力:repaint 特征逐 bar 重放必不可复现,闸门直接红。
3. **分解参数用全样本选**(元参数泄漏):decomposition level / 母小波的选择必须
   进冻结快照(GAConfigSnapshot 同理)。

**因果修复及其代价。** à trous(undecimated)+ 单边因果滤波可严格只用过去
(causal Haar: `A_j[t] = (A_{j−1}[t] + A_{j−1}[t−2^{j−1}])/2`,即 WaveNet dilated
convolution 结构),O(J)/bar 增量、bit-exact。但有不可绕过的三选二:

> **因果性、零滞后、频率选择性——任选其二。**

因果滤波在 level j 引入 ~2^{j−1} bar 群延迟。滞后不是均线的实现缺陷,是因果性的
物理代价,小波只是把滞后**结构化地重新分配**到各尺度(细尺度滞后小、粗尺度大)。

**推理链收口**:修完三宗罪后,因果小波相对一组 EMA 只剩两条理论优势(近似正交的
频带分离、能量守恒),而因果 detail 公式 `D_j = 慢低通 − 快低通` 本身就是两条
均线之差——**因果版小波在数学上坍缩到均线差分附近**,故正确行动是 EMA bank。

## 3. EMA filter bank 的数学

### 3.1 三个恒等式

**① EMA = 一阶 IIR 低通。** `y[t] = α·x[t] + (1−α)y[t−1]`,α = 2/(s+1);
传递函数 `H(z) = α/(1−(1−α)z⁻¹)`;DC 增益 1、高频增益 ≈ 1/s、截止 ∝ 1/s、
低频群延迟 τ = (s−1)/2("均线滞后半窗"的精确表述)。

**② 两个低通之差 = 带通。** s_a < s_b 时 `D = EMA_a − EMA_b`:DC 处 1−1=0
(趋势精确对消),高频处 0−0=0,峰值落在两 span 之间(对数尺度)。MACD = EMA₁₂−EMA₂₆
即此构造特例;与因果 Haar à trous 的 `D_j = A_{j−1} − A_j` 是同一算子家族——
**EMA 差分就是小波 detail 系数的 IIR 化身**。

**③ telescoping 精确分解。** 固定 dyadic span S = (6,12,24,48,96,192):

```
x = (x − EMA₆) + (EMA₆−EMA₁₂) + (EMA₁₂−EMA₂₄) + (EMA₂₄−EMA₄₈)
  + (EMA₄₈−EMA₉₆) + (EMA₉₆−EMA₁₉₂) + EMA₁₉₂
```

恒等成立、零重构误差——即 MRA 的 `price = Σ details + approximation`(基不正交)。
dyadic 间隔 = constant-Q(每 band 一个倍频程),匹配市场 1/f 形态谱,各 band
信息量大致均衡(小波天生 dyadic 的同一理由)。特征化:
`d_k = (EMA_{S[k−1]} − EMA_{S[k]}) / close`,`signal = Σ w_k·d_k + A3·(volRatio−1)`。

**嵌套性(关键实验杠杆)**:望远镜和改写可得
`(close−EMA_L)/EMA_L = Σ(所有比 L 细的 band)/EMA_L`,即 **sigmoid_v1 的
A1·priceDeviation = 强制全部细 band 共享一个权重**。新模型严格嵌套旧模型 →
A/B 有干净解读:GA 收敛回嵌套配置 = 容量无用(证伪);收敛到非平凡谱形 = 谱形
本身就是发现。

### 3.2 GA 工程论证(为什么权重空间比周期空间好搜)

1. **信号对基因线性**:`signal = Σ w_k·d_k`,d_k 固定;w 微动→信号线性微动,
   NAV 分段光滑。period 维穿过递归滤波 + 取整 → fitness 布满悬崖。
2. **crossover 语义化**:权重向量活在向量空间,两个优秀父代 w 混合 ≈ 频谱配置
   插值,子代大概率仍合理;两个优秀 period(20 与 80)杂交出 50,轨迹与双亲都不像
   ≈ 随机重启。K 个 w 放同一 segment(`band_weights`),block crossover 把整张
   频谱画像当语义单元遗传——正是 SegmentInfo 设计意图。
3. **突变尺度均匀化**:period 敏感度极不均匀(5→10 翻天覆地,250→255 无感),
   GeneStep 只能折中;w 维敏感度均匀,TestMutationScaleLinearity 关心的线性
   近似真实成立。

### 3.3 CPU 红利(意外但实质)

现在每个 gene 的 period 不同 → 每次 Evaluate 重算自己的 EMA(正是当年增量指标
39× 优化的热循环)。bank 的 span **固定** → d_k 矩阵每窗口算一次、全种群只读共享
(落点 `Adapter.Reset(plan)`,每 worker 各算一份,确定性不受影响),per-gene 工作量
塌缩为 K 维点积 + 模拟器。**加维度反而可能更快。**

旁证检查:信号对 w 线性 + sigmoid 单调 → 管线 ≈ logistic 回归,research/ 侧可用
ridge/logistic 闭式拟合 w 当 sanity 基线——凸方法找不到正 OOS alpha 则 GA 也不会
无中生有,一天内可证伪。

### 3.4 诚实的账(相对真小波丢什么)

- **频带重叠**:一阶 IIR 滚降仅 −20 dB/decade,相邻 band 渗漏 → d_k 共线 →
  fitness 在 w 空间有平坦方向(可辨识性降,收敛慢、解不唯一)。缓解:K 取 5-7;
  真不够再级联 EMA(二阶,滚降翻倍),v1 不必。
- **无 Parseval 能量守恒**:做"能量占比"特征不严格;对加权求和用法无影响。
- **滞后仍在**:band k 群延迟 ≈ (s_{k−1}+s_k)/4 量级,结构化分配但没有消失
  (§2.3 三选二)。

### 3.5 染色体草图 [INVENTED v1,待校准]

```
删: geneDimA1, geneDimEMAShortPeriod, geneDimEMALongPeriod        (−3)
增: w0..w5 ∈ [−1,1], QuantizationStep 0.05, GeneStep 0.2          (+6)
保留: A2(logReturn), A3(volRatio), β, γ, MAV 两 period, 阈值/macro 段
→ 16 维; 新 segment "band_weights"(6 dims, IsCritical=true)
```

MAV 周期 bank 化(波动率通道同手法)留 v2。

## 4. 架构落点

- **新 strategy(`mra_v1`),不是改 sigmoid_v1**:新 StrategyID、新 gene 语义、
  新 Fingerprint;engine 零改动,不触发 `fitness_version` 事件(两层硬边界红利)。
- 实现继承全部工程性质:天然因果(EMA 只看过去)、`incrIndicatorState` 增量模式
  复用(O(1)/bar)、串行累加 bit-exact。
- §13 铁律照旧:基因不跨粒度;本研究全程锁 1h。

## 5. 验证路径与判决标准

**A/B 原型设计已冻结:`research/mra_ab/README.md`(单一权威源,此处只摘要)。**

- 三臂:A = sigmoid_v1 价格特征(对照) / B = 6-band bank / C = 权重绑定嵌套臂
  (把 B−A 分解为表示之差 C−A 与容量之差 B−C);β γ 冻结 champion 值,volRatio
  两臂同搜——差异隔离到价格特征块单变量。
- 数据:`klines` BTCUSDT 1h 全史(2017-08→2026-05, 76k bars);anchored
  walk-forward 5 折,purge+embargo = 385 bars,OOS 段覆盖 2022 熊/2024 牛。
- 优化:Optuna TPE(复用 optuna_toy venv/PG storage/:8088),500 trials/arm/fold,
  全 2500 trials 计入 DSR NTrials(SearchStats 同哲学);ridge 闭式基线先行作
  一天级证伪通道。
- 简化模拟器**刻意不复刻** simulator.go(保 wedge band/费用/DCA alpha,舍双池/
  macro);numba 必需(6×10⁸ bar-step)。
- **预注册判决表**(冻结,事后不许改阈值):B 胜 = OOS alpha 中位数差 ≥ +1%/yr 且
  DSR(B) ≥ DSR(A) 于 ≥3/5 折 → 立项 mra_v1;ridge+TPE 双双找不到正 OOS alpha
  → 强证伪弃案;B 最优 w 与嵌套配置余弦相似度 > 0.95 → 容量无用弃案;其间 →
  存证据,等 mainnet 实盘偏差数据复议。

### 5.1 ridge 基线结果与方法论教训(2026-06-12;数字权威源 = mra_ab README §10)

**判决:强证伪未触发,Optuna 放行(带 §10.3 两条对称修正)。** 实费率(15bps
单边)10/10 格负 alpha,零摩擦对照 7/10 正 → 失败模式 = 信号弱(IC≈0.04)被
小时级换手摩擦吃光(drag ≈ 8760·E|ΔW|·15bps ≈ 40-80%/yr,毛 alpha 仅几个点
——量级差一个数量级),**非信号为空**。

**接线级教训(可迁移):**

1. **sigmoid 饱和假象。** d_k 量纲是 bps 级,max-norm 权重 × β≈0.88 给出
   exponent ~0.01;sigmoid 泰勒 `1/(1+e^x) ≈ 1/2 − x/4` ⇒ 仓位偏离恒
   ±0.22% < wedge 死区 0.5% ⇒ 策略退化为恒定 50/50 组合。第一次跑的"正
   alpha"全是这个假象(不同谱形给出逐位相同的收益 = 饱和的指纹)。修复:
   signal 除以 IS 段 σ(幅度是一维 nuisance,谱形才是被测对象;σ 仅取自 IS,
   因果不破),exponent ~ β·N(0,1),仓位工作区 0.29↔0.71。
2. **符号约定。** `targetWeight = 1/(1+e^x)` 严格递减 ⇒ 预测收益为正须令
   signal 取负。漏负号回测不报错只报亏损。
3. **权重空间正交 ≠ 信号不相关。** cos(ŵ,n)≈0 是权重空间命题,信号相关
   `corr = ŵᵀΣn/√(·)` 仅当 Σ∝I 时等价;band 共线 + 方差差一个量级,实测
   信号相关 h=24 为 −0.24~−0.12、h=72 为 +0.36~+0.53。正确表述:对
   priceDeviation 回归 R² ≤ 0.28 ⇒ **ridge 信号 ≥72% 方差是旧特征表达
   空间之外的新成分**;"有意义"靠不可表达性(旧模型只能全 band 同号等权,
   ridge 解要求相邻 band 反号)+ 跨折稳定 + 正毛 alpha,而非靠不相关。

**谱形发现:** ŵ ≈ (−0.3, +1.0, −0.9, ~0, +0.3, 0) 跨 5 折 × 2 horizon 稳定
= "12-24h 动量 × 24-48h 反转"带间对比,正是 §3.1 嵌套分析预言的"filter bank
表达得了、单一 deviation 项表达不了"的对象——H1 的正面间接证据。可变现性
(净摩擦 alpha)是 Optuna 阶段的待答问题。

## 6. 跟进清单

| # | 事项 | 状态 | 备注 |
|---|---|---|---|
| 1 | 三候选评估(泰勒/傅里叶/小波) | ✅ 2026-06-11 | 本文 §2,泰勒弃、傅里叶离线工具、小波→EMA bank |
| 2 | A/B 原型设计 | ✅ 已冻结 | `research/mra_ab/README.md`;[INVENTED] 参数待用户校准:span 组/判决阈值/预算/λ |
| 3 | `mra_ab.py` 实现 + ridge 基线 | ✅ ridge 完 2026-06-12 | 强证伪**未触发**:实费率 10/10 负 alpha,但零摩擦 7/10 正 → 失败模式=换手摩擦非信号为空;谱形跨折稳定(12-24h 动量×24-48h 反转,非 priceDeviation 方向)= H1 正面间接证据。详 mra_ab README §10 |
| 4 | `--stage optuna` 实现 + 跑 5 折,按判决表裁决 | ⬜ | 须先吸收 README §10.3 修正(幅度维可搜 + wedge 阈值进共享搜索空间);结论回写本表 |
| 5 | (若立项)Go 侧 `mra_v1` 策略 | ⬜ gated | 新 StrategyID;四窗 crucible + OOS/DSR 全闸门 |
| 6 | (phase 2)GA landscape 平滑性单测 | ⬜ gated | toy GA vs TPE 同预算,验证 §3.2 论断;非立项前置 |
| 7 | (v2)MAV/波动率通道 bank 化 | ⬜ 远期 | 同手法;本轮刻意不动以隔离变量 |
| 8 | CPCV/PBO 加固 | ⬜ | = 预研 §12.4 item 4,框架与 mra_ab 脚本互通 |

## 7. 参考文献

- [Quilty & Adamowski 2018, J. Hydrology 563:336-353](https://uwaterloo.ca/scholar/jquilty/publications/addressing-incorrect-usage-wavelet-based-hydrological-and-water-resources)
  — 小波预测"三宗罪"(边界未来数据/分解参数泄漏/切分不当)的系统归纳
  ([Semantic Scholar 条目](https://www.semanticscholar.org/paper/Addressing-the-incorrect-usage-of-wavelet-based-and-Quilty-Adamowski/d2dc4a6617fc12d7dd39b29b67b1e8ad7ecda64a))
- [Boundary-Corrected MODWT + CNN-LSTM(修复方案示例, J. Hydrology 2021)](https://www.sciencedirect.com/science/article/abs/pii/S0022169421002432)
- [fastWavelets R 包(à trous/MODWT 实现参考)](https://cran.r-project.org/web/packages/fastWavelets/readme/README.html)
- [DWT vs LSTM 对比研究(Springer 2025)](https://link.springer.com/article/10.1007/s11135-025-02325-1)
- [Multiresolution forecasting for futures trading(IEEE, 经典文)](https://pubmed.ncbi.nlm.nih.gov/18249912/)
- [Wavelet Transforms in Financial TS: A Review](https://cmpublisher.com/wavelet-transforms-in-financial-time-series-analysis-a-review-on-stock-price-prediction/)
- [Wavelet-enhanced multimodel framework(ScienceDirect 2025)](https://www.sciencedirect.com/science/article/pii/S2214845025002108)
- 仓内关联:`LEARN-ga-rl-bayesian-preliminary-research.md`(§12.4 执行排序)、
  `docs/decision-ga-reproducibility-constraint.md`(ε=1e-4 复现性闸门)、
  `research/mra_ab/README.md`(A/B 设计权威源)
