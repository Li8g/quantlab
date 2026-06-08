# Decision — GA reproducibility: relax the exact-hash change-gate to tolerance

Status: **decided 2026-06-04.** Supersedes the implicit "any change to scoring
math → `fitness_version` bump" doctrine.
Related: `ga-cpu-optimization` (memory), `review-replay-a1` (memory),
`CLAUDE.md` (Version Constants / Key Invariants),
harness `internal/strategies/sigmoid_v1/ema_divergence_test.go`.

---

## 1. The original constraint, and why it was right then

The design fixed **bit-level reproducibility** as a hard constraint for GA
stability + traceability. In the prototype phase that was correct: cheap
insurance when there are no champions to protect and no performance pressure.

## 2. It is actually *two* constraints bundled

- **(A) in-version determinism** — same code + same gene + same bars + same
  seed → same result, every run. Enforced by serial float accumulation, no
  concurrent reduce, `sort.SliceStable`, single `time.Now`, complete
  `Adapter.Reset`. **Keep unconditionally** — cheap, foundational, orthogonal
  to performance.
- **(B) exact-hash as the cross-*implementation* change gate** — "if the
  scoring math changes *at all*, bump `fitness_version` + rebaseline." **This is
  the one taxing every optimization.**

The question is only about (B).

## 3. What (B) buys vs costs

- **Buys:** a perfect tripwire against *accidental* scoring changes; and
  cross-implementation *trajectory* reproducibility.
- **Costs:** (B) cannot distinguish
  - (a) "I changed *what the strategy does*" — a semantic change (must version), from
  - (b) "I changed *how I compute the same quantity*, to ~machine epsilon" — a
    numerically-equivalent refactor.

  It nukes both with the same heavyweight migration. Every legitimate speedup
  that reorders a sum (incremental indicators, SoA, single-pass windows) is
  treated as a redesign.

## 4. (B) is mostly *doctrine*, not a hard code gate

`TestEvaluateDeterministic` is exact, but the **replay** requirement
(`TestReplayWithinTolerance`) is already *ScoreTotal-within-tolerance* — the
doc's ask, not bit-exact. `bars_hash` / `fingerprint` are exact, but they guard
**inputs** (data provenance) and **gene identity**, not float-accumulation
order. So (B) lives mostly in CLAUDE.md prose + discipline. The "tooling debt"
is self-inflicted: to *serve* (B) you must build rebaseline machinery for every
score-touching change.

## 5. Decision

Replace the implicit exact gate with an explicit **tolerance gate**:

- replay ScoreTotal **relative drift > ε → version event** (semantic change:
  bump `fitness_version` + rebaseline).
- **within ε → numerically-equivalent**: no version event.

**Stays exact:** in-version determinism (A); `bars_hash` (input provenance);
`fingerprint` (gene identity); the "challengers with different `fitness_version`
must not be compared by score" rule.

**Given up:** cross-*implementation* **trajectory portability** — new code may
select a *different* champion from the same seed. We never used this: a champion
is an artifact of its own code + version; you do not re-run an old Epoch under
new code expecting the same winner (that is rebaselining). Each champion stays
reproducible **under its own version** → audit/traceability intact.

## 6. ε is a policy lever — set it by decision-sensitivity, not by float math

Any ε > 0 implies: accept *trajectory* divergence from sub-ε perturbations,
because GA selection is a **sort** and a sort amplifies epsilon. **ε governs
score comparability, not trajectory identity.**

Set ε at the threshold below which a ScoreTotal difference cannot change a
**promote / cross-challenger comparison** decision — calibrate from the live
score distribution. It must sit well above genuine numerically-equivalent drift
(~1e-9) and at/below the smallest score gap the comparison logic treats as
meaningful. **Provisional ε = TBD (calibrate before relying on it)** — same
"don't pick a threshold without data" discipline as the freeze-bps knob.

## 7. Worked example — #6 incremental indicators

#6 bundles two *different* changes:

- **MAV (volRatio)**: windowed mean of |Δclose| → incremental rolling sum.
  **Same quantity**, only float-accumulation order differs → ~1e-9
  (numerically-equivalent).
- **EMA (priceDeviation)**: windowed cold-start EMA over a sliding 900-bar
  buffer → infinite-history EMA from bar 0. **Different quantity.** Measured by
  `ema_divergence_test.go` (synthetic BTC-range walk, 5000 bars, steady state):

  | EMA period | EMA rel-delta (max) | signal Δ (A1·Δpd, max) |
  |---|---|---|
  | 50  | 0 (machine) | 0 |
  | 100 | 2.8e-10 | 1.4e-10 |
  | 200 | 3.3e-6  | 1.7e-6 |
  | 300 (gene clamp max) | **7.8e-5 (~0.008%)** | 3.9e-5 |

  Worst case (longest period) is ~8e-5 relative — **5+ orders of magnitude below
  1%** — because `stepHistoryCap = 3×maxperiod = 900` decays the cold-start seed
  to ~(1-α)^900 ≈ e⁻⁶ at the evaluation point. Short periods reach machine zero.

**On this evidence**, #6's EMA change is within ~1e-4 relative at the signal
level — well inside any sane ε. Under the §5 tolerance gate, #6 (EMA + MAV) is a
**within-tolerance optimization, not a semantic redesign** → no `fitness_version`
bump expected on score-comparability grounds.

**Caveats (honest):**
1. This is **signal-level, not ScoreTotal** — compounding over the NAV path and
   the true cross-challenger number need the #6 seam through `EvaluateWindow`,
   measured when #6 is actually built. The harness is the cheap *pre-evidence*
   that de-risks starting it.
2. Even within ε, the GA **trajectory** can still flip (sort amplification) —
   that is the cross-implementation portability explicitly given up in §5, not a
   score-trust problem.

## 8. Action items

1. **CLAUDE.md**: rewrite the implicit "scoring change → version bump" into the
   §5 tolerance gate; record ε once calibrated. *(Done 2026-06-04 — see the
   "Reproducibility gate" note under Version Constants.)*
2. **Calibrate ε** from the live score distribution (§6). *(Done 2026-06-08 —
   see §9 below. ε = 1e-4; CLAUDE.md updated.)*
3. **When building #6**: thread the incremental seam through `EvaluateWindow`,
   then measure the **ScoreTotal** delta (not just signal) under both modes on
   the current champion; confirm < ε before deciding version vs no-version.
4. Keep `ema_divergence_test.go` as standing pre-evidence + a regression guard on
   the cold-start magnitude.

## 9. Calibration measurement — 2026-06-08

**Snapshot:** 36 persisted challengers (`gene_records`), 13,436 within-population
evaluations (`evaluation_traces`), 12,513 distinct scores, all non-fatal.

### Score distribution

| Layer | Min gap | p10 gap | Median gap | Notes |
|---|---|---|---|---|
| `gene_records` (36 challengers) | 1.5e-5 | — | — | persisted best-of-epoch |
| champion-level contenders (top 5) | 4.4e-5 | — | — | relevant for promote |
| champion vs #2 | 2.88e-4 | — | — | current champion's margin |
| `evaluation_traces` distinct scores | 1.36e-11 | 2.19e-6 | 1.91e-5 | within-pop; many near-ties |

### Decision: ε = 1e-4 (relative ScoreTotal)

**Rationale:**

- **Lower bound satisfied:** ε = 1e-4 is ~5 orders above established numerical
  noise (~1e-9).
- **Promote decisions protected:** ε < current champion-vs-#2 gap (2.88e-4). A
  sub-ε code change cannot make any existing contender overtake the champion.
- **Trajectory divergence accepted:** ε > the minimum champion-level gap
  (4.4e-5). Sub-ε changes *may* flip #3 vs #4 in the top population. This is
  the cross-implementation trajectory portability explicitly given up in §5.
- **EMA consistency with #6 pre-evidence:** the signal-level delta at worst is
  ~4e-5 (A1·Δpd, `ema_divergence_test.go`). The propagation through NAV →
  ScoreTotal strongly compresses this; the actual ScoreTotal delta is expected
  well below 1e-4. *(Confirmation deferred to action item 3 above, when the #6
  seam is built.)*

**Caveats:**
- Dataset is thin (36 challengers). As epochs accumulate, the minimum
  `gene_records` gap will shrink. Recalibrate when challenger count >> 100 or
  if the champion-vs-#2 gap narrows below 3×ε.
- ε is a relative threshold on `ScoreTotal.Value`. The comparison logic remains
  `CompareFitness` with no epsilon — ε only governs the version-event decision,
  not the sort.

## 10. #6 shipped — 2026-06-08 (action item 3 closed)

Commits `e5cf9e2` + `f7b8929`. All tests green. No `fitness_version` bump.

### What was built

**`e5cf9e2` — incremental indicator state**

`internal/strategies/sigmoid_v1/indicator_state.go` introduces `incrIndicatorState`:

- **EMA_long**: α-recurrence from bar 0 (no per-bar cold-start reset over the
  sliding 900-bar buffer). Same α = 2/(period+1) as `quant.EMA`.
- **MAV short/long**: difference ring buffer + running sum. Same last-N absolute
  diffs as `MAVAbsChangeWindow` — result is bit-identical to the batch path.
- **logReturn lookback**: close ring of size MAVShortPeriod+1; `buf[head]` is
  the close from MAVShortPeriod bars ago.

`stepCore` (live trading) is unchanged. A new `stepCoreFromIndicators` is
extracted as the shared compute body; `evaluateWindow`'s hot loop calls it
directly with pre-resolved O(1)/bar values (铁律 1 preserved).

**`f7b8929` — skip DebugSnapshot in backtest**

`stepCoreFromIndicators` gains a `wantDebug bool` parameter. `evaluateWindow`
passes `false`: `applyStrategyOutput` never reads the `DebugSnapshot` field and
`evaluateWindow` does not return it, so the 4 heap allocs/bar from
`buildDebugSnapshot` were pure waste in the backtest path.

### Measured performance (87.6k-bar 10y window, Intel i7-1355U)

| Metric | Before #6 | After e5cf9e2 | After f7b8929 |
|---|---|---|---|
| ns/op | ~700 000 000 | 20 764 000 | **17 789 000** |
| Speedup | — | ~34× | **~39×** |
| allocs/op | ~437 600* | 268 901 | **6 099** |
| B/op | ~630 MB* | 11.9 MB | **6.9 MB** |

\* estimated: 87.6k × 900-elem EMA alloc (7.2B bytes) + 87.6k × 4 debug allocs.

### Numerical verdict

- **MAV**: bit-identical to batch (ring buffer = same diffs, same window). Not a
  version event.
- **EMA**: incremental divergence measured by `TestIncrIndicatorState_EMAWithinTolerance`
  and `TestEMADivergence_WindowedVsIncremental`:

  | EMA period | EMA rel-delta (max) | Signal term Δ (A1·Δpd) |
  |---|---|---|
  | 50  | ~0 (machine) | ~0 |
  | 100 | 2.8e-10 | 1.4e-10 |
  | 200 | 3.3e-6  | 1.7e-6 |
  | 300 (max) | **7.8e-5** | **3.9e-5** |

  Signal-level Δ ≤ 4e-5 at worst. Propagation through NAV → `ScoreTotal`
  compresses this further; the ScoreTotal delta is expected well below ε = 1e-4.
  **No `fitness_version` bump required.**

- **DebugSnapshot skip**: no math touched; pure allocation removal. Not a
  version event.
