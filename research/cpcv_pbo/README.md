# cpcv_pbo — CSCV/PBO 过拟合概率审计(通用模块)

> 来源:LEARN-ga-rl-bayesian §12.4 **item 4**(CPCV/PBO 离线原型)+
> MRA 研究线终局判决的**复议前置加固件**(mra_ab README §12.3)。
> 仓库教义:research/ 永不进 server path,只读数据,零 Go 改动。

## 它回答什么

DSR 折减的是**单个** Sharpe 里的多重检验幸运度;PBO 审计的是**"选择"这个
动作本身**:给定一次参数搜索的全部 N 个候选在同一段数据上的收益矩阵,
"按 IS 成绩选出的冠军,放到 OOS 掉到中位以下"的概率。**PBO > 50% ⇒ 这次
选择大概率在挑噪声**,选出来的"最优"不可信。

实现 = Bailey, Borwein, López de Prado, Zhu (2017) 的 CSCV(组合对称交叉
验证):S 块 → C(S,S/2) 条 IS/OOS 路径 → IS 冠军的 OOS 相对名次分布。
算法细节、纯函数接口、向量化注记、自检:`cscv.py` docstring(单一权威源)。
通俗版讲解(考试比喻、slope 读法、误读警示):
`docs/learn/LEARN-cpcv-pbo.md`。

```
.venv 复用 research/optuna_toy/.venv(纯 numpy,无新依赖)
python cscv.py          # 自检:纯噪声 PBO≈0.5;植入真优势 → PBO→0
```

## 消费者

| 消费者 | 状态 | 入口 |
|---|---|---|
| MRA A/B 实验(15 个 arm×fold 选择事件) | ✅ 首个用例 | `../mra_ab/mra_ab.py --stage pbo`,结论 mra_ab README §13 |
| 历史 challenger Promote 审计(§12.4 item 4 原题:"PBO 会不会推翻过任何一次 Promote") | ⬜ 待路线裁决 | 见下节 |
| GT-Score 离线证据字段(§12.4 item 5) | ⬜ gated | 本框架可复用 |

## 历史 challenger 审计的前置问题(下一步,先裁决再动工)

PBO 需要 **T×N 收益矩阵**(同一时间段 × 全部被比较的候选),而现存数据是:
`evaluation_traces` 有 gene + 分数(单点,无时间序列);`gene_records` 有完整
result package(亦无逐 bar 收益)。两条可行路径,代价/保真度互换:

1. **Go 侧回放**:复用 `verification.RunReview` 重放机制 + A5 `CaptureReturns`
   思路,对历史 epoch 的 top-N 基因批量导出逐 bar 收益 → Python 只做 CSCV。
   保真度 = 引擎级(双池/macro/市场状态全保留);代价 = 需要一个导出 harness
   (research 消费,不进 server path 的一次性 cmd)。
2. **Python 简化镜像**:用 mra_ab 的简化模拟器跑 sigmoid_v1 基因。代价低但
   保真度缺口已知(无双池/macro/状态调制)——mra_ab README §12.2-4 的 caveat
   同样适用,审计结论只能当方向性证据。

倾向路径 1(审计 Promote 决定需要引擎级保真);量级 = 一次性导出工具,
不触碰 GA 热循环,无 `fitness_version` 事件。
