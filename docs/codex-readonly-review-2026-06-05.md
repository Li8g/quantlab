# Codex Read-Only Review Archive — 2026-06-05

Scope: current Codex session, read-only architectural/code review of QuantLab.

Binding review contract: `CODEX_SKILL.md`.

## Stage 0 — Project Map And Baseline

Completed.

Current map:

- Go core: `cmd/` + `internal/`; about 241 Go files, including 130 production files and 111 test files.
- Frontend: `web/`, npm/Vite/React/TypeScript; production TS/TSX files about 16.
- Python: offline research only, currently `research/optuna_toy/quantlab_to_optuna.py` and `research/optuna_toy/toy_study.py`.
- Root `tests/` is only a placeholder with `.gitkeep`.
- Branch observed: `docs/ga-learn-series`.
- Untracked review/prep files observed: `CODEX_SKILL.md`, `.opencode/`, `docs/memory-review-prep-status.md`.

Baseline notes:

- `CODEX_SKILL.md` points Goose migrations at root `migrations/`, but the actual repo path is `internal/saas/store/migrations/`.
- `internal/saas/store/migrations/README.md` states that `database.migration_mode` is independent of `app_role`; unset mode is derived from role.
- Full-repo scans must exclude `web/node_modules`, `.opencode/node_modules`, `research/**/.venv`, and `web/dist`.
- `docs/code-review-plan.md` contains earlier opencode review history and must be treated as background, not as current Codex findings.

Plan correction:

- Database review will use `internal/saas/store/migrations/`, `internal/saas/store/models.go`, `internal/saas/store/migrate.go`, `internal/saas/store/migrate_drift_test.go`, and CI drift guard.
- Priority tests will be reviewed by direct name match plus behavior alias mapping.

## Stage 1 — Architecture Boundary

Completed.

Findings:

### Medium — Strategy Layer Depends On Verification Layer

File: `internal/strategies/sigmoid_v1/evaluate_window.go`

Evidence:

- Imports `quantlab/internal/verification`.
- Calls `verification.ComputeSharpeStats(logReturns)`.

Violated invariant:

- `CODEX_SKILL.md` places `internal/verification` in the engine layer and `internal/strategies` in the strategy layer. Strategy implementations should not depend on engine-layer verification.

Impact:

- No immediate runtime failure found, but this couples concrete strategy evaluation to verification-layer helpers and blurs the engine/strategy split.

### Low — Engine Tests Import Concrete Strategies

Files:

- `internal/engine/engine_test.go`
- `internal/engine/engine_sigmoid_test.go`
- `internal/engine/fatal_audit_test.go`

Evidence:

- These `engine_test` files import `quantlab/internal/strategies/sigmoid_v1` or `quantlab/internal/strategies/toy`.

Violated invariant:

- The literal grep check in `CODEX_SKILL.md` flags `quantlab/internal/strategies` in engine-layer packages.

Impact:

- Production engine import graph is clean, but the engine test suite is coupled to concrete strategies and makes automated boundary checks noisy.

Confirmed OK:

- Production `internal/engine` does not import concrete strategies.
- The only production concrete strategy import is `internal/saas/epoch/registry.go`, which acts as the SaaS composition root.
- `EvolvableStrategy` has the expected 14 verbs.
- `Adapter` has `Reset`, `Evaluate`, and `Close`.
- `RawEvaluateResult` omits `ScoreTotal`.
- Production Go path does not call Python, Optuna, or research scripts.

## Next Stage

Stage 2 will review GA core invariants:

- Worker pool adapter isolation.
- `adapter.Reset(plan)` before every evaluate.
- No goroutines in `Adapter.Evaluate`.
- Engine-owned score aggregation through `fitness.AggregateScoreTotal`.
- Four-window cascade order and Fatal/skipped semantics.
- Stable sorting and nil-safe `CompareFitness`.
- Whether the Stage 1 `sigmoid_v1 -> verification` dependency affects per-gene evaluation semantics.

## Stage 2 — GA Core Invariants

Completed.

Verification command:

```text
GOCACHE=/tmp/quantlab-go-cache go test ./internal/engine ./internal/strategies/sigmoid_v1 ./internal/fitness ./internal/quant ./internal/resultpkg
```

Result: passed.

Note: the first run without `GOCACHE` failed because the sandbox could not access `/home/l9g/.cache/go-build`; rerun with `/tmp` cache passed.

Findings:

### Low — Non-Stable Sort In Datafeeder Stats Path

File: `cmd/datafeeder/main.go`

Evidence:

- `sort.Slice(rows, ...)` is used for stats output sorting.

Violated invariant:

- `CODEX_SKILL.md` says all sorting must use `sort.SliceStable`, not `sort.Slice`.

Impact:

- This is not GA ranking and the SQL query already orders by `symbol, interval`, so practical risk is low. It still violates the repository-wide sorting rule and keeps a known unsafe pattern in production code.

### Low — Toy Strategy Does Not Enforce Canonical Four-Window Order

File: `internal/strategies/toy/toy.go`

Evidence:

- `Toy.Evaluate` iterates `plan.Windows` directly.

Violated invariant:

- Strategy evaluation should use fixed IS order `6m -> 2y -> 5y -> 10y`.

Impact:

- Toy is not in `DefaultRegistry`, and scores are plan-independent, so aggregate score is effectively unaffected. But raw window order is plan-order dependent, making toy a poor boundary test fixture for the fixed-order contract.

Residual risks:

### RawEvaluateResult Validation Is Not Wired Into RunEpoch

Files:

- `internal/resultpkg/validate.go`
- `internal/engine/engine.go`
- `internal/fitness/aggregate.go`

Evidence:

- `RawEvaluateResult.Validate()` exists and validates the `SliceScore` three-state contract.
- The validation comment says it is not wired into the `RunEpoch` hot loop.
- `engine.evaluatePopulation` aggregates `raw.Windows` immediately after `adapter.Evaluate`.
- `fitness.AggregateScoreTotal` skips windows with `Score.Value == nil` or `SkippedBy != nil`.

Impact:

- Current `sigmoid_v1` produces valid normal/fatal/skipped states, and tests cover those states. If a future strategy returns `Fatal=false`, `SkippedBy=nil`, `Value=nil`, the engine would not reject it at the strategy boundary; aggregation would silently drop that window. This is a guard gap, not a confirmed current strategy defect.

Confirmed OK:

- Production `internal/engine` creates one adapter per worker.
- `adapter.Reset(plan)` is called before every `adapter.Evaluate`.
- Best-gene re-evaluation also resets the adapter first.
- No goroutines were found inside concrete strategy `Adapter.Evaluate` implementations.
- `RawEvaluateResult` still cannot carry `ScoreTotal`.
- `ScoreTotal` aggregation is engine-side through `fitness.AggregateScoreTotal`.
- GA ranking uses `sort.SliceStable` and `quant.CompareFitness` via `compareWithFp`.
- `sigmoid_v1` evaluates windows through `resultpkg.AllWindowsInEvalOrder()`.
- `sigmoid_v1` cascade skip emits `Fatal=false`, `Value=nil`, `SkippedBy!=nil`.
- `sigmoid_v1` self-Fatal emits `Fatal=true`, `Value=nil`, `SkippedBy=nil`.
- No sentinel score values such as `-99999` or `-1e18` were found in production score assignment.

Stage 1 follow-up:

- The `sigmoid_v1 -> verification` dependency is used for `verification.ComputeSharpeStats(logReturns)` inside per-window evaluation. It affects per-gene evaluation metadata (`LongestWindowStats`) but not `ScoreTotal` directly. It remains a boundary-design issue from Stage 1.

### Stage 2 Targeted Re-Review Addendum — 2026-06-05

Scope requested by user:

- `RawEvaluateResult.Validate()` hot-loop wiring.
- Window ordering.
- Cascade skipped semantics.
- Score aggregation ownership.
- Worker adapter isolation.

Verification command:

```text
GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/fitness ./internal/engine ./internal/strategies/sigmoid_v1 ./internal/verification
```

Result: passed.

Findings:

#### High — RawEvaluateResult Validate Still Not Wired Into GA Hot Loop

Files:

- `internal/engine/engine.go`
- `internal/resultpkg/validate.go`
- `internal/fitness/aggregate.go`
- `internal/verification/review.go`

Evidence:

- `engine.evaluatePopulation` calls `adapter.Evaluate`, then immediately aggregates `raw.Windows`.
- The final best-gene re-evaluation also returns `bestRaw` without validation.
- `verification.RunReview` also replays and aggregates raw windows without validating them first.
- `fitness.AggregateScoreTotal` skips any window with `Score.Value == nil` or `SkippedBy != nil`.

Impact:

- An invalid strategy/adapter output such as `Fatal=false`, `SkippedBy=nil`, `Value=nil` would be silently treated as a missing/skipped contribution by aggregation instead of failing at the strategy-to-engine boundary.
- A nil `raw` could panic inside the worker goroutine.
- This is currently a guard gap rather than a confirmed `sigmoid_v1` defect, because `sigmoid_v1` emits valid normal/fatal/skipped states.

Suggested fix:

- After every `adapter.Evaluate`, fail closed on `raw == nil` or `raw.Validate() != nil` before aggregation.
- Apply the same boundary check to the final best-gene re-evaluation and replay/OOS verification entry points.

#### Medium — RawEvaluateResult Validate Does Not Enforce Cascade Sequence Semantics

Files:

- `internal/resultpkg/validate.go`
- `internal/resultpkg/enums.go`

Evidence:

- `RawEvaluateResult.Validate()` only validates each `CrucibleResult` independently.
- It does not check canonical order, duplicate windows, empty window list, fatal-to-skipped sequence, or whether `SkippedBy` references an actual earlier fatal.
- `WindowName.IsValid()` accepts `WindowOOS`; OOS is explicitly excluded from `AllWindowsInEvalOrder()`.

Impact:

- IS raw results can pass validation while violating cascade semantics.
- If an IS raw result includes `WindowOOS`, aggregation gives it weight 0 but still includes its value in `validScores`, which can distort the consistency penalty.

Suggested fix:

- Add a Raw-level cascade validator using `resultpkg.AllWindowsInEvalOrder()`.
- Reject `WindowOOS` in IS `RawEvaluateResult`.
- Enforce no duplicates, canonical order, and exact skipped-cause semantics after the first fatal.

#### Low — Toy Strategy Still Does Not Enforce Canonical Four-Window Order

File: `internal/strategies/toy/toy.go`

Evidence:

- `Toy.Evaluate` iterates `plan.Windows` directly.

Impact:

- Toy is not in `DefaultRegistry`, so this is not a production path defect.
- It remains a weak GA boundary fixture because engine tests using toy do not prove the canonical order contract.

Confirmed OK:

- Production `DefaultRegistry` registers only `sigmoid_v1`.
- `sigmoid_v1` evaluates via `resultpkg.AllWindowsInEvalOrder()`.
- `sigmoid_v1` cascade skip emits `Fatal=false`, `Value=nil`, `SkippedBy!=nil`.
- `sigmoid_v1` self-Fatal emits `Fatal=true`, `Value=nil`, `SkippedBy=nil`.
- Worker pool uses one adapter per worker and calls `Reset(plan)` before every `Evaluate`.
- No goroutines were found inside concrete strategy `Adapter.Evaluate` implementations.
- Strategy code does not write `ScoreTotal`; production aggregation remains engine-side through `fitness.AggregateScoreTotal`.

## Stage 3 — Business And Integration Invariants

Completed by targeted re-review after the previous session died during remote compact.

Decision:

- Do not restart Stage 3 from zero. The previous session log was not durable enough to treat as final, but it provided a valid route map.
- Targeted re-review covered `CODEX_SKILL.md` sections 6.2-6.9 and 7-8: GAConfigSnapshot/TestMode, Promote/Retire auth and lifecycle, `decision_status`, OOS, bars_hash/data boundary, kill-switch reconciliation scoping, wire environment/version behavior, and cross-version score comparison paths.

Verification command:

```text
GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./internal/saas/epoch ./internal/verification ./cmd/saas ./internal/saas/wshub ./internal/data ./internal/engine
```

Result: passed outside the sandbox.

Sandbox note: the same command in the sandbox partially passed, then failed in tests using `httptest.NewServer` because local socket listen is denied (`listen tcp6 [::1]:0: socket: operation not permitted`). The sandbox failures were environmental, not code failures.

Findings:

### Major — Retire Is Not CAS-Protected

File: `internal/repository/champion.go`

Evidence:

- `ChampionRepo.Retire` reads the `ChampionHistory` row by `challenger_id`.
- `applyRetire` rejects already-retired rows only from the in-memory `history.RetiredAt`.
- The write uses `Where("id = ?", history.ID).Updates(updates)`.
- The UPDATE predicate does not include `retired_at IS NULL`.

Impact:

- Two concurrent Retire requests can both read `retired_at IS NULL`, both pass `applyRetire`, and both update the same row successfully.
- The later request overwrites `retired_at`, `retired_by`, and `retire_note`, corrupting retirement audit attribution.
- This violates the intended "already retired" transition guard. `RowsAffected == 0` cannot catch this race because both UPDATEs can affect one row.

Suggested fix:

- Change the UPDATE predicate to `id = ? AND retired_at IS NULL`.
- Map `RowsAffected == 0` to `api.ErrAlreadyRetired` so HTTP returns 422.
- Add a CAS/concurrency regression test for `ChampionRepo.Retire`.

Confirmed OK:

- `test_mode=true` effective friction is zeroed before plan construction in `internal/saas/epoch/service.go`.
- `GAConfigSnapshot` is populated from `plan.Friction`, so it stores effective friction, not request mirrors.
- `EvolutionTask` keeps requested taker fee/slippage as DB-only audit fields.
- `Promote` rejects TestMode challengers in `internal/repository/champion.go`.
- Production `cmd/saas` wires both `AuthRequired` and `RequireAdmin`; Promote/Retire routes therefore require admin in production.
- Operator role is excluded from Promote/Retire; tests cover viewer/operator 403 and admin 200.
- `decision_status` remains limited to pending/promoted/rejected in resultpkg and web types.
- Retirement is surfaced through `retired_at_ms` / champion history, not by writing `decision_status="retired"`.
- OOS runs after `engine.RunEpoch` returns.
- OOS Fatal produces OOS failed/red and does not mutate IS `ScoreTotal`.
- OOS DCA baselines are re-simulated on OOS eval bars, not reused from IS.
- OOS span under 90 days returns `insufficient_data` and does not fail the task.
- `bars_hash` is computed through `quant.BarsHash(bars)` after plan assembly; gap metadata is excluded by the canonical hash contract and tests.
- Auto-freeze uses managed assets from expected holdings (`expected` keys) when computing `maxFlaggedDriftBps`; faucet/unmanaged assets are recorded as discrepancies but do not trigger freeze. Tests cover this.
- Wire `Hello.Environment` is additive/backward-compatible: empty environment skips the assertion; mismatch rejects only when configured to reject.
- No production path was found that selects an active champion by sorting/comparing `score_total` across different `fitness_version` values. Active champion lookup comes from `champion_histories`, not score ranking.
- Frontend Promote/Retire uses `SudoModal` to step up to a fresh admin token before the action.

Minor observations / residual risk:

- `engine/package` tests verify `TestMode` is copied through, but do not assert the full request-nonzero-friction plus `test_mode=true` path all the way to `GAConfigSnapshot` zero values. Implementation is correct via `service.go -> plan.Friction -> package.go`; this is a low-risk test gap, not a confirmed defect.

## Stage 5 — Agent Idempotency / Live Message Persistence

Completed targeted high-priority re-review on 2026-06-05.

Scope:

- Agent `trade_command` handling.
- Agent SQLite idempotency store and in-memory store semantics.
- Binance UDS `executionReport` to Agent `OrderEvent` path.
- Agent `order_update` and `delta_report` fill buffering.
- SaaS `ack` / `order_update` / `delta_report` persistence hook.
- `spot_executions` schema/model/repository dedup behavior.
- WS reconnect `state_sync_response` behavior.

Verification command:

```text
GOCACHE=/tmp/quantlab-go-cache go test ./internal/agent ./cmd/saas ./internal/repository ./internal/saas/wshub ./internal/saas/store
```

Result: passed.

### Stage 5 Targeted Re-Review Addendum — 2026-06-06

Scope: highest-priority live order/idempotency risks requested by the user:
`handleTradeCommand` idempotency ordering, SQLite idempotency `Get` error handling,
`onOrderEvent` read-error behavior, and SaaS order status monotonicity.

Verification command:

```text
GOCACHE=/tmp/quantlab-go-cache go test ./internal/agent ./cmd/saas ./internal/repository
```

Result: passed. This confirms the current test baseline only; the negative cases
below are not covered by existing tests.

Re-review result:

- Still open: `handleTradeCommand` checks frozen/kill and `valid_until_ms`
  before `idempotency.Get`, so a replay of an already filled command can return
  `rejected` or `expired` instead of `duplicate_terminal`.
- Severity updated to critical: `handleTradeCommand` drops SQLite idempotency
  `Get` errors, then can `Put(preRec)` through an upsert and continue to
  `exchange.Submit`, making the submit path fail-open on store read failure.
- Still open: `onOrderEvent` drops idempotency `Get` errors into the unknown
  order branch, returning before `OrderUpdate`, before `delta_report` buffering,
  and before local status update.
- Still open and major: SaaS `ack` / `order_update` status writes remain
  non-monotonic; `UpdateTradeStatus` updates by `client_order_id` only, so an
  older `partial_filled`, `cancelled`, or `rejected` message can overwrite
  `filled`.

Regression tests still needed:

- Filled command replay after expiry must return `duplicate_terminal`.
- Filled command replay while frozen must return `duplicate_terminal`.
- Fake idempotency-store `Get` error in `handleTradeCommand` must not submit.
- Fake idempotency-store `Get` error in `onOrderEvent` must not silently drop a
  real fill.
- SaaS terminal-status replay must prove `filled` cannot be downgraded.

Findings:

### Critical — Agent Idempotency Store Read Errors Are Fail-Open

Files:

- `internal/agent/tradecommand.go`
- `internal/agent/client.go`

Evidence:

- `handleTradeCommand` uses `if existing, ok, _ := c.idempotency.Get(tc.ClientOrderID); ok { ... }`.
- `onOrderEvent` uses `rec, ok, _ := c.idempotency.Get(ev.ClientOrderID)`.

Impact:

- On the submit path, a SQLite read error is treated as "idempotency miss"; the Agent can then write a new pending row and submit to the exchange again for an already accepted/filled `client_order_id`.
- On the async fill path, a SQLite read error is treated as unknown order; the Agent returns before sending `OrderUpdate`, before adding the fill to the `delta_report` buffer, and before updating local state.

Suggested fix:

- Fail closed on `Get` errors before exchange submit; send internal error / reject without submitting.
- In `onOrderEvent`, distinguish not-found from store read failure and do not treat read failure as unknown order.
- Add fake-store error tests for both paths.

### Critical — Expiry / Frozen Rejection Can Override Existing Idempotent Orders

File: `internal/agent/tradecommand.go`

Evidence:

- Frozen latch check runs before idempotency lookup.
- `valid_until_ms` expiry check runs before idempotency lookup.
- Duplicate handling is reached only after both rejection gates.

Impact:

- A delayed replay of an already accepted/filled command can receive `rejected` or `expired` instead of `duplicate_pending` / `duplicate_terminal`.
- SaaS maps `expired` to `TradeStatusCancelled` and `rejected` to `TradeStatusRejected`, then `UpdateTradeStatus` overwrites the TradeRecord unconditionally. This can corrupt a real executed order's lifecycle after replay/reconnect.

Suggested fix:

- Move idempotency lookup before expiry/frozen rejection for any already-known `client_order_id`.
- Keep frozen/expiry rejection only for brand-new commands.
- Add regression tests for replay-after-expiry and replay-while-frozen.

### Major — `spot_executions` Dedup Has No Database Backstop

Files:

- `cmd/saas/agentmsg.go`
- `internal/repository/trade.go`
- `internal/saas/store/models.go`
- `internal/saas/store/migrations/00001_baseline.sql`

Evidence:

- `insertFillIfNew` checks `ExecutionExistsByTrade` or `ExecutionExists`, then calls `InsertSpotExecution`.
- `SpotExecution` has ordinary indexes only.
- The baseline migration creates ordinary indexes on `client_order_id`, `exchange_order_id`, and `trade_id`; there is no unique constraint for either dedup identity.

Impact:

- Concurrent old/new WS connections or concurrent `order_update` / `delta_report` processing can both pass the existence check and insert duplicate fill rows.
- The fresh auto-increment IDs can be folded by the ledger as separate executions, causing double-counted positions and false drift/auto-freeze.

Suggested fix:

- Add DB-level unique indexes:
  - `(client_order_id, trade_id)` where `trade_id != 0`.
  - `(client_order_id, filled_at_exchange_ms)` where `trade_id = 0`.
- Map unique violations to an idempotent no-op in the fill insert path.
- Add a concurrency regression test.

### Major — Reconnect State Sync Does Not Replay Durable Fills / Open Orders

Files:

- `internal/agent/handshake.go`
- `internal/wire/statesync.go`
- `internal/saas/wshub/connection.go`

Evidence:

- `sendStateSyncResponse` always sends empty `OpenOrders` and empty `SinceLastFills`.
- Hub receives state sync but does not parse or recover fills by default.
- Agent comments explicitly say v1 does not retain open orders or since-last fills locally.

Impact:

- `delta_report` protects only same-process buffered fills.
- Agent crash, process restart, or a fill event that failed before entering the buffer is not replayed durably on reconnect.
- This falls short of the protocol comment that state sync should carry missed fills and open orders.

Suggested fix:

- Persist undispatched order events/fills in an idempotency-adjacent durable store.
- Populate `state_sync_response.since_last_fills` and open-order state from durable exchange/idempotency data.
- Have SaaS parse state sync and recover fills through the same dedup chokepoint.

### Medium — `order_update` Replays Can Downgrade Terminal Trade Status

File: `cmd/saas/agentmsg.go`

Evidence:

- `handleOrderUpdate` dedups fills, then unconditionally maps the incoming status and calls `UpdateTradeStatus`.
- `UpdateTradeStatus` does not enforce lifecycle monotonicity.

Impact:

- If a `filled` update is processed, then an older/replayed `partial_filled` update arrives later, the TradeRecord can be downgraded from terminal `filled` to non-terminal `partial_filled`.

Suggested fix:

- Enforce status monotonicity in repository update logic or in the message handler.
- Add tests for terminal-status replay ordering.

Confirmed OK:

- SQLite idempotency store is durable across reopen and has WAL/busy-timeout configured.
- `Put` records a pending row before exchange submit.
- Market/limit immediate fills are tee'd into `delta_report` buffer.
- Async Binance `executionReport` fills include `trade_id`.
- `delta_report` send failure requeues drained fills/errors.
- SaaS fill recovery uses one chokepoint for both `order_update` and `delta_report`.
- Same-ms multi-fill dedup logic correctly prefers `(client_order_id, trade_id)` when `trade_id` is present.

## Stage 4 Re-Review — Database / Persistence / Schema Invariants

Scope:

- Retire CAS protection.
- `spot_executions` unique constraints and fill write path.
- Goose/AutoMigrate drift guard.
- DB-guarded status transitions.

Verification command:

```text
GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/store ./internal/saas/config
```

Result: passed. Integration drift tests were not run; they require a reachable Postgres+TimescaleDB config.

Findings:

### Major — Retire Is Still Not CAS-Protected

Files:

- `internal/repository/champion.go`

Evidence:

- `ChampionRepo.Retire` reads the `ChampionHistory` row by `challenger_id`.
- `applyRetire` rejects already-retired rows only from the in-memory `history.RetiredAt`.
- The write uses `WHERE id = ?`, not `WHERE id = ? AND retired_at IS NULL`.

Impact:

- Two concurrent Retire requests can both read `retired_at IS NULL`, both pass the pure guard, and both update one row successfully.
- The later request overwrites `retired_at`, `retired_by`, and `retire_note`, corrupting retirement audit attribution.
- `RowsAffected == 0` does not catch this race because both UPDATEs can affect one row.

Suggested fix:

- Change the UPDATE predicate to `id = ? AND retired_at IS NULL`.
- Map `RowsAffected == 0` to `api.ErrAlreadyRetired`.
- Add a CAS/concurrency regression test for `ChampionRepo.Retire`.

### Major — `spot_executions` Dedup Still Has No Database Backstop

Files:

- `cmd/saas/agentmsg.go`
- `internal/repository/trade.go`
- `internal/saas/store/models.go`
- `internal/saas/store/migrations/00001_baseline.sql`

Evidence:

- `insertFillIfNew` checks `ExecutionExistsByTrade` or `ExecutionExists`, then calls `InsertSpotExecution`.
- `SpotExecution` has ordinary indexes only.
- The Goose baseline creates ordinary indexes on `client_order_id`, `exchange_order_id`, and `trade_id`; there is no unique constraint for either fill-dedup identity.

Impact:

- Concurrent redelivery, concurrent old/new WS connections, or concurrent `order_update` / `delta_report` handling can both pass the existence check and insert duplicate fill rows.
- The fresh auto-increment IDs can be folded by the ledger as separate executions, causing double-counted positions and false drift/auto-freeze.

Suggested fix:

- Add DB-level partial unique indexes:
  - `(client_order_id, trade_id)` where `trade_id <> 0`.
  - `(client_order_id, filled_at_exchange_ms)` where `trade_id = 0`.
- Map unique violations to an idempotent no-op in the fill insert path.
- Add a concurrency regression test.
- Add both AutoMigrate-path raw DDL in `db.go` and a Goose `00002` migration, then run `TestMigrationsMatchAutoMigrate`.

### Major — Instance Status Transitions Are Not DB-Conditioned

Files:

- `internal/api/handlers.go`
- `internal/repository/instance.go`

Evidence:

- `transitionInstance` reads the instance, computes the next status in the handler, then calls `UpdateStatus`.
- `InstanceRepo.UpdateStatus` updates by `instance_id` only.
- `InstanceRepo.SetActiveChampion` also updates by `instance_id` only.

Impact:

- A stale read can overwrite a concurrent terminal transition. For example, start/stop can write `live` or `paused` after another actor has retired the instance.
- Deploy champion can attach a new champion to a retired instance.

Suggested fix:

- Move state-transition legality into DB predicates: `WHERE instance_id = ? AND status IN (...)`.
- Use `RowsAffected` to distinguish not found / illegal transition / race.
- Guard `SetActiveChampion` with at least `status <> 'retired'`.

Confirmed OK:

- Goose/AutoMigrate routing is explicit: `saas` defaults to Goose, and `saas + migration_mode=automigrate` is rejected by config validation.
- `migrate_drift_test.go` builds one AutoMigrate DB and one Goose DB and compares normalized `pg_dump --schema-only`.
- The drift guard can catch schema-path mismatch, but it cannot infer missing business constraints; the `spot_executions` issue is a missing invariant, not Goose/AutoMigrate drift.

## Stage 3C Re-Review — Business State Consistency

Scope:

- Promote/Retire lifecycle and deploy-state consistency.
- Instance start/stop/deploy stale-read behavior.
- Multi-instance account funding and reconciliation baseline.
- SaaS order status monotonicity.

Verification command:

```text
GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/instance ./internal/saas/store
```

Result: passed after escalation for local `httptest` sockets. The first sandboxed run failed with `listen tcp6 [::1]:0: socket: operation not permitted`; this was environmental, not a business assertion failure.

Findings:

### Major — DeployChampion Does Not Prove The Challenger Is The Active Champion For The Instance

Files:

- `internal/api/validate.go`
- `internal/api/handlers.go`
- `internal/repository/instance.go`
- `internal/saas/instance/manager.go`
- `internal/saas/instance/champion_loader.go`

Evidence:

- `DeployChampionRequest.Validate()` only checks that `challenger_id` is non-empty.
- `DeployChampion` passes that ID straight to `Instances.SetActiveChampion`.
- `InstanceRepo.SetActiveChampion` updates `active_champ_id` by `instance_id` only.
- Live Tick resolves the strategy from `inst.StrategyID`, but loads the gene blob directly from `inst.ActiveChampID`.
- `DefaultChampionGeneLoader` reads the challenger package blob without checking `champion_history.retired_at` or `(strategy_id,pair)` membership.

Impact:

- An operator can deploy a retired champion, a challenger for another pair/strategy, or a never-promoted challenger to a live instance.
- A mismatch can make Tick fail during gene decode/load, or worse, run the wrong gene against the instance's current account and pair.
- Retiring a champion does not currently detach, pause, or block already-deployed instances that still reference that champion ID.

Suggested fix:

- Resolve the target instance during deploy.
- Require `champion_history` to contain the requested `challenger_id` with the same `(strategy_id,pair)` and `retired_at IS NULL`.
- Guard the write with `status <> 'retired'`.
- Define the Retire policy for deployed champions: block while deployed, detach references, or pause affected instances.
- Add regression tests for pair/strategy mismatch, retired champion deploy, never-promoted challenger deploy, and Retire with deployed instances.

### Reconfirmed Open Findings

- Retire is still not CAS-protected: `ChampionRepo.Retire` reads then updates with `WHERE id = ?`, so concurrent Retire calls can overwrite audit fields.
- Instance lifecycle transitions are still not DB-conditioned: start/stop can overwrite a concurrent retired transition, and deploy lacks a retired-status guard.
- Multi-instance account funding still duplicates the full exchange snapshot into each fresh instance under one account, then later aggregates the duplicated expected portfolios.
- SaaS order lifecycle updates are still not monotonic: ack/order_update paths can downgrade a filled order because `TradeRepo.UpdateTradeStatus` writes status unconditionally by `client_order_id`.

Confirmed OK:

- The previous Stage 3B confirmations still hold for `test_mode` effective friction, OOS post-epoch isolation, `decision_status` not containing `retired`, Promote/Retire admin gating, and kill-switch unmanaged/faucet assets not counting toward auto-freeze.

## Stage 4B Re-Review — Persistence Concurrency Invariants

Scope:

- `champion_history` active uniqueness and Retire CAS.
- `spot_executions` idempotent fill writes and DB uniqueness.
- `strategy_instances` start/stop/deploy state writes and `RowsAffected` handling.
- Adjacent funding/import claim patterns where the same read-then-write shape appears.

Verification command:

```text
GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/store
```

Result: passed after escalation for local `httptest` sockets. The first sandboxed run passed `internal/repository` then failed in `internal/api` with `listen tcp6 [::1]:0: socket: operation not permitted`; this was environmental.

Integration/drift follow-up:

- `./config.agent.yaml` is Agent-only config and is not accepted by `internal/saas/config.Load` for DB integration tests.
- `./config.yaml` is the SaaS DB config used for the follow-up.
- Schema drift passed after escalation for local Postgres sockets:
  `GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/saas/store/ -run TestMigrationsMatchAutoMigrate -args -config=/home/l9g/quantlab/config.yaml`
- Repository and `cmd/saas` integration tests passed after escalation for local Postgres sockets:
  `GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml`

Findings:

### Confirmed OK — Active Champion Uniqueness Is DB-Backed

Files:

- `internal/repository/champion.go`
- `internal/saas/store/db.go`
- `internal/saas/store/migrations/00001_baseline.sql`

Evidence:

- `ChampionRepo.Promote` still uses an in-transaction `activeOther` count, but maps insert-time unique violations to `api.ErrActiveChampionExists`.
- `db.go` creates `uq_champion_active` on `(strategy_id, pair)` where `retired_at IS NULL AND deleted_at IS NULL`.
- The goose baseline also contains `uq_champion_active`.

Impact:

- The old count-then-insert race is still handled by the DB; this part remains fixed.

### Major — Retire Is Still Not CAS-Protected

Files:

- `internal/repository/champion.go`

Evidence:

- `Retire` reads `ChampionHistory` by `challenger_id`.
- `applyRetire` rejects an already-retired champion only from the in-memory `history.RetiredAt`.
- The update writes `WHERE id = ?` only.
- `RowsAffected == 0` cannot catch two concurrent Retire calls that both read the row before either writes; both updates can affect the same row.

Impact:

- Two concurrent Retire requests can overwrite retirement audit fields.
- The second request does not get the intended already-retired transition error.

Suggested fix:

- Update with `WHERE id = ? AND retired_at IS NULL`.
- Map `RowsAffected == 0` to `api.ErrAlreadyRetired`.
- Add a concurrent Retire regression test.

### Major — `spot_executions` Dedup Still Has No Database Backstop

Files:

- `cmd/saas/agentmsg.go`
- `internal/repository/trade.go`
- `internal/saas/store/models.go`
- `internal/saas/store/migrations/00001_baseline.sql`

Evidence:

- `insertFillIfNew` does `ExecutionExistsByTrade` or `ExecutionExists`, then inserts.
- `TradeRepo.InsertSpotExecution` is a plain create.
- `SpotExecution` has ordinary indexes on `client_order_id`, `exchange_order_id`, and `trade_id`, but no unique dedup key.
- The goose baseline likewise has ordinary indexes only.

Impact:

- Concurrent `order_update` and `delta_report`, or old/new WS replay paths, can both pass the existence check and insert the same fill.
- `NewExecutionsForInstance` uses monotonically increasing `spot_executions.id`, so duplicate rows can be folded as separate executions.

Suggested fix:

- Add partial unique indexes for `(client_order_id, trade_id) WHERE trade_id <> 0` and `(client_order_id, filled_at_exchange_ms) WHERE trade_id = 0`.
- Treat unique violations as idempotent no-op in the fill insert path.
- Add a concurrent duplicate-fill regression test.
- Update both AutoMigrate raw DDL and goose migrations, then run the drift test.

### Major — Instance State Writes Are Still Not DB-Conditioned

Files:

- `internal/api/handlers.go`
- `internal/repository/instance.go`

Evidence:

- `transitionInstance` reads the instance, computes the next status in the handler, then calls `UpdateStatus`.
- `UpdateStatus` writes `status` with `WHERE instance_id = ?` only.
- `SetActiveChampion` writes `active_champ_id` with `WHERE instance_id = ?` only.

Impact:

- A stale start/stop read can overwrite a concurrent terminal retired transition.
- Deploy can attach a champion to a retired instance.

Suggested fix:

- Move transition legality into repository/DB predicates.
- Use `RowsAffected` to distinguish not found, illegal transition, and race.
- Guard deploy with at least `status <> 'retired'`, and combine with the Stage 3C active/unretired champion check.

### Medium — Genesis Funding Claim Happens After The Seed Append

Files:

- `cmd/saas/agentmsg.go`
- `internal/repository/instance.go`
- `internal/repository/portfolio.go`

Evidence:

- `fundInstance` appends the genesis `PortfolioState` first.
- It then calls `MarkFunded`.
- `MarkFunded` uses `WHERE instance_id = ? AND funded_at_ms IS NULL`, but returns only an error, not whether it won the claim.
- `PortfolioState` is keyed by `(instance_id, now_ms)`, so two concurrent reports with different `nowMs` can both leave seed rows.

Impact:

- The code intentionally tolerates a rare double-seed, but the latest seed can become the effective baseline by timestamp rather than by a single successful funding claim.
- This is lower severity than fill dedup because it does not by itself create a new execution row, but it weakens funding auditability and baseline determinism.

Suggested fix:

- Claim funding before appending the seed, or wrap claim and seed in one transaction.
- Return `(claimed bool, error)` from `MarkFunded` or use `UPDATE ... RETURNING`.
- Only the caller that wins the claim should append the genesis portfolio.
- Add a concurrent double-funding regression test.

### Low — Import Job Claiming Is Single-Worker-Only

Files:

- `internal/repository/import_job.go`
- `cmd/saas/main.go`

Evidence:

- The repo comments explicitly state a single background worker model.
- `cmd/saas/main.go` only wires imports for non-`saas` roles.
- `NextQueued` selects a queued job without row locking.
- `MarkRunning` updates by `job_id` only.

Impact:

- This is acceptable under the current single-worker, non-production import wiring.
- If import workers are ever run across multiple replicas, the same queued job can be claimed by more than one worker.

Suggested fix:

- Before horizontal import workers, replace read-then-mark with an atomic claim using `UPDATE ... WHERE status='queued' ... RETURNING` or `SELECT FOR UPDATE SKIP LOCKED`.
- Make `MarkRunning` queued-only and handle `RowsAffected == 0`.

## 2026-06-06 Addendum — Live Reconciliation / Multi-Instance Account Risk

Scope:

- Re-reviewed whether multiple non-retired instances under one `account_id` can each be genesis-funded from the same whole exchange-account snapshot.
- Re-reviewed whether reconcile sums duplicated expected portfolios and can false-trigger managed-asset drift / auto-freeze.

Conclusion:

- The Stage 3B account-level reconciliation finding is still open.
- This is an account-capital ownership invariant gap, not a `maxFlaggedDriftBps` managed-asset filter bug.
- No source code was changed in this pass.

Evidence:

- `internal/saas/store/db.go:134` creates `idx_inst_unique_active` on `(owner_user_id, strategy_id, pair, account_id) WHERE status != 'retired'`; it does not enforce one non-retired instance per `account_id`.
- `internal/repository/instance.go:71` returns all non-retired instances for the account. The integration test at `internal/repository/instance_integration_test.go:45` intentionally creates one live, one idle, and one paused instance under the same account across distinct pairs, then expects all three to be returned.
- `cmd/saas/agentmsg.go:421` feeds those rows into `reconcile`.
- `cmd/saas/agentmsg.go:518` calls `fundInstance` for each unfunded instance, and `cmd/saas/agentmsg.go:610` builds the seed from the same whole `actual` map. `cmd/saas/agentmsg_test.go:273` confirms the base asset and USDT are seeded as whole balances.
- `cmd/saas/agentmsg.go:533` then sums every funded portfolio into one `expected` map. `cmd/saas/agentmsg.go:573` derives managed assets from that map, and `cmd/saas/agentmsg.go:715` advances auto-freeze from the resulting managed drift.
- Existing genesis funding integration coverage is single-instance only (`cmd/saas/genesis_funding_integration_test.go:36`); it does not cover same-account multi-instance duplicate baseline.

Impact:

- If two non-retired instances under one account are funded from the same exchange snapshot, the next unchanged snapshot can look underfunded relative to duplicated expected BTC/USDT.
- Because BTC/USDT are managed assets for the instances, the false discrepancy can count toward the auto-freeze debounce and halt a live agent.

Suggested fix:

- Either enforce a v1 invariant that each `account_id` has at most one non-retired instance, or introduce explicit per-instance capital allocation / managed-balance ownership before genesis funding.
- Add a regression test for the chosen invariant: either the second non-retired instance is rejected, or two fresh instances funded from one unchanged account snapshot do not produce discrepancy / auto-freeze.

Verification:

- `GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/store` passed.

## 2026-06-06 Addendum — DeployChampion State Consistency

Scope:

- Re-reviewed whether `DeployChampion` / `SetActiveChampion` proves the requested challenger is the active, unretired champion for the target instance's `(strategy_id,pair)`.
- Re-reviewed whether deploy can write `active_champ_id` on a retired instance.

Conclusion:

- The DeployChampion major remains open.
- The issue has two coupled parts: missing active/unretired champion membership validation and missing retired-instance write guard.
- No source code was changed in this pass.

Evidence:

- `internal/api/validate.go:139` only validates that `challenger_id` is non-empty.
- `internal/api/handlers.go:697` passes that ID directly to `Instances.SetActiveChampion`; it does not fetch the target instance and does not consult `champion_history`.
- `internal/repository/instance.go:114` updates `strategy_instances.active_champ_id` with `WHERE instance_id = ?` only.
- `internal/saas/store/models.go:336` has a terminal `retired` instance status, and `internal/saas/store/models.go:337` stores `active_champion_id` as a plain indexed string.
- `internal/repository/champion.go:191` and `internal/saas/store/db.go:171` define the active champion concept (`retired_at IS NULL`, with a partial unique index), but deploy does not use that active row to prove the requested challenger belongs to the instance's `(strategy_id,pair)`.
- `internal/saas/instance/manager.go:298` resolves the strategy from the instance, then loads the gene for `inst.ActiveChampID`; `internal/saas/instance/champion_loader.go:35` loads the challenger package blob directly and does not check `champion_history`.
- Existing deploy handler coverage only checks the happy path where `active_champ_id` is set; it does not cover retired instance, wrong pair/strategy, retired champion, or never-promoted challenger rejection.

Impact:

- A retired champion, a challenger for another pair/strategy, or a never-promoted challenger can be attached to an instance.
- Deploy can still mutate a retired instance.
- Tick can fail on load/decode or run a gene that was not promoted for the instance's strategy/pair.

Suggested fix:

- Replace the thin `SetActiveChampion(instanceID, challengerID)` write with a deploy operation that validates instance + champion_history together.
- Require the target instance status to be non-retired.
- Require `champion_histories.challenger_id = req.ChallengerID`, matching `strategy_id` and `pair`, and `retired_at IS NULL`.
- Use a transaction or a single conditional SQL statement and map `RowsAffected == 0` / not found into 404 or 422.
- Add regression tests for retired instance, retired champion, wrong pair/strategy, and never-promoted challenger deployment.

Verification:

- `GOCACHE=/tmp/quantlab-go-cache go test ./internal/api ./internal/repository ./internal/saas/instance` passed.

## 2026-06-06 Addendum — Regression Test Follow-up

Scope:

- Re-reviewed whether every active high/major finding has a permanent regression test.
- Priority checks: Raw validation fail-closed, Retire CAS, fill dedup concurrency, order terminal-status replay, and multi-instance account false-freeze.

Conclusion:

- The current test baseline passes, including the existing Postgres-backed integration tests.
- Most active high/major findings still lack the negative regression tests that should accompany the eventual source fixes.
- Several areas have useful adjacent coverage, but the existing tests would not fail on the specific bugs recorded in the review.

Verification:

- `GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/engine ./internal/verification ./internal/repository ./internal/api ./internal/agent ./cmd/saas` passed.
- `GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml` passed.

Coverage matrix:

| Finding(s) | Coverage status | Evidence and missing regression |
|---|---|---|
| A-1 architecture boundary | Missing | `verification.ComputeSharpeStats` has function tests, and prior review used manual grep/go-list checks, but there is no CI-style import-boundary test that fails on `internal/strategies/sigmoid_v1 -> internal/verification`. |
| G2C-1 / G-1 Raw fail-closed | Missing | `internal/resultpkg/validate_test.go:16` covers `CrucibleResult` state mutexes, and `internal/engine/engine_test.go:186` / `:216` cover valid Raw paths. No test injects `raw == nil` or invalid Raw from an adapter and asserts RunEpoch / best re-evaluate fails closed. |
| G2C-2 Raw cascade contract | Missing | There are no Raw-level tests for empty windows, duplicate windows, non-canonical order, `WindowOOS` inside IS raw, invalid Fatal -> skipped cascade, or `SkippedBy` without a real earlier Fatal. |
| G2C-3 verification replay boundary | Missing | `internal/verification/review_test.go` covers OK, mismatch, and hash/fingerprint short-circuit paths, but not invalid replay Raw returning a Go error before aggregation. |
| D-1 active champion uniqueness | Partial | `internal/saas/store/db_integration_test.go:79` verifies the `uq_champion_active` index lands on the expected table and keeps the partial predicate. It does not try two active champion rows or concurrent Promote calls. |
| C-1 / D4C-1 DeployChampion consistency | Missing | `internal/api/instance_handlers_test.go:240` only checks the happy path. There are no retired instance, retired champion, wrong pair/strategy, or never-promoted challenger rejection tests. |
| C-2 / D4B-1 Retire CAS | Missing | `internal/repository/champion_test.go:145` only tests pure `applyRetire` with an already-retired in-memory row. There is no repository-level CAS / `RowsAffected==0` / double-Retire attribution test. |
| C-3 / D4B-3 instance lifecycle CAS | Partial | `internal/api/instance_handlers_test.go:203` proves start refuses a currently retired instance. It does not prove stale start/stop reads cannot overwrite a concurrent retired state, and deploy still lacks a retired guard test. |
| L-1 / C-4 multi-instance account false-freeze | Missing | `cmd/saas/genesis_funding_integration_test.go:47` is single-instance only. `internal/repository/instance_integration_test.go:20` proves multiple non-retired instances under one account are returned. No test proves two fresh same-account instances do not false-freeze after whole-account genesis funding, or that the second instance is rejected. |
| D4B-2 fill dedup concurrency / DB backstop | Partial | `cmd/saas/agentmsg_dedup_integration_test.go:33` covers sequential order_update replay, cross-channel duplicate, and same-ms distinct trade IDs. `internal/repository/reconciliation_integration_test.go:22` covers the existence query. There is no concurrent check-then-insert test and no DB unique-violation-as-idempotent-no-op test. |
| S5B-1 replay after terminal | Partial | `internal/agent/agent_test.go:409` covers immediate duplicate terminal. `internal/agent/agent_test.go:448` covers brand-new expired rejection, and `internal/agent/kill_switch_test.go:17` covers brand-new frozen rejection. Missing: a filled/terminal `client_order_id` replayed after expiry or while frozen must still return `duplicate_terminal`. |
| S5B-2 submit-path idempotency read error | Missing | `internal/agent/idempotency_sqlite_test.go` covers normal Get/Put/upsert behavior. There is no fake-store read-error test proving `handleTradeCommand` fails closed without submit and without pending upsert. |
| S5B-3 exchange-event idempotency read error | Missing | `internal/agent/agent_test.go:620` covers a known fill event, and `:681` covers unknown order drop. There is no read-error test proving a real fill is not treated as unknown and dropped before order_update / delta_report buffering. |
| S5B-4 / C-5 terminal status monotonicity | Partial | `cmd/saas/agentmsg_test.go:11` covers status mapping and duplicate-terminal no-op; `internal/repository/trade_integration_test.go:246` covers `MarkPartialIfPending`, and `:286` covers orphan sweep not changing terminal rows. Missing: generic `UpdateTradeStatus` / ack / order_update replay cannot downgrade `filled` to `partial_filled`, `cancelled`, or `rejected`. |

Recommended test order:

1. Raw fail-closed and Raw cascade contract tests.
2. Retire CAS and terminal-status monotonicity tests.
3. Agent idempotency read-error and replay-after-terminal tests.
4. Fill dedup DB uniqueness / concurrency tests.
5. Multi-instance account false-freeze test.
6. DeployChampion scope / retired guard tests.
7. Import-boundary CI guard for A-1.

## 2026-06-06 Addendum — Raw Validation / GA Boundary Follow-up

Scope:

- Re-reviewed `internal/engine/engine.go`, `internal/resultpkg/validate.go`, `internal/verification/review.go`, `internal/verification/oos.go`, and `internal/verification/stress.go`.
- Rechecked whether fail-closed and Raw-level cascade negative tests now exist.

Conclusion:

- G2C-1 / G2C-2 / G2C-3 remain open; OOS/stress remain part of the same adapter-output boundary gap.
- The current package tests pass, but they do not include the missing negative cases.
- No source code was changed in this pass.

Evidence:

- `internal/engine/engine.go:414` calls `adapter.Evaluate`; `engine.go:419` immediately aggregates `raw.Windows`. There is no `raw == nil` or `raw.Validate()` guard.
- `internal/engine/engine.go:310` re-evaluates the best gene and stores `bestRaw` without validation.
- `internal/resultpkg/validate.go:69` only checks nil receiver, nil `Windows`, then each `CrucibleResult.Validate()`. It does not enforce Raw-level empty/duplicate/order/OOS/cascade invariants.
- `internal/resultpkg/enums.go:145` still treats `WindowOOS` as a valid window name, so per-window validation alone cannot reject OOS inside IS raw.
- `internal/verification/review.go:126` replays raw output, then `review.go:130` aggregates it directly.
- `internal/verification/oos.go:154` reads raw output and `oos.go:158` immediately checks `len(raw.Windows)`, so nil raw is not fail-closed.
- `internal/verification/stress.go:53` reads raw output and `stress.go:57` treats `raw == nil` as a no-series skip.

Existing tests checked:

- `internal/resultpkg/validate_test.go:16` covers `CrucibleResult` three-state mutexes, not Raw-level sequence invariants.
- `internal/engine/engine_test.go:186` and `:216` cover valid best raw output and re-aggregation determinism.
- `internal/verification/review_test.go:41`, `:68`, and `:97` cover OK/mismatch/hash gate paths.
- `internal/verification/oos_test.go` covers insufficient data, OOS result colors, adapter reset, warmup, and NewAdapter error.
- `internal/verification/stress_test.go` covers normal captured returns, no-series skip, and NewAdapter error.

Missing permanent negative tests:

- Engine adapter returns nil raw: RunEpoch must return an error, not panic in a worker.
- Engine adapter returns invalid raw: RunEpoch and best-gene re-evaluate must fail before aggregation.
- Raw validator rejects empty windows, duplicate windows, non-canonical order, `WindowOOS` in IS raw, invalid Fatal -> skipped cascade, and `SkippedBy` without an earlier Fatal.
- RunReview invalid replay raw returns a Go error, not mismatch or OK.
- RunOOS nil/invalid raw fails closed.
- RunStress invalid non-nil raw does not silently skip.

Verification:

- `GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/fitness ./internal/engine ./internal/verification ./internal/strategies/sigmoid_v1 ./internal/strategies/toy` passed.
