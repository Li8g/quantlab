# Go 内存逃逸分析：从"4 allocs/bar"到"0 allocs/bar"

**目标读者**：有 Python/C/Java 经验的 Go 新手。

**背景**：本文从 `sigmoid_v1` 回测热路径里的一个真实优化案例出发（commit `f7b8929`），
讲清楚 Go 的逃逸分析机制和热路径零分配的惯用写法。

---

## 1. 你已知道的：栈 vs 堆

**C 语言里**你手动区分：

```c
// 栈：函数返回后自动消失
int x = 42;

// 堆：手动管理，必须 free
int *p = malloc(sizeof(int));
*p = 42;
free(p);
```

**Python/Java 里**你从来不想这件事——所有对象都在堆上，GC 全权负责。

**Go 的位置**：有 GC（所以不用 free），但编译器会尽量把变量分配在栈上以减少 GC
压力。分配在栈上意味着函数返回时自动消失，零 GC 开销。分配在堆上意味着 GC 要扫描、
标记、回收。

---

## 2. Go 的逃逸分析（escape analysis）

Go 编译器在编译时自动判断每个变量该放栈还是堆。这个过程叫**逃逸分析**（escape
analysis）。

**核心规则**：一个变量的地址如果"逃出"了它被创建的函数，就必须放堆上——因为栈帧在
函数返回后就消失了，留在栈上的地址会变成悬空指针。

最常见的触发方式：**对局部变量取地址并返回**。

```go
func foo() *int {
    x := 42    // 看起来是个栈变量
    return &x  // 但地址被返回了——x 必须逃逸到堆
}
```

Go 程序员把这种现象叫"逃逸"（escape）。

---

## 3. `buildDebugSnapshot` 里的四次逃逸

本仓库 `internal/strategies/sigmoid_v1/step.go` 里有这个函数：

```go
func buildDebugSnapshot(signal, targetWeight float64, state MarketState) *strategy.DebugSnapshot {
    sig := signal        // 局部 float64
    tw  := targetWeight  // 局部 float64
    st  := string(state) // 局部 string

    return &strategy.DebugSnapshot{
        Signal:       &sig,  // ← 取了 sig 的地址
        TargetWeight: &tw,   // ← 取了 tw 的地址
        MarketState:  &st,   // ← 取了 st 的地址
    }                        // ← 结构体本身也返回了指针
}
```

四个地方各触发一次逃逸：

| 触发点 | 逃逸原因 |
|---|---|
| `&sig` | sig 的地址被放进结构体并返回 |
| `&tw` | 同上 |
| `&st` | 同上 |
| `&strategy.DebugSnapshot{...}` | 结构体指针从函数返回 |

每次调用 = 4 次 `malloc`。热路径每 bar 调用一次 = **87,600 bars × 4 = 350,400 次
malloc**，然后马上被 GC 回收，因为没有任何代码读过它的值。

---

## 4. 为什么结构体字段用 `*float64` 而不是 `float64`？

这是 Go 里一个常见的习惯用法，值得单独解释。

```go
type DebugSnapshot struct {
    Signal       *float64 `json:"signal,omitempty"`
    TargetWeight *float64 `json:"target_weight,omitempty"`
    MarketState  *string  `json:"market_state,omitempty"`
}
```

为什么不直接 `Signal float64`？

**表达"有值"和"无值"的区别**。Go 没有 Python 的 `None` 或 Java 的 `Optional`，基本
类型也没有空值。用指针可以：

- `Signal = nil` → "这一拍没有 signal 数据"
- `Signal = &someFloat` → "signal = someFloat"

这在 JSON 序列化时特别有用：`omitempty` 对指针字段在 `nil` 时直接跳过整个 key。

**代价**：每次赋值都要取地址 → 逃逸。这是 Go 里 `*float64` / `*string` 字段的经典
性能陷阱，在热路径上代价很高。

---

## 5. 用编译器自己证明

运行这个命令，让编译器打印出所有逃逸决策：

```bash
go build -gcflags='-m' ./internal/strategies/sigmoid_v1/ 2>&1 | grep buildDebugSnapshot
```

输出大概是这样：

```
step.go:296:6: sig escapes to heap
step.go:297:6: tw escapes to heap
step.go:298:6: st escapes to heap
step.go:299:9: &strategy.DebugSnapshot{...} escapes to heap
```

每一行对应一次 `malloc`。`-m -m`（两个 `-m`）可以打印更详细的原因链。

---

## 6. 修法：用 `bool` 参数控制是否分配

最小侵入的修法：在调用链最顶层加一个开关。

```go
// 之前：无论什么路径都分配
func stepCoreFromIndicators(...) (..., *strategy.DebugSnapshot) {
    ...
    return ..., buildDebugSnapshot(signal, micro.TargetWeight, marketState)
}

// 之后：backtest 传 false，live trading 传 true
func stepCoreFromIndicators(..., wantDebug bool) (..., *strategy.DebugSnapshot) {
    ...
    var dbg *strategy.DebugSnapshot  // 零值是 nil，不分配任何东西
    if wantDebug {
        dbg = buildDebugSnapshot(signal, micro.TargetWeight, marketState)
    }
    return ..., dbg
}
```

注意 `var dbg *strategy.DebugSnapshot` 这一行——声明一个指针但不初始化，Go 的零值是
`nil`，**不会分配任何堆内存**。只有 `wantDebug=true` 时才真正分配。

调用方：

```go
// evaluate_window.go（backtest 热路径）
stepCoreFromIndicators(..., false)   // 0 allocs

// step.go（live trading）
stepCoreFromIndicators(..., true)    // 保持原有行为
```

---

## 7. 实测对比

跑基准测试（`go test -bench=... -benchmem`）：

```
# 修改前
BenchmarkEvaluateWindow_87kBars   20 764 000 ns/op   11 899 032 B/op   268 901 allocs/op

# 修改后
BenchmarkEvaluateWindow_87kBars   17 789 000 ns/op    6 992 625 B/op     6 099 allocs/op
```

| 指标 | 修改前 | 修改后 | 变化 |
|---|---|---|---|
| allocs/op | 268,901 | 6,099 | **−97.7%** |
| 内存/op | 11.9 MB | 6.9 MB | −42% |
| 时间/op | 20.8ms | 17.8ms | −14% |

6,099 次 allocs 剩下的是什么？主要是偶发的 macro/micro order slices——策略真正需要发
单时才分配，频率远低于每 bar 一次。

---

## 关键心智模型

**Python/Java 程序员**转到 Go 最容易忽略的一点：Go 有 GC 不代表分配是"免费"的。GC
需要时间扫描存活对象，短命对象（分配后马上没人引用）尤其浪费——malloc 进来、GC 扫
到、标记为垃圾、回收，一套流程走完却什么用都没有。

热路径上的原则：**能在栈上分配的，绝不上堆。** 不取地址、不返回指针、不把局部变量
塞进接口——这三条能避开绝大多数意外逃逸。遇到性能问题先跑
`go build -gcflags='-m'` 和 `-benchmem`，让数据说话。
