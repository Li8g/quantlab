---
name: go-skill-quantlab
description: >-
  Go code-review and code-generation skill tailored for the quantlab project:
  a single-operator, compute-heavy quantitative research codebase (genetic-
  algorithm strategy search, backtesting, TimescaleDB/PostgreSQL persistence)
  running on a self-hosted server and developed through Claude Code. Use this
  skill whenever writing or reviewing quantlab Go code. It keeps the general
  Go-engineering rules that matter for batch/compute workloads and adds a
  quant-correctness section covering the domain bugs (non-reproducibility,
  float money math, look-ahead/leakage, time-series misalignment) that are the
  expensive failures in a quant lab. Derived from the general Go Production
  Skill v4, trimmed for quantlab's domain.
---

# Go Skill — quantlab

A focused profile for quantlab. quantlab is a **batch/compute research system**, not
a request-driven cloud service, so this profile keeps the engineering rules that fire
on compute-heavy code, demotes the service/cloud-native rules to a short appendix, and
adds a **quant-correctness** section — because code that passes every general Go check
can still ship a backtest with a silent look-ahead bug.

## Philosophy for a research codebase

Favor debuggability over cleverness and reproducibility over speed. Two extra priorities
specific to quantlab:

- **Reproducibility is a correctness property.** A run that can't be reproduced can't be
  trusted, debugged, or compared against another run.
- **Internal API churn is fine.** This is a single-operator research codebase with no
  external consumers, so iterate freely on internal interfaces — do not impose
  library-grade API-stability ceremony (see Appendix A).

---

## 1. Toolchain and build discipline

Wire these into CI and the editor save hook:

```
gofmt -l .          # formatting (or gofumpt)
go vet ./...        # suspicious constructs
staticcheck ./...   # static analysis
govulncheck ./...   # vulnerability scan
go test -race ./...  # race detector — mandatory for the GA/worker code
go test ./...
```

Pin a minimum Go version in `go.mod`; track the latest stable line (Go 1.26.x as of
this writing) and support the two most recent major releases. Prefer modern stdlib:
`log/slog`, `math/rand/v2`, native fuzzing, per-iteration loop-variable scoping (Go 1.22+),
context-aware APIs. Run `go mod tidy`; keep the dependency surface small.

---

## 2. Naming and package design

- **MixedCaps**, initialisms keep case: `userID`, `ParseURL`, `ID`, `URL`, `HTTP`, `API`,
  `JSON` — never `userId` or `parse_url`.
- Short local names, descriptive exported names, no stutter (`strategy.Runner`, not
  `strategy.StrategyRunner`).
- Packages singular, lowercase, domain-oriented (`backtest`, `ga`, `marketdata`,
  `ledger`) — never `util`, `common`, `helpers`. Avoid circular dependencies.

---

## 3. Error handling

Errors are values; handle each **exactly once — log it or return it, never both.**

- Add operational context and preserve the chain with `%w`:
  ```go
  if err != nil {
      return fmt.Errorf("evaluating individual %d in generation %d: %w", id, gen, err)
  }
  ```
- Match with `errors.Is` / `errors.As`, never string matching or `==` on wrapped errors.
- Exported sentinels (`var ErrNoData = errors.New(...)`) only when callers branch on them.
- Ignoring a cleanup-path error is fine when idempotent and non-actionable — comment why:
  ```go
  // Rollback returns sql.ErrTxDone if the tx already committed; nothing to do.
  defer func() { _ = tx.Rollback() }()
  ```

---

## 4. Concurrency and lifecycle management — highest priority for quantlab

A GA evaluating 300 individuals over 25 generations is the textbook case for the
unbounded-goroutine footgun. **Every goroutine has an owner, a cancellation path, and a
termination condition.**

- **Never launch one goroutine per individual unbounded:**
  ```go
  // Bad: 300 goroutines per generation, unbounded
  for _, ind := range population {
      go evaluate(ind)
  }
  ```
- **Bound parallelism with errgroup** (typically to CPU count, since fitness evaluation is
  CPU-bound):
  ```go
  g, ctx := errgroup.WithContext(ctx)
  g.SetLimit(runtime.GOMAXPROCS(0))
  results := make([]Fitness, len(population))
  for i, ind := range population {
      i, ind := i, ind // harmless; redundant on Go 1.22+ (per-iteration scoping)
      g.Go(func() error {
          f, err := evaluate(ctx, ind)
          if err != nil {
              return fmt.Errorf("individual %d: %w", i, err)
          }
          results[i] = f // distinct index per goroutine — no shared write
          return nil
      })
  }
  if err := g.Wait(); err != nil {
      return nil, err
  }
  ```
  Writing to `results[i]` from each goroutine is race-free because every task owns a
  distinct index; do **not** append to a shared slice or write a shared `err`.
- Mutexes for shared accumulators (e.g. a running stats struct); channels for handing off
  work. Keep critical sections small; the creator owns cleanup; the sender closes channels.
- Thread `ctx` through evaluation, data loading, and DB calls so a long GA run is
  cancellable.
- `go test -race` is mandatory — most of these bugs are invisible without it.

Context rules: `ctx` is the first parameter, named `ctx`; never store it in a struct;
never pass `nil` (`context.Background()` at the top of a run).

---

## 5. Database and persistence (TimescaleDB / PostgreSQL)

- Use context-aware operations everywhere: `QueryContext`, `ExecContext`, `BeginTx`.
- **Always `defer rows.Close()` AND check `rows.Err()` after the loop** — skipping
  `rows.Err()` silently drops partial result sets, which corrupts backtest inputs without
  any error surfacing.
- Roll back on all failure paths (see §3 for the deferred-rollback rationale).
- **Always parameterize queries;** never concatenate symbol names, dates, or any input
  into SQL.
- For bulk backtest-result or tick ingestion, prefer batched inserts / `COPY` over
  row-by-row; keep transactions scoped to one logical unit, don't leak them across layers.
- TimescaleDB specifics worth a comment in code: ensure hypertable time-column queries are
  bounded by a time range so chunk exclusion can work; don't `SELECT *` across the full
  history when a window suffices.

---

## 6. Quant correctness — the domain bugs that pass every Go check

This is the section a general Go skill omits, and it catches quantlab's expensive failures.

### 6.1 Reproducibility and seeding

- **Every stochastic run takes an explicit seed and records it** with the results. A GA,
  bootstrap, or Monte-Carlo run that can't be replayed can't be debugged or compared.
  Prefer `math/rand/v2` (the modern stdlib choice — higher-quality generators, no implicit
  global seeding); the top-level `math/rand` functions are auto-seeded since Go 1.20 and so
  are non-reproducible by default.
  ```go
  // Bad: top-level functions use a global, auto-seeded source — non-reproducible,
  // and the global is shared/locked across the worker pool.
  x := rand.Float64()

  // Good: explicit source seeded from the run config; record cfg.Seed with the output.
  // NewPCG takes two uint64 seed words; convert if cfg.Seed is int64/uint64 accordingly.
  rng := rand.New(rand.NewPCG(cfg.Seed, 0)) // math/rand/v2; cfg.Seed is uint64
  ```
- **Do not share one `*rand.Rand` across goroutines** — a single `*rand.Rand` is not safe
  for concurrent use, and sharing it makes draws depend on scheduling order, destroying
  reproducibility. Give each worker its own generator seeded deterministically from the run
  seed and the worker index (e.g. `rand.NewPCG(cfg.Seed, uint64(workerIndex))`) so results
  are independent of goroutine scheduling.
- Avoid map-iteration order in anything that affects results — Go randomizes it. Sort keys
  before iterating when the order feeds computation or output.
- Pin any data-dependent ordering (e.g. tie-breaking in selection/ranking) to a stable,
  documented rule.

### 6.2 Money and price arithmetic — pick the type by *role*, not by "it's money"

The dogma "never use `float64` for money" holds only when *money* means a **settlement
amount**: a value that must be exact to the minor unit, reconciles against an external
authority, or carries a fixed rounding rule. Three distinct requirements get conflated under
"money" — separate them before choosing a type:

- **Exactness** — `$0.10` is `0.10`, not `0.0999…`. float64 *cannot* (`0.1` is non-terminating
  in binary; `0.1 + 0.2 == 0.30000000000000004`).
- **Bounded drift** — total error after thousands of adds stays negligible. float64 *can*,
  if accumulation is controlled (fixed serial order; Kahan/Welford on long series → error
  ~2ε regardless of n).
- **Reproducibility** — same input, bit-identical output. float64 *can*, if operation order
  is fixed (`(a+b)+c ≠ a+(b+c)`; also beware Go's permitted FMA fusion differing across
  architectures — pin the arch or block fusion with explicit conversions).

Decide by role:

- **Settlement / exchange-reconciliation / order-construction surface → decimal or scaled
  integers.** Anything that mirrors an exchange's authoritative balance/fill, is compared
  against it, or builds an order that must satisfy tick/lot-size filters. Parse exchange
  decimal strings straight into `shopspring/decimal` or scaled int64 — **never round-trip
  through `float64`**, and **never run the reconciliation comparison in `float64`** (ULP
  noise manufactures false discrepancies or hides real ones). Keep decimals as strings on the
  wire. Scaled int64 is the allocation-free choice for the hottest accounting paths (§7);
  integers below 2^53 are exact.
  ```go
  // Settlement surface — exact, parsed from the exchange string, never via float64.
  fillQty := decimal.RequireFromString(msg.QuantityDecimal)
  pnl = pnl.Add(fillPrice.Mul(fillQty))
  ```
- **Statistic surface → `float64` is correct and standard.** Backtest equity/PnL feeding a
  fitness score, returns, volatility, Sharpe, MDD, drift monitors. A backtest is *not* a
  decimal-exact domain anyway — bps fees (`notional × bps/10000`), compounding, and ratio
  returns produce non-terminating values that decimal would also round, at allocation cost
  and zero exactness gain. The guards that matter here are *bounded drift* and
  *reproducibility* above, not the type.
  ```go
  // Statistic surface — float64 is fine, but accumulate in a fixed serial order.
  var equity float64
  equity += bar.Close * qty // deterministic order; Kahan if the series is long.
  ```
- **The two surfaces meet at a conversion seam** (e.g. SaaS down-converting agent decimals to
  `float64` for a monitoring view). That seam is safe only if the float64 side is purely a
  statistic/display — if it settles or reconciles money, the down-convert reintroduces the bug.
- A **testnet path uses the same types as live** — same API, same tick/lot filters — so it is
  where the decimal order/reconciliation chain gets verified before real money flows; it is
  not a "looser" tier.
- Never compare prices/PnL with `==` after float math; compare within an explicit, documented
  epsilon.

### 6.3 Look-ahead bias and data leakage — the most expensive backtest bug

- **A bar/feature may only use information available at or before its own timestamp.** The
  classic leak is using a bar's close to make a decision that executes at that same bar's
  open, or using a full-sample statistic (mean, scaler, normalization) computed over data
  that includes the future.
- Compute rolling/online statistics, not full-sample ones, inside the backtest loop. If you
  normalize features, fit the scaler only on the training window.
- Fill/execution must lag the signal: a signal formed on bar *t* executes no earlier than
  bar *t+1* (or *t*'s open only if the signal used *t-1* data). Make this lag explicit and
  testable.
- Beware survivorship bias in the symbol universe and restatement/point-in-time issues in
  fundamental data — only use the data as it was known at the time.
- **Write a test that fails on leakage:** feed a series where the future is corrupted past a
  cutoff and assert that decisions up to the cutoff are unchanged.

### 6.4 Time-series alignment and time handling

- **Use UTC internally**; convert to exchange/local time only at boundaries. Mixing zones
  silently misaligns bars.
- Joining multiple series (price, signal, benchmark) must align on timestamps explicitly —
  never assume two slices are index-aligned. Mismatched lengths or offsets shift every
  subsequent calculation by one bar.
- Be explicit about bar-close vs bar-open timestamps and half-open intervals; document which
  convention a series uses.
- Handle gaps (holidays, halts, missing ticks) deliberately — forward-fill, drop, or error,
  but never let a gap silently become a zero or a stale value.
- Calendar/holiday handling and corporate actions (splits, dividends) must adjust the series
  consistently across price and volume.

### 6.5 Numerical stability

- Prefer online/Welford-style accumulation for variance and means over long series rather
  than the sum-of-squares formula, which loses precision through catastrophic cancellation
  (subtracting two large, nearly-equal sums) on `float64` return data.
- Guard against divide-by-zero and NaN/Inf propagation in indicators (e.g. zero volatility,
  empty windows); a single NaN silently poisons every downstream metric.

---

## 7. Performance engineering

Quant compute is genuinely CPU- and allocation-sensitive, so this matters here more than in
typical services — but still **optimize only after measurement.**

- Benchmark first (`go test -bench`); profile with pprof (CPU, heap, goroutine, mutex/block).
- Avoid per-bar allocations in the hot backtest loop; reuse buffers, understand escape
  analysis (`go build -gcflags=-m`).
- Use `sync.Pool` only for high-frequency temporary objects under proven allocation pressure.
- Don't hand-optimize `defer`; modern Go makes it cheap.

---

## 8. Code structure and dependencies

- Small, focused, domain-oriented packages (`marketdata`, `backtest`, `ga`, `ledger`,
  `metrics`); explicit dependencies; no hidden globals.
- **Inject dependencies through constructors** (manual injection — no DI framework for a
  project this size):
  ```go
  type Backtester struct {
      data   marketdata.Source
      ledger *ledger.Book
      logger *slog.Logger
  }
  func NewBacktester(d marketdata.Source, l *ledger.Book, log *slog.Logger) *Backtester {
      return &Backtester{data: d, ledger: l, logger: log}
  }
  ```
- Standard library first. **No premature abstraction** — don't add an interface or generic
  layer before multiple concrete uses exist. Duplication is cheaper than speculative design
  in a research codebase.
- Generics only where they cut real duplication (typed numeric helpers, containers); prefer
  simple concrete code for strategy logic.

---

## 9. Testing and reliability

- **Table-driven tests** with deterministic cases, `t.Run` subtests, clear `got`/`want`
  messages reporting inputs. Use `t.Helper()` and `t.Cleanup()`.
- **Golden-file / regression tests for backtests:** pin a known dataset + seed and assert the
  resulting equity curve / final PnL is unchanged. This is your guard against silent
  numerical or leakage regressions across refactors.
- **Property/invariant tests for the ledger:** cash + position value conserved across fills;
  no negative quantities where disallowed; PnL reconciles.
- **Fuzz** the data parsers (CSV/tick/market-data ingestion) and anything handling untrusted
  or messy input.
- A leakage test (§6.3) and a reproducibility test (same seed → identical output) should be
  permanent fixtures.
- Separate slow integration tests (real TimescaleDB) with `//go:build integration`.

---

## 10. Logging and secrets (lightweight)

- Structured logging via `log/slog`; log run IDs, seed, generation, and key metrics as
  structured fields so runs are queryable.
  ```go
  logger.Info("generation complete",
      "run_id", runID, "generation", gen,
      "best_fitness", best, "seed", cfg.Seed)
  ```
- Never log broker/API credentials, DB passwords, or data-vendor tokens.
- Validate external input (market-data files, config) — sizes, formats, ranges, encodings.

---

## 11. Review checklist (merge gate)

General Go:

- [ ] Errors handled exactly once; no log-and-return; cleanup-path ignores commented.
- [ ] `errors.Is`/`errors.As` instead of string/`==` matching.
- [ ] `ctx` first parameter, propagated through evaluation/data/DB, never stored, never nil.
- [ ] Every goroutine has owner + cancellation + termination; concurrency bounded; no
      unbounded goroutine-per-individual loop.
- [ ] No shared-`err` or shared-slice-append races; `go test -race` passes.
- [ ] `rows.Close()` deferred and `rows.Err()` checked; all SQL parameterized.
- [ ] No hidden globals; dependencies constructor-injected; no premature abstraction.
- [ ] `gofmt`, `go vet`, `staticcheck`, `govulncheck` clean.

Quant correctness:

- [ ] Run takes an explicit seed; seed recorded with output; reproducible (same seed →
      identical result).
- [ ] No shared `*rand.Rand` across workers; per-worker seeds deterministic.
- [ ] No result-affecting reliance on map iteration order.
- [ ] Settlement / exchange-reconciliation / order amounts use decimal or scaled int (parsed
      from strings, never via `float64`; reconciliation comparison not done in `float64`);
      statistic-surface values (fitness/returns/Sharpe/monitoring) may use `float64`. No
      float `==`. Any decimal→`float64` seam serves display/stats only.
- [ ] No look-ahead: decisions use only data available at/before their timestamp; execution
      lags signal; scalers/stats fit on past data only.
- [ ] Series aligned explicitly on timestamps; UTC internally; gaps handled deliberately.
- [ ] NaN/Inf and divide-by-zero guarded in indicators.
- [ ] Backtest golden/regression test and ledger invariants in place.

---

## Sources

Derived from the general Go Production Skill v4 (itself synthesized from Effective Go, the
Google and Uber Go Style Guides, Go team guidance, and cloud-native/SRE practice), trimmed
for quantlab and extended with quantitative-research correctness practice (reproducibility,
decimal accounting, look-ahead/leakage avoidance, time-series alignment, numerical
stability). Validate code samples against the two most recent stable Go releases.
