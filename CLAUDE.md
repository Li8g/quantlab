# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Behavioral Guidelines

Behavioral guidelines to reduce common LLM coding mistakes. Merge with project-specific instructions as needed.

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

### 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

### 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

### 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

### 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

---

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to overcomplication, and clarifying questions come before implementation rather than after mistakes.

---

## Project Overview

**QuantLab** is a Go-only server-side evolutionary trading system ("进化系统"). It uses a Genetic Algorithm (GA) to optimize quantitative trading strategy parameters (chromosome/gene), then evaluates challengers across four time windows and supports human-gated Promote workflows. The current phase is "原型迭代期" (prototype iteration).

Primary reference documents (in `docs/`):
- `进化计算引擎.md` (H1: 进化系统_程序框架规划_v5.4.1) — system architecture, module duties, interface contracts (source of truth for design)
- `进化计算引擎_数据契约.md` (H1: 进化系统 v5.3.3 Go-only JSON Schema) — frozen JSON schema v5.3.3 for all external boundaries
- `进化计算引擎_Go_struct_草案.md` (H1: Go struct 冻结版定义草案 v3) — frozen Go struct definitions for `api/types.go` and `resultpkg/types.go`
- `Coding-plan-dev-phases-prompts_v3_2_2.md` — phased implementation plan and prompt guide

## Architecture

### Two-Layer Hard Boundary

The system is split at a strict interface boundary. **The engine layer must never import strategy internals.**

- **Engine Layer** (`/engine`, `/fitness`, `/data`, `/repository`, `/api`): manages population lifecycle, window construction, fitness scheduling, result packaging, Promote workflow.
- **Strategy Layer** (`/strategy`): implements `EvolvableStrategy` interface (14 verbs) and the `Adapter` interface. Defines gene semantics, Clamp/Validate/Crossover/Mutate/Fingerprint/Evaluate logic.

### Package Structure

All Go packages live under `internal/` (the module is `quantlab`; nothing is exported as a public library). Entry points are under `cmd/`. Tests are **colocated** next to the code they cover (idiomatic Go `_test.go`) — the top-level `tests/` dir is an empty placeholder, do NOT add tests there.

**Engine core (Tier 1):**
```
internal/domain        # Gene, SegmentInfo, SpawnPoint, SliceScore, Bar, CrucibleWindow, EvaluablePlan
internal/engine        # Epoch lifecycle, worker pool, convergence detection, population management
internal/strategy      # EvolvableStrategy interface + Adapter interface (definitions only)
internal/strategies    # Concrete strategy impls + their Adapters (sigmoid_v1, toy)
internal/fitness       # Single-window scoring, DCA dual baseline, ScoreTotal aggregation, consistency penalty
internal/verification  # OOS backtest, ReviewBacktest, DSR, stress tests
internal/data          # K-line reading, Gap detection, EvaluablePlan construction
internal/repository    # DB access, result package persistence, SharpeBank
internal/report        # Challenger report generation, diagnostics output
internal/api           # HTTP handlers: task create/query, challenger/champion, Promote/Retire
internal/resultpkg     # Result package types/enums/versions/validate
internal/quant         # canonical_json (bars_hash), closes, compare helpers
internal/migrate       # One-off backfill harness (Filter+Transform skeleton)
```

**Tier 2 SaaS + live trading (Phases 6–9, shipped):**
```
internal/saas          # Tier2 server: store (GORM models/migrate), auth, agentauth, instance,
                       #   wshub, epoch, cron, agentstatus, config
internal/agent         # Live trading agent: Binance client, delta_report sender
internal/wire          # Agent↔server WS message codec/protocol (ack, control, deltareport, errormsg)
internal/wsconn        # WebSocket connection wrapper
cmd/{saas,agent,datafeeder}  # Main entry points
research               # Python offline analysis scripts (never enters server path)
docs/learn             # Pedagogical explainers derived from the codebase (not normative specs)
```

Package split for boundary types (all under `internal/`):
- `internal/api/types.go` — `CreateEvolutionTaskRequest`, `EvolutionTaskStatusResponse`, `PromoteChallengerRequest`, `RetireChampionRequest`
- `internal/resultpkg/types.go` — all result package structs (see struct doc)
- `internal/resultpkg/enums.go` — all shared enum constants
- `internal/resultpkg/versions.go` — version constants (`SchemaVersionV533`, `FitnessVersionV1RawStd`, `FingerprintVersionV1`)
- `internal/quant/canonical_json.go` — `bars_hash` serialization boundary (file-top comment is a frozen contract)

### Core Interfaces

**`EvolvableStrategy`** (14 verbs — engine only calls these):
`StrategyID`, `Segments`, `Sample`, `Clamp`, `Validate`, `Crossover`, `Mutate`, `Fingerprint`, `Evaluate`, `ReviewBacktest`, `EncodeResult`, `DecodeElite`, `MinEvalBars`, `NewAdapter`

**`Adapter`**:
```go
type Adapter interface {
    Reset(plan *EvaluablePlan) error
    Evaluate(gene Gene) (*RawEvaluateResult, error)  // NO ScoreTotal field
    Close() error
}
```

### Evaluation Pipeline (Two-Stage — Critical Separation)

| Stage | Who | Output type |
|---|---|---|
| Per-gene multi-window eval | `Adapter.Evaluate(gene)` | `*RawEvaluateResult` |
| Cascade short-circuit + window combine | `EvolvableStrategy.Evaluate(ctx, gene, plan)` | `*RawEvaluateResult` |
| ScoreTotal aggregation | `fitness.AggregateScoreTotal(...)` (engine) | `ScoreTotal` |
| Result package evaluation layer assembly | engine `RunEpoch` | `EvaluationLayer` |

**`RawEvaluateResult` physically has no `ScoreTotal` field** — the type system enforces that strategies cannot write aggregate scores.

### Four-Window Crucible (Cascade Short-Circuit)

Evaluation runs in fixed order: `6m → 2y → 5y → 10y`. On Fatal (`MDD >= FatalMDD`), immediately terminate and mark subsequent windows with `SkippedBy`. Weights: 0.10 / 0.20 / 0.30 / 0.40.

### Fatal Sorting — Nil Safety Requirement

`SliceScore.Value` is `*float64`, nil when `Fatal=true` or cascaded-skipped. **Never dereference `*Value` in sort comparisons.** Always use the encapsulated `CompareFitness(a, b ScoreTotal) int` function. **Never write sentinel values (`-99999`, `-1e18`) into `Value`.**

`SliceScore` three-state semantics (mutually exclusive):
1. Normal: `Fatal=false, SkippedBy=nil → Value != nil`
2. Cascade-skipped: `SkippedBy != nil → Fatal=false, Value=nil`
3. Self-Fatal: `Fatal=true, SkippedBy=nil → Value=nil`

### Result Package Structure

```
ChallengerResultPackage
├── core         (ResultCore: champion_gene, spawn_point, reproducibility_metadata, ga_config)
├── evaluation   (EvaluationLayer: window_scores, score_total [engine-filled], friction_actual)
├── verification (VerificationLayer: oos_result [status ∈ ok|insufficient_data|failed|not_run], dsr_summary, review_summary)
├── diagnostics  (DiagnosticsLayer: mutation_ramp_log, fatal_audit_samples, ...)
└── promote      (PromoteLayer: decision_status ∈ {pending, promoted, rejected})
```

### Version Constants (schema v5.3.3 baseline)

```go
SchemaVersion      = "v5.3.3"
FitnessVersion     = "v1-raw-std"   // λ_cons = 0.3 (raw std dev)
FingerprintVersion = "fp-v1"
```

Challengers with different `fitness_version` must **not** be compared by score.

## Key Invariants

- `GAConfigSnapshot` stores **effective values**, not request mirrors: `test_mode=true → taker_fee_bps=0, slippage_bps=0`. User's original request intent goes to `EvolutionTask.requested_taker_fee_bps/slippage_bps` (DB only, never in result package).
- `test_mode=true` results **cannot be Promoted**.
- `decision_status` enum: `{pending, promoted, rejected}` — `retired` is NOT here; Champion retirement is managed in `champion_history` table via a separate API.
- `bars_hash`: SHA256 over canonical JSON of complete OHLCV + `OpenTime`. `Bar.IsGap`/`Bar.GapType` are **excluded**. Defined in `/internal/quant/canonical_json.go` top comment.
- `plan_hash`: SHA256 over canonical JSON of `EvaluablePlan`.
- Worker pool: each worker gets its own `Adapter` via `NewAdapter(plan)`. Engine calls `adapter.Reset(plan)` before every `Evaluate`. **Incomplete Reset breaks determinism.**
- `Adapter.Evaluate` must not launch goroutines internally. All float accumulations must be serial (no concurrent reduce).
- All sorting must use `sort.SliceStable`, not `sort.Slice`.
- Evaluation window order is fixed (`6m→2y→5y→10y`); violating it breaks `SkippedBy` enum semantics.
- OOS Anchored Holdout (`verification.RunOOS`) runs **after** `RunEpoch` returns — Fatal on the OOS bars never touches the IS `ScoreTotal` already written. Alpha is annualized excess return (`strat_ann - dca_ann`), DCA baseline re-simulated on the OOS bars (not reused from IS). Span < 90 days → `status=insufficient_data`, task NOT rejected. DecisionColor thresholds are asymmetric: green ≥ +5%/yr monthly AND weekly ≥ 0; red ≤ -3%/yr monthly OR Fatal; yellow otherwise.
- Promote (`/challengers/:id/promote`) and Retire (`/champions/:id/retire`) are **admin-only**. Operator role is explicitly excluded (`docs/saas-tier2-schema-v1.md` §3.2). Both routes go through `AuthRequired + RequireAdmin`; nil-bypass only for handler tests.

## Priority Tests (Must Ship First — §10.1)

1. `TestEvaluateDeterministic`
2. `TestEvaluateOrderInvariance`
3. `TestAdapterResetIsolation`
4. `TestClampValidateContract`
5. `TestSegmentsCoverage`
6. `TestCrossoverBlockFidelity`
7. `TestReplayWithinTolerance`
8. `TestGapHandlingNoFakeTrades`
9. `TestMutationScaleLinearity`
10. `TestCascadeShortCircuit`
11. `TestCompareFitnessNilSafe` (all Fatal/Normal combos including double-Fatal must not panic)
12. `TestBarsHashExcludesMetadata`

## HTTP API

Engine / evolution:
```
POST /api/v1/evolution/tasks
GET  /api/v1/evolution/tasks/:task_id
GET  /api/v1/challengers/:challenger_id
GET  /api/v1/challengers/:challenger_id/package
POST /api/v1/challengers/:challenger_id/promote   (admin only)
POST /api/v1/champions/:champion_id/retire        (admin only)
GET  /api/v1/champions/history
GET  /api/v1/genome/champion
GET  /api/v1/ga/sharpebank/stats
```

Auth (sudo-style step-up; default viewer 24h, admin explicit + 10min TTL):
```
POST /api/v1/auth/login
```

Data import (async jobs, AppRole=saas gated + admin):
```
GET  /api/v1/data/coverage
GET  /api/v1/data/gaps
POST /api/v1/data/import
GET  /api/v1/data/imports
GET  /api/v1/data/import/:job_id
POST /api/v1/data/import/:job_id/cancel
```

Tier 2 live-trading fleet (instance-scoped; start/stop/deploy = operator, kill = admin):
```
GET  /api/v1/instances
GET  /api/v1/instances/:instance_id
GET  /api/v1/instances/:instance_id/live
GET  /api/v1/instances/:instance_id/trades
POST /api/v1/instances/:instance_id/start
POST /api/v1/instances/:instance_id/stop
POST /api/v1/instances/:instance_id/deploy-champion
POST /api/v1/instances/:instance_id/kill          (kill-switch; manual admin trigger)
```

Agent↔server traffic rides a WebSocket channel (see `docs/saas-ws-protocol-v1.md`), not REST.

## Database Tables

Postgres via GORM `AutoMigrate` (dev/lab; production `app_role=saas` uses Atlas migrations — see `internal/saas/store/db.go`). Canonical model list: `AllModels()` in `internal/saas/store/models.go` — keep it in sync when adding tables. Table names are GORM-default snake_case plural unless a `TableName()` override exists.

**Tier 1 (engine / evolution):**
`evolution_tasks`, `gene_records` (the challenger record — full result package JSON lives in its `full_package_json` column; there is **no** `challengers` or `challenger_result_packages` table), `evaluation_traces`, `klines`, `kline_gaps`, `sharpe_banks`, `champion_histories`.

**Tier 2 (SaaS / live trading):**
`users`, `strategy_templates`, `strategy_instances`, `portfolio_states`, `runtime_states`, `spot_lots`, `trade_records`, `spot_executions`, `audit_logs`, `agent_tokens`, `reconciliation_discrepancies`, `agent_errors`, `import_jobs`.

Agent-side dedup uses a local SQLite `idempotency` table (`internal/agent/idempotency_sqlite.go`), not the Postgres schema.

## Open Questions / Deferred

Tracked in schema doc Appendix B. Still deferred: `pair` type, `slice_score.reason` enumeration, `risk_bounds` struct (spawn input, not a computed field), `dsr_summary` formalization. Resolved since the prototype: `stress_summary` (SBB Monte Carlo per framework doc §I-4.3, SHIPPED — `internal/verification`), `alpha_breakdown` (IS forward-filled, diagnostics-only), `score_raw` (`Σ weight·score`, consistency-penalty-free weighted sum — `internal/fitness/aggregate.go`); `fatal_reason` (enumerated `mdd_exceeded`/`nav_non_positive` via `resultpkg.FatalReason` + `CrucibleResult.Validate` gate).
