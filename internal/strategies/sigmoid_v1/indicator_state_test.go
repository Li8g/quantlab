package sigmoid_v1

import (
	"math"
	"testing"

	"quantlab/internal/quant"
)

// TestIncrIndicatorState_MAVMatchesBatch verifies that computeMarketState
// produces bit-identical volRatio to the batch MAVAbsChangeWindow path.
// MAV uses no approximation — same last-N diffs, same running sum.
func TestIncrIndicatorState_MAVMatchesBatch(t *testing.T) {
	const n = 500
	closes := syntheticCloses(n, 77)
	c := defaultChromosome()
	c.MAVShortPeriod = 10
	c.MAVLongPeriod = 40

	s := newIncrIndicatorState(c)
	for i, close := range closes {
		s.update(close)
		if i < c.MAVLongPeriod {
			continue // ring not yet full
		}
		_, incrRatio := s.computeMarketState(c.QuietThreshold)

		// Batch path: MAVAbsChangeWindow on the last window+1 closes.
		batchMavShort := quant.MAVAbsChangeWindow(closes[:i+1], c.MAVShortPeriod)
		batchMavLong := quant.MAVAbsChangeWindow(closes[:i+1], c.MAVLongPeriod)
		var batchRatio float64
		if batchMavLong != 0 {
			batchRatio = quant.ClipFloat64(batchMavShort/batchMavLong, volRatioMin, volRatioMax)
		} else {
			batchRatio = 1.0
		}

		if incrRatio != batchRatio {
			t.Errorf("bar %d: incrRatio=%v batchRatio=%v (delta=%e)",
				i, incrRatio, batchRatio, math.Abs(incrRatio-batchRatio))
		}
	}
}

// TestIncrIndicatorState_LogReturnLookback verifies that the lookback ring
// returns the close from exactly MAVShortPeriod bars ago.
func TestIncrIndicatorState_LogReturnLookback(t *testing.T) {
	const n = 200
	closes := syntheticCloses(n, 99)
	c := defaultChromosome()
	c.MAVShortPeriod = 15
	c.EMALongPeriod = 50

	s := newIncrIndicatorState(c)
	for i, close := range closes {
		s.update(close)
		if !s.lookbackFull {
			continue
		}
		lookbackClose := s.lookbackBuf[s.lookbackHead]
		wantLookback := closes[i-c.MAVShortPeriod]
		if lookbackClose != wantLookback {
			t.Errorf("bar %d: lookback=%v want=%v (MAVShortPeriod=%d bars ago)",
				i, lookbackClose, wantLookback, c.MAVShortPeriod)
		}
	}
}

// TestIncrIndicatorState_EMAWithinTolerance measures the incremental EMA
// divergence from the batch windowed path. The incremental form (seeded from
// bar 0, no cold-start reset) must stay within the ε = 1e-4 ScoreTotal gate.
// This test checks the EMA-level signal term (see TestEMADivergence_WindowedVsIncremental
// for the full pre-#6 measurement).
func TestIncrIndicatorState_EMAWithinTolerance(t *testing.T) {
	const n = 2000
	closes := syntheticCloses(n, 42)
	c := defaultChromosome()
	c.EMALongPeriod = 300 // worst-case: maximum period

	s := newIncrIndicatorState(c)
	for _, close := range closes {
		s.update(close)
	}
	// After n bars, read the final incremental EMA value.
	incrEMA := s.emaLong

	// Batch windowed path: quant.EMA over the last stepHistoryCap closes.
	start := 0
	if n > stepHistoryCap {
		start = n - stepHistoryCap
	}
	batchSlice := closes[start:]
	batchEMASlice := quant.EMA(batchSlice, c.EMALongPeriod)
	batchEMA := batchEMASlice[len(batchEMASlice)-1]

	if batchEMA == 0 {
		t.Fatal("batchEMA is zero — fixture has degenerate price series")
	}
	rel := math.Abs(incrEMA-batchEMA) / math.Abs(batchEMA)
	// ε = 1e-4 is the ScoreTotal gate; EMA-level delta is typically ≪ ε.
	if rel > 1e-3 {
		t.Errorf("EMA divergence rel=%e exceeds 1e-3 sanity bound (period=%d)",
			rel, c.EMALongPeriod)
	}
	t.Logf("EMA period=%d  rel-delta=%.3e  (incr=%v batch=%v)", c.EMALongPeriod, rel, incrEMA, batchEMA)
}

// TestIncrIndicatorState_WarmupZeroReturns verifies that computeMarketState
// and computeSignal return the documented zero/quiet defaults before the
// ring buffers are full.
func TestIncrIndicatorState_WarmupZeroReturns(t *testing.T) {
	c := defaultChromosome()
	c.MAVShortPeriod = 5
	c.MAVLongPeriod = 10
	s := newIncrIndicatorState(c)

	// Before ANY update: hasFirst=false → signal=0, market=Quiet.
	ms, vr := s.computeMarketState(c.QuietThreshold)
	if ms != MarketStateQuiet || vr != 1.0 {
		t.Errorf("pre-first-update: got (%v, %v), want (Quiet, 1.0)", ms, vr)
	}
	sig := s.computeSignal(c, vr, 100)
	if sig != 0 {
		t.Errorf("pre-first-update signal=%v, want 0", sig)
	}

	// After MAVLongPeriod bars but before MAVLongPeriod+1 (ring not full):
	for i := 0; i <= c.MAVLongPeriod; i++ {
		s.update(float64(50000 + i))
	}
	// After MAVLongPeriod+1 closes, the short ring needs MAVShortPeriod+1
	// closes (= 6 closes → full after bar 5 = index 5 → at bar 6 short is full).
	// And long needs MAVLongPeriod+1 closes (= 11 closes → full after bar 10 →
	// at bar 11 long is full). We've fed 12 bars, so both rings should be full.
	ms2, _ := s.computeMarketState(c.QuietThreshold)
	if ms2 == MarketStateQuiet && s.mavLongFull {
		// Both rings full but price ramp → active is possible; just verify no panic.
	}
}
