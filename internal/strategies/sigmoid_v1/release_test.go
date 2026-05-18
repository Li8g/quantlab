package sigmoid_v1

import (
	"testing"
	"time"
)

// dayMs is shorthand for tests that want to express "N days from
// reference epoch" without depending on real wall-clock.
const dayMs = int64(24) * 60 * 60 * 1000

// refMs anchors the release tests at a fixed UTC instant so that
// "+7 days from refMs" reads naturally. Value is arbitrary but past
// 1970 to keep timestamps positive.
var refMs = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

// ----- rollNAVPeakWindow -----

func TestRollNAVPeakWindow_FirstCallSeeds(t *testing.T) {
	rs := freshRuntimeState()
	peak := rollNAVPeakWindow(&rs, refMs, 1000)
	if peak != 1000 {
		t.Errorf("first call: peak = %v, want 1000", peak)
	}
	if len(rs.NAVPeakWindowMs) != 1 || len(rs.NAVPeakWindowValue) != 1 {
		t.Errorf("first call: window lens = %d/%d, want 1/1",
			len(rs.NAVPeakWindowMs), len(rs.NAVPeakWindowValue))
	}
	if rs.NAVPeakWindowMs[0] != refMs || rs.NAVPeakWindowValue[0] != 1000 {
		t.Errorf("first call: window[0] = (%d, %v), want (%d, 1000)",
			rs.NAVPeakWindowMs[0], rs.NAVPeakWindowValue[0], refMs)
	}
}

func TestRollNAVPeakWindow_TrimsStaleEntries(t *testing.T) {
	// Hand-stamp three entries: 40 days, 20 days, and 10 days old.
	// Cut-off is now - 30d, so the 40-day entry should be dropped.
	rs := freshRuntimeState()
	rs.NAVPeakWindowMs = []int64{refMs - 40*dayMs, refMs - 20*dayMs, refMs - 10*dayMs}
	rs.NAVPeakWindowValue = []float64{500, 800, 700}

	peak := rollNAVPeakWindow(&rs, refMs, 900)
	if len(rs.NAVPeakWindowMs) != 3 {
		t.Fatalf("after trim+append: len = %d, want 3 (-1 trimmed, +1 appended)",
			len(rs.NAVPeakWindowMs))
	}
	if peak != 900 {
		t.Errorf("peak = %v, want 900 (max of 800, 700, 900)", peak)
	}
}

func TestRollNAVPeakWindow_AllStaleDropped(t *testing.T) {
	// Everything older than 30 days; only the newly appended entry
	// survives.
	rs := freshRuntimeState()
	rs.NAVPeakWindowMs = []int64{refMs - 60*dayMs, refMs - 40*dayMs}
	rs.NAVPeakWindowValue = []float64{1500, 2000}

	peak := rollNAVPeakWindow(&rs, refMs, 1000)
	if len(rs.NAVPeakWindowMs) != 1 {
		t.Fatalf("all stale: len = %d, want 1 (only the appended)",
			len(rs.NAVPeakWindowMs))
	}
	if peak != 1000 {
		t.Errorf("all stale: peak = %v, want 1000 (new entry)", peak)
	}
}

func TestRollNAVPeakWindow_PeakIsHistoricalMax(t *testing.T) {
	// Sequential appends; the peak must remember the highest seen
	// value even if the latest NAV drops.
	rs := freshRuntimeState()
	rollNAVPeakWindow(&rs, refMs+0*dayMs, 1000)
	rollNAVPeakWindow(&rs, refMs+1*dayMs, 1200)
	rollNAVPeakWindow(&rs, refMs+2*dayMs, 1100)
	peak := rollNAVPeakWindow(&rs, refMs+3*dayMs, 900)
	if peak != 1200 {
		t.Errorf("peak = %v, want 1200 (historic max)", peak)
	}
}

func TestRollNAVPeakWindow_CutoffBoundaryIsInclusive(t *testing.T) {
	// An entry exactly at now - 30d should be KEPT (ts >= cutoff).
	rs := freshRuntimeState()
	rs.NAVPeakWindowMs = []int64{refMs - 30*dayMs}
	rs.NAVPeakWindowValue = []float64{777}

	peak := rollNAVPeakWindow(&rs, refMs, 500)
	if len(rs.NAVPeakWindowMs) != 2 {
		t.Errorf("boundary: len = %d, want 2 (kept + appended)", len(rs.NAVPeakWindowMs))
	}
	if peak != 777 {
		t.Errorf("boundary peak = %v, want 777 (kept entry)", peak)
	}
}

// ----- evaluateRelease -----

func TestEvaluateRelease_BelowThreshold(t *testing.T) {
	rs := freshRuntimeState()
	// peak=1000, nav=950 → drawdown=0.05 < threshold 0.3 → no fire.
	d := evaluateRelease(&rs, refMs, 950, 1000, 1.0, 0.5, 0.3)
	if d.ShouldRelease {
		t.Errorf("drawdown 5%% under 30%% threshold: ShouldRelease=true (%+v)", d)
	}
}

func TestEvaluateRelease_EqualThresholdNoFire(t *testing.T) {
	rs := freshRuntimeState()
	// drawdown == threshold exactly → strict `>` → no fire.
	d := evaluateRelease(&rs, refMs, 700, 1000, 1.0, 0.5, 0.3)
	if d.ShouldRelease {
		t.Errorf("drawdown == threshold: ShouldRelease=true, want false")
	}
}

func TestEvaluateRelease_AboveThresholdFires(t *testing.T) {
	rs := freshRuntimeState()
	d := evaluateRelease(&rs, refMs, 600, 1000, 1.0, 0.5, 0.3)
	if !d.ShouldRelease {
		t.Fatalf("drawdown 40%%: ShouldRelease=false, want true")
	}
	// Cap1 = 1.0 * 0.10 = 0.10. Cap2 = 0.5 * 0.20 = 0.10. Tie → 0.10.
	if d.Quantity != 0.10 {
		t.Errorf("Quantity = %v, want 0.10", d.Quantity)
	}
	if d.Drawdown < 0.39 || d.Drawdown > 0.41 {
		t.Errorf("Drawdown = %v, want ≈ 0.40", d.Drawdown)
	}
}

func TestEvaluateRelease_CooldownBlocks(t *testing.T) {
	rs := freshRuntimeState()
	rs.LastReleaseMs = refMs - 6*dayMs // 6 days ago, < 7-day cooldown
	d := evaluateRelease(&rs, refMs, 500, 1000, 1.0, 0.5, 0.3)
	if d.ShouldRelease {
		t.Errorf("6-day cooldown: ShouldRelease=true, want false")
	}
}

func TestEvaluateRelease_CooldownColdStartAllowsFire(t *testing.T) {
	// LastReleaseMs == 0 (cold start) must NOT be treated as
	// "released at epoch" — the cooldown is bypassed.
	rs := freshRuntimeState()
	d := evaluateRelease(&rs, refMs, 500, 1000, 1.0, 0.5, 0.3)
	if !d.ShouldRelease {
		t.Errorf("cold-start cooldown: ShouldRelease=false, want true")
	}
}

func TestEvaluateRelease_CooldownBoundaryInclusive(t *testing.T) {
	rs := freshRuntimeState()
	rs.LastReleaseMs = refMs - releaseCooldownMs // exactly 7 days ago
	d := evaluateRelease(&rs, refMs, 500, 1000, 1.0, 0.5, 0.3)
	if !d.ShouldRelease {
		t.Errorf("7-day exact boundary: ShouldRelease=false, want true (>= 7d)")
	}
}

func TestEvaluateRelease_DegeneratePeak(t *testing.T) {
	// peak = 0 → no fire (would divide-by-zero on drawdown).
	rs := freshRuntimeState()
	d := evaluateRelease(&rs, refMs, 0, 0, 1.0, 0.5, 0.3)
	if d.ShouldRelease {
		t.Errorf("peak=0: ShouldRelease=true, want false")
	}
}

func TestEvaluateRelease_ZeroFloatBTCNoFire(t *testing.T) {
	// Drawdown above threshold but FloatBTC = 0 means qty caps to 0
	// → suppress the intent (no point in moving BTC nowhere).
	rs := freshRuntimeState()
	d := evaluateRelease(&rs, refMs, 500, 1000, 1.0, 0.0, 0.3)
	if d.ShouldRelease {
		t.Errorf("floatBTC=0: ShouldRelease=true, want false (qty would be 0)")
	}
}

// ----- computeReleaseQty -----

func TestComputeReleaseQty_FloatCapBinds(t *testing.T) {
	// DeadBTC=10 → cap1 = 1.0. FloatBTC=0.5 → cap2 = 0.1. Cap2 wins.
	if got := computeReleaseQty(10, 0.5); got != 0.1 {
		t.Errorf("float-cap binds: qty=%v, want 0.1", got)
	}
}

func TestComputeReleaseQty_DeadCapBinds(t *testing.T) {
	// DeadBTC=0.5 → cap1 = 0.05. FloatBTC=10 → cap2 = 2.0. Cap1 wins.
	if got := computeReleaseQty(0.5, 10); got != 0.05 {
		t.Errorf("dead-cap binds: qty=%v, want 0.05", got)
	}
}

func TestComputeReleaseQty_BothBoundsRespected(t *testing.T) {
	// Property check: across a small grid, qty ≤ DeadBTC*0.10 AND
	// qty ≤ FloatBTC*0.20.
	for _, dead := range []float64{0.1, 1, 5, 100} {
		for _, fl := range []float64{0.05, 1, 10, 50} {
			qty := computeReleaseQty(dead, fl)
			if qty > dead*releaseCapDeadFraction+1e-12 {
				t.Errorf("dead=%v float=%v: qty=%v exceeds dead cap %v",
					dead, fl, qty, dead*releaseCapDeadFraction)
			}
			if qty > fl*releaseCapFloatFraction+1e-12 {
				t.Errorf("dead=%v float=%v: qty=%v exceeds float cap %v",
					dead, fl, qty, fl*releaseCapFloatFraction)
			}
			if qty < 0 {
				t.Errorf("dead=%v float=%v: qty=%v < 0", dead, fl, qty)
			}
		}
	}
}

func TestComputeReleaseQty_ZeroWallets(t *testing.T) {
	if got := computeReleaseQty(0, 10); got != 0 {
		t.Errorf("deadBTC=0: qty=%v, want 0", got)
	}
	if got := computeReleaseQty(10, 0); got != 0 {
		t.Errorf("floatBTC=0: qty=%v, want 0", got)
	}
	if got := computeReleaseQty(-1, 10); got != 0 {
		t.Errorf("deadBTC<0: qty=%v, want 0", got)
	}
}
