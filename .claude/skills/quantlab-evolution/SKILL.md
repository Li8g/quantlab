---
name: quantlab-evolution
description: Use when modifying or evolving QuantLab source code, especially Go GA/strategy logic, SaaS live trading, Agent idempotency, persistence/schema migrations, API workflows, frontend changes, or regression tests in /home/l9g/quantlab.
---

# QuantLab Evolution

Use this skill for code changes in `/home/l9g/quantlab`. It complements the read-only
review skill; when the user explicitly asks for a read-only staged audit, use the review
workflow instead.

## First read

Before editing, read only the smallest useful set:

1. `CODEX_SKILL.md` for binding architecture and quant invariants.
2. `docs/memory-review-prep-status.md` for the current risk queue and verification notes.
3. `docs/codex-readonly-review-2026-06-05.md` and `docs/code-review-plan.md` only when the
   task touches an archived finding.
4. The domain spec closest to the change:
   - GA/result package: `docs/进化计算引擎*.md`, `docs/decision-ga-reproducibility-constraint.md`
   - OOS: `docs/phase-5d-oos-v1.md`
   - SaaS/live trading: `docs/saas-tier2-schema-v1.md`, `docs/saas-ws-protocol-v1.md`,
     `docs/agent-local-sqlite-rationale.md`
   - Frontend: `docs/frontend-*.md`, `web/README.md`

Treat current source and migrations as authoritative when docs disagree. Known correction:
Goose migrations live under `internal/saas/store/migrations/`, not root `migrations/`.

## Repository map

- Production Go: `cmd/{saas,agent,datafeeder}` and `internal/**`.
- Frontend: `web/` using Vite/React/TypeScript.
- Offline research: `research/`; do not import it into production paths.
- Tests are colocated `_test.go` files. Root `tests/` is a placeholder.
- SaaS schema is dual-sourced by Goose plus GORM AutoMigrate; keep `AllModels()` and
  migrations synchronized.

## Change workflow

1. Classify the change before editing:
   - GA/strategy/result package
   - persistence/schema/repository
   - SaaS API or lifecycle
   - Agent/live order path
   - frontend
   - docs or tooling
2. Identify the invariant that will make the change safe. Prefer a narrow invariant over a
   broad refactor.
3. Patch the smallest ownership boundary that owns the invariant. Avoid cross-layer helper
   packages unless they remove a real dependency inversion.
4. Add or update the permanent regression test before broad cleanup.
5. Run focused verification first, then widen only as risk grows.
6. If the task resolves an archived high/major finding, update the review/status docs after
   the code and tests pass.

## Non-negotiable project invariants

### Architecture

- Engine-layer packages must not import `internal/strategies` concrete packages.
- Strategies are called through `internal/strategy.EvolvableStrategy` and `Adapter`.
- `Adapter.Evaluate` must not spawn goroutines; keep float accumulation serial.
- `RawEvaluateResult` never owns aggregate `ScoreTotal`; aggregation is engine/fitness-owned.
- `internal/wire` protocol changes are additive-only: do not remove fields or change field
  types.

### GA correctness

- Window order is fixed: `6m -> 2y -> 5y -> 10y`.
- Fatal cascade semantics are exact: self-fatal has `Fatal=true, Value=nil, SkippedBy=nil`;
  cascade-skipped has `Fatal=false, Value=nil, SkippedBy!=nil`.
- Fail closed on invalid adapter output before aggregation: `raw == nil` or `raw.Validate()`
  error is not a warning.
- Use `sort.SliceStable` for score/ranking-sensitive ordering.
- Use nil-safe `quant.CompareFitness`; never write sentinel scores into `SliceScore.Value`.
- Preserve determinism: stable ordering, complete `adapter.Reset(plan)`, explicit seeds, and no
  concurrent reductions over score values.
- `fitness_version` changes only for material score semantics changes; numerically equivalent
  refactors should not bump it.

### Money, fills, and state

- Live exchange quantities, prices, fees, and reconciliation identities stay in decimal/string
  space. Float64 is acceptable for backtest/fitness/statistics where bounded drift and
  reproducibility are the real contracts.
- Idempotency paths must fail closed on store read errors before submitting or dropping real
  exchange events.
- Terminal order/trade states must be monotonic; a replayed stale update must not downgrade
  `filled` or another terminal state.
- Fill dedup must have a database backstop, not only check-then-insert application logic.
- For v1 live accounts, prefer one non-retired instance per `(owner_user_id, account_id)`
  unless a real per-instance capital allocation model is added first.

### Persistence

- Use database-conditioned writes for lifecycle transitions: compare-and-set status, active
  champion, retirement, and funding claims.
- Concurrency identities belong in unique indexes or atomic `INSERT ... ON CONFLICT` paths.
- For Postgres partial uniqueness, ensure the predicate matches the business identity and the
  repository maps unique violations to idempotent/no-op or conflict responses as appropriate.
- Any table/model addition must update:
  - GORM model
  - `internal/saas/store/models.go` `AllModels()`
  - Goose migration under `internal/saas/store/migrations/`
  - schema drift/integration tests

### Go implementation discipline

- Do not discard errors with `_`; return, handle, or explicitly justify exceptional cases.
- Make goroutine lifetimes obvious. Every goroutine needs an owner, cancellation path, and
  termination condition.
- Use `context.Context` as the first parameter for request/workflow-scoped operations; do not
  store request contexts in long-lived structs.
- Prefer small domain packages and existing local helpers over new `util`/`common` packages.
- Do not panic for ordinary control flow.

## Current high-value evolution queue

When the user asks what to fix next, bias toward this order unless they specify otherwise:

1. Raw adapter-output validation: engine hot loop, best re-evaluate, `RunReview`, `RunOOS`,
   and `RunStress`.
2. Agent order idempotency: duplicate lookup before expiry/frozen rejection for known
   `client_order_id`; fail closed on SQLite read errors.
3. SaaS trade status monotonicity and terminal replay tests.
4. `spot_executions` database unique backstop plus idempotent unique-violation handling.
5. One-account/one-non-retired-instance v1 invariant, or explicit capital allocation before
   allowing multiple live instances per exchange account.
6. Genesis funding atomic claim-before-seed behavior.
7. Stage 1 architecture cleanup: move `ComputeSharpeStats` out of `verification` or stop
   strategy-layer dependency on it; reduce engine-test concrete strategy coupling.

## Verification commands

Always use a writable Go cache in this environment:

```bash
GOCACHE=/tmp/quantlab-go-cache go test <packages>
```

Focused package sets:

```bash
# GA/result boundary
GOCACHE=/tmp/quantlab-go-cache go test ./internal/resultpkg ./internal/fitness ./internal/engine ./internal/verification ./internal/strategies/sigmoid_v1 ./internal/strategies/toy

# SaaS lifecycle/repository/API
GOCACHE=/tmp/quantlab-go-cache go test ./internal/repository ./internal/api ./cmd/saas ./internal/saas/store

# Agent/live order path
GOCACHE=/tmp/quantlab-go-cache go test ./internal/agent ./cmd/agent ./cmd/saas ./internal/repository

# Frontend
cd web && npm test
cd web && npm run build
```

DB-backed integration checks use the SaaS config, not the Agent config:

```bash
GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/saas/store/ -run TestMigrationsMatchAutoMigrate -args -config=/home/l9g/quantlab/config.yaml
GOCACHE=/tmp/quantlab-go-cache go test -tags=integration ./internal/repository ./cmd/saas -args -config=/home/l9g/quantlab/config.yaml
```

Full gates when the blast radius is broad:

```bash
GOCACHE=/tmp/quantlab-go-cache go test ./...
gofmt -w <changed-go-files>
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.3.0 ./...
```

If `cmd/saas` or `cmd/agent` tests fail with local `httptest.NewServer` socket restrictions,
rerun the same command with sandbox escalation. Treat that as an environment issue only after
the escalated run confirms it.

## Migration checklist

1. Add an incremental Goose migration in `internal/saas/store/migrations/`; do not edit a
   shipped baseline unless the project is intentionally rebasing the schema.
2. Mirror the schema in the GORM model and `AllModels()`.
3. Prefer DB-enforced invariants for concurrency-sensitive identities.
4. For dedup/idempotency, map unique violations to the intended semantic outcome.
5. Run the schema drift guard and the relevant repository/cmd integration package.

## Test checklist

Add negative tests for the failure mode, not only happy paths:

- invalid `RawEvaluateResult` must fail before aggregation
- duplicate/replayed terminal order events must be monotonic no-ops
- idempotency store read error must not submit a new exchange order
- concurrent fill replay must not produce duplicate ledger effects
- stale lifecycle writes must report a conflict or refused transition
- same-account live instance creation must enforce the chosen v1 invariant
- OOS failures must not mutate IS scores

## External reference anchors

Use these as background when refreshing rules, not as substitutes for repo-local evidence:

- OpenAI Codex skills docs: `https://developers.openai.com/codex/skills`
- Go code review comments: `https://go.dev/wiki/CodeReviewComments`
- Go concurrency review notes: `https://go.dev/wiki/CodeReviewConcurrency`
- Go race detector: `https://go.dev/doc/articles/race_detector`
- Go vulnerability management: `https://go.dev/doc/security/vuln/`
- PostgreSQL unique indexes: `https://www.postgresql.org/docs/current/indexes-unique.html`
- PostgreSQL `INSERT ... ON CONFLICT`: `https://www.postgresql.org/docs/current/sql-insert.html`

## Final response expectations

When finishing a code change:

- State the files changed and the invariant now enforced.
- List the exact verification commands and outcomes.
- Call out skipped checks, sandbox escalations, or tests that still do not exist.
- If review docs were updated, name them explicitly.
