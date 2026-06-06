# QuantLab — Codex Read-Only Review Constraints

**Mode: READ-ONLY. Do not edit, generate, or propose file changes.**
Your role is to audit the codebase for correctness, invariant violations, and design drift.
Report findings only. Never write code.

---

## 1. Project Snapshot

**QuantLab** is a Go-only server-side evolutionary trading system. It uses a Genetic Algorithm
(GA) to optimize quantitative trading strategy parameters, then evaluates challengers across
four time windows (6m / 2y / 5y / 10y) and supports human-gated Promote workflows.

**Module root:** `quantlab` (nothing exported as a public library)
**Go module:** `go.mod` at repo root
**Entry points:** `cmd/{saas,agent,datafeeder}/`
**Tests:** colocated `_test.go` next to source — `tests/` dir at root is an empty placeholder,
ignore it.

**Current phase:** prototype iteration (Phase 1–9 shipped; Tier 2 live trading active).

---

## 2. Architecture: Two-Layer Hard Boundary

The single most important structural rule in this codebase:

```
Engine Layer                           Strategy Layer
─────────────────────────────────────  ──────────────────────────────────────
internal/domain                        internal/strategy      (interface defs)
internal/engine                        internal/strategies    (concrete impls)
internal/fitness
internal/data
internal/repository
internal/api
internal/resultpkg
internal/verification
internal/quant
```

**The engine layer MUST NOT import anything from `internal/strategies` or any concrete
strategy package.** Engine calls strategies exclusively through the `EvolvableStrategy`
interface (14 verbs) and `Adapter` interface defined in `internal/strategy/`.

**Check:** `grep -r '"quantlab/internal/strategies'` in any engine-layer package is a
boundary violation.

---

## 3. Core Interfaces (source of truth: `internal/strategy/evolvable.go`)

### EvolvableStrategy (14 verbs)
```
StrategyID, Segments, Sample, Clamp, Validate, Crossover, Mutate,
Fingerprint, Evaluate, ReviewBacktest, EncodeResult, DecodeElite,
MinEvalBars, NewAdapter
```

### Adapter
```go
Reset(plan *EvaluablePlan) error
Evaluate(gene Gene) (*RawEvaluateResult, error)   // NO ScoreTotal field
Close() error
```

`Adapter.Evaluate` **must not** launch goroutines. All float accumulations must be serial.
`Reset` must fully reinitialize all state; incomplete Reset breaks determinism.

---

## 4. Evaluation Pipeline (two-stage — critical separation)

| Stage | Owner | Output |
|---|---|---|
| Per-gene multi-window eval | `Adapter.Evaluate(gene)` | `*RawEvaluateResult` |
| Cascade short-circuit + window combine | `EvolvableStrategy.Evaluate(ctx,gene,plan)` | `*RawEvaluateResult` |
| ScoreTotal aggregation | `fitness.AggregateScoreTotal(...)` (engine only) | `ScoreTotal` |
| Result-package assembly | engine `RunEpoch` | `EvaluationLayer` |

**`RawEvaluateResult` has no `ScoreTotal` field** — the type system enforces that strategies
cannot write aggregate scores. Any code that tries to set a `ScoreTotal` on a
`RawEvaluateResult` is a structural violation.

---

## 5. Four-Window Crucible and Cascade Short-Circuit

Windows evaluated in **fixed order only**: `6m → 2y → 5y → 10y`
Weights: 0.10 / 0.20 / 0.30 / 0.40

On Fatal (`MDD >= FatalMDD`): stop immediately; mark all subsequent windows `SkippedBy`.
Violating window order breaks `SkippedBy` enum semantics.

**`SliceScore.Value` is `*float64`, nil when Fatal or cascade-skipped.**
- Never dereference `*Value` in sort comparisons.
- Always use `CompareFitness(a, b ScoreTotal) int` (source: `internal/quant/compare.go`).
- Never write sentinel values (`-99999`, `-1e18`) into `Value`.

`SliceScore` three-state semantics (mutually exclusive, all three combinations must hold):
1. Normal: `Fatal=false, SkippedBy=nil → Value != nil`
2. Cascade-skipped: `SkippedBy != nil → Fatal=false, Value=nil`
3. Self-Fatal: `Fatal=true, SkippedBy=nil → Value=nil`

All sorting must use `sort.SliceStable`, not `sort.Slice`.

---

## 6. Key Invariants to Audit

### 6.1 Reproducibility
- `bars_hash`: SHA256 over canonical JSON of complete OHLCV + `OpenTime`.
  `Bar.IsGap` / `Bar.GapType` **excluded**. Contract frozen in
  `internal/quant/canonical_json.go` file-top comment.
- `plan_hash`: SHA256 over canonical JSON of `EvaluablePlan`.
- `fingerprint`: gene identity hash — must remain bit-exact.
- In-version determinism: serial accumulation, `sort.SliceStable`, single seed,
  complete `Reset`.
- `fitness_version` bump is required only for **material** score changes (> ε relative).
  Numerically-equivalent refactors are NOT version events. Current: `v1-raw-std`.

### 6.2 GAConfigSnapshot Semantics
`GAConfigSnapshot` stores **effective values**, not request mirrors:
- `test_mode=true → taker_fee_bps=0, slippage_bps=0` (must be enforced at snapshot creation)
- User's original request intent → `EvolutionTask.requested_taker_fee_bps/slippage_bps`
  (DB only, never in result package)
- `test_mode=true` challengers **cannot be Promoted** — check this gate in
  `internal/api/handlers.go` and `internal/api/handlers_phase9.go`.

### 6.3 Promote / Retire Auth
Both routes must pass through `AuthRequired + RequireAdmin`.
Operator role is **explicitly excluded** from Promote and Retire.
Source: `docs/saas-tier2-schema-v1.md` §3.2.

### 6.4 `decision_status` Enum
Valid values: `{pending, promoted, rejected}`
`retired` is **NOT** in `decision_status` — Champion retirement is a separate concept
managed in `champion_history` table. Any code using `"retired"` as a `decision_status`
value is a bug.

### 6.5 OOS Anchored Holdout (verification layer)
`verification.RunOOS` runs **after** `RunEpoch` returns — Fatal on OOS bars must NEVER
touch the IS `ScoreTotal` already written.
- DCA baseline must be re-simulated on OOS bars (not reused from IS).
- Span < 90 days → `status=insufficient_data`, task NOT rejected.
- `DecisionColor` thresholds (asymmetric): green ≥ +5%/yr monthly AND weekly ≥ 0;
  red ≤ -3%/yr monthly OR Fatal; yellow otherwise.

### 6.6 Worker Pool Isolation
Each worker gets its own `Adapter` via `NewAdapter(plan)`.
`adapter.Reset(plan)` is called before **every** `Evaluate`.
Workers must not share Adapter instances.

### 6.7 Import Boundary (data-layer `bars_hash`)
`Bar.IsGap` and `Bar.GapType` are computed metadata — never included in `bars_hash`.
Their computation happens in `internal/data/` after loading, not before hashing.

### 6.8 Kill-Switch and Reconciliation Scoping
Auto-freeze drift calculation must use **only managed assets** (assets present in the
instance's `expected` key), not all exchange assets. Faucet-injected tokens must not
trigger freeze. Source: `internal/saas/instance/manager.go`.

### 6.9 Wire Protocol Additive-Only
`internal/wire/` message types are frozen protocol. New fields may only be **added**,
never removed or type-changed. Existing fields must remain backward-compatible.
`wire.Hello` `environment` field is additive (server rejects `auth_fail` on mismatch
only when `app_role=saas`; dev-lab warns and passes).

---

## 7. Version Constants (must match everywhere they appear)

```go
// internal/resultpkg/versions.go
SchemaVersion      = "v5.3.3"
FitnessVersion     = "v1-raw-std"   // λ_cons = 0.3
FingerprintVersion = "fp-v1"
```

Challengers with different `fitness_version` must not be compared by score — check any
cross-challenger comparison code.

---

## 8. Database Schema Invariants

**Table ownership:**
- Challenger record lives in `gene_records.full_package_json` (no separate
  `challengers` or `challenger_result_packages` table).
- `evolution_tasks`, `gene_records`, `evaluation_traces`, `klines`, `kline_gaps`,
  `sharpe_banks`, `champion_histories` — engine/evolution tier.
- `users`, `strategy_instances`, `portfolio_states`, `runtime_states`, `spot_lots`,
  `trade_records`, `spot_executions`, `audit_logs`, `agent_tokens`,
  `reconciliation_discrepancies`, `agent_errors`, `import_jobs` — Tier 2 live.

**Migration:** `app_role=saas` uses Goose migrations only (`migrations/`).
`app_role=dev` may use GORM AutoMigrate. Never add new Goose migrations that
AutoMigrate would silently handle for dev; both paths must stay in sync
(`internal/saas/store/migrate_drift_test.go` enforces this in CI).

**Canonical model list:** `AllModels()` in `internal/saas/store/models.go` — must
be kept in sync when tables are added.

**Agent-side idempotency:** local SQLite `idempotency` table
(`internal/agent/idempotency_sqlite.go`), not Postgres. This is intentional — see
`docs/agent-local-sqlite-rationale.md`.

---

## 9. Navigation: Where to Find Things

| What | Where |
|---|---|
| Domain types (Gene, Bar, CrucibleWindow, …) | `internal/domain/types.go` |
| EvolvableStrategy + Adapter interface | `internal/strategy/evolvable.go`, `contract.go` |
| Engine epoch loop + worker pool | `internal/engine/engine.go` |
| Result package types | `internal/engine/package.go` |
| ScoreTotal aggregation | `internal/fitness/aggregate.go` |
| DCA ghost baseline | `internal/fitness/ghost_dca.go` |
| OOS holdout | `internal/verification/oos.go` |
| SBB Monte Carlo stress | `internal/verification/sbb.go`, `stress.go` |
| bars_hash contract | `internal/quant/canonical_json.go` (file-top comment) |
| CompareFitness (nil-safe) | `internal/quant/compare.go` |
| Result package structs | `internal/resultpkg/types.go` |
| Result package enums | `internal/resultpkg/enums.go` |
| Version constants | `internal/resultpkg/versions.go` |
| Validate (fatal_reason gate) | `internal/resultpkg/validate.go` |
| API handlers (Promote/Retire auth) | `internal/api/handlers.go`, `handlers_phase9.go` |
| Kill-switch + auto-freeze | `internal/saas/wshub/hub.go`, `internal/saas/instance/manager.go` |
| Agent delta-report sender | `internal/agent/client.go` |
| Wire protocol messages | `internal/wire/*.go` |
| Sigmoid strategy gene + eval | `internal/strategies/sigmoid_v1/` |
| Goose migrations | `migrations/` |
| Schema drift test | `internal/saas/store/migrate_drift_test.go` |
| System design (source of truth) | `docs/进化计算引擎.md` |
| JSON schema v5.3.3 | `docs/进化计算引擎_数据契约.md` |
| Frozen Go struct draft | `docs/进化计算引擎_Go_struct_草案.md` |
| Reproducibility decision | `docs/decision-ga-reproducibility-constraint.md` |
| OOS spec | `docs/phase-5d-oos-v1.md` |
| Tier 2 auth + role spec | `docs/saas-tier2-schema-v1.md` |
| WS protocol spec | `docs/saas-ws-protocol-v1.md` |

---

## 10. Priority Audit Targets (§10.1 tests — must all exist and be correct)

1. `TestEvaluateDeterministic` — same gene+plan → identical ScoreTotal every run
2. `TestEvaluateOrderInvariance` — population eval result independent of goroutine scheduling
3. `TestAdapterResetIsolation` — adapter state fully cleared between calls
4. `TestClampValidateContract` — Clamp output always passes Validate
5. `TestSegmentsCoverage` — Segments cover full bar range without gaps or overlaps
6. `TestCrossoverBlockFidelity` — crossover gene contains only parent-derived blocks
7. `TestReplayWithinTolerance` — replay score within ε of original (not bit-exact)
8. `TestGapHandlingNoFakeTrades` — gap bars never generate synthetic trades
9. `TestMutationScaleLinearity` — mutation magnitude scales with temperature parameter
10. `TestCascadeShortCircuit` — Fatal on window N marks N+1..4 as SkippedBy, not Fatal
11. `TestCompareFitnessNilSafe` — all nine Fatal/Normal combos (including double-Fatal) don't panic
12. `TestBarsHashExcludesMetadata` — adding/removing IsGap doesn't change bars_hash

Verify each test: (a) exists, (b) actually tests what the name claims, (c) would catch
a real regression (not a vacuous assertion).

---

## 11. Known Design Decisions (do not re-litigate)

- **No GPU acceleration** — bottleneck is per-bar indicator recalculation (O(n·window)),
  not parallelism. Worker pool is already near-optimal. Incremental indicators are a
  future fitness_version event.
- **Goose over Atlas** — Atlas declarative approach is neutralized by TimescaleDB
  hypertable / partial-index / raw DDL. Goose + Squawk lint is the chosen path.
- **Agent SQLite for idempotency** — intentional; SQLite survives agent restarts without
  a network hop. See `docs/agent-local-sqlite-rationale.md`.
- **No `retired` in `decision_status`** — Promote/Retire are orthogonal workflows.
- **ε tolerance instead of bit-exact reproducibility** — cross-implementation trajectory
  portability is traded away; per-version bit-exactness within the same binary is kept.
  ε is not yet calibrated (pending week of 2026-06-08).
- **`stress_summary` uses absolute-from-start ruin definition** — ruin = NAV drops
  below `1 - FatalMDD` measured from the start of the SBB window, not from any peak.

---

## 12. Deferred / Open (do not flag these as bugs)

- `pair` type (currently `string`)
- `slice_score.reason` enumeration
- `risk_bounds` struct (spawn input, not computed)
- `dsr_summary` formalization
- ε calibration for reproducibility tolerance gate
- Per-order price guard (deferred pending real trading data)
- Frontend observability for stale-data (⑤) and env-mismatch (⑥) signals

---

## 13. Review Checklist

When reading each file, check:

- [ ] Layer boundary: does this engine-layer file import `internal/strategies`?
- [ ] Nil safety: any direct `*SliceScore.Value` dereference without Fatal guard?
- [ ] Sort stability: `sort.Slice` instead of `sort.SliceStable`?
- [ ] Sentinel values: `-99999` or `-1e18` written into `Value`?
- [ ] `RawEvaluateResult` ScoreTotal: does any strategy code set an aggregate score?
- [ ] Window order: does any code evaluate windows out of `6m→2y→5y→10y` sequence?
- [ ] Adapter goroutines: does any `Adapter.Evaluate` impl spawn goroutines?
- [ ] Concurrent float accumulation: any parallel reduce over window scores?
- [ ] `test_mode` gate: can a `test_mode=true` result reach the Promote path?
- [ ] `decision_status`: any use of `"retired"` as a decision_status value?
- [ ] Auth: do Promote and Retire routes go through `RequireAdmin`?
- [ ] `bars_hash`: does it include `IsGap` or `GapType`?
- [ ] `GAConfigSnapshot`: does it reflect request values instead of effective values?
- [ ] Missing `Reset` call before `Evaluate` in worker pool?
- [ ] OOS: does it reuse IS DCA result instead of re-simulating?
- [ ] `AllModels()`: is it in sync with the actual GORM model list?
- [ ] Wire protocol: any field removal or type change (vs. additive-only policy)?
- [ ] `fitness_version` comparison: any cross-version score comparison?
