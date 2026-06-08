# Go 没有 const 指针：共享 mutation 风险与缓解

**目标读者**：有 C/C++ 经验、刚接触 Go 的开发者，或者想理解"指针传递优化"潜在代价的 Go 开发者。

**背景**：本文从 `sigmoid_v1` 回测热路径的一次真实优化（commit `88bd547`）出发，
讲清楚 Go 缺少 `const` 指针语义带来的开发期风险，以及工程上如何判断和缓解。

---

## 1. 问题的起点：值传递 vs 指针传递

`StrategyInput` 是一个 216 字节的结构体，在回测热循环里每根 bar 被按值传入三层调用：

```
evaluateWindow (每 bar)
    → stepCoreFromIndicators(input, ...)       // 复制 216B
        → applyMacroDecision(input, ...)       // 再复制 216B
        → applyReleaseDecision(input, ...)     // 再复制 216B
```

87,600 根 bar × 3 层 × 216B = **56.7MB 结构体拷贝 / 次评估**。
pprof 确认这占了 `runtime.duffcopy` 总量的 ~75%。

改成指针传递可以消除这些拷贝，实测评估时间从 16.83ms → 15.51ms（−7.9%）。
代价是引入了一个 C/C++ 开发者很熟悉、但 Go 开发者容易忽视的风险。

---

## 2. C++ 有 const 指针，Go 没有

在 C++ 里，"只读指针"可以通过类型系统强制：

```cpp
void applyMacroDecision(const StrategyInput* input, ...) {
    input->nowMs = 123;   // ❌ 编译错误：const 对象不可修改
}
```

Go 没有这个机制。`*strategy.StrategyInput` 和"可写指针"是同一个类型：

```go
func applyMacroDecision(input *strategy.StrategyInput, ...) {
    input.NowMs = 123   // ✅ 编译通过，但语义上是错误的
}
```

**Go 无法在类型层面区分"只读指针"和"读写指针"。**

---

## 3. 为什么这是开发期风险，不是运行时风险

这一点值得说清楚，因为两类风险的应对策略完全不同。

**运行时风险**的特征：程序行为依赖环境、调度、输入，可能随机出现，难以复现。
典型例子：竞态条件、未初始化内存、数值溢出。

**开发期代码误操作风险**的特征：程序行为完全确定——代码写错了就错了，写对了就对了。
风险在于开发者，不在于运行时。

`*strategy.StrategyInput` 的共享 mutation 风险属于第二类：

- 不会有随机崩溃，也不会有依赖环境的行为差异。
- 如果有人误写了 `input.X = y`，每次运行都会产生同样的错误结果（确定性的 bug）。
- 没有人误写，就完全没有问题。

本质上它和"不小心修改了函数参数"或"用了全局变量"是同一类问题：**代码层面的错误，不是系统层面的不稳定**。

---

## 4. 具体会出错的场景

改后，`evaluateWindow` 里的 `input` 是 loop 外声明的一个结构体，
所有调用通过同一个指针访问它：

```go
input := strategy.StrategyInput{Chromosome: gene}

for i, bar := range window.Bars {
    input.NowMs = bar.OpenTime     // 每 bar 重置
    input.Portfolio = p             // 每 bar 重置
    input.LastProcessedBarTime = lastProcessedBarTime

    stepCoreFromIndicators(&input, ...)   // 同一个 &input 传下去
}
```

调用链内部的读取顺序：

```
computeMicroRebalance  → 读 input.Portfolio
buildMicroOrders       → 读 input.NowMs
applyMacroDecision     → 读 input.NowMs, input.Portfolio.USDT
applyReleaseDecision   → 读 input.NowMs, input.Portfolio.DeadBTC/FloatBTC
```

**危险的误操作**：假设有人在 `applyMacroDecision` 里加了一行"扣掉已花的 USDT"：

```go
// 意图：本地记录一下扣款
// 实际效果：污染了共享 input
input.Portfolio.USDT -= d.AmountUSD
```

在旧的值语义下，这只改了本地副本，完全无害。
在指针语义下，`applyReleaseDecision` 随后读到的 `input.Portfolio.USDT` 是被扣减后的值，
Release 的持仓判断基于了错误的账本。

这类 bug 有三个特性让它特别难查：

1. **只在特定 bar 条件下触发**：宏注入要 `ShouldInject=true`，Release 要同一根 bar 也满足触发条件，两者同时发生的概率不高。
2. **单函数单元测试完全看不出来**：`applyMacroDecision` 单测传进去的是一个独立的指针，测完没问题。问题出在两个函数在同一 bar 内连续调用时。
3. **语法与值语义一模一样**：`input.X = y` 在值语义下是无害的本地操作，指针语义下是有副作用的写。**视觉上无区别。**

---

## 5. 为什么不会"跨 bar 污染"

理解风险边界同样重要。loop 开头的三次重置截断了跨 bar 的传播：

```go
input.NowMs = bar.OpenTime
input.Portfolio = p                // 从 applyStrategyOutput 的正确结果重置
input.LastProcessedBarTime = ...
```

账本 `p` 的演进路径是 `applyStrategyOutput(p, out, bar.Close, friction)`，
它读取的是 `p`（上一轮结束时的正确状态），不读 `input.Portfolio`。
即便 bar N 里发生了 mutation，bar N+1 开始时字段都会被覆盖。

**风险窗口仅限于单根 bar 的调用链内部。**

---

## 6. 当前的缓解措施

函数签名上加了注释：

```go
// input is a read-only pointer; callers must not mutate it through this pointer.
func stepCoreFromIndicators(input *strategy.StrategyInput, ...) {
```

对于同包的小规模内部调用链，这个防线够用：
- 修改这几个函数的 PR 在代码审查时会被自然检查。
- bug 是确定性的，一旦出现很快能复现和定位。

---

## 7. 更强的选项（及其代价）

如果这个调用链未来变得更复杂，有两个更强的方案：

### 选项 A：最小结构体（推荐）

内部函数只接受真正需要的字段，彻底消除指针：

```go
// 不再传 StrategyInput，只传热路径实际用到的值
func applyMacroDecision(
    nowMs, lastBarTime int64,
    portfolioUSDT float64,
    c Chromosome, rs *RuntimeState, totalEquity float64,
) ([]strategy.OrderIntent, RuntimeState)
```

优点：签名即文档，每个函数依赖什么一目了然，指针 mutation 风险归零。
缺点：调用处参数变多，3 个函数都要修改。

### 选项 B：只读 view 类型

定义一个内部 view 结构体，赋值时只拷贝 3 个字段（~24B）而不是 216B：

```go
type stepInputView struct {
    NowMs                int64
    Portfolio            strategy.PortfolioSnapshot
    LastProcessedBarTime int64
}

func applyMacroDecision(input stepInputView, ...) { ... }
```

优点：值语义回归，mutation 自动隔离，拷贝量从 216B 降到 ~24B。
缺点：新增类型，两套字段名需要维护同步。

---

## 8. 判断标准

什么情况下从注释防线升级到更强选项？

- 这几个函数开始被多人并行修改。
- 调用链继续扩展，新增了更多读 `input` 的函数。
- `champion-vs-#2` score 间距缩窄到 3×ε 以下，任何计算偏差的容忍度降低。

当前阶段三个函数稳定、同包可审查，注释防线足够。

---

## 总结

| 问题 | Go 缺少 `const` 指针，无法通过类型系统阻止对只读指针的写操作 |
|------|--------------------------------------------------------------|
| 风险类型 | 开发期代码误操作，不是运行时不稳定 |
| 出错条件 | 调用链内某函数误写 `input.X = y`，后续函数读到错误值 |
| 风险窗口 | 单根 bar 的调用链内部（跨 bar 被 loop 重置截断） |
| 当前缓解 | 函数头注释 + 同包代码审查 |
| 升级触发 | 多人并行修改 / 调用链扩展 / score 容忍度收紧 |
