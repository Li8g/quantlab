---
name: quantlab-review
description: Use when performing QuantLab read-only code reviews, especially CODEX_SKILL.md, GA invariants, Promote/OOS auth gates, Go core, TypeScript frontend, or Python research boundaries.
---

# QuantLab Read-Only Review

Use this skill for QuantLab mid-development code reviews. The canonical source file is `CODEX_SKILL.md` at the repository root. Read it before inspecting code and treat it as the binding review contract.

Mode: read-only review. Do not edit, generate, reformat, or propose file changes while performing the review. Report findings only.

## Repository Scope

- Go is the production core and server-side implementation.
- TypeScript is production frontend.
- Python is offline research only and must not enter the production server/runtime path.
- Tests are colocated `_test.go` files next to source; ignore the root `tests/` placeholder.

## Core Architecture Rules

- Engine layer packages must not import `quantlab/internal/strategies` or concrete strategy implementations.
- Engine calls strategies only through `internal/strategy.EvolvableStrategy` and `Adapter`.
- `EvolvableStrategy` must retain the 14 verbs: `StrategyID`, `Segments`, `Sample`, `Clamp`, `Validate`, `Crossover`, `Mutate`, `Fingerprint`, `Evaluate`, `ReviewBacktest`, `EncodeResult`, `DecodeElite`, `MinEvalBars`, `NewAdapter`.
- `Adapter` exposes `Reset(plan *EvaluablePlan) error`, `Evaluate(gene Gene) (*RawEvaluateResult, error)`, and `Close() error`.
- `Adapter.Evaluate` must not launch goroutines, and float accumulation must remain serial.
- `Reset` must fully reinitialize all state before each evaluation.

## Evaluation Pipeline

- Strategy/adapter evaluation returns `*RawEvaluateResult` only.
- `RawEvaluateResult` must not contain or set aggregate `ScoreTotal`.
- `fitness.AggregateScoreTotal` is the engine-owned aggregation boundary.
- Result package assembly is engine-owned.

## Four-Window Crucible

- Window order is fixed: `6m -> 2y -> 5y -> 10y`.
- Weights are `0.10 / 0.20 / 0.30 / 0.40`.
- On Fatal (`MDD >= FatalMDD`), stop immediately and mark later windows with `SkippedBy`.
- `SliceScore.Value` is nil when Fatal or cascade-skipped.
- Never dereference `*SliceScore.Value` in sort comparisons.
- Always use nil-safe `CompareFitness(a, b ScoreTotal) int` for fitness ranking.
- Never write sentinel values such as `-99999` or `-1e18` into `Value`.
- All sorting must use `sort.SliceStable`, not `sort.Slice`.

## Key Invariants

- `bars_hash` is SHA256 over canonical JSON of complete OHLCV plus `OpenTime`; exclude `Bar.IsGap` and `Bar.GapType`.
- `plan_hash` is SHA256 over canonical JSON of `EvaluablePlan`.
- Fingerprint must remain bit-exact for gene identity.
- In-version determinism requires serial accumulation, stable sorting, single seed, and complete adapter reset.
- Version constants must match: schema `v5.3.3`, fitness `v1-raw-std`, fingerprint `fp-v1`.
- Challengers with different `fitness_version` must not be compared by score.
- `GAConfigSnapshot` stores effective values, not request mirrors.
- `test_mode=true` implies effective `taker_fee_bps=0` and `slippage_bps=0` in snapshots.
- Original request fees/slippage belong only in `EvolutionTask.requested_*` DB fields, not result packages.
- `test_mode=true` challengers cannot be Promoted.
- Promote and Retire must pass through `AuthRequired + RequireAdmin`; operator is excluded.
- Valid `decision_status` values are `pending`, `promoted`, and `rejected`; `retired` is invalid there.
- OOS runs after `RunEpoch` and must not mutate already-written IS `ScoreTotal`.
- OOS DCA baseline must be re-simulated on OOS bars.
- OOS span under 90 days is `insufficient_data`, not task rejection.
- Worker pool gives each worker its own adapter via `NewAdapter(plan)` and calls `adapter.Reset(plan)` before every `Evaluate`.
- Kill-switch/reconciliation drift uses only managed assets present in the instance `expected` key.
- Wire protocol changes under `internal/wire/` must be additive-only.
- `AllModels()` must stay in sync with actual GORM model tables.

## Priority Test Audit

Verify that each test exists, is non-vacuous, and would catch a real regression:

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
11. `TestCompareFitnessNilSafe`
12. `TestBarsHashExcludesMetadata`

## Navigation Hints

- Domain types: `internal/domain/types.go`.
- EvolvableStrategy and Adapter interfaces: `internal/strategy/evolvable.go`, `internal/strategy/contract.go`.
- Engine epoch loop and worker pool: `internal/engine/engine.go`.
- Score aggregation and DCA baseline: `internal/fitness/aggregate.go`, `internal/fitness/ghost_dca.go`.
- OOS and stress verification: `internal/verification/oos.go`, `internal/verification/sbb.go`, `internal/verification/stress.go`.
- bars_hash and fitness comparison contracts: `internal/quant/canonical_json.go`, `internal/quant/compare.go`.
- Result package structs, enums, versions, validation: `internal/resultpkg/`.
- Promote/Retire auth: `internal/api/handlers.go`, `internal/api/handlers_phase9.go`.
- Kill-switch and auto-freeze: `internal/saas/wshub/hub.go`, `internal/saas/instance/manager.go`.
- Wire protocol: `internal/wire/*.go`.
- Sigmoid strategy implementation: `internal/strategies/sigmoid_v1/`.
- Migrations and drift test: `migrations/`, `internal/saas/store/migrate_drift_test.go`.
- Normative docs: `docs/进化计算引擎.md`, `docs/进化计算引擎_数据契约.md`, `docs/进化计算引擎_Go_struct_草案.md`, `docs/saas-tier2-schema-v1.md`, `docs/saas-ws-protocol-v1.md`.

## Review Checklist

- Search for engine-layer imports of `internal/strategies`.
- Search for direct `*SliceScore.Value` dereferences and unsafe comparisons.
- Search for `sort.Slice` where `sort.SliceStable` is required.
- Search for sentinel score values like `-99999` or `-1e18`.
- Search for any aggregate score set on `RawEvaluateResult`.
- Verify fixed window order and cascade semantics.
- Verify no goroutines inside `Adapter.Evaluate` implementations.
- Verify no concurrent float reductions over score/window values.
- Verify `test_mode` Promote gate and effective snapshot fees.
- Verify `decision_status` never uses `retired`.
- Verify Promote/Retire routes require admin.
- Verify `bars_hash` excludes gap metadata.
- Verify worker pool reset and adapter isolation.
- Verify OOS does not reuse IS DCA results.
- Verify `AllModels()` and migrations stay in sync.
- Verify wire protocol compatibility is additive-only.
- Verify cross-version challenger score comparisons are blocked.

## Findings Format

Report findings first, ordered by severity. For each finding include the file path, line reference, violated invariant, evidence, and impact. Separate confirmed defects from residual risks and missing/weak tests. If no findings are found, state that explicitly and describe remaining coverage limits.

## Do Not Flag

- No GPU acceleration; incremental indicators are future work and would be a fitness_version event.
- Goose over Atlas for production migrations.
- Agent SQLite idempotency is intentional; it is not a missing Postgres table.
- No `retired` in `decision_status`; Promote/Retire are orthogonal workflows.
- Epsilon-tolerance reproducibility instead of cross-implementation bit-exact trajectory portability.
- `stress_summary` absolute-from-start ruin definition.
- `pair` type still being `string`.
- Missing `slice_score.reason` enumeration.
- Deferred `risk_bounds` struct.
- Deferred `dsr_summary` formalization.
- Uncalibrated epsilon tolerance.
- Deferred per-order price guard.
- Deferred frontend observability for stale-data and environment-mismatch signals.
