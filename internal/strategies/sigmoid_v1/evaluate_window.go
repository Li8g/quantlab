// Per-window backtest loop for sigmoid_v1. Source of truth:
// docs/strategies/sigmoid_v1.md §9 (asset accounting) + §10 (test
// matrix) + docs/进化计算引擎.md §5.6/§5.7 (Adapter contract).
//
// evaluateWindow ties Phase 4b building blocks together: Step() per
// bar, simulator-applied OrderIntents, running peak/drawdown, and
// produces one CrucibleResult for the Adapter cascade (Phase 4d-
// adapter).
//
// The function takes inputs through interface boundaries that don't
// depend on sigmoid_v1 internals — when the asset-accounting layer is
// lifted to internal/adapters/backtest/ per upstream §3.4, this loop
// follows it with minimal rewrites.
package sigmoid_v1

import (
	"fmt"
	"math"

	"quantlab/internal/domain"
	"quantlab/internal/quant"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
)

// stepHistoryCap is the EMA convergence horizon: at MaxChromosomePeriod × 3
// bars, the longest-period EMA (period = 300) has decayed its seed to
// α^(3·period) ≈ e⁻⁶ of the initial value. The constant is no longer used
// in the production hot loop (replaced by incremental state in incrIndicatorState)
// but is kept as a reference bound and is used by ema_divergence_test.go.
const stepHistoryCap = MaxChromosomePeriod * 3

// evaluateWindow runs strat.Step over every bar in window.Bars,
// realises the emitted orders via applyStrategyOutput, and produces
// a single CrucibleResult per the §5.1 SliceScore three-state
// semantics:
//
//	Self-Fatal       — running drawdown ≥ fatalMDD (plan.FatalMDD)
//	Normal           — final score = log(navFinal / navAtWarmupStart)
//	(Cascade-skipped: not produced here; populated by the Adapter
//	                  cascade in Phase 4d-adapter)
//
// Bars [0, WarmupLen) feed Step() to warm internal indicators but
// don't contribute to the equity path (no peak/drawdown bookkeeping,
// not counted in BarsEvaluated). The scored period is
// bars[WarmupLen .. end-1], inclusive.
//
// Gap bars (bar.IsGap=true) still call Step() so RuntimeState (the
// NAV peak sliding window etc.) evolves smoothly through the gap,
// but the emitted MacroOrders / MicroOrders / ReleaseIntents are
// discarded before applyStrategyOutput — the test §10
// TestGapHandlingNoFakeTrades invariant ("never trade on a
// synthesised bar").
// evaluateWindow returns, in order: the window's CrucibleResult; the
// SharpeStats (nil on Fatal/degenerate); the per-bar log-return series
// (the input to those stats); and an error. The caller keeps the
// returns only for the longest non-Fatal window and only when
// CaptureReturns is set — see sigmoidAdapter.Evaluate.
func evaluateWindow(strat *Sigmoid, gene domain.Gene, window domain.CrucibleWindow, friction domain.FrictionParams, fatalMDD, initialUSDT float64) (resultpkg.CrucibleResult, *resultpkg.SharpeStats, []float64, error) {
	if len(window.Bars) == 0 {
		return resultpkg.CrucibleResult{}, nil, nil, fmt.Errorf("evaluateWindow: window %q has no bars", window.Name)
	}
	if window.WarmupLen >= len(window.Bars) {
		return resultpkg.CrucibleResult{}, nil, nil, fmt.Errorf(
			"evaluateWindow: window %q WarmupLen=%d >= len(Bars)=%d",
			window.Name, window.WarmupLen, len(window.Bars),
		)
	}

	p := strategy.PortfolioSnapshot{USDT: initialUSDT}

	// Chromosome decode is per-evaluation, not per-bar — the gene is
	// fixed for the whole window. Avoids re-running the 13-field
	// rebuild inside the hot loop.
	c, err := DecodeChromosome(gene)
	if err != nil {
		return resultpkg.CrucibleResult{}, nil, nil, fmt.Errorf(
			"evaluateWindow: window %q decode chromosome: %w", window.Name, err,
		)
	}

	// Typed RuntimeState carried across bars in-memory. Live trading's
	// Step() round-trips this through JSON on every tick because the
	// state must survive process restarts via DB; the backtest loop
	// owns the whole evaluation in-process, so we skip the ~22KB
	// json.Unmarshal+Marshal per bar by calling stepCoreFromIndicators
	// directly. Same compute body either way (铁律 1).
	rs := freshRuntimeState()
	var lastProcessedBarTime int64

	// Incremental indicator state: O(1)/bar EMA + MAV + logReturn
	// lookback ring, replacing the O(window) batch recomputation that
	// drove the hot-loop cost before #6.
	incrState := newIncrIndicatorState(c)

	var (
		navAtWarmupStart float64
		peak             float64
		maxDD            float64
		fatal            bool
		fatalBarTS       int64
		observedMDD      float64
		barsScored       int
		prevPreNav       float64
	)

	// Log-return series for SharpeStats (§I-4.2). Capacity bounded by
	// the post-warmup bar count; sized once to avoid append growth.
	// One entry per scored bar (preNav transitions for bars > WarmupLen
	// plus a final entry covering navFinal vs the last preNav).
	scoredCap := len(window.Bars) - window.WarmupLen
	logReturns := make([]float64, 0, scoredCap)

	// StrategyInput is hoisted outside the loop and its three per-bar
	// fields updated in-place. We pass &input to stepCoreFromIndicators
	// to avoid a 216B by-value copy on every bar.
	// Closes/Timestamps/Spawn/RuntimeState remain zero — indicators are
	// pre-resolved via incrState; stepCoreFromIndicators does not read them.
	input := strategy.StrategyInput{Chromosome: gene}

	for i, bar := range window.Bars {
		// Advance incremental indicators (O(1): EMA, MAV rings, logReturn
		// lookback). Must precede computeMarketState/computeSignal calls.
		incrState.update(bar.Close)

		// Snapshot NAV just BEFORE this bar's orders apply. This is
		// the value used for peak/drawdown tracking at index i — it
		// captures the equity the strategy is operating against.
		preNav := (p.DeadBTC+p.FloatBTC)*bar.Close + p.USDT

		// Begin scoring at i == WarmupLen. Equity-path bookkeeping
		// uses preNav so the first scored bar's peak == the entry
		// nav (no spurious 0 → first-nav drawdown).
		if i == window.WarmupLen {
			navAtWarmupStart = preNav
			peak = preNav
			prevPreNav = preNav
		}
		if i >= window.WarmupLen {
			barsScored++
			if i > window.WarmupLen && prevPreNav > 0 && preNav > 0 {
				logReturns = append(logReturns, math.Log(preNav/prevPreNav))
			}
			prevPreNav = preNav
			if preNav > peak {
				peak = preNav
			}
			if peak > 0 {
				if dd := (peak - preNav) / peak; dd > maxDD {
					maxDD = dd
				}
				// First time we cross the Fatal line — record and break.
				if !fatal {
					if dd := (peak - preNav) / peak; dd >= fatalMDD {
						fatal = true
						fatalBarTS = bar.OpenTime
						observedMDD = dd
						break
					}
				}
			}
		}

		// Resolve indicators (O(1)/bar) and drive the strategy body.
		marketState, volRatio := incrState.computeMarketState(c.QuietThreshold)
		signal := incrState.computeSignal(c, volRatio, bar.Close)

		input.NowMs = bar.OpenTime
		input.Portfolio = p
		input.LastProcessedBarTime = lastProcessedBarTime
		macroOrders, microOrders, releaseIntents, newRS, dbg := stepCoreFromIndicators(
			&input, rs, c, marketState, volRatio, signal, bar.Close, false,
		)

		// Gap bars: discard orders so no fake trades; RuntimeState
		// still evolves through stepCore so peak window etc. stay
		// continuous.
		if bar.IsGap {
			macroOrders = nil
			microOrders = nil
			releaseIntents = nil
		}

		out := strategy.StrategyOutput{
			MacroOrders:    macroOrders,
			MicroOrders:    microOrders,
			ReleaseIntents: releaseIntents,
			DebugSnapshot:  dbg,
		}
		p = applyStrategyOutput(p, out, bar.Close, friction)
		rs = newRS
		lastProcessedBarTime = bar.OpenTime
	}

	if fatal {
		reason := string(resultpkg.FatalReasonMDDExceeded)
		return resultpkg.CrucibleResult{
			Window:        window.Name,
			Score:         resultpkg.SliceScore{Fatal: true, Value: nil},
			FatalReason:   &reason,
			FatalAtBarTS:  &fatalBarTS,
			FatalMDDValue: &observedMDD,
			BarsEvaluated: barsScored,
		}, nil, nil, nil
	}

	// Normal exit: compute log-return on the scored path. Final NAV
	// is taken AFTER the last bar's orders applied (mirror of
	// navAtWarmupStart which is taken BEFORE the first scored bar's
	// orders — together they span the full economic effect of the
	// scoring period).
	finalBar := window.Bars[len(window.Bars)-1]
	navFinal := (p.DeadBTC+p.FloatBTC)*finalBar.Close + p.USDT

	if navAtWarmupStart <= 0 || navFinal <= 0 {
		// Degenerate: log undefined. Treat as Fatal (something went
		// catastrophically wrong upstream of MDD threshold). Records
		// MDD as 1.0 to flag total wipeout.
		reason := string(resultpkg.FatalReasonNavNonPositive)
		ts := finalBar.OpenTime
		one := 1.0
		return resultpkg.CrucibleResult{
			Window:        window.Name,
			Score:         resultpkg.SliceScore{Fatal: true, Value: nil},
			FatalReason:   &reason,
			FatalAtBarTS:  &ts,
			FatalMDDValue: &one,
			BarsEvaluated: barsScored,
		}, nil, nil, nil
	}

	// Final return spans the last scored bar's orders being settled
	// against navFinal (which captures post-last-order inventory at
	// the last close). Adds one entry to logReturns so the series
	// covers `barsScored` periods total.
	if prevPreNav > 0 {
		logReturns = append(logReturns, math.Log(navFinal/prevPreNav))
	}
	stats := quant.ComputeSharpeStats(logReturns)

	score := math.Log(navFinal / navAtWarmupStart)
	return resultpkg.CrucibleResult{
		Window:        window.Name,
		Score:         resultpkg.SliceScore{Fatal: false, Value: &score},
		BarsEvaluated: barsScored,
	}, &stats, logReturns, nil
}
