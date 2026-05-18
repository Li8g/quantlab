// Signal synthesis + micro Sigmoid rebalance for sigmoid_v1. Source of
// truth: docs/strategies/sigmoid_v1.md §7 steps 3-4.
//
// Both helpers are pure and stateless: feed them the latest bar's
// closes + chromosome + (already-computed) volRatio / portfolio, get
// back the dimensionless signal and target weight. Step() then turns
// the result into OrderIntents — wiring that lands in Phase 4c.
package sigmoid_v1

import (
	"math"

	"quantlab/internal/quant"
)

// ComputeSignal evaluates the §7-step-3 weighted sum:
//
//	signal = A1 * priceDeviation
//	       + A2 * logReturn
//	       + A3 * (volRatio - 1)
//
// where priceDeviation = (close - EMA_long) / EMA_long and
// logReturn = ln(close / close[-mavShort]).
//
// volRatio is the already-clipped value returned by
// ComputeMarketState; passing it in avoids re-scanning the close series
// for MAV.
//
// Returns 0 in two unreachable-in-production cases (kept lenient so
// fixture tests don't need guards):
//   - closes has fewer than 2 elements
//   - closes has fewer than MAVShortPeriod+1 elements (cannot form
//     the logReturn lookback)
//
// MinEvalBars (§8.2) guarantees the warmup is always sufficient
// during real evaluation.
func ComputeSignal(closes []float64, c Chromosome, volRatio float64) float64 {
	n := len(closes)
	if n < 2 || n <= c.MAVShortPeriod {
		return 0
	}
	last := n - 1

	// Price deviation from long EMA. We compute the full EMA series
	// because quant.EMA's recurrence depends on every prior bar; the
	// allocation is amortised against the per-Step cost. If EMA_long
	// underflows to zero (degenerate flat history), drop the term.
	emaLong := quant.EMA(closes, c.EMALongPeriod)
	var priceDeviation float64
	if emaLong[last] != 0 {
		priceDeviation = (closes[last] - emaLong[last]) / emaLong[last]
	}

	// Log-return over MAVShortPeriod bars. Guard against non-positive
	// prices — unreachable on Binance data but inputs of tests have
	// no such guarantee.
	prev := closes[last-c.MAVShortPeriod]
	var logReturn float64
	if prev > 0 && closes[last] > 0 {
		logReturn = math.Log(closes[last] / prev)
	}

	volRatioCentred := volRatio - 1.0
	return c.A1*priceDeviation + c.A2*logReturn + c.A3*volRatioCentred
}

// microRebalanceInputs is the §7-step-4 right-hand-side. Bundled into
// a struct so the test fixtures read top-down and Step() (Phase 4c) can
// pass through the values it already computed without a long
// positional call.
type microRebalanceInputs struct {
	DeadBTC       float64
	FloatBTC      float64
	USDT          float64
	Price         float64
	Signal        float64
	MarketBetaMul float64
}

// microRebalanceResult exposes every intermediate from §7-step-4. The
// extras (TotalEquity, CurrentWeight) are needed by DebugSnapshot;
// keeping them on the result struct avoids recomputing in Step().
type microRebalanceResult struct {
	TotalEquity    float64
	CurrentWeight  float64
	EffectiveBeta  float64
	TargetWeight   float64
	DeltaWeight    float64
	TheoreticalUSD float64
}

// effectiveBetaFloor pins β away from 0 so a quiet market with a
// chromosome β near the lower bound (0.5 * 0.3 = 0.15) doesn't drift
// to a degenerate "no rebalance" exponent. The 0.01 value is from §7
// step 4 pseudocode (`effectiveBeta = max(0.01, ...)`).
const effectiveBetaFloor = 0.01

// totalEquityFloor prevents division-by-zero when computing the
// current FloatBTC weight at cold start (totalEquity = 0 ⇒ 0/0). The
// 1e-12 value is the same defensive scale used elsewhere in the
// codebase for "ε to avoid NaN".
const totalEquityFloor = 1e-12

// computeMicroRebalance executes §7 step 4 verbatim. It is the
// stateless math half of the rebalancing decision; the wedge-filter
// (§7 step 5) and OrderIntent construction live in Step() because
// they touch ChromosomeMicroReservePct / portfolio cash semantics.
func computeMicroRebalance(c Chromosome, in microRebalanceInputs) microRebalanceResult {
	totalEquity := (in.DeadBTC+in.FloatBTC)*in.Price + in.USDT
	denom := totalEquity
	if denom < totalEquityFloor {
		denom = totalEquityFloor
	}
	currentWeight := in.FloatBTC * in.Price / denom

	effectiveBeta := c.Beta * in.MarketBetaMul
	if effectiveBeta < effectiveBetaFloor {
		effectiveBeta = effectiveBetaFloor
	}

	invBias := quant.ClipFloat64(currentWeight, 0, 1) - 0.5
	exponent := effectiveBeta*in.Signal + c.Gamma*invBias
	// 1/(1+exp(x)) ∈ (0,1) for finite x; ±Inf cleanly fold to 0 and 1
	// before the ClipFloat64 guard, which protects against NaN ever
	// leaking from an exotic exponent.
	targetWeight := quant.ClipFloat64(1.0/(1.0+math.Exp(exponent)), 0, 1)
	deltaWeight := targetWeight - currentWeight
	theoreticalUSD := deltaWeight * totalEquity

	return microRebalanceResult{
		TotalEquity:    totalEquity,
		CurrentWeight:  currentWeight,
		EffectiveBeta:  effectiveBeta,
		TargetWeight:   targetWeight,
		DeltaWeight:    deltaWeight,
		TheoreticalUSD: theoreticalUSD,
	}
}
