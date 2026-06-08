// Step() per-tick orchestrator for sigmoid_v1. Source of truth:
// docs/strategies/sigmoid_v1.md §7. Upstream wedge-filter contract:
// docs/策略数学引擎.md §5.5. Upstream SpendableUSDT formula:
// docs/策略数学引擎.md §1.1.
//
// 铁律 1 (Step 同构): no `if isBacktest`, no os.Getenv — the same
//
//	function runs in backtest, dry-run, and live.
//
// 铁律 2 (NowMs 唯一时间源): every time comparison flows from
//
//	input.NowMs and timestamps already carried on input. No
//	time.Now(), no time.Since().
//
// 铁律 摩擦: Step() emits THEORETICAL USD quantities. Taker fee and
//
//	slippage are applied later — by the backtest adapter or by the
//	Agent fill report (§5.8 upstream).
package sigmoid_v1

import (
	"fmt"
	"math"

	"quantlab/internal/strategy"
)

// minMicroOrderUSD is the §5.5 "粉尘最小阈值". Orders with
// |theoreticalUSD| below this magnitude are normally dropped (treated
// as dust); §5.5 allows a wedge-break to force a minimum-sized order
// through in active state.
//
// 5.0 mirrors Binance's typical BTC/USDT spot minimum NOTIONAL — going
// lower would be unfillable on the real venue.
const minMicroOrderUSD = 5.0

// Wedge-break thresholds. Either condition forces a dust-amount order
// through in active state (§5.5 "wedge break: |DeltaWeight| ≥ τ OR
// VolatilityRatio ≥ τ").
//
//   - deltaWeight ≥ 0.5%: even sub-$5 quantities matter when the
//     target shift is mechanically meaningful (e.g. on a $1k portfolio
//     a 0.5% shift = $5, right at the dust boundary).
//   - volRatio ≥ 2.5: signals the market is near the clip-cap (3.0),
//     i.e. genuinely active — passing dust orders here is preferable
//     to silent inaction during a real move.
//
// The two numbers can independently drift without breaking each other.
const (
	wedgeDeltaWeightThreshold = 0.005
	wedgeVolRatioThreshold    = 2.5
)

// orderTTLMs is the OrderIntent ValidUntilMs offset from input.NowMs.
// 60 seconds matches the §3.2 macro example in sigmoid_v1.md verbatim.
// Same TTL is reused for micro orders — Step() emits at most one of
// each kind per bar so collision is impossible.
const orderTTLMs = int64(60_000)

// minReserveUSDT is the §1.1 MinReserveUSDT floor (the fixed-dollar
// part of ReserveFloor, layered with the percent-of-equity part).
// v1 leaves it at zero; the chromosome's micro_reserve_pct alone
// drives the reserve. Splitting the floor out as a named constant
// makes the §1.1 formula readable and easy to bump later.
const minReserveUSDT = 0.0

// Step is the per-tick pure-function orchestrator. It composes the
// Phase 4b building blocks in §7-pseudocode order:
//
//	decodeRuntimeState → DecodeChromosome → ComputeMarketState →
//	ComputeSignal → computeMicroRebalance → wedge filter →
//	EvaluateMacroEngine (+ §3.3 cash check) → rollNAVPeakWindow →
//	evaluateRelease → encodeRuntimeState
//
// Errors from decoders propagate; the engine's worker pool fails the
// gene cleanly rather than silently swallowing a malformed
// chromosome / RuntimeState (which would only happen on a real engine
// bug — never on data drawn from real Binance bars).
//
// Live trading enters here every tick; backtest's per-bar loop calls
// stepCore directly so the inner ~22KB JSON round-trip stays out of the
// hot path. Both paths share the same compute body — 铁律 1 holds.
func (s *Sigmoid) Step(input strategy.StrategyInput) (strategy.StrategyOutput, error) {
	rs, err := decodeRuntimeState(input.RuntimeState)
	if err != nil {
		return strategy.StrategyOutput{}, fmt.Errorf("sigmoid_v1.Step: decode runtime state: %w", err)
	}
	c, err := DecodeChromosome(input.Chromosome)
	if err != nil {
		return strategy.StrategyOutput{}, fmt.Errorf("sigmoid_v1.Step: decode chromosome: %w", err)
	}

	macro, micro, release, newRS, dbg := stepCore(input, rs, c)

	encoded, err := encodeRuntimeState(newRS)
	if err != nil {
		return strategy.StrategyOutput{}, fmt.Errorf("sigmoid_v1.Step: encode runtime state: %w", err)
	}

	return strategy.StrategyOutput{
		MacroOrders:    macro,
		MicroOrders:    micro,
		ReleaseIntents: release,
		RuntimeState:   encoded,
		DebugSnapshot:  dbg,
	}, nil
}

// stepCore is the typed-input compute body Step wraps. It is the only
// place the §7-pseudocode pipeline lives for the live-trading path; both
// the JSON-boundary Step and any caller that already has a full Closes
// history use this entry point.
//
// Inputs are by value and treated as immutable; rs is taken by value
// and returned by value so the function is referentially transparent
// with respect to the caller's state.
func stepCore(input strategy.StrategyInput, rs RuntimeState, c Chromosome) (
	[]strategy.OrderIntent, []strategy.OrderIntent, []strategy.ReleaseIntent,
	RuntimeState, *strategy.DebugSnapshot,
) {
	// §2 market state.
	marketState, volRatio := ComputeMarketState(
		input.Closes, c.MAVShortPeriod, c.MAVLongPeriod, c.QuietThreshold,
	)

	// Latest close = price proxy. Empty closes (impossible after
	// MinEvalBars warmup) collapses price to 0, which keeps the rest
	// of the pipeline finite via the totalEquityFloor guard.
	price := 0.0
	if n := len(input.Closes); n > 0 {
		price = input.Closes[n-1]
	}

	// §7-3 signal synthesis.
	signal := ComputeSignal(input.Closes, c, volRatio)

	return stepCoreFromIndicators(input, rs, c, marketState, volRatio, signal, price)
}

// stepCoreFromIndicators is the §7-pseudocode compute body downstream of
// indicator resolution. stepCore calls it after the O(window) batch path;
// evaluateWindow's hot loop calls it directly with O(1)/bar incremental
// values from incrIndicatorState — both arrive at identical logic (铁律 1).
func stepCoreFromIndicators(
	input strategy.StrategyInput, rs RuntimeState, c Chromosome,
	marketState MarketState, volRatio, signal, price float64,
) (
	[]strategy.OrderIntent, []strategy.OrderIntent, []strategy.ReleaseIntent,
	RuntimeState, *strategy.DebugSnapshot,
) {
	marketBetaMul := MarketBetaMultiplier(marketState)

	// §7-4 micro rebalance.
	micro := computeMicroRebalance(c, microRebalanceInputs{
		DeadBTC:       input.Portfolio.DeadBTC,
		FloatBTC:      input.Portfolio.FloatBTC,
		USDT:          input.Portfolio.USDT,
		Price:         price,
		Signal:        signal,
		MarketBetaMul: marketBetaMul,
	})

	// §7-5 wedge filter → at most one MicroOrder per bar.
	microOrders := buildMicroOrders(input.NowMs, micro, volRatio, marketState)

	// §3 macro engine (§3.3 cash check applied here, not inside the
	// pure decision layer, because spendable depends on Portfolio).
	macroOrders, rs := applyMacroDecision(input, c, &rs, micro.TotalEquity)

	// §5 release: roll the NAV peak window first (peak must include
	// the current bar per §5.1 "τ ∈ [t-N, t]") then evaluate.
	nav := (input.Portfolio.DeadBTC+input.Portfolio.FloatBTC)*price + input.Portfolio.USDT
	peak := rollNAVPeakWindow(&rs, input.NowMs, nav)
	releaseIntents, rs := applyReleaseDecision(input, c, &rs, nav, peak)

	return macroOrders, microOrders, releaseIntents, rs,
		buildDebugSnapshot(signal, micro.TargetWeight, marketState)
}

// buildMicroOrders applies the §5.5 wedge filter and synthesises at
// most one MicroOrder. Side is derived from the sign of DeltaWeight.
//
// Three branches:
//
//	|theoreticalUSD| == 0       → no order (degenerate input)
//	|theoreticalUSD| ≥ dust     → emit at |theoreticalUSD|
//	dust borderline + active   → emit at minMicroOrderUSD iff wedge
//	                              break fires; otherwise drop
//	dust borderline + quiet    → drop (spec §2.3: quiet → 归零)
func buildMicroOrders(nowMs int64, m microRebalanceResult, volRatio float64, state MarketState) []strategy.OrderIntent {
	abs := math.Abs(m.TheoreticalUSD)
	if abs == 0 {
		return nil
	}
	quantityUSD := abs
	if abs < minMicroOrderUSD {
		if state == MarketStateQuiet {
			return nil
		}
		wedgeBreak := math.Abs(m.DeltaWeight) >= wedgeDeltaWeightThreshold ||
			volRatio >= wedgeVolRatioThreshold
		if !wedgeBreak {
			return nil
		}
		quantityUSD = minMicroOrderUSD
	}
	side := strategy.OrderSideBuy
	if m.DeltaWeight < 0 {
		side = strategy.OrderSideSell
	}
	return []strategy.OrderIntent{{
		Kind:          strategy.OrderKindMicro,
		Side:          side,
		OrderType:     strategy.OrderTypeMarket,
		QuantityUSD:   quantityUSD,
		ClientOrderID: fmt.Sprintf("micro-%d", nowMs),
		ValidUntilMs:  nowMs + orderTTLMs,
	}}
}

// applyMacroDecision wraps EvaluateMacroEngine with the §3.3 cash
// availability check and mutates rs.LastMacroBuyMs on success. Returns
// the order list (0 or 1 element) and the updated RuntimeState by
// value so the caller's chain reads top-to-bottom.
//
// §3.3 rule: AmountUSD > SpendableUSDT → skip silently (do NOT emit a
// reduced order). The skip protects micro from being starved by macro
// when cash is tight.
func applyMacroDecision(input strategy.StrategyInput, c Chromosome, rs *RuntimeState, totalEquity float64) ([]strategy.OrderIntent, RuntimeState) {
	d := EvaluateMacroEngine(
		input.NowMs, input.LastProcessedBarTime, rs.LastMacroBuyMs, c.MacroInjectUSD,
	)
	if !d.ShouldInject {
		return nil, *rs
	}
	spendable := spendableUSDT(input.Portfolio.USDT, c.MicroReservePct, totalEquity)
	if d.AmountUSD > spendable {
		// "insufficient cash for macro buy" — §3.3 says no reduced
		// order, no error.
		return nil, *rs
	}
	rs.LastMacroBuyMs = input.NowMs
	return []strategy.OrderIntent{{
		Kind:          strategy.OrderKindMacro,
		Side:          strategy.OrderSideBuy,
		OrderType:     strategy.OrderTypeMarket,
		QuantityUSD:   d.AmountUSD,
		ClientOrderID: fmt.Sprintf("macro-%d", input.NowMs),
		ValidUntilMs:  input.NowMs + orderTTLMs,
	}}, *rs
}

// applyReleaseDecision wraps evaluateRelease and stamps rs.LastReleaseMs
// on a successful fire. peak must already reflect the current bar
// (caller invokes rollNAVPeakWindow first).
func applyReleaseDecision(input strategy.StrategyInput, c Chromosome, rs *RuntimeState, nav, peak float64) ([]strategy.ReleaseIntent, RuntimeState) {
	d := evaluateRelease(
		rs, input.NowMs, nav, peak,
		input.Portfolio.DeadBTC, input.Portfolio.FloatBTC,
		c.ReleaseDrawdownThreshold,
	)
	if !d.ShouldRelease {
		return nil, *rs
	}
	rs.LastReleaseMs = input.NowMs
	return []strategy.ReleaseIntent{{
		NowMs:    input.NowMs,
		Quantity: d.Quantity,
		Reason:   fmt.Sprintf("drawdown_%.2f", d.Drawdown),
	}}, *rs
}

// spendableUSDT implements the §1.1 derivation:
//
//	ReserveFloor  = max(MinReserveUSDT, micro_reserve_pct × TotalEquity)
//	SpendableUSDT = max(0, USDT − ReserveFloor)
//
// Pulled out as a named helper so the macro-cash branch reads at one
// level of abstraction.
func spendableUSDT(usdt, microReservePct, totalEquity float64) float64 {
	reserve := microReservePct * totalEquity
	if reserve < minReserveUSDT {
		reserve = minReserveUSDT
	}
	if spendable := usdt - reserve; spendable > 0 {
		return spendable
	}
	return 0
}

// buildDebugSnapshot copies the three observable diagnostic fields
// into a pointer-bearing struct. *float64 / *string mirror the
// optional-field semantics on strategy.DebugSnapshot — nil means
// "no value", non-nil means "this was the value at Step() time".
func buildDebugSnapshot(signal, targetWeight float64, state MarketState) *strategy.DebugSnapshot {
	sig := signal
	tw := targetWeight
	st := string(state)
	return &strategy.DebugSnapshot{
		Signal:       &sig,
		TargetWeight: &tw,
		MarketState:  &st,
	}
}
