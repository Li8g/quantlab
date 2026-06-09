---
name: quantlab-live-trading-pitfalls
description: >-
  Use when writing or reviewing QuantLab live-trading / persistence code — the
  agent (Binance client, order dispatch, fill ingestion, idempotency), the saas
  layer (delta_report reconciliation, auto-freeze/kill_switch, instance
  lifecycle, epoch scheduling), the repository layer (GORM/goose DDL, dedup
  constraints), or the React fleet UI. It encodes the recurring bug classes hit
  during the 2026-06-02..06-09 live-trading hardening week so they are not
  reintroduced. Consult before merging changes to internal/agent, internal/saas,
  internal/repository, internal/wshub, or web/ live components.
---

# QuantLab Live-Trading Pitfalls

A regression-prevention checklist distilled from the bugs fixed during the
2026-06-02..06-09 live-trading hardening week. Each pattern is a recurring
root cause, not a one-off. When a change touches the area named, walk the
"Rule" lines and confirm none are violated. Commit hashes are cited as
worked evidence — read them when a rule is unclear.

The unifying context: a single delivered exchange event can arrive **twice**
(an `order_update` path and a `delta_report` path both carry fills), the agent
runs against a **real exchange** with hard filters, and detached goroutines run
across **SIGTERM and restart**. Most bugs below are one of these three realities
meeting code that assumed the happy single-delivery, mock-exchange, never-restart
path.

## A. Check-then-act under concurrent re-delivery

Symptom: duplicate rows, double-seeded portfolios, a `filled` row downgraded to
`partial_filled`, two active instances on one account.

Root cause: `EXISTS`-then-`INSERT` / `SELECT`-then-`UPDATE` is non-atomic. Two
concurrent deliveries of the same fill both pass the check, both write.

Rules:
- A check-then-insert path that can be re-delivered MUST have a DB-level backstop
  (a partial unique index), and the insert helper MUST translate a unique
  violation into a **no-op (nil)**, not an error. (`6c9d622` fill dedup;
  `c3f287c` one-instance-per-account)
- A "first one wins" claim (funding, latch, lifecycle transition) MUST be a
  single atomic write that reports won/lost, and the side effect runs **only on
  win**. Do not read-then-decide-then-write. (`93da7b2` `MarkFunded` returns
  `(bool, error)`, claim-first; `2e0c8d9` lifecycle CAS)
- Idempotency lookups go at the **top** of the handler, before frozen/expiry/any
  other gate, and a lookup error is **fail-closed** (return internal_error,
  never reach exchange Submit). (`c802830`)
- Status updates must be **monotonic**: a terminal state (`filled`/`cancelled`/
  `rejected`) is never overwritten by a delayed non-terminal replay. Guard the
  UPDATE with a `status NOT IN (terminal...)` predicate; `RowsAffected=0` is a
  no-op, not an error. (`6c9d622`)
- Dedup keys must match the entity's true identity. A `(client_order_id, ms)`
  key is too coarse when one market sweep fills many lots sharing one
  `transactTime` — use the exchange per-trade id when present, fall back to ms
  only for mock/legacy rows (`trade_id = 0`). (see `cac6b5e` lineage)

## B. Reconciliation & auto-freeze scoping

Symptom: the kill_switch auto-freezes an instance the moment the agent connects,
before it can trade.

Root cause: the freeze/drift decision reconciled assets or instances that the
system does not actually manage, fabricating 100% drift against a zero baseline.

Rules:
- The auto-freeze decision is scoped to **managed assets only** — the keys of the
  reconcile `expected` map (base assets + USDT the account's instances track).
  Unmanaged exchange balances (e.g. testnet faucet coins) are still recorded as
  `ReconciliationDiscrepancy` rows for the forensic trail but MUST NOT arm the
  freeze streak. (`bce1bf1`)
- Account-level reconciliation scope EXCLUDES `retired` instances (terminal,
  positions handed off). `idle`/`paused` still hold positions and stay in scope.
  Any account-rollup query (`ListByAccount`, expected-holdings sum) must filter
  out retired. (`d2b999b`)
- Forensic recording ≠ enforcement. Keep the discrepancy visible in `/live`, but
  separate "what we log" from "what trips the latch".

## C. Binance / exchange integration reality

Symptom: every real-testnet order rejected `-1013 Filter failure`; the user data
stream never connects (`410 Gone`); TRADE events silently dropped; wrong
timestamps/ids on fills.

Root causes & rules:
- The **agent owns exchange-side constraints** because the exchange owns the
  filters. Before submit, floor quantity onto the `LOT_SIZE` step grid, snap
  limit price to the `PRICE_FILTER` tick grid, and reject locally anything below
  `minQty`/`minNotional` (the exchange would `-1013` it anyway). Cache
  `exchangeInfo` per symbol — filters are static. The SaaS dispatcher sizes in
  USD at fixed decimals, finer than most symbols' `stepSize`. (`1e5b8ac`)
- Go's `encoding/json` matches keys **case-insensitively**. Binance's
  `executionReport` uses upper/lower **twin keys** (`E`/`e`, `O`/`o`, `C`/`c`,
  `I`/`i`, `t`/`T`) to carry *different* semantics, so each twin lands in its
  lowercase-named sibling field unless given an exact-match home. A single
  missing twin silently corrupts a field or drops the whole event. Map **all**
  twins explicitly; do not assume one fix covers the struct. (`940dfa2`,
  `8f01d32`)
- **Minimal/synthetic test frames hide these.** A test frame that omits the
  twin keys decodes fine while the real frame fails. Validate decoders against a
  full real-shaped frame, and validate the whole path against **real testnet**,
  not just the mock exchange. (`940dfa2` found 5 collisions a minimal test missed)
- Exchange APIs get removed. listenKey REST is gone (`410`); the UDS uses the WS
  API signature subscription (HMAC). When an integration "can't connect on real
  net but works on mock", suspect a removed/changed endpoint. (`8f01d32`)

## D. Lifecycle, shutdown, and app_role gating

Symptom: SIGTERM mid-epoch leaves a DB task row stuck `running` forever; the
in-process mutex is lost on restart; WS Hub / Cron run where they shouldn't.

Rules:
- Detached/background goroutines MUST run on a cancellable lifecycle context
  (not `context.Background()`), register in a `WaitGroup`, and have a
  `Shutdown(ctx)` that cancels and waits bounded by the deadline. Terminal
  bookkeeping (`MarkFailed`, panic recovery) runs on a **fresh short detached
  ctx** because the lifecycle ctx is dead once cancelled. (`2a008b9`)
- Pair graceful cancel with a **boot-time orphan sweep**: on restart, reconcile
  rows left `running` by a crash (defense in depth — graceful path can be skipped
  by SIGKILL). (`2a008b9`)
- Long-running subsystems are gated by `app_role` per `docs/系统总体拓扑结构.md`
  §2: WS Hub listener = saas+dev only; Cron scheduler = saas only; lab runs
  neither. Gate the **startup goroutines**, but still construct the objects so
  handler wiring needs no nil-handling; Shutdown on a never-listened server is a
  safe no-op. (`4822f2c`)

## E. Schema / DDL drift

Symptom: `NewDB` aborts on fresh migrate with `relation "..." does not exist`
(`42P01`); model-based queries pass while raw DDL fails; CI lint gates red.

Rules:
- Hand-written raw DDL (partial unique indexes, preflight scripts) MUST use
  GORM's **default plural snake_case** table name (`champion_histories`, not
  `champion_history`). `db.Model(&X{})` resolves the plural automatically, so
  the drift is invisible to model queries — only literal table names in raw SQL
  carry the bug. Pin it with a test. (`106e55d`)
- The goose migration DDL and the `AutoMigrate` path MUST stay in sync; the
  drift guard `TestMigrationsMatchAutoMigrate` is the gate (CI integration tag
  runs it only for `./internal/saas/store/`, NOT `./internal/repository/` —
  verify repo-layer schema changes against a real local Postgres). (`6c9d622`,
  `c3f287c`)
- New incremental migrations are linted by squawk under
  `assume_in_transaction = true` (goose wraps each file in a txn). The two
  concurrent-index rules are excluded (CONCURRENTLY can't run in a txn); a new
  `CREATE INDEX` migration MUST carry the `SET lock_timeout` / `SET
  statement_timeout` prelude to satisfy `require-timeout-settings`. (CI green
  fix, PR #15)
- Run `gofmt` on files you touch (CI `lint` job gates a clean tree).

## F. Frontend defensive typing

- Optional fields on view models (`mark_price_ms?: number`) must be `!= null`
  guarded before use in code paths that require the non-optional type, or `tsc`
  fails the build. (`8576cbe`)

## How to use this skill in review

1. Identify which package(s) the diff touches and jump to the matching section(s).
2. For each rule in scope, find the line in the diff that satisfies it — or flag
   its absence. Most of these bugs were *missing* code, not wrong code.
3. The recurring tell across A–D: "does this still hold when the same event
   arrives twice, against a real exchange, across a restart?" If the change
   assumes single-delivery / mock / never-restart, it is suspect.
