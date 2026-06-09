# QuantLab Development Evolution Memo

Status: living memo, intended to be updated as architecture decisions and fixes land.
Last updated: 2026-06-07.

## Purpose

QuantLab's core product loop is:

```text
historical data -> GA evolution -> robust candidate selection -> small-capital live experiment -> promote / retire / freeze
```

The next code evolution should make this loop safer and more explicit. The project is a
single-trader platform, not a general multi-tenant SaaS product, so the right direction is to
reduce live-trading ambiguity and strengthen the correctness gates between research, candidate
selection, and real-money execution.

## Consistency Check

This memo was checked against:

- `docs/skills/quantlab-evolution/SKILL.md`
- `docs/memory-review-prep-status.md`
- `docs/codex-readonly-review-2026-06-05.md`
- `docs/saas-tier2-schema-v1.md`

Current confirmed state:

- Lifecycle/CAS work for Retire, instance status transitions, and DeployChampion scope/retired
  guards has landed.
- Raw adapter-output validation is still not enforced consistently before aggregation.
- Agent order idempotency still needs fail-closed behavior on store read errors and duplicate
  terminal replay handling before expiry/frozen rejection.
- SaaS trade status updates still need monotonic terminal-state protection.
- `spot_executions` fill dedup still needs a DB unique backstop.
- The schema/design docs still allow one account to map to many instances, while current genesis
  funding and reconciliation logic assume whole-account ownership by a single live controller.

## Guiding Decision

For v1, optimize for:

- one human trader
- one exchange account under active live control at a time
- small-capital production experiments
- strong auditability
- deterministic GA/replay evidence
- fail-closed live-order behavior

Do not optimize v1 for:

- many tenants
- team RBAC
- multiple simultaneous live strategies sharing one exchange account
- per-instance capital allocation without an explicit capital ownership model
- broad package reshuffling before correctness boundaries are closed

## High-Level Structural Direction

### 1. Add A GA Raw-Result Boundary

Current problem: `RawEvaluateResult.Validate()` exists, but engine, review, OOS, and stress paths
do not all use one fail-closed boundary before aggregation.

Target shape:

- A shared adapter-output validation entry point.
- Mode-aware validation for IS, OOS, replay/review, and stress.
- Rejection of `raw == nil`, empty raw results, duplicate/out-of-order windows, `WindowOOS`
  inside IS raw, and invalid Fatal -> skipped cascade semantics.
- Engine and verification paths call the same boundary before any `fitness.AggregateScoreTotal`
  operation.

Why this matters: the GA winner is only meaningful if the raw strategy output is structurally
valid before scoring.

### 2. Turn Live Order Handling Into A State Machine

Current problem: order lifecycle behavior is split across Agent idempotency, SaaS message
handling, repository writes, and fill persistence.

Target shape:

```text
TradeCommand -> AgentAck -> ExchangeOrderEvent -> Fill -> PortfolioState -> Reconciliation
```

Each transition should have explicit rules:

- duplicate terminal command replay returns duplicate-terminal semantics, not expired/rejected
- idempotency store read errors fail closed before exchange submit
- old order updates cannot downgrade terminal states
- fill insertion is idempotent with DB-backed uniqueness
- portfolio state updates consume deduped fills only

Why this matters: small-capital live experiments still use real exchange orders. Correctness
must not depend on message arrival order or process-local memory.

### 3. Make DB Constraints The Concurrency Boundary

Current problem: several important invariants have historically been enforced by read-then-write
application logic.

Target shape:

- DB-conditioned writes for lifecycle state transitions.
- Unique or partial unique indexes for business identities.
- Atomic `INSERT ... ON CONFLICT` or unique-violation handling for idempotency/dedup.
- Repository APIs return conflict/refused/no-op outcomes based on `RowsAffected` or unique
  constraint results.

High-priority examples:

- `spot_executions` dedup identities.
- one non-retired live instance per account for v1.
- genesis funding claim-before-seed behavior.

### 4. Collapse Live Account Semantics For Single-Trader V1

Current mismatch: `docs/saas-tier2-schema-v1.md` says an account may map to many instances, but
current genesis funding uses a whole-balance anchor and effectively assumes one instance per
exchange account.

Recommended v1 invariant:

```text
at most one non-retired StrategyInstance per (owner_user_id, account_id)
```

Implementation direction:

- Add a partial unique DB backstop on `(owner_user_id, account_id) WHERE status != 'retired'`.
- Keep retired rows from blocking recreation.
- Map create-instance unique violations to the existing conflict path.
- Add repo/API tests proving the second non-retired same-account instance is rejected.
- Defer multi-instance-per-account until `TradingAccount` / `LiveAllocation` or equivalent
  capital ownership concepts exist.

Why this matters: for a personal trading platform, one live controller per exchange account is
simpler and safer than half-supporting shared-account capital allocation.

### 5. Separate Candidate Selection From Live Experiment

Current risk: "best GA score" can be mistaken for "safe to trade".

Target shape:

```text
GA best gene -> CandidateEvidencePackage -> paper/testnet gate -> prod_small experiment
```

Candidate evidence should include:

- gene parameters, fingerprint, seed, schema/fitness/fingerprint versions
- data hashes and exact IS/OOS windows
- top-N context, not only the winner
- score components, trade count, drawdown, turnover, fee/slippage sensitivity
- OOS, stress, replay, and robustness validation results
- promotion gate verdict and reason

Why this matters: GA is a search procedure over many trials. The live candidate needs an audit
record that explains why it survived the search and validation process.

## Single-Trader Product Shape

### Control Plane

Treat the platform as a local/private trading control plane:

- keep `owner_user_id` for compatibility, but avoid adding multi-tenant complexity
- focus dangerous operations on explicit confirmation and audit
- prioritize Promote, Retire, Deploy, Start live, Freeze, Clear freeze, and environment switch
  safety over role hierarchies

### Runtime Modes

Make live mode explicit:

```text
research -> paper/test_mode -> testnet_live -> prod_small -> promoted_live
                                      |             |
                                      v             v
                                   frozen       retired
```

Each mode should define allowed actions. For example, frozen mode should allow duplicate-terminal
queries, cancel/sync/reconcile, and state inspection, but should block brand-new exchange submits.

### Operator Cockpit

Frontend evolution should prioritize a personal trader cockpit:

- current account and environment
- current live champion
- frozen/risk state
- recent commands, acks, order updates, and fills
- reconciliation discrepancies
- candidate evidence comparison
- live experiment status and budget consumption
- audit/export view

Avoid spending early effort on SaaS tenant/admin screens.

## GA-To-Live Robustness Backlog

### P0: Correctness Gates

1. Raw adapter-output validation boundary.
2. Agent idempotency fail-closed and duplicate-terminal replay behavior.
3. SaaS order/trade status monotonicity.
4. `spot_executions` DB dedup backstop.
5. One-account/one-non-retired-instance v1 invariant.

### P1: Candidate Evidence And Promotion

1. Introduce `CandidateEvidencePackage` or equivalent persisted record.
2. Store top-N and rejected candidates enough to understand selection pressure.
3. Gate Promote/Deploy on evidence package status, not score alone.
4. Preserve immutable data hashes, seeds, versions, and validation windows.

### P2: Robustness Validation

1. Walk-forward validation interface.
2. Regime slices: bull, bear, sideways, high volatility, low volatility.
3. Fee/slippage perturbation checks.
4. Leakage tests that corrupt future data and assert cutoff decisions do not change.
5. Multiple-testing metadata: number of trials, population, generations, seeds, and run count.
6. Later: DSR/PBO/CPCV-style overfitting diagnostics if the data volume supports it.

### P3: Live Experiment Ledger

1. Add an explicit live experiment record linked to champion/candidate evidence.
2. Track capital budget, max order, max daily loss, max cumulative loss, minimum observation
   period, and minimum fill count.
3. Store live PnL, realized fees/slippage, manual interventions, freeze reasons, and final
   verdict.
4. Keep live experiment results separate from IS/OOS training data unless a deliberate research
   import step is created.

## Do Not Do Yet

- Do not support multiple live strategies sharing one exchange account until capital allocation
  is modeled.
- Do not treat highest `ScoreTotal` as sufficient for production deployment.
- Do not broaden RBAC/multi-tenant abstractions before personal-trader safety is complete.
- Do not mix live experiment results back into training/OOS by default.
- Do not refactor package structure broadly before Raw validation, order state, and DB backstops
  are closed.

## Update Checklist

When this memo is updated, also check whether the change should update:

- `docs/skills/quantlab-evolution/SKILL.md`
- `docs/memory-review-prep-status.md`
- `docs/codex-readonly-review-2026-06-05.md`
- `docs/code-review-plan.md`
- `docs/saas-tier2-schema-v1.md`
- `CODEX_SKILL.md`

If a recommendation becomes implemented behavior, move it from "target shape" to "current
confirmed state" and include the verification command that proved it.
