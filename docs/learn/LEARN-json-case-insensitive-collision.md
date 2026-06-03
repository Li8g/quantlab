---
# Go JSON 大小写不敏感:一个潜伏的字段碰撞 bug

> 一句话:**`encoding/json` 的字段匹配是大小写不敏感的。当一个 JSON key 找不到
> 精确匹配的字段时,它会退而塞进同名异写的字段——数字撞进字符串就报错,同型则
> 静默覆盖。对 Binance 这种用大小写区分字段语义的 API,这是一类成片的隐患。**

本文用本项目 agent 端真实发生(并潜伏许久)的一个 bug 讲清:为什么"我只声明我关心
的字段、其余自动忽略"这个直觉在 Go 里是错的;以及如何系统性地排查同类碰撞。

读完你能判断:什么时候一个看似无害的 struct tag 会悄悄吃掉相邻字段的值。

---

## 1. 现象:一条解不开的事件

迁移到 Binance WS API、第一次接上真实 user data stream 后,executionReport 解码报:

    json: cannot unmarshal number into Go struct field rawExecutionReport.e of type string

`e` 字段我们声明的是 `string`(事件类型 `"executionReport"`),怎么会有人往里塞数字?

---

## 2. 根因:`encoding/json` 的两段式匹配

Binance 的事件用**大小写区分两个不同字段**:

    {
      "e": "executionReport",   // 小写 e = event type,字符串
      "E": 1718000000123,        // 大写 E = event time,数字(毫秒)
      ...
    }

而我们最初的 struct 只声明了小写 `e`(时间用不上,没写):

    var head struct {
        EventType string `json:"e"`
    }

直觉:"我只要 `e`,JSON 里多余的 `E` 会被忽略。" **错。**

`encoding/json` 给每个 JSON key 找目标字段的算法是**两段式**的(见标准库 `decode.go`):

1. 先找 tag/字段名**完全精确**匹配的;
2. 找不到精确的,再做一次**大小写不敏感(equalFold)**的匹配。

文档原话:*"Unmarshal ... prefers an exact match but also accepts a
case-insensitive match."*

于是 `"E": 1718...` 来找家:

| 步骤 | 结果 |
|---|---|
| 精确匹配 tag `E` 的字段? | 没有(我们只声明了 `e`) |
| 大小写不敏感匹配? | `E` ≈ `e` ✅ 命中 `EventType` |

`E` 无家可归,就鸠占鹊巢挤进 `e` 的位置——而它带的是数字,塞进字符串字段 → 报错。

---

## 3. 为什么潜伏这么久没被发现

这是最有教学价值的一点:**测试 mock 帧恰好不带 `E`**。

手写的测试 JSON 只写了要断言的字段(`e`、`s`、`x`…),偷懒省掉了 `E`。没有 `E`,
就没有那个无家可归的数字,`e` 安静地匹配 `EventType`,测试全绿。

但**真实 Binance 每个事件都带 `E`**。所以这个 bug 从写下那天起就存在,只是:

- 单测覆盖不到(mock 数据比真实数据"干净");
- 之前从没真正连过 testnet(卡在已下线的 listenKey 上,根本走不到这段解码)。

直到 listenKey 被下线、改走 WS API、第一次收到真事件,它才爆出来。

> **教训一:mock 数据要忠实于真实 payload 的形状,尤其是"你不关心的字段"。
> 你省略的,往往正是会致命的那个。**

---

## 4. 修复原理:给碰撞的 key 一个"家"

修复不是去关掉大小写不敏感匹配(关不掉,标准库无此开关),而是**消除歧义**——
显式声明一个 tag 精确等于 `E` 的字段:

    var head struct {
        EventType string `json:"e"`
        EventTime int64  `json:"E"`   // 仅仅为了给 E 一个归宿,本身不用
    }

现在 `"E"` 精确命中 `EventTime`(类型也对),不再去抢 `e`。这个字段甚至从不被读取,
唯一作用就是"占位",把那个数字从 `e` 身边引走。

---

## 5. 体检:这绝不止一处——同一 struct 里还有 5 个同类碰撞

`E` 的修复只是堵了**六个洞里的一个**。Binance 的 executionReport 大量使用大小写
成对的字段。我们用官方文档的**完整**帧跑了一次解码,逐个暴露:

| 未声明的真实 key | 撞进的已声明字段 | 类型 | 后果 |
|---|---|---|---|
| `O` 订单创建时间(数字) | `o` OrderType(字符串) | 数字→字符串 | **硬报错**,整帧解不开 |
| `C` 原始客户端单号(="") | `c` ClientOrderID(字符串) | 同型,静默覆盖 | ClientOrderID 被清空 → "missing client_order_id" → **每笔成交被丢弃** |
| `I` execution id(数字) | `i` OrderID(数字) | 同型,静默覆盖 | ExchangeOrderID 变成 execution id,**对不上单** |
| `t` trade id(数字) | `T` TransactTime(数字) | 同型,静默覆盖 | 成交时间戳变成 trade id(如 12345) |
| `Z` 累计成交**计价**量 | `z` 累计成交**基础**量 | 同型,静默覆盖 | 累计成交量单位错(报计价额而非币量) |

关键机制(同一条规则的不同表现):

- **数字撞字符串 → 硬报错**(只有 `O`/`o` 这一对)。响亮,但能挡掉所有真实帧。
- **同型相撞 → 静默覆盖**(`C`/`I`/`t`/`Z`)。无报错、无日志,值被悄悄改写。
  覆盖方向:JSON 里**后出现**的 key 赢。Binance 帧里 `C` 在 `c` 之后、`I` 在 `i`
  之后、`t` 在 `T` 之后、`Z` 在 `z` 之后——所以全是"错误值覆盖正确值"。

静默的那几个比硬报错**更危险**:程序不会崩,只是悄悄上报错误的订单号、时间和成交量。

> **教训二:大小写不敏感匹配下,只声明一对中的一个,等于给另一个发了通行证去抢位。
> 凡是 API 用大小写区分语义,要么两个都声明,要么都别声明(都别声明则两个都被忽略,
> 安全)。最危险的是"只声明一个"。**

修复模式与 `E` 完全一致:给每个会撞的孪生 key 一个专属字段(`C`/`O`/`I`/`t`/`Z`),
哪怕用不上。

---

## 6. 可迁移的排查清单

下次接入任何大小写敏感的外部 JSON(交易所、IoT、某些金融报文),按这个清单过一遍:

1. **列出 API 实际会发的全部 key**(查官方文档的完整 payload,不是你记忆里的子集)。
2. **找出大小写成对的 key**(`e/E`、`s/S`、`c/C`…)。
3. 对每一对,检查 struct:**是否只声明了其中一个?** 只声明一个 = 隐患。
   - 另一个是数字、本字段是字符串 → 将来硬报错。
   - 两个同型 → 将来静默覆盖(且后出现者赢)。
4. **用一份真实的完整帧写回归测试**,而不是手搓最小集。本项目的
   `TestDecodeExecutionReport_WithEventTime` 当初就因为帧不完整,只堵住了 `E`,
   放过了另外五个。
5. 记住:**"从没真正跑通过的代码路径 = 未测试的代码"**,即使单测全绿。集成层
   首次打通时,这类潜伏问题会集中爆发。

---

## 附:相关文件

- `internal/agent/binance/uds_stream.go` — `rawExecutionReport` 结构与 `decodeExecutionReport`
- `internal/agent/binance/uds_stream_test.go` — 回归测试(`TestDecodeExecutionReport_*`)
- 修复提交 `8f01d32` — listenKey → WS API 迁移,顺带修了 `E`(本文第 2-4 节)
