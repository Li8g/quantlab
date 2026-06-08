# LEARN: Using Open-Source Trading Platforms To Refactor QuantLab

Date: 2026-06-07

Purpose: summarize the first two discussion topics in this session into a
practical guide: how to learn from strong open-source quantitative trading
projects, map their architecture ideas onto QuantLab's current code, and turn
that into a safer major refactor plan.

This is not a recommendation to copy another platform. The useful lesson is
to study how mature systems place boundaries, protect invariants, and make
live trading recoverable.

## 1. Projects Worth Studying

Use these projects as architecture references, not as drop-in dependencies.

| Project | Reference | What To Study |
|---|---|---|
| QuantConnect LEAN | <https://github.com/QuantConnect/Lean> | Unified research/backtest/live workflow, event-driven engine, pluggable data/brokerage/result handlers. |
| NautilusTrader | <https://nautilustrader.io/> | Same core for backtest and live, typed domain objects, message bus, cache, deterministic simulation, professional execution model. |
| vn.py / VeighNa | <https://github.com/vnpy/vnpy> | Event engine, gateway abstraction, strategy apps, modular platform ecosystem. |
| Hummingbot | <https://github.com/hummingbot/hummingbot> | Exchange connector design, order tracking, market-making operations, replay-tolerant execution thinking. |
| Freqtrade | <https://github.com/freqtrade/freqtrade> | Productized bot operations: dry-run, CLI, Web UI, status surfaces, config/reporting/debug workflows. |
| Microsoft Qlib | <https://github.com/microsoft/qlib> | Research pipeline, data/feature/model/portfolio/backtest separation, experiment discipline. |
| Backtrader | <https://www.backtrader.com/> | Simple strategy/data/broker/analyzer composition model for research and education. |
| Zipline Reloaded | <https://github.com/stefan-jansen/zipline-reloaded> | Pythonic event-driven backtesting, calendars, bundle-style data ingestion. |
Suggested depth order for QuantLab:

1. NautilusTrader
2. Hummingbot
3. LEAN
4. vn.py
5. Freqtrade
6. Qlib

Reason: QuantLab's immediate risk is live execution and invariant hardening,
not research feature expansion. NautilusTrader and Hummingbot are the most
relevant to that risk: Hummingbot's in-flight order tracker and connector-level
"store read error ≠ cache miss" handling maps directly onto the Agent
idempotency fixes (Chapter 1 of the upgrade plan). LEAN is useful but
lower-priority given its enterprise C# shape. FinRL-X (RL research framework)
was removed: QuantLab uses GA, not RL, and the project's own §6 already says
"do not treat research frameworks as live execution frameworks."

## 2. How To Read These Projects

Do not start by reading every file. Read for one architecture question at a
time.

Good questions:

- How does this project keep backtest and live semantics aligned?
- Where does strategy code stop and execution code begin?
- How does an exchange/broker connector report order status?
- What happens after disconnect, reconnect, duplicate event, or stale event?
- Which invariants are enforced by DB constraints, typed state machines, or
  tests instead of comments?
- What belongs in the application entrypoint versus a reusable service
  package?
- What does the operator see when something goes wrong?

For each project, extract:

1. The boundary.
2. The invariant protected by that boundary.
3. The failure mode the boundary prevents.
4. The QuantLab package that should own the same invariant.
5. The test that would prove the invariant.

Avoid:

- Copying class names or package names without the underlying invariant.
- Copying a large engine structure into QuantLab.
- Treating a research framework as a live OMS design.
- Treating a crypto bot's UI/CLI conventions as a substitute for persistence
  correctness.

## 3. Mapping External Ideas To QuantLab

### 3.1 LEAN and NautilusTrader: one semantic core for backtest and live

External idea:

Backtest, research, optimization, and live trading should use the same domain
semantics. Differences should be in adapters, data sources, and execution
interfaces, not in strategy logic.

QuantLab current fit:

- `internal/strategy/contract.go` already states the right idea: the same
  `Step()` runs in backtest and live.
- `internal/saas/instance.Manager.Tick` is the live orchestration point.
- `internal/engine` and `internal/verification` consume adapter output for GA,
  replay, OOS, and stress paths.

Code modules:

- `internal/strategy`
- `internal/strategies/sigmoid_v1`
- `internal/engine`
- `internal/verification`
- `internal/saas/instance`
- `internal/resultpkg`
- `internal/fitness`

Refactor guidance:

1. Preserve `Step()` isomorphism.
2. Keep strategy output as intent, not exchange execution.
3. Harden `RawEvaluateResult` as the strategy-to-engine contract.
4. Validate every `adapter.Evaluate` output before score aggregation.
5. Keep score aggregation owned by `fitness` / engine-side code, not strategy.

Current concrete risk:

`RawEvaluateResult.Validate()` exists, but current validation is not strong
enough and is not consistently wired before aggregation. The refactor should
make invalid adapter output fail closed before `fitness.AggregateScoreTotal`.

Acceptance tests:

- Engine rejects nil raw.
- Engine rejects duplicate/out-of-order windows.
- Engine rejects `WindowOOS` in IS raw.
- Review/OOS/stress paths reject invalid adapter output instead of silently
  skipping or mis-scoring it.

### 3.2 Hummingbot and vn.py: strategy intent must be separated from execution reality

External idea:

Strategy code should not know exchange quirks. Exchange connectors/gateways own
REST/WS calls, order lifecycle tracking, retries, reconnects, fills, and
connector-specific recovery.

QuantLab current fit:

- SaaS sends `TradeCommand`.
- Agent holds exchange credentials.
- `internal/agent/binance` owns Binance-specific implementation.
- `internal/agent/tradecommand.go` translates command to exchange submit.
- `internal/agent/client.go` handles exchange order events.

Code modules:

- `internal/wire`
- `internal/agent`
- `internal/agent/binance`
- `internal/wsconn`
- `cmd/saas/dispatcher.go`
- `cmd/saas/agentmsg.go`

Refactor guidance:

1. Treat Agent as QuantLab's exchange gateway.
2. Keep exchange credentials and exchange request construction inside Agent.
3. Make idempotency state authoritative before any submit side effect.
4. Separate "not found" from "store read failed".
5. Make reconnect replay durable enough to recover missed fills/open orders.

Current concrete risks:

- `handleTradeCommand` checks frozen/expiry before duplicate lookup, so a
  known terminal order replay can come back rejected/expired instead of
  duplicate terminal.
- `idempotency.Get` errors are currently easy to treat as misses.
- `onOrderEvent` can drop a real fill path if the idempotency store read fails.
- `state_sync_response` has the right wire shape, but open orders and
  since-last fills are still not populated from durable state.

Acceptance tests:

- Filled order replay after expiry returns duplicate terminal.
- Filled order replay while frozen returns duplicate terminal.
- idempotency read error prevents exchange submit.
- idempotency read error on order event does not silently drop fill recovery.
- reconnect state sync can replay missed fills through the same SaaS dedup path.

### 3.3 Hummingbot and NautilusTrader: order status is a state machine

External idea:

Live order state is not just a string field. It is a state machine under
at-least-once messages, stale events, duplicate events, partial fills, terminal
states, and reconnect replay.

QuantLab current fit:

- SaaS persists `TradeRecord`.
- Agent sends `Ack` and `OrderUpdate`.
- `cmd/saas/agentmsg.go` maps wire statuses into store statuses.
- `internal/repository/trade.go` updates `TradeRecord.Status`.

Code modules:

- `cmd/saas/agentmsg.go`
- `internal/repository/trade.go`
- `internal/saas/store/models.go`
- `internal/wire/ack.go`
- `internal/wire/orderupdate.go`

Refactor guidance:

1. Define legal trade-status transitions in one place.
2. Make terminal states monotonic.
3. Replace blind update-by-`client_order_id` with transition-aware update.
4. Treat stale or lower-rank updates as no-op, not as errors unless the
   protocol requires an alert.

Current concrete risk:

A replayed or stale `partial_filled`, `cancelled`, `rejected`, or expired ack
can downgrade a `filled` trade if the repository update has no transition
predicate.

Acceptance tests:

- `filled` cannot move to `partial_filled`.
- `filled` cannot move to `cancelled`.
- `filled` cannot move to `rejected`.
- expired ack cannot cancel a previously filled trade.
- duplicate terminal ack remains no-op.

### 3.4 Database-backed platforms: concurrency invariants belong in schema

External idea:

For durable trading data, application-level check-then-insert is a hint, not a
guarantee. If duplicates would corrupt ledger state, uniqueness must live in
the database.

QuantLab current fit:

- `cmd/saas/agentmsg.go:insertFillIfNew` dedups `order_update` and
  `delta_report` fills at the application level.
- `internal/repository/trade.go` exposes existence checks and insert.
- `internal/saas/store/models.go` has ordinary indexes on execution identity
  fields.
- Goose and AutoMigrate drift checks exist, but drift equality does not prove
  business uniqueness.

Code modules:

- `cmd/saas/agentmsg.go`
- `internal/repository/trade.go`
- `internal/saas/store/models.go`
- `internal/saas/store/db.go`
- `internal/saas/store/migrations/00001_baseline.sql`

Refactor guidance:

1. Add partial unique indexes for fill identity.
2. Map unique violations to idempotent no-op.
3. Keep Goose and AutoMigrate DDL aligned.
4. Add concurrent writer tests.

Recommended indexes:

```sql
CREATE UNIQUE INDEX IF NOT EXISTS uq_spot_exec_client_trade
ON spot_executions (client_order_id, trade_id)
WHERE trade_id <> 0;

CREATE UNIQUE INDEX IF NOT EXISTS uq_spot_exec_client_fill_ms_no_trade
ON spot_executions (client_order_id, filled_at_exchange_ms)
WHERE trade_id = 0;
```

Acceptance tests:

- Two concurrent inserts of the same `(client_order_id, trade_id)` produce one
  row.
- Two concurrent inserts of a no-trade-id fill with the same
  `(client_order_id, filled_at_exchange_ms)` produce one row.
- Duplicate insert is handled as already recorded.
- Migration drift test passes.

### 3.5 Freqtrade: operations are part of architecture

External idea:

A live trading system needs an operator control plane: dry-run, live status,
commands, reports, warnings, and clear state. This is not just frontend polish;
it is how humans avoid making dangerous manual interventions.

QuantLab current fit:

- `web/src/pages/InstanceLivePage.tsx`
- `web/src/pages/InstancesPage.tsx`
- `web/src/pages/ChampionsPage.tsx`
- `internal/api/live_handlers.go`
- `internal/api/instance_handlers_test.go`
- `cmd/saas/killaudit.go`
- `cmd/saas/kill.go`

Refactor guidance:

1. Expose real state, not explanatory text.
2. Show Agent connected/degraded/frozen state.
3. Show last `state_sync_response`.
4. Show pending/open orders.
5. Show latest trade statuses and terminal transitions.
6. Show reconciliation discrepancies and managed drift.
7. Show kill-switch audit trail.
8. Show whether instance funding has happened.

Do this after persistence invariants are hardened. A UI over weak state gives
false confidence.

### 3.6 Qlib, Backtrader, and Zipline: research pipeline discipline

External idea:

Research frameworks are useful for data, feature, experiment, and backtest
discipline. They are not enough for live order management.

QuantLab current fit:

- `internal/data` builds evaluation plans and bars.
- `internal/engine` runs GA epochs.
- `internal/verification` handles OOS/review/stress logic.
- `internal/resultpkg` captures package data.
- `internal/repository/evaluation_trace.go` and related repos persist results.

Code modules:

- `internal/data`
- `internal/engine`
- `internal/verification`
- `internal/resultpkg`
- `internal/fitness`
- `internal/quant`
- `research/optuna_toy`

Refactor guidance:

1. Keep research data contracts deterministic.
2. Keep `bars_hash` and plan hash semantics explicit.
3. Keep OOS from polluting IS score.
4. Keep `test_mode` friction semantics visible and tested.
5. Keep research extensions out of live OMS paths unless explicitly adapted.

Do not borrow live execution architecture from Backtrader/Zipline. Borrow
research ergonomics and determinism instead.

## 4. What To Refactor First In QuantLab

Use this order if the goal is a safer major refactor.

### Step 1: Harden the strategy-to-engine contract

Open-source reference:

- LEAN
- NautilusTrader

QuantLab targets:

- `internal/resultpkg/validate.go`
- `internal/engine/engine.go`
- `internal/verification/review.go`
- `internal/verification/oos.go`
- `internal/verification/stress.go`

Deliverable:

- Mode-aware raw validation.
- Fail-closed checks before aggregation.
- Negative tests.

### Step 2: Harden Agent execution idempotency

Open-source reference:

- Hummingbot
- NautilusTrader

QuantLab targets:

- `internal/agent/tradecommand.go`
- `internal/agent/client.go`
- `internal/agent/idempotency.go`
- `internal/agent/idempotency_sqlite.go`

Deliverable:

- Duplicate lookup before new-command frozen/expiry rejection.
- read-error != not-found behavior.
- replay-after-terminal tests.
- store-error tests.

### Step 3: Make SaaS order lifecycle monotonic

Open-source reference:

- Hummingbot
- NautilusTrader

QuantLab targets:

- `cmd/saas/agentmsg.go`
- `internal/repository/trade.go`
- `internal/saas/store/models.go`

Deliverable:

- Trade status transition map.
- Monotonic repository update.
- stale replay tests.

### Step 4: Add DB backstops for fill dedup

Open-source reference:

- Any mature DB-backed execution ledger pattern.

QuantLab targets:

- `cmd/saas/agentmsg.go`
- `internal/repository/trade.go`
- `internal/saas/store/models.go`
- `internal/saas/store/db.go`
- `internal/saas/store/migrations/`

Deliverable:

- Partial unique indexes.
- Unique violation as no-op.
- Concurrent integration tests.
- Drift tests.

### Step 5: Decide account ownership before package extraction

Open-source reference:

- NautilusTrader portfolio/account model
- Freqtrade operational constraints

QuantLab targets:

- `internal/repository/instance.go`
- `internal/saas/store/models.go`
- `internal/saas/store/db.go`
- `cmd/saas/agentmsg.go`
- `internal/api/handlers.go`

Recommended v1 deliverable:

- One non-retired instance per `(owner_user_id, account_id)`.
- DB partial unique index.
- API conflict mapping.
- tests proving retired instances do not block recreation.

Alternative deliverable:

- Explicit per-instance capital allocation model.
- funding and reconcile rewritten around allocation ownership.
- tests proving no same-account false-freeze.

### Step 6: Extract business services from `cmd/saas`

Open-source reference:

- vn.py modular apps
- LEAN pluggable components
- Freqtrade operational modules

QuantLab targets:

- `cmd/saas/agentmsg.go`

Suggested destination packages:

```text
internal/saas/execution
internal/saas/reconciliation
internal/saas/funding
internal/saas/risk
```

Deliverable:

- `cmd/saas` becomes wiring and envelope routing.
- Pure logic has DB-free tests.
- persistence invariants remain in repository/schema tests.

### Step 7: Build durable reconnect replay

Open-source reference:

- Hummingbot connector recovery
- NautilusTrader execution/cache design

QuantLab targets:

- `internal/agent/handshake.go`
- `internal/wire/statesync.go`
- `internal/agent/idempotency_sqlite.go`
- `cmd/saas/agentmsg.go`
- `internal/saas/wshub`

Deliverable:

- Agent populates `OpenOrders`.
- Agent populates `SinceLastFills`.
- SaaS routes replayed fills through the same dedup chokepoint.
- tests cover restart/reconnect with missed fills.

## 5. A Repeatable Method For Learning From Open Source

For each open-source system and each QuantLab refactor item:

1. Pick one risk, for example `spot_executions` duplicate fill insert.
2. Find how the reference system models the same risk.
3. Ignore naming. Extract the invariant.
4. Locate the QuantLab package that should own the invariant.
5. Decide whether the invariant belongs in:
   - type validation
   - service boundary
   - repository predicate
   - database constraint
   - durable local store
   - UI/control-plane state
6. Add or update the negative test first.
7. Implement the smallest code change that makes the test pass.
8. Run focused tests.
9. Update review docs if the risk is closed or reclassified.

Example:

```text
Reference idea:
  Hummingbot-style connector must not treat unknown storage state as safe to
  submit a new exchange order.

QuantLab invariant:
  idempotency.Get read error must stop exchange submit.

Owner:
  internal/agent/tradecommand.go and idempotency store interface.

Test:
  fake idempotency store returns error; exchange Submit must not be called.

Acceptance:
  command returns internal error/rejection without side effect.
```

## 6. What Not To Do

Do not:

- Rewrite QuantLab around another project's package structure.
- Add abstractions before deciding the invariant they protect.
- Extract `cmd/saas` code before pinning behavior with tests.
- Expand same-account multi-instance support before adding capital ownership.
- Add UI surfaces for state that is not yet durable or monotonic.
- Treat research frameworks as live execution frameworks.
- Treat schema drift tests as proof of business uniqueness.
- Treat interface signatures as enough contract.
- Treat idempotency read errors as harmless cache misses.

## 7. The Target Mental Model

After the major refactor, QuantLab should read like this:

```text
strategy
  owns trading decision logic only

engine / verification / fitness
  own evaluation, validation, score aggregation, replay, OOS, stress

agent
  owns exchange credentials, exchange submit, exchange events, local durable
  idempotency, reconnect replay

saas execution
  owns ack/order_update/fill persistence and order lifecycle monotonicity

saas reconciliation
  owns expected-vs-actual drift and discrepancy persistence

saas funding
  owns account/instance capital ownership and genesis funding

saas risk
  owns kill-switch and auto-freeze policy

repository / store
  own persistence predicates, uniqueness, migrations, and DB-backed invariants

cmd/saas
  wires services, config, API, hub, and shutdown
```

This shape is closer to what the better open-source platforms teach: strategy
logic is small, execution is explicit, lifecycle state is monotonic, and
business invariants are executable.

## 8. Verification Commands

No-DB focused tests:

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

Use `/home/l9g/quantlab/config.yaml` for SaaS DB tests. `config.agent.yaml`
is Agent-only.

## 9. Bottom Line

The right way to learn from strong open-source trading platforms is not to
copy their code. It is to copy their discipline:

- make boundaries executable
- separate strategy intent from execution reality
- make live order state monotonic
- make persistence concurrency-safe
- decide product invariants before schema freedom
- expose durable state to operators
- test the bad worlds, not only happy paths

For QuantLab, the immediate major refactor should be an invariant-hardening
refactor. Package extraction should come after the live-order, persistence, and
account-ownership invariants are pinned by tests.
