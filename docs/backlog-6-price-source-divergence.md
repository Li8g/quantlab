# Backlog ⑥ — Signal/Execution Price-Source Divergence

Status: **analysis frozen; v1 = environment-consistency assertion (this doc §6)**
Date: 2026-06-04
Related: [⑤ stale-kline Tick guard](../internal/saas/instance/manager.go) (`ErrInstanceDataStale`),
`docs/saas-ws-protocol-v1.md` (frozen wire protocol), kill_switch Option 3 reconcile auto-freeze.

---

## 1. The problem (as observed)

During real Binance **testnet** end-to-end runs the SaaS sized orders off
**imported klines** while the agent filled against the **testnet order book** —
two prices that were ~66893 (kline) vs ~56306 (book). The signal/sizing price
source and the execution venue are physically separate.

Concretely, sizing happens at the SaaS:

```
wshub.buildTradeCommand:  qty = OrderIntent.QuantityUSD / latestClose   (latestClose ← kline close)
```

and the agent only ever receives `quantity_decimal` — never the reference price
it was sized against. When the kline price and the live book diverge, the
realized notional ≠ the intended USD.

## 2. Why this is largely a *testnet* artifact, not a prod bug

- **Same venue in prod.** The datafeeder imports **mainnet** klines
  (`data.binance.vision` archive / `api.binance.com` REST fallback) and a prod
  agent trades **mainnet**. Kline-close ≈ live book within intra-minute drift.
  The huge testnet gap exists only because testnet has its own simulated book
  disconnected from the mainnet historical archive (testnet has no usable kline
  history of its own — using mainnet klines is the *intended* testnet setup).
- **The ledger already self-corrects.** ③ folds the **real** fill notional
  (`FillQuantity × FillPrice`) into `PortfolioState`, so the books reflect what
  actually traded, not the intended USD.
- **Reconcile is the post-hoc net.** kill_switch Option 3 auto-freezes on
  managed-asset drift, bounding the blast radius of any sizing error to a couple
  of reports.
- **⑤ already covers the time axis.** The stale-kline Tick guard refuses to
  trade when the newest 1m bar is too old, removing the "priced off a stale/zero
  close" slice.

So in production the residual divergence is ordinary intra-minute market-order
slippage — already measured by `ActualSlippageBps` — not a standing correctness
bug.

## 3. The three options considered

Shared pipeline:

```
SaaS                                              Agent
manager.Tick (QuantityUSD, latestClose←kline)     handleTradeCommand
  recordingDispatcher (pre-insert pending)          frozen/expiry/idempotency/parse
  wshub.Dispatch                                     exchange.Submit (MarketRef = book, captured POST)
  buildTradeCommand: qty = USD / latestClose ──WS─► Ack + OrderUpdate(fills, ActualSlippageBps)
  agentMsgs.Hook ◄──────────────────────────WS──── (回程)
    Ack→TradeRecord status; OrderUpdate→SpotExecution(TradeID dedup)
    → ③ fold next Tick → reconcile / auto-freeze
```

### A — execution-time price-divergence guard (per-order, reject)

| Hop | Change |
|---|---|
| `internal/wire` | TradeCommand += `reference_price_decimal,omitempty` (additive, like `trade_id`; note in frozen §5.8) |
| `wshub.buildTradeCommand` | write `latestClose` into the new field |
| **agent** | **new pre-submit step**: market order fetches the book, computes `|ref-book|/book` bps, rejects (`price_divergence`) above a threshold — never submits |
| SaaS回程 | reject Ack → existing `ackToTradeStatus` → rejected; pending row swept by ④ (zero new code) |

- **+** Only layer that stops a bad order *before* execution; defense-in-depth
  with ⑤ (time) and reconcile (post-hoc). Freeze-respecting (additive field).
- **−** **testnet-hostile by default** (divergence is always huge → rejects every
  order unless threshold cranked/disabled per-env). Adds a book round-trip per
  market order (latency + rate-limit weight) for a true pre-submit check — the
  agent's `MarketRef` is captured *post*-POST today, so a clean pre-check needs
  an extra `BookTicker`. Another `[INVENTED v1]` threshold with no real-paper
  data to tune it. Grows agent (safety-critical edge) complexity. In prod it
  only catches a narrow band (fresh-but-moved / misconfig), and **misconfig is
  better caught at deploy time, not per order**.

### B — observe + warn only

Same wire field + SaaS populate; agent computes divergence and logs / reports
`agent_errors` but **still submits** (surfaces in the Tier L `/live` panel).

- **+** testnet-safe (never blocks); smallest behavior change; collects the real
  divergence distribution to later tune A; reuses the `agent_errors → Tier L`
  plumbing.
- **−** Detection, not protection — a real flash divergence still executes.
  Overlaps with `ActualSlippageBps` (the SaaS-ref vs book-ref distinction is
  ~zero in prod). Likely a stepping stone to A → two PRs.

### C — re-architect to agent-side USD sizing

Send `QuantityUSD` over the wire; the agent sizes `qty = USD / book` against the
live book (LOT_SIZE floor already lives in `symbolfilters.go`).

- **+** Root-cause fix: sizing and execution use the **same** price → intended
  USD ≈ realized USD by construction; ⑥ *disappears*. Also kills the ⑤
  `latestClose=0` sizing failure. testnet and prod behave identically.
- **−** **Reverses a deliberate frozen decision** (`§5.8`: TradeCommand carries
  asset-unit qty, "precision lost intentionally"); **non-additive** wire change +
  doc rewrite + marker churn. Moves financial sizing onto the safety-critical
  edge (a bug spends real money wrong). **Hurts determinism/replay** —
  engine-side sizing was reproducible; agent-side depends on the live book at
  submit. Biggest diff for the smallest prod payoff (prod klines ≈ book already).

## 4. Priority of "must hard-block misconfig/flash before execution"

Lower than it feels. The instinct comes from the testnet trauma (huge divergence,
repeated self-freeze), which is an **inter-venue testnet artifact**, not prod.
Decomposed:

- **Misconfig** (agent pointed at the wrong env/symbol) is a **static, deploy-time**
  condition — *every* order is wrong, deterministically. Its right capture layer
  is a **one-time startup/handshake assertion**, not a per-order price guard.
  And it is already bounded by reconcile auto-freeze (a few small orders → halt).
- **Flash crash** (fast intra-minute move) mostly *is* ordinary market-order
  slippage that ③ absorbs at the **real** market price. Worse, a divergence
  guard **cannot tell a bad mis-size from a favorable/unfavorable market move** —
  hard-blocking a market order on price is second-guessing the order type. The
  correct tool for price protection already exists: **limit orders** (with a
  strategy-chosen band).

Hidden cost of hard-blocking: it is **itself a new failure mode** — a tight
threshold or a transiently stale book fetch rejects *legitimate* orders, so the
instance silently stops trading during high volatility (exactly when you most
want to act). For a trading system, "refused to trade through the move" can cost
more than "filled slightly off." You would be trading a rare correctness risk
for an availability risk, added to the safety-critical edge.

**Verdict:** the genuinely prod-valuable sliver is not a per-order price guard —
it is a **deploy/startup consistency assertion** for the misconfig case, plus
leaning on **limit orders** for flash protection and reconcile as the post-hoc
net. The per-order guard (A) is deferred until real-paper divergence data exists
to tune it; C is rejected.

## 5. What "env+symbol consistency assertion" is (and is *not* A)

| | A: execution-time price guard | startup consistency assertion |
|---|---|---|
| Granularity | every order, runtime | once, at startup / handshake |
| Compares | sizing price vs live book (**magnitude** vs threshold) | agent **env/symbol identity** vs expectation (no price) |
| Lives in | wire field + agent submit branch | handshake metadata, off the per-order path |
| Threshold | yes (untunable without data) | none (boolean match) |
| Per-order cost | extra book round-trip | zero |
| testnet | hostile (always rejects) | friendly (compares identity, not absolute price) |
| Flash crash | catches it (but shouldn't) | out of scope (left to limit orders + ③) |

The consistency assertion takes only the **misconfig** half of A's intent, at a
cheaper and more correct layer, and deliberately drops the per-order flash guard.

## 6. Decision — v1 scope

Implement the **environment-consistency assertion** at the WS handshake:

1. **`wire.Hello += Environment string` (additive).** The agent derives it from
   its exchange base_url: `testnet` / `mainnet` / `mock`. Old agents send empty
   → check skipped (backward compatible).
2. **SaaS declares its expected environment** via config
   (`live.expected_environment`, empty = no check).
3. **Handshake assertion** (after token verify, before `auth_ok`): if the
   expected env is set and the agent's reported env differs:
   - **`app_role=saas` (prod) → hard `auth_fail`** (a prod fleet must not run a
     misconfigured agent).
   - **dev/lab → WARN + allow** (so the *intended* testnet workflow — mainnet
     klines + testnet agent — keeps running, with the mismatch visibly flagged).

   The hardness is **app_role-gated**, which sidesteps testnet-hostility entirely
   using the boundary the codebase already relies on.

### Out of scope (already covered / deferred)

- **Symbol consistency** is already handled: the SaaS sets
  `TradeCommand.Symbol = inst.Pair`, and the agent's `symbolFilterFor` fetches
  exchangeInfo and **rejects unknown symbols at submit** (`-1013` /
  `ErrExchangeRejected`). No separate symbol assertion needed for v1.
- **Per-order price guard (A)** — deferred to real-paper divergence data.
- **agent-side USD sizing (C)** — rejected (frozen-protocol reversal, determinism
  loss, disproportionate cost).
- **DB-recording the handshake mismatch into `agent_errors`/Tier L** — optional
  follow-up; v1 surfaces it via a loud WARN (and prod hard-fail).
