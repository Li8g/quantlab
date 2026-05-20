// Market-state classification for sigmoid_v1. Source of truth:
// docs/strategies/sigmoid_v1.md §2.
//
// Two states (quiet / active) per v1 decision #1. The classifier is
// stateless and consumes only price closes — by design (§2.2 "纯依赖
// StrategyInput.Closes，不读 RuntimeState") so iron-rule #1 ("Step 同构")
// holds: any caller can recompute the state from the same inputs and get
// the same answer.
package sigmoid_v1

import "quantlab/internal/quant"

// MarketState is the §2.1 enum. The string form is also the JSON wire
// form used in DebugSnapshot.
type MarketState string

const (
	MarketStateQuiet  MarketState = "quiet"
	MarketStateActive MarketState = "active"
)

// volRatio clip bounds from §2.2: `volRatio = clip(MAV_short / MAV_long,
// 0.1, 3.0)`. Kept here, not in the chromosome, because they are the
// numerical guard rails on MAV ratio — not tunable knobs.
const (
	volRatioMin = 0.1
	volRatioMax = 3.0
)

// MarketBetaMultiplier values from §2.3. 0.3 in quiet, 1.0 in active.
// Decision #2 confirmed these stay constants (not chromosome dims) in
// v1; the rationale is in §11.
const (
	marketBetaMulQuiet  = 0.3
	marketBetaMulActive = 1.0
)

// ComputeMarketState classifies the latest bar and returns both the
// state and the clipped volatility ratio. Callers use the state for the
// quiet/active branch and reuse the (centred) ratio in ComputeSignal,
// avoiding a second pass over the close series.
//
// Degenerate fallback: when MAV_long is exactly zero (perfectly flat
// long-window history, only reachable on hand-built inputs because
// §8.2 MinEvalBars guarantees real data), return (Quiet, 1.0). "No
// long-window movement" is the natural definition of quiet, and the
// 1.0 ratio makes volRatioCentred = 0 so the signal stays neutral.
// (The spec doesn't cover this degenerate corner.)
func ComputeMarketState(closes []float64, mavShortPeriod, mavLongPeriod int, quietThreshold float64) (MarketState, float64) {
	mavShort := quant.MAVAbsChangeWindow(closes, mavShortPeriod)
	mavLong := quant.MAVAbsChangeWindow(closes, mavLongPeriod)
	if mavLong == 0 {
		return MarketStateQuiet, 1.0
	}
	volRatio := quant.ClipFloat64(mavShort/mavLong, volRatioMin, volRatioMax)
	if volRatio < quietThreshold {
		return MarketStateQuiet, volRatio
	}
	return MarketStateActive, volRatio
}

// MarketBetaMultiplier maps a state to its §2.3 β multiplier. Pulled
// out of ComputeMarketState because DebugSnapshot needs both pieces and
// keeping them adjacent in the call site is more readable than a tuple
// return.
func MarketBetaMultiplier(state MarketState) float64 {
	if state == MarketStateQuiet {
		return marketBetaMulQuiet
	}
	return marketBetaMulActive
}
