# QuantLab Architecture Improvement Manual

Date: 2026-06-07

Purpose: turn the architecture retrospective into a practical guide for the
next major refactor. This document is not a generic Go essay. It maps the
lessons directly onto QuantLab's current modules, current code examples, and
known open risks.

Before using this during a refactor, refresh these local sources:

- `docs/系统总体拓扑结构.md`
- `docs/code-review-plan.md`
- `docs/memory-review-prep-status.md`
- `docs/codex-readonly-review-2026-06-05.md`
- current `git status --short`

## Executive Summary

QuantLab's basic architecture is sound: it already has a three-end deployment
model, a strategy/engine boundary, a SaaS/Agent split, domain-oriented Go
packages, durable review records, and a serious reproducibility culture.

The project has reached the second stage of complex-system engineering:
packages and interfaces exist, but several critical invariants are still
enforced by comments, handler order, application-level checks, or tests that
cover only friendly paths. The next major refactor should not primarily add
features. It should move invariants into executable boundaries: validators,
state machines, repository predicates, database constraints, and permanent
negative tests.

The current highest-value refactor targets are:

1. Fail closed on invalid `RawEvaluateResult` before any score aggregation.
2. Fail closed on Agent idempotency-store read errors before exchange submit
   or fill processing.
3. Enforce monotonic trade-status transitions in SaaS persistence.
4. Add database unique backstops for `spot_executions` fill deduplication.
5. Decide and encode the v1 account ownership invariant. Recommended v1:
   one non-retired instance per `(owner_user_id, account_id)`.
6. Extract business logic out of `cmd/saas/agentmsg.go` into service packages.

## Current Architecture Facts

The design document already states the right high-level shape:

- Three physical ends: Lab, SaaS, Agent.
- SaaS owns HTTP API, GA worker pool, Promote entry points, WebSocket Hub,
  Cron Tick, Postgres, and Redis.
- Agent owns exchange credentials and translates SaaS `TradeCommand` into
  exchange REST/WS calls.
- Lab is a local/research mode, not a different strategy semantics.

The most important existing architecture promise is the strategy boundary:

- `internal/strategy` defines contracts.
- `internal/strategies/<name>` implements strategy behavior.
- `internal/engine`, `internal/fitness`, `internal/verification`,
  `internal/data`, `internal/repository`, `internal/api`, `internal/resultpkg`,
  and `internal/quant` are engine/service/lower layers.
- Engine must not reach into concrete strategy internals.
- Strategy `Step()` must not read wall clock or perform I/O.

This is a strong foundation. The main problem is not missing package names.
The problem is that some semantic contracts are still weaker in code than they
are in prose.

## What Was Done Well

### 1. The SaaS/Agent split is the right security boundary

Agent keeps exchange API keys local. SaaS sends intent and receives state. That
is the right design for a trading system where cloud compromise must not imply
exchange credential compromise.

Current code examples:

- `internal/wire/tradecommand.go`
- `internal/wire/orderupdate.go`
- `internal/wire/deltareport.go`
- `internal/agent/tradecommand.go`
- `cmd/saas/dispatcher.go`
- `cmd/saas/agentmsg.go`

Keep this split. Do not move exchange credentials, order signing, or exchange
request construction into SaaS.

### 2. Strategy/live isomorphism is the right core abstraction

`internal/strategy/contract.go` states that the same `Step()` runs in backtest
and live mode. This is one of QuantLab's best architecture decisions.

Keep:

- `StrategyInput.NowMs` as the only time source for strategy code.
- `StrategyInput.Portfolio` as the live/backtest portfolio bridge.
- `StrategyOutput` as strategy intent, not exchange execution.
- `internal/saas/instance.Manager.Tick` as the live orchestration point.

Avoid:

- `if backtest` / `if live` branches inside strategy code.
- Strategy code that imports Agent, repository, API, or exchange packages.
- Strategy code that writes aggregate scores directly.

### 3. Repository and migration boundaries are already meaningful

`internal/repository` is the only intended GORM-facing layer, while
`internal/saas/store` owns models and migration bootstrapping. That is a good
Go shape. The lesson is to make database invariants as strong as the model
requires, not to discard this separation.

Keep:

- Repository methods narrow and domain-named.
- Goose migrations in `internal/saas/store/migrations/`.
- AutoMigrate/Goose drift checks.

Add:

- Business uniqueness checks that drift tests cannot prove.
- Concurrent-writer integration tests for invariants that matter under replay.

### 4. Review records and memory docs are part of the architecture

For this project, review docs are not noise. They preserve why a boundary
exists, what was verified, which findings were fixed, and which remain open.
During refactors, keep this practice. The dangerous failure mode is stale prose
that gets copied forward without being reconciled against current source.

## Core Lessons And How To Apply Them

### Lesson 1: Prose boundaries must become executable boundaries

The current architecture document says the strategy layer must not depend on
engine-layer details. That is correct, but a comment cannot prevent import
drift.

Current code lesson:

- Production `internal/engine` is mostly clean.
- Known boundary noise remains around concrete strategy code depending on
  higher-level verification helpers, and engine tests importing concrete
  strategies.

Refactor rule:

1. Move shared math/stat helpers down to a lower-level package such as
   `internal/quant` or a narrow result/stat package.
2. Keep concrete strategy registration in one composition root, currently
   `internal/saas/epoch/registry.go`.
3. Make import-boundary checks executable.

Suggested CI check:

```bash
GOCACHE=/tmp/quantlab-go-cache go list -f '{{.ImportPath}} {{join .Imports " "}}' ./internal/...
```

Then enforce:

- `internal/engine` must not import `internal/strategies`.
- `internal/fitness` must not import `internal/strategies`.
- `internal/verification` should not be a dependency of concrete strategy
  code unless the helper is genuinely verification-layer behavior.
- `cmd/...` may wire implementations but should not own reusable business
  policy.

Acceptance criteria:

- Boundary tests fail when a forbidden import appears.
- Any allowed exception is named as a composition-root exception.
- The exception list is short and reviewed.

### Lesson 2: Go interfaces describe shape, not semantics

`strategy.Adapter.Evaluate` returns `*resultpkg.RawEvaluateResult`. The type
shape says nothing about canonical window order, cascade semantics, OOS/IS
mode, duplicate windows, or whether `Score.Value` is valid for a non-fatal,
non-skipped result.

Current code lesson:

- `internal/resultpkg/validate.go` has `RawEvaluateResult.Validate()`.
- The current validator checks each `CrucibleResult` but not enough Raw-level
  sequence semantics.
- `internal/engine/engine.go` still aggregates `raw.Windows` immediately after
  `adapter.Evaluate`.
- `internal/verification/review.go`, `internal/verification/oos.go`, and
  `internal/verification/stress.go` are also adapter-output boundaries.

Refactor rule:

Every external or cross-layer result must be validated at the receiver before
business meaning is derived from it.

For `RawEvaluateResult`, add mode-aware validators next to the existing
`validate.go`, for example:

```go
// internal/resultpkg/validate_raw.go
func (r *RawEvaluateResult) ValidateForIS() error
func (r *RawEvaluateResult) ValidateForOOS() error
func (r *RawEvaluateResult) ValidateForStress() error
```

These belong in `internal/resultpkg`, not in a child package such as
`internal/resultpkg/rawvalidate`. They are extensions of the
`RawEvaluateResult` contract, and there is no import cycle that forces a
package split. Keep higher-level workflow policy outside resultpkg; for
example, `RunStress` may still decide that a valid raw result with no captured
returns means "skip stress".

The IS validator should reject:

- `raw == nil`
- nil `Windows`
- empty `Windows`
- duplicate windows
- non-canonical order
- `WindowOOS` in IS raw
- non-fatal/non-skipped windows with nil score
- skipped windows without a real earlier fatal cause
- fatal windows after a skipped cascade has already begun

Code paths to update:

- `internal/engine/engine.go`: hot-loop `evaluatePopulation`
- `internal/engine/engine.go`: best-gene re-evaluation
- `internal/verification/review.go`: replay review
- `internal/verification/oos.go`: OOS run
- `internal/verification/stress.go`: stress run

Acceptance criteria:

- Invalid raw output returns a Go error before `fitness.AggregateScoreTotal`.
- Tests prove invalid raw cannot silently change score or become a replay
  mismatch.
- `sigmoid_v1` still passes canonical producer tests.

### Lesson 3: Side-effect paths must fail closed

For trading systems, "store read failed" is not equivalent to "record not
found". Treating those as the same condition is a side-effect safety bug.

Current code lesson:

- `internal/agent/tradecommand.go` checks frozen/expiry before idempotency
  lookup for known `client_order_id`.
- It also ignores the error return from `idempotency.Get`.
- `internal/agent/client.go` ignores the error return from `idempotency.Get`
  in `onOrderEvent`.

Why this matters:

- A delayed replay of an already accepted or filled order can be rejected as
  expired/frozen instead of reported as duplicate terminal.
- A SQLite read error can be treated as a cache miss, followed by pending
  upsert and possible exchange submit.
- A real fill event can be dropped before `OrderUpdate`, before delta_report
  buffering, and before local idempotency status update.

Refactor rule:

Before any external side effect, reads of authoritative local state must have
three outcomes:

- found: apply duplicate or existing-state behavior
- not found: process as new
- read error: stop the side effect and surface an internal error

Do not collapse read error into not found.

Suggested Agent order:

1. Decode `TradeCommand`.
2. Read idempotency record.
3. If read error, send internal error or fail closed without exchange submit.
4. If found, return `duplicate_pending` or `duplicate_terminal`, regardless of
   frozen/expired state.
5. If not found, apply frozen/expiry/new-command validation.
6. Pre-record pending.
7. Submit to exchange.
8. Persist accepted/filled lifecycle.

Acceptance criteria:

- Filled command replay after `valid_until_ms` returns duplicate terminal.
- Filled command replay while Agent is frozen returns duplicate terminal.
- Fake idempotency read error prevents exchange submit.
- Fake idempotency read error in `onOrderEvent` does not silently drop fill
  semantics as unknown order.

### Lesson 4: Database constraints own concurrency invariants

Application-level check-then-insert is not a concurrency invariant. It can
pass sequential tests and fail under replay or concurrent connections.

Current code lesson:

- `cmd/saas/agentmsg.go:insertFillIfNew` checks `ExecutionExistsByTrade` or
  `ExecutionExists`.
- It then calls `InsertSpotExecution`.
- `internal/repository/trade.go` currently documents that `SpotExecution` has
  no unique index for the dedup identity.
- `internal/saas/store/models.go` indexes `client_order_id`, `exchange_order_id`,
  and `trade_id`, but ordinary indexes are not uniqueness guarantees.

Refactor rule:

Fill dedup must be backed by database uniqueness. The application may still
check first for fast-path logging, but correctness must survive two writers
arriving at the same time.

Required schema backstops:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS uq_spot_exec_client_trade
ON spot_executions (client_order_id, trade_id)
WHERE trade_id <> 0;

CREATE UNIQUE INDEX IF NOT EXISTS uq_spot_exec_client_fill_ms_no_trade
ON spot_executions (client_order_id, filled_at_exchange_ms)
WHERE trade_id = 0;
```

Required code behavior:

- `InsertSpotExecution` or `insertFillIfNew` must map unique violations to an
  idempotent no-op.
- Goose migration and AutoMigrate raw DDL must stay in sync.
- Drift tests must pass, but drift tests alone are not enough.

Acceptance criteria:

- Concurrent duplicate fill insert test inserts exactly one row.
- Duplicate unique violation does not return an operational error to the
  message handler.
- `go test -tags=integration ./internal/saas/store/ -run TestMigrationsMatchAutoMigrate -args -config=/home/l9g/quantlab/config.yaml`
  passes.
- `go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml`
  passes.

### Lesson 5: State machines need transition rules, not blind updates

Orders, instances, champions, funding, and kill-switch state are state
machines. Treating them as plain CRUD rows leads to stale writes and backward
movement.

Current code lesson:

- Instance status CAS and deploy champion guard have already been improved.
- `ChampionRepo.Retire` now has a retired-at CAS guard.
- `InstanceRepo.UpdateStatus` now uses `(from, to)` semantics.
- `InstanceRepo.SetActiveChampion` now requires a non-retired instance plus a
  matching active/unretired champion.
- Trade status is still weaker: `TradeRepo.UpdateTradeStatus` updates by
  `client_order_id` without a monotonic transition predicate.

Refactor rule:

Every lifecycle table should have an explicit state transition map and a
repository method that enforces it.

Example trade-status order:

```text
pending < partial_filled < filled
pending < cancelled
pending < rejected
partial_filled < cancelled
partial_filled < rejected
filled is terminal and cannot move backward
cancelled is terminal and cannot move backward
rejected is terminal and cannot move backward
```

The exact map is a product decision. Once decided, encode it in one place.

Implementation options:

- Repository method `UpdateTradeStatusMonotonic`.
- SQL predicate based on status rank.
- Transition table in Go plus conditional write.

Acceptance criteria:

- A replayed `partial_filled`, `cancelled`, or `rejected` cannot downgrade
  `filled`.
- `AckStatusExpired` cannot cancel a previously filled trade record.
- Duplicate terminal ack remains no-op.
- Tests cover legal and illegal transitions.

### Lesson 6: Product invariants must precede schema freedom

Do not let the schema represent product states that the rest of the system
cannot model.

Current code lesson:

- `idx_inst_unique_active` allows multiple non-retired instances for the same
  account if strategy or pair differs.
- `InstanceRepo.ListByAccount` returns all non-retired instances.
- `cmd/saas/agentmsg.go:fundInstance` seeds each fresh instance from the whole
  exchange-account snapshot.
- `cmd/saas/agentmsg.go:reconcile` later sums all instance portfolios into
  one account-level expected map.

This is inconsistent unless the system has per-instance capital allocation.

Recommended v1 decision:

Enforce at most one non-retired instance per `(owner_user_id, account_id)`.

Suggested schema:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS uq_instance_one_live_per_account
ON strategy_instances (owner_user_id, account_id)
WHERE status != 'retired';
```

If product chooses multi-instance per account instead, the refactor is larger:

- Add explicit allocation ownership.
- Decide whether allocation is by base asset, USDT budget, percent, or lot.
- Change genesis funding to seed only the owned allocation.
- Change reconcile to compare expected/actual per ownership slice.
- Prove two fresh same-account instances do not duplicate the same balance.

Acceptance criteria for recommended v1:

- Creating a second non-retired instance for the same `(owner_user_id,
  account_id)` returns conflict.
- Retired instance does not block new instance creation.
- Existing single-instance genesis funding tests still pass.
- Multi-instance same-account false-freeze test proves the second instance is
  rejected before funding/reconcile can duplicate the balance.

### Lesson 7: `cmd/...` should wire, not own business policy

`cmd/saas/agentmsg.go` is currently too dense for long-term maintainability.
It handles:

- WebSocket message hook dispatch
- ack persistence
- order_update persistence
- fill parsing and dedup
- delta_report recovered fills
- position parsing
- reconciliation math
- discrepancy persistence
- genesis funding
- auto-freeze debounce and dispatch
- audit integration

This is understandable for a prototype, but it is a poor final architecture.

Refactor rule:

Move reusable business behavior into packages under `internal/saas/...`.
Keep `cmd/saas` as construction and wiring.

Suggested package split:

```text
internal/saas/execution
  - HandleAck
  - HandleOrderUpdate
  - InsertFillIfNew
  - trade status transition policy

internal/saas/reconciliation
  - ParsePositions
  - ReconcilePositions
  - BuildManagedSet
  - PersistDiscrepancies

internal/saas/funding
  - BuildSeedPortfolio
  - ClaimAndFundInstance
  - account ownership invariant

internal/saas/risk
  - AutoFreeze debounce
  - kill-switch trigger policy

cmd/saas
  - config
  - dependency construction
  - route/hub setup
  - signal/shutdown
```

Do not create abstract "manager" packages with vague names. Each package
should own one business invariant.

Acceptance criteria:

- `cmd/saas/agentmsg.go` becomes a thin adapter from wire envelope to service.
- New packages have DB-free unit tests for pure policy.
- Repository integration tests cover persistence invariants.
- No new package imports `cmd/saas`.

### Lesson 8: Negative tests are architecture, not just QA

The major missing tests are not random edge cases. They are executable
architecture boundaries.

Test categories to add before or during the refactor:

1. Raw validation fail-closed tests.
2. Agent idempotency read-error tests.
3. Replay-after-terminal tests.
4. Trade-status monotonicity tests.
5. `spot_executions` concurrent dedup tests.
6. Same-account multi-instance rejection or allocation tests.
7. Import-boundary tests.
8. State-sync recovery tests once durable replay is implemented.

Prefer tests that create bad-world scenarios:

- storage read error
- stale message
- duplicated envelope
- duplicate fill with fresh message ID
- concurrent insert race
- invalid strategy output
- reconnect after missed fill
- product state that schema permits but business logic cannot safely handle

Happy-path tests are still useful, but they should not be mistaken for
invariant coverage.

### Lesson 9: Temporary assumptions need an owner and an exit path

The project uses `[INVENTED v1]` labels in architecture docs. That is useful
when moving fast, but an invented default must not become invisible product
truth.

Refactor rule:

Every invented assumption should become one of:

- accepted product decision
- rejected design
- explicit temporary constraint with a removal condition
- open question blocking a richer model

Current assumptions that need decisions:

- one instance per exchange account versus per-instance capital allocation
- whole-balance genesis funding
- Retire policy for already-deployed champions: block, detach, or pause
- state_sync replay depth and retention window
- whether SaaS ledger float64 is monitoring-only or can ever become settlement

Do not loosen schema or handlers until the corresponding product invariant is
decided.

## Refactor Roadmap

Use this order. It front-loads correctness before package movement.

### Phase 0: Baseline And Guardrails

Goal: make sure the refactor starts from current truth, not stale memory.

Steps:

1. Read `docs/memory-review-prep-status.md`.
2. Read `docs/code-review-plan.md`.
3. Run `git status --short`.
4. Record which findings are fixed versus open.
5. Run focused package tests before changing code.

Suggested baseline:

```bash
GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/fitness ./internal/engine ./internal/verification ./internal/strategies/sigmoid_v1 ./internal/agent ./internal/repository ./cmd/saas ./internal/saas/store
```

If local socket restrictions affect `cmd/saas` tests, rerun with appropriate
approval rather than treating the result as a source failure.

### Phase 1: Strategy/Evaluation Contract Hardening

Goal: prevent invalid strategy/adapter output from becoming a score.

Steps:

1. Upgrade `RawEvaluateResult.Validate()` and add mode-specific validators in
   `internal/resultpkg/validate_raw.go`.
2. Wire validation immediately after every `adapter.Evaluate`.
3. Reject nil raw before dereferencing `raw.Windows`.
4. Add negative tests for invalid raw.
5. Move shared stats helpers out of strategy-to-verification dependency paths.
6. Add import-boundary CI checks.

Do not change GA scoring math in this phase unless a test proves the old math
was wrong. This phase is about guarding the boundary.

### Phase 2: Live Order Idempotency And Status Monotonicity

Goal: make order lifecycle safe under replay, storage errors, and stale events.

Steps:

1. Change Agent `handleTradeCommand` so known duplicates are recognized before
   frozen/expiry rejection.
2. Treat idempotency-store read errors as hard failures before submit.
3. Split `onOrderEvent` not-found from read-error.
4. Add trade status transition policy in SaaS.
5. Replace blind `UpdateTradeStatus` with monotonic update behavior.
6. Add replay-after-terminal and read-error tests.

Do not begin package extraction before these behaviors are pinned by tests.

### Phase 3: Fill Persistence And DB Backstops

Goal: make each exchange fill fold into the ledger at most once, even under
concurrent writers.

Steps:

1. Add partial unique indexes for `spot_executions`.
2. Update AutoMigrate raw DDL and Goose migration.
3. Map unique violations to idempotent no-op.
4. Add concurrent integration tests.
5. Run drift and DB-backed integration verification.

Suggested DB verification:

```bash
GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/saas/store/ -run TestMigrationsMatchAutoMigrate -args -config=/home/l9g/quantlab/config.yaml
GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml
```

### Phase 4: Account Ownership Model

Goal: remove the contradiction between multi-instance schema freedom and
whole-account genesis funding.

Recommended v1 path:

1. Add one-account-one-non-retired-instance unique index.
2. Map unique violation to API conflict.
3. Add repository/API tests.
4. Add a negative test proving a second same-account instance cannot reach
   funding/reconcile.

Alternative richer path:

1. Design allocation schema.
2. Add ownership fields/tables.
3. Change funding to seed only owned capital.
4. Change reconcile to compare owned slices.
5. Add multi-instance no-false-freeze tests.

Choose one path before touching broad live reconciliation code.

### Phase 5: Extract SaaS Business Services

Goal: shrink `cmd/saas/agentmsg.go` and make policy testable.

Order:

1. Extract pure reconciliation math first.
2. Extract auto-freeze policy next.
3. Extract execution/fill handling after DB dedup is already fixed.
4. Extract funding after account ownership is decided.
5. Leave `cmd/saas` as dependency construction and envelope routing.

Avoid:

- moving code and changing semantics in the same commit without tests
- creating vague `service` packages that still know every dependency
- importing `cmd/saas` from any internal package

### Phase 6: Durable Reconnect Replay

Goal: make state sync live up to the protocol shape.

Current state:

- `state_sync_response` carries positions.
- `OpenOrders` and `SinceLastFills` are currently empty.

Target:

1. Persist undispatched order events/fills in Agent local durable storage.
2. Populate `OpenOrders` and `SinceLastFills` from durable state.
3. SaaS routes replayed fills through the same dedup chokepoint.
4. SaaS reconciles open orders against `TradeRecord`.
5. Tests cover Agent restart or reconnect with missed fills.

This phase should happen after fill dedup has DB backstops. Otherwise replay
can amplify duplicate insertion risk.

### Phase 7: Control Plane And Observability

Goal: expose the state machine to operators.

Useful surfaces:

- Agent connected/degraded/frozen status
- last state_sync time
- pending/open orders
- latest trade status and terminal status
- fill recovery count
- reconciliation discrepancy history
- managed drift versus freeze threshold
- kill-switch audit trail
- instance funded/unfunded state

Prefer showing real state over adding explanatory UI text. The control plane
should help an operator answer: "Is it safe to trade, why did it halt, and
what state is durable?"

## Package-Level Target Shape

Current packages worth preserving:

```text
internal/strategy
internal/strategies/sigmoid_v1
internal/engine
internal/fitness
internal/verification
internal/data
internal/resultpkg
internal/repository
internal/saas/store
internal/saas/instance
internal/agent
internal/agent/binance
internal/wire
internal/saas/wshub
internal/api
```

Packages to consider adding during the refactor:

```text
internal/saas/execution
internal/saas/reconciliation
internal/saas/funding
internal/saas/risk
```

Do not add a package unless it owns an invariant. Good package names describe
business responsibility, not implementation convenience.
Do not add `internal/resultpkg/rawvalidate` for RawEvaluateResult validation;
place those validators in `internal/resultpkg/validate_raw.go`.

## Major Refactor Checklist

Use this as the step-by-step checklist.

### A. Before Editing

- [ ] Read current review docs and this manual.
- [ ] Confirm branch and dirty worktree.
- [ ] Confirm which findings are still open.
- [ ] Run focused tests for the packages you will touch.
- [ ] Decide whether the change is behavior-hardening, schema-hardening, or
      package extraction.

### B. While Editing

- [ ] Keep one invariant per change set when possible.
- [ ] Add the negative test before or with the fix.
- [ ] Use repository predicates or DB constraints for concurrent invariants.
- [ ] Validate cross-layer return values at the receiving boundary.
- [ ] Keep strategy code free of I/O, wall clock, repository, and Agent imports.
- [ ] Keep `cmd/saas` as wiring when extracting code.

### C. Before Merging

- [ ] Run package tests.
- [ ] Run integration tests if schema or repository behavior changed.
- [ ] Re-run migration drift tests if AutoMigrate/Goose changed.
- [ ] Confirm no forbidden imports were introduced.
- [ ] Update review docs if the change closes or reclassifies an active finding.
- [ ] Record any product decision that changed schema freedom.

## Verification Command Reference

No-DB focused sweep:

```bash
GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/fitness ./internal/engine ./internal/verification ./internal/strategies/sigmoid_v1
GOCACHE=/tmp/quantlab-go-cache go test ./internal/agent ./cmd/saas ./internal/repository ./internal/saas/store
```

DB-backed schema drift:

```bash
GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/saas/store/ -run TestMigrationsMatchAutoMigrate -args -config=/home/l9g/quantlab/config.yaml
```

DB-backed repository/cmd sweep:

```bash
GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml
```

Important environment note:

- Use `/home/l9g/quantlab/config.yaml` for SaaS DB integration tests.
- `config.agent.yaml` is Agent-only and fails SaaS config validation.
- In this sandbox, `/tmp`-backed `GOCACHE` is the reliable default.
- Some `httptest.NewServer` tests can hit local socket restrictions. If the
  command is important, rerun with the appropriate approval rather than
  rewriting around the environment.

## Avoidance List

Do not:

- Define boundaries only in prose.
- Trust interface shape as a complete semantic contract.
- Treat authoritative store read errors as cache misses.
- Enforce uniqueness only with application-level `SELECT exists`.
- Update lifecycle status without transition predicates.
- Let schema allow product states that funding/reconcile cannot model.
- Rely mainly on happy-path tests.
- Let `cmd/...` accumulate reusable business logic.
- Mix package extraction with untested semantic changes.
- Copy old review conclusions forward after source fixes have landed.

## Final Guidance

QuantLab does not need an architectural reset. It needs an invariant-hardening
refactor.

The strongest next-version architecture is not "more abstraction"; it is:

- fewer implicit assumptions
- stronger boundary validation
- database-backed concurrency guarantees
- explicit state machines
- smaller business packages
- negative tests that encode the bad worlds the system must survive

When in doubt, ask: "Where is this invariant executable?" If the answer is
"only in a comment" or "only in handler order", that is the next refactor
target.
