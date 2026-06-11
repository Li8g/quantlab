# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Behavioral Guidelines

Bias toward caution over speed; for trivial tasks use judgment.

1. **Think before coding** — State assumptions explicitly; if uncertain or multiple interpretations exist, ask, don't pick silently. Push back when a simpler approach exists.
2. **Simplicity first** — Minimum code that solves the problem, nothing speculative: no unrequested features/abstractions/flexibility/error-handling. If 200 lines could be 50, rewrite.
3. **Surgical changes** — Every changed line traces to the request; match existing style; don't refactor what isn't broken. Remove only orphans YOUR change created; flag pre-existing dead code, don't delete it. Exception: `gofmt`/`goimports` on a file you're *already* editing is fine (separate commit; flag tree-wide sweeps — repo has no formatter gate).
4. **Goal-driven** — Turn tasks into verifiable goals (write the failing/reproducing test first; ensure tests pass before and after). State a brief step→verify plan for multi-step work.

Working if: fewer unnecessary diff lines, fewer overcomplication rewrites, clarifying questions before mistakes not after.

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

> **GA navigation:** for a source-file map of the Genetic Algorithm (engine-layer
> loop vs strategy-layer gene operators) plus a recommended reading order, see
> `docs/learn/LEARN-ga-source-map.md`.

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

`EvolvableStrategy` (14 verbs the engine calls) and `Adapter` (`Reset(plan)` / `Evaluate(gene) → *RawEvaluateResult` / `Close`) are defined in `internal/strategy`.

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

`ChallengerResultPackage` = `core` / `evaluation` / `verification` / `diagnostics` / `promote` layers (full shape in `internal/resultpkg/types.go`). Load-bearing: `score_total` is **engine-filled** (strategies can't write it); `oos_result.status ∈ {ok, insufficient_data, failed, not_run}`; `decision_status ∈ {pending, promoted, rejected}`.

### Version Constants (schema v5.3.3 baseline)

```go
SchemaVersion      = "v5.3.3"
FitnessVersion     = "v1-raw-std"   // λ_cons = 0.3 (raw std dev)
FingerprintVersion = "fp-v1"
```

Challengers with different `fitness_version` must **not** be compared by score.

**Reproducibility gate (tolerance, not bit-exact).** What forces a
`fitness_version` bump is a **material** change to scoring, not any change to the
float math. A change is a version event only when it moves replay `ScoreTotal`
by more than ε (relative); a numerically-equivalent refactor (reassociated sums,
incremental indicators, SoA) that stays within ε is **not** a version event.
In-version determinism stays bit-exact (serial accumulation, `SliceStable`,
single seed, complete `Reset`); `bars_hash` (inputs) and `fingerprint` (gene
identity) stay exact. What is given up is cross-*implementation* trajectory
portability (a champion is reproducible under its own version, which is enough
for audit). Rationale + the worked #6 example + the measurement harness:
`docs/decision-ga-reproducibility-constraint.md`. **ε = 1e-4 (relative
ScoreTotal, calibrated 2026-06-08; see §9 of the decision doc for the full
measurement).** Below ε, a code change is numerically equivalent — no version
event. Above ε, bump `fitness_version`.

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
- **B2 price cap (marketable-limit IOC).** The SaaS dispatcher (`wshub.buildTradeCommand`) rewrites every **market** intent as a marketable LIMIT IOC priced at `latestClose×(1±cap/1e4)` (buy +, sell −) when `orders.price_cap_bps > 0` (`internal/saas/config` `OrdersConfig`; absent → 5bps, explicit `0` → market passthrough). cap is a human-held **execution guardrail** — it never enters the GA or the backtest. **Invariant: cap ≥ the deployed champion's effective `slippage_bps`** (enforced at `deploy-champion`, else 422 `ErrDeployCapBelowSlippage`); with that, the backtest's `close×(1±slippage)` fill always lands inside the cap, so the limit is **numerically identical** to the market fill it replaces — **no `fitness_version` event, champions are not re-tested**. Strategy-emitted limit orders keep their own price and GTC. `time_in_force` rides the wire additively (omitempty ⇒ GTC, pre-B2 Agent compat); an unfilled IOC's Binance `EXPIRED` already maps to wire `cancelled` (UDS). Decision + cap definition: `docs/decision-b2-limit-order-price-protection.md` §4.5/§7.

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

Routes are registered in `internal/api` (engine/evolution + auth + data import) and the Tier-2 instance handlers — grep the router for the current list. Non-obvious gating:
- `POST /challengers/:id/promote` + `/champions/:id/retire` — **admin only** (operator explicitly excluded).
- Data import (`/data/coverage|gaps|import`...) — `AppRole=saas`-gated + admin.
- Tier-2 fleet: start/stop/deploy-champion = operator; `/instances/:id/kill` = admin.
- Auth: `POST /auth/login`, sudo-style step-up (default viewer 24h; admin explicit + 10min TTL).

Agent↔server traffic rides a WebSocket channel (`docs/saas-ws-protocol-v1.md`), not REST.

## Database Tables

Postgres via GORM `AutoMigrate` (dev/lab); production `app_role=saas` applies **Goose** versioned migrations (selected by `migration_mode`, see `internal/saas/store/db.go`). Canonical table list: `AllModels()` in `internal/saas/store/models.go` — keep in sync when adding tables. Names are GORM-default snake_case plural unless a `TableName()` override exists.

Non-obvious:
- `gene_records` IS the challenger record — the full result package JSON lives in its `full_package_json` column; there is **no** `challengers`/`challenger_result_packages` table.
- Agent-side dedup uses a local SQLite `idempotency` table (`internal/agent/idempotency_sqlite.go`), not the Postgres schema.

## Open Questions / Deferred

Tracked in schema doc Appendix B. Still deferred: `pair` type, `slice_score.reason` enum, `risk_bounds` struct (spawn input, not a computed field), `dsr_summary` formalization. Several prototype open-questions — `stress_summary`, `alpha_breakdown`, `score_raw`, `fatal_reason` — are now resolved/shipped.
