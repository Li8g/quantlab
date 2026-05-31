# Go struct 冻结版定义草案 v3

> **v3 升级说明(对应 coding plan v3.2 / schema v5.3.3)**
>
> | 修订 | 内容 |
> |---|---|
> | P01 | 新增 `RawEvaluateResult`(策略侧纯评估结果,物理上不含 `ScoreTotal`);原 `EvaluationResult` 重命名为 `EvaluationLayer`(引擎侧,含 `ScoreTotal`) |
> | P02 | `DecisionStatus` 枚举 `approved` → `promoted`,对齐状态机真实终态 |
> | P03 | `GAConfigSnapshot` 摩擦字段语义改为生效值(非请求镜像);新增 `EvolutionTask.RequestedTakerFeeBPS` / `RequestedSlippageBPS` |
> | P04 | `Bar` struct 注释加固:明确 `IsGap`/`GapType` 不参与 `bars_hash` |
> | P05 | `schema_version` 版本常量升级 `v5.3.2` → `v5.3.3` |

**用途：** 本文档将已冻结的边界对象整理为可直接落地到 `api/types.go` 与 `resultpkg/types.go` 的 Go struct 草案，用于固定字段名、类型、JSON tag、枚举常量与最小语义约束。

**范围：** 覆盖核心边界对象：`CreateEvolutionTaskRequest`、`EvolutionTaskStatusResponse`、`SliceScore`、`CrucibleResult`、`ScoreTotal`、`PromoteLayer`、`ReproducibilityMetadata`、`ChallengerResultPackage`、**`RawEvaluateResult`(v3 新增)**、**`EvaluationLayer`(v3 重命名)**。

**冻结原则：** 本文档中的字段名、JSON tag、required / nullable 语义、最小枚举集合视为 v3 基线；如需破坏性修改，应通过 `schema_version` 升级。

---

## 1. 包划分建议

建议按边界职责拆分：

- `api/types.go`
  - `CreateEvolutionTaskRequest`
  - `EvolutionTaskStatusResponse`
  - `PromoteChallengerRequest`
  - `RetireChampionRequest`

- `resultpkg/types.go`
  - `SliceScore`
  - `CrucibleResult`
  - `ScoreTotal`
  - `ResultCore`
  - `RawEvaluateResult`  ← v3 P01 新增（策略侧纯评估产出）
  - `EvaluationLayer`    ← v3 P01 重命名（原 EvaluationResult，引擎侧含 ScoreTotal）
  - `VerificationLayer`
  - `DiagnosticsLayer`
  - `PromoteLayer`
  - `ReproducibilityMetadata`
  - `GAConfigSnapshot`
  - `ChallengerResultPackage`

- `resultpkg/enums.go`
  - 所有结果包与 API 共享枚举常量

这样可以保证 API 请求响应对象与结果包对象分层清晰，同时又允许共享少量公共枚举。[code_file:0]

---

## 2. 枚举冻结版

### 2.1 类型与常量

```go
package resultpkg

type TaskStatus string

type DecisionStatus string

type VerificationStatus string

type DecisionColor string

type WindowName string

type SkippedBy string

// SpawnMode 冻结为三态枚举（v5.3.2 修订，不再是自由字符串）
type SpawnMode string

const (
    TaskStatusQueued    TaskStatus = "queued"
    TaskStatusRunning   TaskStatus = "running"
    TaskStatusSucceeded TaskStatus = "succeeded"
    TaskStatusFailed    TaskStatus = "failed"
    TaskStatusCancelled TaskStatus = "cancelled"
)

const (
    // Challenger 结果包中的决策状态，仅三态。
    // v3 修订(P02)：approved → promoted，对齐状态机真实终态语义：
    //   pending  → 等待人工审批
    //   promoted → 已审批通过并晋升为 Champion（同步写入 champion_history.promoted_at）
    //   rejected → 已否决
    // Champion 的退役状态（retired）在 champion_history 表独立管理，不属于此枚举。
    DecisionStatusPending  DecisionStatus = "pending"
    DecisionStatusPromoted DecisionStatus = "promoted"  // v3 P02: 原 approved 重命名
    DecisionStatusRejected DecisionStatus = "rejected"
)

const (
    VerificationStatusNotRun           VerificationStatus = "not_run"
    VerificationStatusOK               VerificationStatus = "ok"
    VerificationStatusFailed           VerificationStatus = "failed"
    VerificationStatusInsufficientData VerificationStatus = "insufficient_data"
)

const (
    DecisionColorGreen  DecisionColor = "green"
    DecisionColorYellow DecisionColor = "yellow"
    DecisionColorRed    DecisionColor = "red"
    DecisionColorGray   DecisionColor = "gray"
)

const (
    Window6M  WindowName = "6m"
    Window2Y  WindowName = "2y"
    Window5Y  WindowName = "5y"
    Window10Y WindowName = "10y"
)

const (
    SkippedByCascadeFrom6M SkippedBy = "cascaded_from_6m"
    SkippedByCascadeFrom2Y SkippedBy = "cascaded_from_2y"
    SkippedByCascadeFrom5Y SkippedBy = "cascaded_from_5y"
)

// SpawnMode 冻结枚举：引擎根据此值决定 SpawnPoint 的注入方式
const (
    SpawnModeInherit     SpawnMode = "inherit"     // 继承当前 champion 的 SpawnPoint
    SpawnModeRandomOnce  SpawnMode = "random_once" // 任务排队时随机生成并冻结
    SpawnModeManual      SpawnMode = "manual"      // 由请求体中的 spawn_point 字段指定
)
```

### 2.2 仍有疑问的枚举

以下枚举暂不冻结为常量，只保留字符串字段，待策略 / 任务治理规则进一步明确后再升级：

- `[QUESTION]` `fatal_reason`：触发 Fatal 的原因分类，暂用自由字符串
- `[QUESTION]` `slice_score.reason`：SliceScore 的附加说明，暂用自由字符串

> **已在本版（v1 修订版）解决的问题：**
> - `spawn_mode` 已冻结为 `SpawnMode` 枚举（`inherit / random_once / manual`），见 §2.1。
> - `test_mode` 已收紧为 `bool` 类型，见 §3.1 / §4.6。

---

## 3. api/types.go 草案

### 3.1 CreateEvolutionTaskRequest

```go
package api

import "your/module/resultpkg"

// CreateEvolutionTaskRequest 对应 POST /api/v1/evolution/tasks 的请求体。
//
// v1 修订说明：
//   - 删除原 OOSConfig 嵌套对象（其 Enabled *bool 三态逻辑冗余，
//     OOSWindow 字段存在非法 JSON tag "start_ts/end_ts"）。
//   - 改为顶层 OosDays *int：nil = 不启用 OOS；> 0 = 启用 Anchored Holdout，
//     天数从最新 bar 往前推，Epoch 创建时冻结，不随后续 Epoch 滚动。
//   - SpawnMode 改为 resultpkg.SpawnMode 枚举，消除自由字符串风险。
//   - TestMode 改为 bool，消除 "True" vs "true" 等前端传参错误。
type CreateEvolutionTaskRequest struct {
    StrategyID           string            `json:"strategy_id"`
    Pair                 string            `json:"pair"`
    PopSize              int               `json:"pop_size"`
    MaxGenerations       int               `json:"max_generations"`
    EliteRatio           float64           `json:"elite_ratio"`
    FatalMDD             float64           `json:"fatal_mdd"`
    TakerFeeBPS          float64           `json:"taker_fee_bps"`
    SlippageBPS          float64           `json:"slippage_bps"`
    SpawnMode            resultpkg.SpawnMode `json:"spawn_mode"`
    TestMode             bool              `json:"test_mode"`
    OosDays              *int              `json:"oos_days,omitempty"`
    FatalAuditSampleRate *float64          `json:"fatal_audit_sample_rate,omitempty"`
    SpawnPoint           *json.RawMessage  `json:"spawn_point,omitempty"` // 仅 SpawnMode=manual 时使用
}
```

**冻结语义：**

- 必填字段使用非指针类型（或 value type）表达必填约束。
- `OosDays`、`FatalAuditSampleRate`、`SpawnPoint` 使用指针表达可空 / 可省略。
- `SpawnMode` 枚举值：`inherit` / `random_once` / `manual`（见 `resultpkg.SpawnMode`）。
- `TestMode=true` 时引擎强制将 `TakerFeeBPS=0, SlippageBPS=0` 写入 `GAConfigSnapshot`；用户原始请求值记入 `EvolutionTask.RequestedTakerFeeBPS/RequestedSlippageBPS`（见 §4.6 P03）；test_mode 产物不可 Promote。

**仍有疑问：**

- `[QUESTION]` `Pair` 是否需要专门类型 `TradingPair`。
- `[QUESTION]` `OosDays` 是否应该限制最小值为某个工程常量（如 30）。

### 3.2 EvolutionTaskStatusResponse

```go
package api

import "your/module/resultpkg"

type EvolutionTaskStatusResponse struct {
    TaskID            string                `json:"task_id"`
    Status            resultpkg.TaskStatus  `json:"status"`
    CurrentGeneration int                   `json:"current_generation"`
    BestScore         *float64              `json:"best_score,omitempty"`
    ChallengerID      *string               `json:"challenger_id,omitempty"`
    FailureReason     *string               `json:"failure_reason,omitempty"`
}
```

**仍有疑问：**

- `[QUESTION]` 是否要增加 `StartedAtTS`、`FinishedAtTS`、`ProgressPct`。  

### 3.3 Promote / Retire 请求

虽然不在你点名的 8 个对象里，但既然马上会写 `api/types.go`，建议一并固定。[code_file:0]

```go
package api

type PromoteChallengerRequest struct {
    ReviewedBy   string  `json:"reviewed_by"`
    DecisionNote *string `json:"decision_note,omitempty"`
}

type RetireChampionRequest struct {
    ReviewedBy   string  `json:"reviewed_by"`
    DecisionNote *string `json:"decision_note,omitempty"`
}
```

---

## 4. resultpkg/types.go 草案

### 4.1 SliceScore

```go
package resultpkg

// SliceScore 是单个评估窗口的得分，采用 Sum-type 语义，三态互斥：
//
//   (1) 正常评估完成：Fatal=false, Value!=nil, CrucibleResult.SkippedBy=nil
//   (2) 被级联短路跳过：Fatal=false, Value=nil, CrucibleResult.SkippedBy!=nil
//   (3) 自身触发 Fatal：Fatal=true, Value=nil, CrucibleResult.SkippedBy=nil
//
// ⚠️  Fatal 排序 Panic 风险：Fatal=true 时 Value 为 nil；被跳过窗口 Value 也为 nil。
// 引擎排序时禁止直接解引用 *Value，必须通过封装的 CompareFitness 函数。
// 禁止向 Value 写入 -99999 / -1e18 等哨兵数值。
type SliceScore struct {
    Fatal  bool     `json:"fatal"`
    Value  *float64 `json:"value,omitempty"`
    Reason *string  `json:"reason,omitempty"`
}
```

**冻结语义：**

- `Fatal=false && SkippedBy==nil`（在 CrucibleResult 上下文）时，`Value` 必须非 nil。
- `Fatal=true` 时，`Value` 建议为 nil，避免与正常排序语义混淆。
- 引擎必须封装 `CompareFitness(a, b ScoreTotal) int` 函数处理 nil Value，见框架规划 §2.6。

**仍有疑问：**

- `[QUESTION]` `Reason` 是否最终收敛为枚举（`mdd_exceeded` 等）。

### 4.2 CrucibleScoreComponents

```go
package resultpkg

type CrucibleScoreComponents struct {
    MonthlyScore    *float64 `json:"monthly_score,omitempty"`
    WeeklyScore     *float64 `json:"weekly_score,omitempty"`
    BaseScore       *float64 `json:"base_score,omitempty"`
    TurnoverPenalty *float64 `json:"turnover_penalty,omitempty"`
}
```

### 4.3 CrucibleResult

```go
package resultpkg

type CrucibleResult struct {
    Window         WindowName               `json:"window"`
    Score          SliceScore               `json:"score"`
    FatalReason    *string                  `json:"fatal_reason,omitempty"`
    FatalAtBarTS   *int64                   `json:"fatal_at_bar_ts,omitempty"`
    FatalMDDValue  *float64                 `json:"fatal_mdd_value,omitempty"`
    BarsEvaluated  int                      `json:"bars_evaluated"`
    SkippedBy      *SkippedBy               `json:"skipped_by,omitempty"`
    Components     *CrucibleScoreComponents `json:"components,omitempty"`
}
```

**冻结语义：**

- `Window` 暂冻结为四窗口枚举：`6m`、`2y`、`5y`、`10y`。[file:131][code_file:0]
- `SkippedBy` 仅用于级联短路场景。[file:131][code_file:0]

**仍有疑问：**

- `[QUESTION]` 被短路跳过的窗口，`Score.Fatal` 应该设成什么值，目前建议 `false + nil value`，但需团队统一。  
- `[QUESTION]` `FatalReason` 是否与 `Score.Reason` 合并。  

### 4.4 ScoreTotal

```go
package resultpkg

type ScoreTotal struct {
    Fatal              bool     `json:"fatal"`
    Value              *float64 `json:"value,omitempty"`
    Reason             *string  `json:"reason,omitempty"`
    ScoreRaw           *float64 `json:"score_raw,omitempty"`
    ConsistencyPenalty *float64 `json:"consistency_penalty,omitempty"`
}
```

**仍有疑问：**

- `[QUESTION]` `ScoreRaw` 的精确定义要不要固定为“一致性惩罚前总分”。  

### 4.5 ReproducibilityMetadata

```go
package resultpkg

type ReproducibilityMetadata struct {
    EpochSeed          int64  `json:"epoch_seed"`
    DataVersion        string `json:"data_version"`
    EngineVersion      string `json:"engine_version"`
    StrategyVersion    string `json:"strategy_version"`
    SchemaVersion      string `json:"schema_version"`
    FitnessVersion     string `json:"fitness_version"`
    FingerprintVersion string `json:"fingerprint_version"`

    // HardwareSignature 格式建议："{GOOS}/{GOARCH}/{CPU型号}"
    // 例如 "linux/amd64/Intel-Xeon-E5-2680v4"
    // 用于标记跨硬件可复现性边界，不作为强约束条件。
    HardwareSignature  string `json:"hardware_signature"`
    GoVersion          string `json:"go_version"`
    BuildID            string `json:"build_id"`

    // PlanHash：对 EvaluablePlan 序列化后的 canonical JSON 计算 SHA256，
    // 结果以小写 Hex 编码存储（64 字符）。
    // 用于 replay 时校验评估上下文是否与原始 Epoch 完全一致。
    PlanHash           string `json:"plan_hash"`

    // BarsHash：对本 Epoch 使用的全量 K 线序列（含 warmup）计算 SHA256，
    // 结果以小写 Hex 编码存储（64 字符）。
    //
    // v3 P04 固化序列化范围：
    //   仅包含 Bar 的价格数据字段：OpenTime, Open, High, Low, Close, Volume（完整 OHLCV）。
    //   Bar.IsGap / Bar.GapType 是元数据字段，不参与 bars_hash。
    //   这保证缺口检测算法升级不影响 bars_hash 稳定性。
    //   该约定写入 internal/quant/canonical_json.go 文件顶部注释。
    BarsHash           string `json:"bars_hash"`
}
```

**冻结常量建议：**

```go
package resultpkg

const (
    // v3 P05: schema_version 升级到 v5.3.3
    // (对应 P01 EvaluationLayer 拆分 + P02 promoted 重命名 + P03 GAConfigSnapshot 生效值)
    SchemaVersionV533      = "v5.3.3"
    // SchemaVersionV532   = "v5.3.2"   // 已废弃，仅用于历史结果包反序列化兼容

    FitnessVersionV1RawStd = "v1-raw-std"
    FitnessVersionV2ZScore = "v2-zscore"
    FingerprintVersionV1   = "fp-v1"

    CurrentSchemaVersion = SchemaVersionV533
)
```

**已解决（v1 修订版）：**

- `PlanHash` / `BarsHash`：原型阶段统一采用 `SHA256(canonical_json(...))` 计算，小写 Hex 编码，见上方字段注释。
- `HardwareSignature`：建议格式 `{GOOS}/{GOARCH}/{CPU型号}`，见上方字段注释。

**仍有疑问：**

- **已在 v3 P04 解决：** `BarsHash` 序列化范围固化为完整 OHLCV + OpenTime，排除元数据字段，见 §4.5 字段注释。

### 4.6 GAConfigSnapshot

由于 `core` 层要求结果包写入任务配置快照，建议直接复用任务创建字段形成结果包镜像对象。

> **v1 修订说明：**
> - 删除原 `OOSConfigSnapshot` 结构体。该结构体存在两个问题：(1) `Enabled *bool` 引入三态逻辑；(2) `OOSWindow *int` 的 JSON tag `"start_ts/end_ts,omitempty"` 包含非法字符 `/`，Go `encoding/json` 会静默忽略此 tag，导致序列化时字段名使用 Go 默认名 `OOSWindow` 而非预期名称。
> - 改为顶层 `OosDays *int`（与 `CreateEvolutionTaskRequest` 保持一致）。
> - 新增 `Pair string`（脱离交易对的参数快照无法复现 EvaluablePlan）。
> - `SpawnMode` 改为 `SpawnMode` 枚举类型；`TestMode` 改为 `bool`。

```go
package resultpkg

// GAConfigSnapshot 是任务创建时固化的参数快照，写入结果包 core 层，
// 供 replay 与审计使用。
//
// v3 P03 修订：摩擦字段语义从"请求镜像"改为"生效值"。
//   - TestMode=true 时，TakerFeeBPS/SlippageBPS 必须为 0（引擎写入前覆写）。
//   - TestMode=false 时，TakerFeeBPS/SlippageBPS 为用户请求值。
//   - 用户原始请求意图（如 test_mode=true 但请求 taker_fee_bps=10）
//     由 EvolutionTask 表的 RequestedTakerFeeBPS / RequestedSlippageBPS 字段记录，
//     不进入结果包，避免快照内出现"test_mode=true 但 taker_fee_bps=10"的自相矛盾。
type GAConfigSnapshot struct {
    StrategyID           string    `json:"strategy_id"`
    Pair                 string    `json:"pair"`
    PopSize              int       `json:"pop_size"`
    MaxGenerations       int       `json:"max_generations"`
    EliteRatio           float64   `json:"elite_ratio"`
    FatalMDD             float64   `json:"fatal_mdd"`
    TakerFeeBPS          float64   `json:"taker_fee_bps"`          // 生效值：TestMode=true 时为 0
    SlippageBPS          float64   `json:"slippage_bps"`           // 生效值：TestMode=true 时为 0
    SpawnMode            SpawnMode `json:"spawn_mode"`
    TestMode             bool      `json:"test_mode"`              // 任务请求标志，用于 Promote 拒绝判断
    OosDays              *int      `json:"oos_days,omitempty"`
    FatalAuditSampleRate *float64  `json:"fatal_audit_sample_rate,omitempty"`
}
```

> **v3 新增：EvolutionTask 表字段（对应 P03）**
>
> 数据库 `evolution_tasks` 表新增两列，记录用户原始请求意图（审计用，不进入结果包）：
>
> ```go
> // internal/saas/store/models.go 中的 EvolutionTask GORM 模型追加：
> RequestedTakerFeeBPS int `gorm:"column:requested_taker_fee_bps"`
> RequestedSlippageBPS int `gorm:"column:requested_slippage_bps"`
> ```

### 4.7 ChampionGenePayload

`champion_gene` 当前 schema 仍是宽松对象，为避免太早绑定策略内部编码，建议冻结一个"外壳结构"，把真实载荷放到 `json.RawMessage`。

```go
package resultpkg

import "encoding/json"

// ChampionGenePayload 包裹策略私有的基因编码。
// 原型阶段 Encoding 必须为 "json"（即 GeneEncodingJSON）。
// 反序列化时由策略层的 DecodeElite 负责解析 Payload，引擎层不得读取内部字段。
type ChampionGenePayload struct {
    // Encoding 原型阶段仅允许 "json"，如未来支持其他编码需升级 fingerprint_version。
    Encoding string          `json:"encoding"`
    Payload  json.RawMessage `json:"payload"`
}

// GeneEncodingJSON 原型阶段唯一合法的基因编码格式
const GeneEncodingJSON = "json"
```

**冻结语义：**

- 原型阶段 `Encoding` 只能是 `GeneEncodingJSON`（即 `"json"`），落库前须做校验。
- `Payload` 的内部结构由策略文档定义，引擎层通过 `DecodeElite(blob)` 透传，不解析。

**仍有疑问：**

- `[QUESTION]` `Payload` 将来是否统一成数组编码或 base64 二进制表示。

### 4.8 SpawnPointPayload

`spawn_point` 当前也保持边界稳定但内部宽松，适合用最小固定外壳。[code_file:0]

```go
package resultpkg

import "encoding/json"

type SpawnPointPayload struct {
    SpawnMode     SpawnMode        `json:"spawn_mode"`
    CapitalPolicy *string          `json:"capital_policy,omitempty"`
    RiskBounds    json.RawMessage  `json:"risk_bounds,omitempty"`
    Meta          json.RawMessage  `json:"meta,omitempty"`
}
```

**仍有疑问：**

- `[QUESTION]` `RiskBounds` 是否要升级为明确结构体。  
- `[QUESTION]` `Meta` 是否应该拆掉，避免变成兜底垃圾桶字段。  

### 4.9 ResultCore

```go
package resultpkg

type ResultCore struct {
    StrategyID              string                  `json:"strategy_id"`
    ChampionGene            ChampionGenePayload     `json:"champion_gene"`
    SpawnPoint              SpawnPointPayload       `json:"spawn_point"`
    ReproducibilityMetadata ReproducibilityMetadata `json:"reproducibility_metadata"`
    GAConfig                GAConfigSnapshot        `json:"ga_config"`
    SchemaVersion           string                  `json:"schema_version"`
    FitnessVersion          string                  `json:"fitness_version"`
    FingerprintVersion      string                  `json:"fingerprint_version"`
}
```

### 4.10 RawEvaluateResult 与 EvaluationLayer（v3 P01 重构）

> **v3 关键修订（P01）：** 原 `EvaluationResult` 拆分为两个 struct，按"评估 vs 聚合"两阶段分离：
>
> - `RawEvaluateResult`：策略层 / Adapter 产出，**物理上不含 `ScoreTotal`**，通过类型系统强保证策略不能写入聚合值。
> - `EvaluationLayer`：引擎层组装产出，含 `ScoreTotal`，直接对应结果包 `evaluation` 层的 JSON 结构。
>
> **职责契约：**
>
> | 阶段 | 谁负责 | 产出类型 |
> |---|---|---|
> | 单 gene 单窗口评估 | `Adapter.Evaluate(gene)` | `*RawEvaluateResult` |
> | 级联短路 + 多窗口组合 | `EvolvableStrategy.Evaluate(ctx, gene, plan)` | `*RawEvaluateResult` |
> | ScoreTotal 聚合 | `fitness.AggregateScoreTotal(...)` (引擎共享函数) | `ScoreTotal` |
> | 结果包 evaluation 层组装 | 引擎 `RunEpoch` | `EvaluationLayer` |

```go
package resultpkg

import "encoding/json"

// FrictionActual 记录评估时实际生效的摩擦参数
type FrictionActual struct {
    TakerFeeBPS float64  `json:"taker_fee_bps"`
    SlippageBPS float64  `json:"slippage_bps"`
    MakerFeeBPS *float64 `json:"maker_fee_bps,omitempty"`
    SpreadBPS   *float64 `json:"spread_bps,omitempty"`
}

type GapStats struct {
    TotalGapMinutes   *float64 `json:"total_gap_minutes,omitempty"`
    LongestGapMinutes *float64 `json:"longest_gap_minutes,omitempty"`
    GapCount          *int     `json:"gap_count,omitempty"`
}

// RawEvaluateResult 是策略层 / Adapter 的评估产出（v3 P01 新增）。
//
// ⚠️  此 struct 物理上不含 ScoreTotal 字段。
// 策略层和 Adapter 返回此类型，保证它们无法写入聚合值。
// ScoreTotal 由引擎调用 fitness.AggregateScoreTotal 后填入 EvaluationLayer。
type RawEvaluateResult struct {
    Windows        []CrucibleResult `json:"windows"`
    FrictionActual FrictionActual   `json:"friction_actual"`
    BarsEvaluated  int              `json:"bars_evaluated"`
}

// EvaluationLayer 是引擎组装的结果包 evaluation 层（v3 P01：原 EvaluationResult 重命名）。
// 由引擎在 RunEpoch 中基于 RawEvaluateResult + fitness.AggregateScoreTotal 组装。
// 不允许策略层直接构造此类型。
type EvaluationLayer struct {
    WindowScores          []CrucibleResult `json:"window_scores"`
    ScoreTotal            ScoreTotal       `json:"score_total"`       // 引擎填充，策略层不可写
    AlphaBreakdown        json.RawMessage  `json:"alpha_breakdown,omitempty"`
    FrictionActual        FrictionActual   `json:"friction_actual"`
    GapsEncounteredInEval *GapStats        `json:"gaps_encountered_in_eval,omitempty"`
}
```

**Validate() 建议：**

```go
func (r *RawEvaluateResult) Validate() error {
    if r == nil { return errors.New("RawEvaluateResult is nil") }
    if r.Windows == nil { return errors.New("Windows is nil") }
    for _, w := range r.Windows {
        if err := w.Validate(); err != nil { return err }
    }
    return nil
    // 注意：不校验 ScoreTotal（该 struct 无此字段）
}
```

**仍有疑问：**

- `[QUESTION]` `AlphaBreakdown` 是否后续需要明确定义成结构体（二期）。

### 4.11 OOSResult / ReviewSummary / VerificationLayer

```go
package resultpkg

import "encoding/json"

type OOSResult struct {
    Status          VerificationStatus `json:"status"`
    OOSAlphaMonthly *float64           `json:"oos_alpha_monthly,omitempty"`
    OOSAlphaWeekly  *float64           `json:"oos_alpha_weekly,omitempty"`
    DecisionColor   *DecisionColor     `json:"decision_color,omitempty"`
    Notes           *string            `json:"notes,omitempty"`
}

type ReviewSummary struct {
    Status    VerificationStatus `json:"status"`
    Notes     *string            `json:"notes,omitempty"`
    DataScope *string            `json:"data_scope,omitempty"`
}

type VerificationLayer struct {
    OOSResult     OOSResult       `json:"oos_result"`
    ReviewSummary *ReviewSummary  `json:"review_summary,omitempty"`
    DSRSummary    json.RawMessage `json:"dsr_summary,omitempty"`
    StressSummary json.RawMessage `json:"stress_summary,omitempty"`
}
```

### 4.12 DiagnosticsLayer

```go
package resultpkg

import "encoding/json"

type AuditSampleSummary struct {
    SampleID     string           `json:"sample_id"`
    ScoreTotal   ScoreTotal       `json:"score_total"`
    WindowScores []CrucibleResult `json:"window_scores,omitempty"`
    Notes        *string          `json:"notes,omitempty"`
}

type DiagnosticsLayer struct {
    MutationRampLog    json.RawMessage      `json:"mutation_ramp_log,omitempty"`
    DiversityRescueLog json.RawMessage      `json:"diversity_rescue_log,omitempty"`
    ClampModifications json.RawMessage      `json:"clamp_modifications,omitempty"`
    CrossoverFallback  json.RawMessage      `json:"crossover_fallback,omitempty"`
    TurnoverMetrics    json.RawMessage      `json:"turnover_metrics,omitempty"`
    FatalAuditSamples  []AuditSampleSummary `json:"fatal_audit_samples,omitempty"`
}
```

### 4.13 PromoteLayer

```go
package resultpkg

// PromoteLayer 记录 Challenger 的人工审批结果。
// v3 P02 修订：DecisionStatus 枚举 approved → promoted。
// 合法值：pending（待审批）/ promoted（已晋升）/ rejected（已否决）。
// Champion 的退役状态在 champion_history 表独立管理，不在此字段体现。
type PromoteLayer struct {
    DecisionStatus DecisionStatus `json:"decision_status"`
    DecisionNote   *string        `json:"decision_note,omitempty"`
    ReviewedAtTS   *int64         `json:"reviewed_at_ts,omitempty"`
    ReviewedBy     *string        `json:"reviewed_by,omitempty"`
}
```

**冻结语义（v3 P02 修订）：**

- `DecisionStatus` 合法值：`DecisionStatusPending`（待审批）/ `DecisionStatusPromoted`（已晋升为 Champion）/ `DecisionStatusRejected`（不晋升）。
- `retired` 已从 `DecisionStatus` 枚举中删除（见 §2.1），Champion 退役通过独立接口 `POST /api/v1/champions/:id/retire` 处理。

### 4.14 ChallengerResultPackage

```go
package resultpkg

type ChallengerResultPackage struct {
    Core         ResultCore        `json:"core"`
    Evaluation   EvaluationLayer   `json:"evaluation"`   // v3 P01：原 EvaluationResult → EvaluationLayer
    Verification VerificationLayer `json:"verification"`
    Diagnostics  DiagnosticsLayer  `json:"diagnostics"`
    Promote      PromoteLayer      `json:"promote"`
}
```

---

## 5. 校验函数建议

除了 struct，本阶段建议顺手补最小 `Validate()` 逻辑，用于 API 契约测试与结果包落库前自检。[file:131][code_file:0]

### 5.1 CreateEvolutionTaskRequest.Validate()

至少校验：

- `StrategyID != ""`
- `Pair != ""`
- `PopSize >= 1`
- `MaxGenerations >= 1`
- `0 <= EliteRatio <= 1`
- `0 <= FatalMDD <= 1`
- `TakerFeeBPS >= 0`
- `SlippageBPS >= 0`
- `SpawnMode` 属于 `{SpawnModeInherit, SpawnModeRandomOnce, SpawnModeManual}` 枚举（不再是非空字符串）
- `SpawnMode == SpawnModeManual` 时，`SpawnPoint != nil`
- `FatalAuditSampleRate == nil || (0 <= *x <= 1)`
- `OosDays == nil || *OosDays >= 1`
- `TestMode` 是 `bool`，无需额外校验（编译期保证）

### 5.2 SliceScore.Validate()

`SliceScore` 自身只携带 `Fatal` 和 `Value`，不含 `SkippedBy`（后者在 `CrucibleResult` 上）。因此独立校验只能做如下两条：

- `Fatal == true` 时，建议 `Value == nil`（避免与正常分数混淆）
- `Fatal == false` 时，`Value` 的非空性**不在此处强制**——因为被短路跳过的窗口（`SkippedBy != nil`）也会 `Fatal=false, Value=nil`，这是合法状态。该组合约束见 §5.3。

> **修订说明（搭档条目 3）：** 原规则"Fatal == false → Value != nil"与级联短路的跳过语义冲突——被短路窗口 `Fatal=false` 但 `Value=nil`。修复后，完整的三态约束在 `CrucibleResult.Validate()` 中联合校验。

### 5.3 CrucibleResult.Validate()

至少校验：

- `Window` 属于四窗口枚举（`6m / 2y / 5y / 10y`）
- `BarsEvaluated >= 0`
- `SkippedBy != nil` 时，其值必须属于固定枚举（`cascaded_from_6m / 2y / 5y`）

**三态互斥约束（v1 修订，对应搭档条目 3）：**

```
// 状态 1：正常评估完成
Score.Fatal == false && SkippedBy == nil  →  Score.Value != nil

// 状态 2：被级联短路跳过
SkippedBy != nil  →  Score.Fatal == false && Score.Value == nil && BarsEvaluated == 0

// 状态 3：自身触发 Fatal
Score.Fatal == true  →  SkippedBy == nil && Score.Value == nil
```

这三种状态互斥，任意两个不能同时成立。校验逻辑建议写成：

```go
func (r *CrucibleResult) Validate() error {
    // 状态互斥：Fatal 和 SkippedBy 不能同时非零
    if r.Score.Fatal && r.SkippedBy != nil {
        return errors.New("Fatal=true 和 SkippedBy 不能同时成立")
    }
    // 状态 1：正常窗口必须有分数
    if !r.Score.Fatal && r.SkippedBy == nil && r.Score.Value == nil {
        return errors.New("正常评估窗口的 Score.Value 不能为 nil")
    }
    // 状态 2：跳过窗口不应有分数
    if r.SkippedBy != nil && r.Score.Value != nil {
        return errors.New("被短路跳过的窗口不应有 Score.Value")
    }
    // 状态 3：Fatal 窗口不应有分数
    if r.Score.Fatal && r.Score.Value != nil {
        return errors.New("Fatal 窗口的 Score.Value 应为 nil")
    }
    return nil
}
```

### 5.4 ChallengerResultPackage.Validate()

至少校验：

- 五大层非零值
- `Core.SchemaVersion == Core.ReproducibilityMetadata.SchemaVersion`
- `Core.FitnessVersion == Core.ReproducibilityMetadata.FitnessVersion`
- `Core.FingerprintVersion == Core.ReproducibilityMetadata.FingerprintVersion`

---

## 6. 最小落地顺序

建议编码顺序如下：[file:131][code_file:0]

1. `resultpkg/enums.go`
2. `api/types.go`
3. `resultpkg/types.go`
4. `api/validate.go`
5. `resultpkg/validate.go`
6. JSON round-trip 单元测试
7. API 契约测试

这样做的好处是：先冻结最底层字符串常量和 struct，再补校验，再做测试，避免一边写 handler 一边改对象定义。[code_file:0]

---

## 7. 当前仍待决的问题清单

以下问题暂不阻塞第一阶段编码，但必须保留标记，避免后面遗忘：

- `[QUESTION]` `pair` 是否收敛为标准格式或专用类型 `TradingPair`。
- `[QUESTION]` `slice_score.reason` / `fatal_reason` 是否最终枚举化（候选值：`mdd_exceeded`、`invalid_path`、`insufficient_bars`）。
- `[RESOLVED]` `score_raw` = `Σ weight·score`（一致性惩罚前加权总分，不归一化），原型期固定，见 `fitness/aggregate.go` `AggregateScoreTotal`（FitnessVersion `v1-raw-std`）。
- `[QUESTION]` `BarsHash` 序列化范围：仅含 `OpenTime+Close` 还是完整 `OHLCV` 字段，待实现层确认。
- `[QUESTION]` `champion_gene.payload` 将来是否改为统一数组编码或 base64 二进制。
- `[QUESTION]` `spawn_point.risk_bounds` 是否升级为明确结构体。
- `[QUESTION]` `alpha_breakdown`、`dsr_summary`、`stress_summary` 何时收紧为正式结构体（第二阶段）。

**已在本版（v1 修订版）解决的问题：**

| 原问题 | 解决方案 | 修改位置 |
|---|---|---|
| `spawn_mode` 的正式枚举值 | 冻结为 `SpawnMode` 枚举（`inherit/random_once/manual`） | §2.1、§3.1、§4.6 |
| `test_mode` 的正式类型 | 改为 `bool` | §3.1、§4.6 |
| 被短路跳过窗口的 `score` 语义 | 补充三态互斥约束，`SkippedBy != nil → Value=nil` 合法 | §4.1、§5.2、§5.3 |
| `plan_hash` / `bars_hash` 的计算算法 | SHA256(canonical JSON)，小写 Hex 编码 | §4.5 |
| `hardware_signature` 的标准格式 | `{GOOS}/{GOARCH}/{CPU型号}` | §4.5 |
| `DecisionStatusRetired` 语义越界 | 从枚举中删除，Champion 退役独立管理 | §2.1、§4.13 |
| `OOSConfig` 三态逻辑 + 非法 JSON tag | 删除 `OOSConfigSnapshot`，改为顶层 `OosDays *int` | §3.1、§4.6 |
| `champion_gene.encoding` 缺乏约束 | 补充 `GeneEncodingJSON` 常量，原型阶段仅 `"json"` | §4.7 |
