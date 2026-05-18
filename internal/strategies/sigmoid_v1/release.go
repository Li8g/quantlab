// DeadBTC release engine for sigmoid_v1. Source of truth:
// docs/strategies/sigmoid_v1.md §5.
//
// Two concerns, kept in this file:
//
//  1. rollNAVPeakWindow — slides the §6 RuntimeState NAV peak window
//     forward by one bar and returns the post-roll peak. Caller must
//     invoke this exactly once per bar with monotonically increasing
//     nowMs (Step() enforces by construction).
//
//  2. evaluateRelease — given the post-roll peak + current portfolio,
//     decide if a §5 ReleaseIntent fires this bar. Returns the
//     quantity capped per §5.2 (DeadBTC * 0.10 AND FloatBTC * 0.20).
//
// Step() (Phase 4c) chains: rollNAVPeakWindow → evaluateRelease →
// build ReleaseIntent. Adapter (Phase 4d) does the DeadBTC→FloatBTC
// book transfer on intent reception (§9 invariants).
package sigmoid_v1

// navPeakWindowDurationMs is decision #8 in spec §11: 30 days. The
// per-bar size = navPeakWindowDurationMs / barIntervalMs is computed
// implicitly here — we just trim by timestamp, no bar arithmetic.
const navPeakWindowDurationMs = int64(30) * 24 * 60 * 60 * 1000

// releaseCooldownMs is decision #9: 7 days minimum between releases.
const releaseCooldownMs = int64(7) * 24 * 60 * 60 * 1000

// §5.2 release-quantity caps. Decision #10 confirmed both values:
//   - Cap 1 (DeadBTC * 0.10) is the redundant defence; rarely binds
//     after DCA has been running for months because the DeadBTC pile
//     becomes large relative to FloatBTC.
//   - Cap 2 (FloatBTC * 0.20) is the primary brake; it protects the
//     Sigmoid target-weight logic from being destabilised by a single
//     fat release shock.
const (
	releaseCapDeadFraction  = 0.10
	releaseCapFloatFraction = 0.20
)

// ReleaseDecision is the §5.1 verdict.
type ReleaseDecision struct {
	ShouldRelease bool
	Quantity      float64 // BTC quantity to move from DeadBTC → FloatBTC
	Drawdown      float64 // for the §5.3 Reason string
}

// rollNAVPeakWindow trims entries older than navPeakWindowDurationMs,
// then appends (nowMs, nav). Returns the max NAV in the resulting
// window — peak per §5.1.
//
// The slice is assumed sorted by timestamp ascending (Step() processes
// bars in order). The "find first non-stale index, slice from there"
// pattern is O(window) amortised and avoids reallocating the backing
// arrays on every bar.
//
// Memory note: the trim slices forward without compacting, so the
// underlying array's [0:keep] tail is unreclaimable until Go's slice
// growth replaces it. At steady state the slice tracks one window of
// bars (~43,200 on 1m), so the headroom is bounded.
func rollNAVPeakWindow(rs *RuntimeState, nowMs int64, nav float64) float64 {
	cutoff := nowMs - navPeakWindowDurationMs

	keep := len(rs.NAVPeakWindowMs)
	for i, ts := range rs.NAVPeakWindowMs {
		if ts >= cutoff {
			keep = i
			break
		}
	}
	rs.NAVPeakWindowMs = rs.NAVPeakWindowMs[keep:]
	rs.NAVPeakWindowValue = rs.NAVPeakWindowValue[keep:]

	rs.NAVPeakWindowMs = append(rs.NAVPeakWindowMs, nowMs)
	rs.NAVPeakWindowValue = append(rs.NAVPeakWindowValue, nav)

	peak := nav
	for _, v := range rs.NAVPeakWindowValue {
		if v > peak {
			peak = v
		}
	}
	return peak
}

// evaluateRelease applies the §5.1 trigger:
//
//	drawdown = (peak - nav) / peak
//	fire if  drawdown > threshold  AND  nowMs - LastReleaseMs ≥ 7 days
//
// Strict `>` on drawdown matches §5.1 wording; cooldown uses `≥` (7
// days exactly is enough). LastReleaseMs == 0 means "never released"
// and is treated as cooldown-expired — not gating cold-start releases
// on a synthetic cooldown that doesn't exist.
//
// peak ≤ 0 (degenerate, only at cold-start before any positive NAV)
// short-circuits to no-fire to keep `drawdown` finite.
func evaluateRelease(rs *RuntimeState, nowMs int64, nav, peak, deadBTC, floatBTC, threshold float64) ReleaseDecision {
	if peak <= 0 {
		return ReleaseDecision{}
	}
	drawdown := (peak - nav) / peak
	if drawdown <= threshold {
		return ReleaseDecision{}
	}
	if rs.LastReleaseMs != 0 && nowMs-rs.LastReleaseMs < releaseCooldownMs {
		return ReleaseDecision{}
	}
	qty := computeReleaseQty(deadBTC, floatBTC)
	if qty <= 0 {
		return ReleaseDecision{}
	}
	return ReleaseDecision{
		ShouldRelease: true,
		Quantity:      qty,
		Drawdown:      drawdown,
	}
}

// computeReleaseQty enforces the §5.2 caps. Either wallet at or below
// zero collapses to qty=0 — no release point in moving from an empty
// DeadBTC pile, and the FloatBTC cap of 0 means the receiving side
// can't absorb anything either.
func computeReleaseQty(deadBTC, floatBTC float64) float64 {
	if deadBTC <= 0 || floatBTC <= 0 {
		return 0
	}
	capDead := deadBTC * releaseCapDeadFraction
	capFloat := floatBTC * releaseCapFloatFraction
	if capFloat < capDead {
		return capFloat
	}
	return capDead
}
