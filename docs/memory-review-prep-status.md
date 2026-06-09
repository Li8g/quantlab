# Memory: Code Review Preparation Status

Date: 2026-06-05

Purpose: preserve the current QuantLab review-preparation state after an opencode restart.

## Current State

- Current branch: `docs/ga-learn-series`.
- Review skill loaded successfully in the session: `quantlab-review`.
- Root review contract exists: `CODEX_SKILL.md`.
- Project read-only reviewer agent exists: `.opencode/agents/quantlab-readonly-reviewer.md`.
- Project review skill exists: `.claude/skills/quantlab-review/SKILL.md`.
- Existing review plan/history exists: `docs/code-review-plan.md`.
- Current Codex review archive exists: `docs/codex-readonly-review-2026-06-05.md`.
- `.opencode/.gitignore` ignores `node_modules`, `package.json`, `package-lock.json`, and `bun.lock`.
- `CODEX_SKILL.md`, `.opencode/`, `docs/codex-readonly-review-2026-06-05.md`, and this memory file are currently untracked in git.

## Current Review Progress

- Stage 0 completed: project map and baseline.
- Stage 1 completed: architecture boundary.
- Stage 1 architecture boundary was lightly re-reviewed again on 2026-06-06 per user request. Production `internal/engine` remains clean; active boundary noise is still `sigmoid_v1 -> verification` and `engine_test -> concrete strategies`.
- Stage 2 completed: GA core invariants.
- Stage 3 quant-correctness completed by targeted re-review after the previous session died during remote compact.
- Stage 3 business integration invariants re-reviewed on 2026-06-05 per user request: Promote/Retire lifecycle, `test_mode` friction, OOS/IS score isolation, and kill-switch managed-asset scope.
- Stage 3 business re-review confirmed the implementation is correct for `test_mode` effective friction, OOS post-epoch isolation, Promote/Retire admin gating, `decision_status` not containing `retired`, and kill-switch unmanaged/faucet assets not triggering freeze.
- Stage 2 GA core invariants were targeted re-reviewed again on 2026-06-05 per user request: `RawEvaluateResult.Validate()` hot-loop wiring, canonical window order, cascade skipped semantics, engine-owned score aggregation, and worker adapter isolation.
- Stage 2 targeted re-review confirmed `sigmoid_v1` production order/cascade behavior, engine-owned aggregation, and adapter reset isolation are OK; it reclassified the missing RawEvaluateResult validation as a high-priority guard gap because invalid adapter output can be silently aggregated.
- Today's item 1, GA boundary / `RawEvaluateResult` contract, was re-reviewed on 2026-06-06. It confirmed the same high-priority engine guard gap, added `verification.RunStress` to the adapter-output boundary scope, and confirmed current `sigmoid_v1` producer output remains valid.
- Today's item 2, business state consistency, was re-reviewed on 2026-06-06. It found a new major DeployChampion consistency gap and reconfirmed Retire/instance CAS gaps at that time; the later lifecycle/CAS fix on 2026-06-06 closed DeployChampion scope/retired guard, Retire CAS, and instance status CAS. Multi-instance genesis funding and trade status monotonicity risks remain open.
- Stage 4 completed and re-reviewed on 2026-06-05: database / persistence / schema invariants.
- Stage 5 completed targeted high-priority re-review: Agent TradeCommand handling, SQLite idempotency, Binance order-event edge cases, delta_report buffering, SaaS ack/order_update/delta_report persistence, `spot_executions` dedup, and reconnect state-sync behavior.
- Today's item 3, Stage 5 live order/idempotency risk, was re-reviewed on 2026-06-06. It confirmed two critical Agent idempotency failures: known terminal commands can be rejected/expired before duplicate detection, and SQLite `Get` errors fail open into pending upsert + possible exchange submit. It also reconfirmed `onOrderEvent` read-error fill loss and SaaS trade-status downgrade risks.
- Agent order idempotency / terminal replay item was re-reviewed and tested again on 2026-06-06 per user request. The same S5B risks remain open: replay-after-terminal can still be rejected/expired before duplicate detection, idempotency read errors still fail open or drop fills, and generic SaaS status writes still lack monotonic guards. Forced focused tests passed after escalation for local `httptest` sockets, but the missing negative cases are still absent.
- Today's item 4, persistence concurrency invariants, was re-reviewed on 2026-06-06. It reconfirmed active champion uniqueness is DB-backed and, before the later lifecycle/CAS fix, reconfirmed Retire/instance/deploy CAS gaps. The later lifecycle/CAS fix closed those three CAS gaps; `spot_executions` DB dedup, genesis funding claim ordering, and single-worker import job claiming remain open.
- Today's item 5, live reconciliation / multi-instance account risk, was re-reviewed on 2026-06-06. It reconfirmed that the same `account_id` can have multiple non-retired instances, each fresh instance can be genesis-funded from the same whole exchange snapshot, and the next reconcile sums duplicated expected portfolios before auto-freeze. No source fix was made.
- Today's item 5 was re-reviewed and tested again on 2026-06-07. The issue remains open: current code still has no one-account/one-non-retired-instance invariant and no per-instance capital allocation model. For v1, the recommended decision is to enforce at most one non-retired instance per `(owner_user_id, account_id)`; per-instance allocation needs a new capital ownership model before whole-account genesis funding can be safe.
- DeployChampion state consistency was re-reviewed again on 2026-06-06 and later fixed in the lifecycle/CAS follow-up: deploy now proves the requested challenger is active/unretired for the target instance's `(strategy_id,pair)` and refuses retired instances.
- Regression test follow-up was reviewed on 2026-06-06. Current tests cover several positive/adjacent paths, and the later lifecycle/CAS fix added permanent Retire CAS, instance status CAS, and DeployChampion scope/retired guard coverage. The largest remaining gaps are Raw validation fail-closed, fill dedup concurrency/DB unique backstop, terminal-status replay/downgrade, multi-instance account false-freeze, and idempotency read-error paths.
- Raw validation / GA boundary negative-test follow-up was re-reviewed and tested on 2026-06-06. The same high/major gaps remain: engine hot loop and best re-evaluate do not validate raw output before aggregation, `RawEvaluateResult.Validate()` still lacks Raw-level cascade/order checks, `RunReview` aggregates invalid replay raw, and OOS/stress still have only partial or skip-style guards. Existing package tests pass but do not include the missing negative cases.
- Today's lifecycle / CAS item was fixed and tested on 2026-06-06: Retire now writes with `retired_at IS NULL` CAS, instance start/stop status writes use expected-status CAS, and DeployChampion now requires a non-retired instance plus a matching active/unretired `champion_history` row. Added repository integration tests and API 422 mapping tests; focused and full repository/cmd integration tests passed with `config.yaml`.
- Next normal review step: either start implementing the missing regression tests/fixes, or continue with the next user-selected targeted review.

## Today Review Work Plan

Date: 2026-06-06

Purpose: keep the next review queue visible for the rest of the day. User accepted the recommendation to prioritize unfinished risk areas, starting with the GA boundary / `RawEvaluateResult` contract.

1. GA boundary / `RawEvaluateResult` contract — reviewed 2026-06-06
   - Review whether every `adapter.Evaluate` result fails closed on `raw == nil || raw.Validate() != nil` before aggregation.
   - Cover `engine.evaluatePopulation`, final best-gene re-evaluation, `verification.RunReview`, and `verification.RunOOS`.
   - Review `RawEvaluateResult.Validate()` for Raw-level invariants: empty result rejection, exact canonical window order, duplicate-window rejection, `WindowOOS` rejection in IS raw, and Fatal -> cascade-skipped sequence semantics.
   - Result: still open. Engine hot loop / best re-evaluate and `RunReview` do not fail closed; `RunOOS` only has partial local checks; `RunStress` can skip invalid raw as no-series. Next concrete review item is business state consistency.

2. Business state consistency — reviewed 2026-06-06
   - Recheck Promote/Retire lifecycle, especially Retire compare-and-set behavior.
   - Recheck instance start/stop/deploy champion paths for stale reads overriding `retired` state.
   - Reconfirm `test_mode`, OOS, kill-switch, and result package state remain mutually consistent.
   - Result: source-fixed in the 2026-06-06 lifecycle/CAS follow-up for Retire CAS, instance transition CAS, and DeployChampion active/unretired + retired-instance guard. Multi-instance genesis funding and order status monotonicity risks remain open.

3. Stage 5 live order/idempotency risk — reviewed 2026-06-06
   - Recheck whether `handleTradeCommand` performs idempotency lookup before frozen/expired rejection for already-known `client_order_id`.
   - Recheck whether SQLite idempotency `Get` errors fail closed before `Put(preRec)` and exchange submit.
   - Recheck whether `onOrderEvent` distinguishes not-found from store read failure before dropping exchange fills.
   - Recheck whether SaaS `ack` / `order_update` status writes are monotonic and cannot downgrade `filled`.
   - Result: still open. Current source still checks frozen/expiry before duplicate lookup, discards idempotency `Get` errors in both Agent paths, and updates SaaS `TradeRecord.Status` by `client_order_id` only. The 2026-06-06 item recheck ran `GOCACHE=/tmp/quantlab-go-cache go test -count=1 ./internal/agent ./cmd/saas ./internal/repository`; it passed after escalation for local `httptest` sockets, but existing tests still do not cover replay-after-terminal, idempotency read-error, or terminal-status downgrade negative cases.

4. Persistence concurrency invariants — reviewed 2026-06-06
   - Recheck `champion_history` active uniqueness and Retire CAS.
   - Recheck `spot_executions` idempotent fill writes and whether DB unique constraints back the dedup identities.
   - Recheck `strategy_instances` state transitions for DB-conditioned predicates and `RowsAffected` handling.
   - Result: partially fixed in the 2026-06-06 lifecycle/CAS follow-up. `uq_champion_active` remains the correct DB backstop for Promote; Retire now has `retired_at IS NULL` CAS; start/stop status writes now use expected-status CAS; DeployChampion now guards retired instances and requires a matching active/unretired champion. `spot_executions` still has no unique dedup identities, and the genesis funding/import claim修缮点 remain open.

5. Live reconciliation / multi-instance account risk — reviewed 2026-06-06
   - Review whether multiple non-retired instances under one `account_id` can each be genesis-funded from the same whole exchange-account snapshot.
   - Review whether reconcile sums duplicated expected portfolios and can false-trigger managed-asset drift / auto-freeze.
   - Result: still open. `idx_inst_unique_active` only prevents duplicate non-retired rows for the same `(owner_user_id,strategy_id,pair,account_id)`, while `ListByAccount` returns all non-retired instances for the account. `fundInstance` seeds each fresh instance from the whole delta_report base/USDT snapshot, and later reconcile sums all funded portfolios into one expected map before computing managed drift and `maybeAutoFreeze`. Verification command passed, but existing tests do not cover the multi-instance account negative case.
   - 2026-06-07 re-review result: still open. `docs/saas-tier2-schema-v1.md` still says one account may map to many instances, but `cmd/saas/agentmsg.go` still documents and implements whole-balance genesis funding as `[INVENTED v1: whole-balance anchor; one instance per exchange account]`. Current code has not chosen either invariant in implementation. Recommended v1 decision: enforce a partial unique constraint over `(owner_user_id, account_id) WHERE status != 'retired'`, plus API/repo tests proving a second non-retired instance is rejected. A per-instance allocation fix should wait for explicit capital ownership fields/tables and funding math.

6. DeployChampion state consistency — reviewed 2026-06-06
   - Recheck whether `DeployChampion` / `SetActiveChampion` proves the requested challenger is the active, unretired champion for the target instance's `(strategy_id,pair)`.
   - Recheck whether deploy can write `active_champ_id` on a retired instance.
   - Result: source-fixed in the 2026-06-06 lifecycle/CAS follow-up. `SetActiveChampion` now performs a single conditional write requiring `status <> retired` and a matching active/unretired `champion_histories` row for the instance's `(strategy_id,pair)`. Repository integration tests cover wrong pair, retired champion, retired instance, and happy path.

7. Test gap follow-up — reviewed 2026-06-06
   - For each active major/high finding, verify there is or should be a permanent regression test.
   - Track integration / TimescaleDB / drift tests that were not run.
   - Consider whether Stage 1 boundary grep can become a CI rule after `sigmoid_v1 -> verification` and `engine_test -> concrete strategies` are cleaned up.
   - Result: still open for many areas, but lifecycle/CAS coverage is now in place. Existing coverage includes `CrucibleResult` three-state validation, positive RunEpoch/RunReview/OOS tests, DeployChampion happy path plus scope/retired guard negatives, Retire DB CAS, instance stale-status CAS, sequential fill replay/cross-channel dedup integration, immediate duplicate-terminal agent ack, and single-instance genesis funding. Missing permanent negative tests remain for invalid Raw fail-closed in engine/review, Raw-level cascade order/OOS rejection, `spot_executions` concurrent dedup/unique-violation handling, terminal status downgrade, replay-after-terminal under expiry/frozen, idempotency read-error submit/event paths, and multi-instance same-account false-freeze.

## Active Findings / Follow-up

- Fixed 2026-06-06 lifecycle/CAS item: `internal/repository/champion.go` `ChampionRepo.Retire` now writes with `WHERE id = ? AND retired_at IS NULL`; `RowsAffected == 0` maps to `api.ErrAlreadyRetired`. `internal/repository/lifecycle_cas_integration_test.go` verifies stale Retire cannot overwrite first-writer audit attribution.
- Major Stage 4 finding: `spot_executions` fill dedup is implemented as check-then-insert in `cmd/saas/agentmsg.go` / `internal/repository/trade.go`, but the schema has only ordinary indexes on `client_order_id`, `exchange_order_id`, and `trade_id`. Concurrent old/new WS connections for the same account can both pass the existence check and insert duplicate fills, which can be double-folded into the SaaS ledger.
- Suggested fix: add DB-level unique constraints matching the two dedup identities: `(client_order_id, trade_id)` for `trade_id != 0`, and `(client_order_id, filled_at_exchange_ms)` for `trade_id = 0`; map unique violations to idempotent no-op in `insertFillIfNew`; add a concurrency regression test.
- 2026-06-07 item 4 re-review/test result: D4B-2 remains open. Existing sequential replay/cross-channel integration tests pass, but current code still performs `ExecutionExists*` followed by plain `InsertSpotExecution`; `SpotExecution` model/AutoMigrate raw DDL/Goose baseline do not define a dedup unique index. A live Postgres `pg_indexes` query showed only ordinary `spot_executions` indexes plus the primary key. Two rollback probes inserted duplicate `(client_order_id, trade_id)` rows and duplicate `trade_id=0` `(client_order_id, filled_at_exchange_ms)` rows successfully, each reaching count=2 before `ROLLBACK`; probe residual count was 0.
- Fixed 2026-06-06 lifecycle/CAS item: instance start/stop status writes now pass the expected status into `InstanceRepo.UpdateStatus`, which writes with `WHERE instance_id = ? AND status = ?`; `SetActiveChampion` now refuses retired instances and requires a matching active/unretired champion row. Integration tests cover stale retired overwrite rejection and deploy scope/retired guards.
- Medium Stage 4B修缮点: genesis funding is not claimed before the seed row is appended. `cmd/saas/agentmsg.go:637` appends `PortfolioState`, then `internal/repository/instance.go:104` updates `funded_at_ms` with a NULL guard but does not return whether the caller won the claim. Concurrent delta reports with different `nowMs` can leave multiple genesis seed rows; `Latest()` later picks one by timestamp, so the baseline/audit row can be whichever report won recency rather than the single funding claim.
- Suggested fix: make funding claim atomic and observable before appending, or wrap claim+seed in a transaction; return `(claimed bool, error)` from `MarkFunded` or use `UPDATE ... WHERE funded_at_ms IS NULL RETURNING`; only the winner appends the genesis portfolio. Add a concurrent double-funding regression test.
- Low Stage 4B修缮点: `ImportJobRepo.NextQueued`/`MarkRunning` is intentionally single-worker, but the DB does not enforce the claim. `NextQueued` selects the oldest queued row without lock, and `MarkRunning` updates by `job_id` only. This is acceptable while import routes and the worker stay non-saas/single-process, but multiple workers or replicas could run the same import.
- Suggested fix: if import workers become horizontally scalable, replace read-then-mark with `UPDATE ... WHERE status='queued' ... RETURNING` or `SELECT ... FOR UPDATE SKIP LOCKED`, and make `MarkRunning` queued-only with `RowsAffected` handling.
- Major Stage 3D / 2026-06-07 item 5 finding: multi-instance genesis funding under one `account_id` can duplicate the whole exchange-account balance into every fresh instance. `idx_inst_unique_active` only covers `(owner_user_id,strategy_id,pair,account_id)`, so different pair/strategy instances can share one account. `ListByAccount` returns all non-retired instances, `fundInstance` seeds each fresh instance from the full `delta_report` snapshot, and the next reconcile sums every instance portfolio into `expected`; managed BTC/USDT can therefore be inflated and trip a false drift / auto-freeze.
- Recommended fix decision: for v1, enforce one non-retired instance per `(owner_user_id, account_id)` because the existing ledger, `PortfolioState`, and genesis funding code have no per-instance capital ownership model. Add the DB partial unique backstop, map unique violations to the create-instance conflict path, and add regression coverage that creating a second non-retired instance for the same account is rejected. If product chooses per-instance allocation instead, add explicit allocation/managed-balance ownership before funding and test that two fresh same-account instances funded from an unchanged snapshot produce no discrepancy or auto-freeze.
- High Stage 2 finding: `RawEvaluateResult.Validate()` is still not wired into `engine.evaluatePopulation`, final best-gene re-evaluation, or replay/OOS verification entry points. `fitness.AggregateScoreTotal` skips `Score.Value == nil`, so an invalid non-fatal/non-skipped nil score can be silently treated as missing contribution and distort `ScoreRaw` / consistency penalty.
- Suggested fix: after every `adapter.Evaluate`, fail closed on `raw == nil` or `raw.Validate() != nil` before aggregation; apply the same guard to best-gene re-evaluation and verification replay/OOS paths. The 2026-06-06 re-review also flagged `verification.RunStress` as needing the same adapter-output boundary discipline.
- Medium Stage 2 finding: `RawEvaluateResult.Validate()` only validates each `CrucibleResult` independently and does not enforce Raw-level cascade sequence semantics. It does not reject empty results, duplicate windows, non-canonical order, `SkippedBy` without a real earlier Fatal, or `WindowOOS` inside IS raw results; OOS would have weight 0 but still affect consistency penalty if given a value.
- Suggested fix: add a Raw-level cascade validator based on `resultpkg.AllWindowsInEvalOrder()`, reject `WindowOOS` in IS raw, and enforce exact fatal-to-skipped semantics.
- Major Stage 2C finding: `verification.RunReview` directly re-aggregates replay raw output without Raw validation. A contract violation should return a Go error, not become a replay mismatch or a silent match.
- Suggested fix: validate replay raw before aggregation and return an error for invalid strategy/adapter output.
- Medium Stage 2C finding: `verification.RunOOS` has only partial local checks and lacks a nil-raw guard; `verification.RunStress` treats nil/empty returns as skip and does not validate non-nil raw.
- Suggested fix: share an adapter raw validation helper across engine and verification, with mode-specific IS/OOS/stress rules.
- Stage 2D Raw validation / GA boundary follow-up (rechecked 2026-06-06): current source still directly aggregates `raw.Windows` at `internal/engine/engine.go:419` and `internal/verification/review.go:130`; best-gene re-evaluate stores `bestRaw` at `engine.go:310` without validation; `RunOOS` dereferences `raw.Windows` after only `Evaluate` error handling; `RunStress` treats `raw == nil` as no-series skip. Tests still cover positive paths only.
- Suggested negative tests before source fix: adapter returning `nil` raw must fail closed in RunEpoch/best re-evaluate and RunReview/OOS; adapter returning invalid non-fatal/non-skipped nil score must fail before aggregation; Raw validator must reject empty/duplicate/out-of-order windows, `WindowOOS` in IS raw, and invalid Fatal -> skipped cascade; stress must not silently skip invalid non-nil raw.
- Low Stage 2 finding: `internal/strategies/toy/toy.go` still iterates `plan.Windows` directly. This is not production because `DefaultRegistry` only registers `sigmoid_v1`, but toy remains a weak boundary fixture for canonical window order.
- Fixed 2026-06-06 lifecycle/CAS item: `DeployChampion` / `SetActiveChampion` now uses one conditional SQL write to require a non-retired target instance and a matching active, unretired `champion_histories` row for the instance's `(strategy_id,pair)`. `RowsAffected == 0` maps to 404 for missing instance or 422 `ErrDeployChampionRefused` for invalid scope/state. Remaining policy decision: whether Retire should block, detach, or pause already-deployed instances.
- Critical Stage 5 finding (rechecked 2026-06-06): Agent `handleTradeCommand` checks frozen/kill and `valid_until_ms` before `idempotency.Get`. A delayed replay of an already accepted/filled `client_order_id` can receive `AckStatusRejected` or `AckStatusExpired` instead of `duplicate_pending`/`duplicate_terminal`; SaaS maps these to `TradeStatusRejected` / `TradeStatusCancelled`, so the replay can overwrite an already executed order's lifecycle.
- Suggested fix: move the idempotency lookup before expiry/frozen rejection for already-known `client_order_id`; keep kill/expiry rejection for brand-new commands; add regression tests where a previously filled command is replayed after `ValidUntilMs` and while frozen, and both must return `duplicate_terminal`.
- Critical Stage 5 finding (rechecked 2026-06-06): Agent ignores idempotency-store errors in `handleTradeCommand`. In `internal/agent/tradecommand.go:57`, `existing, ok, _ := c.idempotency.Get(...)` drops the error. If SQLite `Get` fails, the code treats it as a miss, then `Put(preRec)` at `tradecommand.go:105` uses SQLite upsert and can overwrite an existing accepted/filled record with `pending`, then calls `exchange.Submit(...)` again. This is fail-open on the order-submit path and can duplicate real exchange orders.
- Major Stage 5 finding (rechecked): In `internal/agent/client.go:578`, `rec, ok, _ := c.idempotency.Get(ev.ClientOrderID)` drops the error. If a real exchange `OrderEvent` arrives while SQLite `Get` fails, the code goes down the `!ok` branch, logs `agent_order_event_unknown_order`, and returns before sending `OrderUpdate`, before adding the fill to the `delta_report` buffer, and before updating local state. The `UpdateStatus` call at `client.go:636` also ignores errors; that is lower-impact because the `OrderUpdate`/delta buffer work already happened, but local idempotency state can remain stale.
- Suggested fix: fail closed on idempotency `Get` errors before exchange submit (send internal error / reject without submit), and in `onOrderEvent` distinguish "not found" from "store read failed" instead of treating both as unknown order. Log and retain/report the error path; add fake-store error tests for both `handleTradeCommand` and `onOrderEvent`.
- Major Stage 5 finding: reconnect `state_sync_response` still sends empty `open_orders` and `since_last_fills`, and Hub does not parse state sync for fill recovery by default. Same-process `delta_report` buffering helps, but Agent crash/restart or events lost before buffer insertion have no durable replay path.
- Suggested fix: persist undispatched order events/fills in an idempotency-adjacent durable store; populate state sync from durable state; route SaaS state-sync fill recovery through the same dedup chokepoint.
- Major Stage 5 finding (rechecked 2026-06-06): `cmd/saas/agentmsg.go` dedups fills but then unconditionally applies order status. A replayed or older `partial_filled`, `cancelled`, or `rejected` update can downgrade a TradeRecord already marked `filled`; `internal/repository/trade.go:42` updates by `client_order_id` only and has no status-transition predicate.
- Suggested fix: enforce monotonic status transitions in repository update logic or the message handler; add terminal-status replay tests.
- Stage 5 2026-06-06 verification command passed: `GOCACHE=/tmp/quantlab-go-cache go test ./internal/agent ./cmd/saas ./internal/repository`.
- Stage 5 item recheck forced verification on 2026-06-06: sandboxed `GOCACHE=/tmp/quantlab-go-cache go test -count=1 ./internal/agent ./cmd/saas ./internal/repository` passed `internal/agent` and `internal/repository` but `cmd/saas` hit local socket restriction in `httptest.NewServer`; the same command passed after escalation.
- Stage 5 test gaps after 2026-06-06 re-review: no regression test for replaying a filled command after expiry or while frozen, no fake idempotency-store read-error tests for submit/event paths, and no terminal-status downgrade test for SaaS `UpdateTradeStatus`.
- Low-risk Stage 3 test gap: no explicit end-to-end assertion that a request with nonzero friction plus `test_mode=true` produces `GAConfigSnapshot` taker/slippage of zero. Implementation path was reviewed as correct.
- Regression test follow-up finding (updated after 2026-06-06 lifecycle/CAS fix): existing high/major regression coverage is still partial, but Retire DB CAS, instance stale-status CAS, and DeployChampion scope/retired guards now have permanent coverage. Remaining major gaps include Raw-level sequence/fail-closed, fill dedup concurrent insert/DB unique backstop, terminal-status monotonicity, Agent idempotency read-error/replay-after-terminal, and multi-instance account false-freeze.
- Suggested test follow-up order after lifecycle/CAS fix: Raw fail-closed, trade status monotonicity, Agent idempotency read-error/replay-after-terminal, fill dedup DB uniqueness/concurrency, multi-instance account false-freeze, then CI import-boundary guard for A-1.

## Tools And Scripts

- Available in current `PATH`: `go`, `npm`.
- Not found in current `PATH`: `staticcheck`, `govulncheck`, `golangci-lint`, `pnpm`, `yarn`, `ruff`, `mypy`, `pytest`.
- `.opencode/` currently has no extra review script or plugin files; it only has agent/skill config and npm dependency files.
- `scripts/preflight_champion_dup_check.sql` exists and is a database preflight script for duplicate active champions.
- `scripts/run_testnet.sh` exists and is executable, but it is a testnet bring-up script, not a read-only review script. It can build binaries, start processes, change config, and mutate DB state, so do not run it during read-only review unless explicitly intended.

## Remaining Prep

- Today's active review plan is tracked in "Today Review Work Plan" above. Recommended next concrete review: GA boundary / `RawEvaluateResult` contract.
- Stage 0 does not need priority re-review unless the repo structure or review contract changes.
- Decide whether to track `CODEX_SKILL.md` and the intended `.opencode/` files in git.
- Do not track `.opencode/node_modules`, `.opencode/package.json`, or `.opencode/package-lock.json` unless explicitly intended.
- Confirm review scope: whole repository, or branch diff against a specific base.
- Install or otherwise make available the missing review tools if full tool-gate coverage is required.
- Run the read-only checklist scans from `CODEX_SKILL.md` / `quantlab-review`.
- Verify the 12 priority tests exist and are non-vacuous.
- Run appropriate verification commands such as `go test ./...` after scope is confirmed.

## Notes

- No repository file named `memory` existed before this note was created.
- Earlier access to `~/.claude` memory-like paths was rejected, so external memory contents were not read.
- Stage 3 verification command passed outside the sandbox:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./internal/saas/epoch ./internal/verification ./cmd/saas ./internal/saas/wshub ./internal/data ./internal/engine`
- The same command inside the sandbox hit expected local socket restrictions in tests using `httptest.NewServer` (`listen tcp6 [::1]:0: socket: operation not permitted`). This was environmental, not a code failure.
- Stage 4 verification command passed inside the sandbox:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/saas/config ./internal/repository ./internal/saas/store ./internal/saas/wshub`
- Stage 4 integration drift tests were not run because they require an external Postgres/TimescaleDB config.
- Stage 4 re-review verification command passed inside the sandbox:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/store ./internal/saas/config`
- Stage 4 re-review did not run integration drift tests because they require an external Postgres/TimescaleDB config.
- Stage 5 verification commands passed inside the sandbox:
  `GOCACHE=/tmp/quantlab-go-cache go test -race ./internal/agent`
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/agent -run 'TestTradeCommand|TestDeltaReport|TestOnOrderEvent|TestSqliteStore'`
  `GOCACHE=/tmp/quantlab-go-cache go test ./cmd/saas -run 'TestAck|TestBuildSpot|TestReconcile|TestFreeze|TestAgentMsg|TestDelta|TestOrderUpdate'`
- Stage 5 targeted re-review verification command passed inside the sandbox:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/agent ./cmd/saas ./internal/repository ./internal/saas/wshub ./internal/saas/store`
- `GOCACHE=/tmp/quantlab-go-cache go test ./internal/agent ./cmd/agent` passed `internal/agent` but `cmd/agent` hit the sandbox's local socket restriction in `httptest.NewServer` (`listen tcp6 [::1]:0: socket: operation not permitted`). This was environmental.
- Stage 3B business integration re-review verification command passed inside the sandbox:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./internal/saas/epoch ./internal/verification ./cmd/saas ./internal/saas/wshub ./internal/data ./internal/engine`
- Stage 3B did not run integration/TimescaleDB tests.
- Stage 2 targeted re-review verification command passed inside the sandbox:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/fitness ./internal/engine ./internal/strategies/sigmoid_v1 ./internal/verification`
- Stage 3C business state consistency verification command passed after escalation for local `httptest` sockets:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/instance ./internal/saas/store`
- Stage 4B persistence concurrency re-review verification command passed after escalation for local `httptest` sockets:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/store`
- Stage 4B integration/drift follow-up on 2026-06-06: `./config.agent.yaml` is Agent-only config and failed SaaS config validation (`app_role must be one of saas/lab/dev`); `./config.yaml` is the SaaS DB config used for integration.
- Stage 4B schema drift test passed after escalation for local Postgres sockets:
  `GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/saas/store/ -run TestMigrationsMatchAutoMigrate -args -config=/home/l9g/quantlab/config.yaml`
- Stage 4B repository/cmd integration tests passed after escalation for local Postgres sockets:
  `GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml`
- Stage 4D spot_executions dedup re-review verification on 2026-06-07:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/store` passed.
  `GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/saas/store/ -run TestMigrationsMatchAutoMigrate -args -config=/home/l9g/quantlab/config.yaml` passed.
  `GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml` passed.
  Live Postgres read/probe checks confirmed no `spot_executions` dedup unique backstop and no probe row residue after rollback.
- Stage 3D / item 5 multi-instance account false-freeze re-review verification on 2026-06-07:
  `GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml` passed inside the sandbox.
  `GOCACHE=/tmp/quantlab-go-cache go test -count=1 ./cmd/saas ./internal/repository` hit the known sandbox local-socket restriction in `cmd/saas` `httptest.NewServer`, then passed after escalation; `internal/repository` passed in the sandbox.
- Stage 3D live reconciliation / multi-instance account re-review verification command passed inside the sandbox:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/store`
- DeployChampion state consistency re-review verification command passed inside the sandbox:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/api ./internal/repository ./internal/saas/instance`
- Regression test follow-up verification commands passed:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/engine ./internal/verification ./internal/repository ./internal/api ./internal/agent ./cmd/saas`
  `GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml`
- Raw validation / GA boundary follow-up verification command passed:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/fitness ./internal/engine ./internal/verification ./internal/strategies/sigmoid_v1 ./internal/strategies/toy`
- Lifecycle/CAS fix verification commands passed:
  `GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/store`
  `GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository -run 'TestChampionRepo_RetireCASRejectsStaleHistory|TestInstanceRepo_UpdateStatusCASRejectsStaleState|TestInstanceRepo_SetActiveChampionRequiresMatchingActiveChampionAndLiveInstance' -args -config=/home/l9g/quantlab/config.yaml`
  `GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml`
