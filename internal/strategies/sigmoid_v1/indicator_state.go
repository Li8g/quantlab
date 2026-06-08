// Incremental per-bar indicator state for sigmoid_v1's backtest hot loop.
// The batch functions (quant.EMA, quant.MAVAbsChangeWindow) are O(window)
// per bar and drive the O(n·window) cost of evaluateWindow. This file
// provides O(1)/bar replacements used exclusively in the inner loop.
//
// MAV ring buffers reproduce MAVAbsChangeWindow exactly (same last-N absolute
// diffs, plain running sum — the numerical error vs KahanSum is ~10⁻¹³
// relative for realistic price series, far below any relevant threshold).
//
// The incremental EMA diverges slightly from the windowed cold-start form
// used by Step()'s full-history batch path (quant.EMA on a capped buffer).
// The measured divergence is ≤ 8e-5 relative at the EMA level, propagating
// to a ScoreTotal delta well below ε = 1e-4 (decision-ga-reproducibility-
// constraint.md §worked-example-6 + §9).
package sigmoid_v1

import (
	"math"

	"quantlab/internal/quant"
)

// incrIndicatorState holds incremental indicator state for the evaluateWindow
// hot loop. Allocated once per evaluateWindow call (sized to the chromosome's
// periods), advanced by update() at each bar.
type incrIndicatorState struct {
	// EMA_long: seeded from bar 0, O(1)/bar (no cold-start reset).
	emaLong   float64
	alphaLong float64
	hasFirst  bool

	// MAV short: ring buffer of |Δclose| over the last MAVShortPeriod bars.
	mavShortBuf  []float64
	mavShortSum  float64
	mavShortHead int
	mavShortFull bool

	// MAV long: same, over MAVLongPeriod bars.
	mavLongBuf  []float64
	mavLongSum  float64
	mavLongHead int
	mavLongFull bool

	// Lookback ring: stores the last (MAVShortPeriod+1) close prices so that
	// lookbackBuf[lookbackHead] == close[t − MAVShortPeriod] after update().
	// Used for the logReturn term in computeSignal.
	lookbackBuf  []float64
	lookbackHead int
	lookbackFull bool

	prevClose float64
}

// newIncrIndicatorState allocates ring buffers sized to c's chromosome periods.
func newIncrIndicatorState(c Chromosome) incrIndicatorState {
	return incrIndicatorState{
		alphaLong:   2.0 / (float64(c.EMALongPeriod) + 1.0),
		mavShortBuf: make([]float64, c.MAVShortPeriod),
		mavLongBuf:  make([]float64, c.MAVLongPeriod),
		// MAVShortPeriod+1 slots: after filling, lookbackBuf[head] is the
		// close from MAVShortPeriod bars ago.
		lookbackBuf: make([]float64, c.MAVShortPeriod+1),
	}
}

// update advances the indicator state by one bar. Must be called in bar order,
// once per bar, before querying computeMarketState or computeSignal.
func (s *incrIndicatorState) update(close float64) {
	// Lookback ring is updated unconditionally (before the hasFirst early
	// return) so the ring contains close[t − MAVShortPeriod] from the
	// (MAVShortPeriod+1)-th bar onward.
	s.lookbackBuf[s.lookbackHead] = close
	s.lookbackHead = (s.lookbackHead + 1) % len(s.lookbackBuf)
	if !s.lookbackFull && s.lookbackHead == 0 {
		s.lookbackFull = true
	}

	if !s.hasFirst {
		s.emaLong = close
		s.prevClose = close
		s.hasFirst = true
		return
	}

	// EMA: O(1) recurrence (same α as quant.EMA).
	s.emaLong = s.alphaLong*close + (1.0-s.alphaLong)*s.emaLong

	// Absolute price change (shared by both MAV rings).
	diff := math.Abs(close - s.prevClose)

	// MAV short ring.
	if s.mavShortFull {
		s.mavShortSum -= s.mavShortBuf[s.mavShortHead]
	}
	s.mavShortBuf[s.mavShortHead] = diff
	s.mavShortSum += diff
	s.mavShortHead = (s.mavShortHead + 1) % len(s.mavShortBuf)
	if !s.mavShortFull && s.mavShortHead == 0 {
		s.mavShortFull = true
	}

	// MAV long ring.
	if s.mavLongFull {
		s.mavLongSum -= s.mavLongBuf[s.mavLongHead]
	}
	s.mavLongBuf[s.mavLongHead] = diff
	s.mavLongSum += diff
	s.mavLongHead = (s.mavLongHead + 1) % len(s.mavLongBuf)
	if !s.mavLongFull && s.mavLongHead == 0 {
		s.mavLongFull = true
	}

	s.prevClose = close
}

// computeMarketState returns market state and clipped vol ratio from the
// incremental MAV state. Mirrors ComputeMarketState semantics: returns
// (Quiet, 1.0) when either ring is not yet full (warmup).
func (s *incrIndicatorState) computeMarketState(quietThreshold float64) (MarketState, float64) {
	if !s.mavLongFull || !s.mavShortFull {
		return MarketStateQuiet, 1.0
	}
	mavShort := s.mavShortSum / float64(len(s.mavShortBuf))
	mavLong := s.mavLongSum / float64(len(s.mavLongBuf))
	if mavLong == 0 {
		return MarketStateQuiet, 1.0
	}
	volRatio := quant.ClipFloat64(mavShort/mavLong, volRatioMin, volRatioMax)
	if volRatio < quietThreshold {
		return MarketStateQuiet, volRatio
	}
	return MarketStateActive, volRatio
}

// computeSignal returns the per-bar signal. Mirrors ComputeSignal semantics:
// returns 0 before hasFirst; logReturn term is 0 before lookback ring fills.
func (s *incrIndicatorState) computeSignal(c Chromosome, volRatio, close float64) float64 {
	if !s.hasFirst {
		return 0
	}
	var priceDeviation float64
	if s.emaLong != 0 {
		priceDeviation = (close - s.emaLong) / s.emaLong
	}
	var logReturn float64
	if s.lookbackFull {
		// lookbackBuf[lookbackHead] is the close from MAVShortPeriod bars ago.
		if lookbackClose := s.lookbackBuf[s.lookbackHead]; lookbackClose > 0 && close > 0 {
			logReturn = math.Log(close / lookbackClose)
		}
	}
	return c.A1*priceDeviation + c.A2*logReturn + c.A3*(volRatio-1.0)
}
