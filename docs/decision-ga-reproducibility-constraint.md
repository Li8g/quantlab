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
   §5 tolerance gate; record ε once calibrated. *(Deliberate normative edit —
   not folded in silently.)*
2. **Calibrate ε** from the live score distribution (§6).
3. **When building #6**: thread the incremental seam through `EvaluateWindow`,
   then measure the **ScoreTotal** delta (not just signal) under both modes on
   the current champion; confirm < ε before deciding version vs no-version.
4. Keep `ema_divergence_test.go` as standing pre-evidence + a regression guard on
   the cold-start magnitude.
