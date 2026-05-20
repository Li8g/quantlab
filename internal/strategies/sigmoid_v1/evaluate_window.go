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
	"encoding/json"
	"fmt"
	"math"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
	"quantlab/internal/verification"
)

// stepHistoryCap bounds the length of the trailing closes/timestamps
// slices fed into Step(). At MaxChromosomePeriod × 3 = 900 bars, the
// longest-period EMA (max ema_long_period = 300) has converged to
// well under 1% of its asymptotic value (α^(3·period) ≈ e^-6). The
// MAV short lookback ≤ 50 bars fits trivially.
//
// Without this cap, ComputeSignal's quant.EMA call is O(n) per bar
// and the loop is O(n²) — 260k 1m bars × 260k = 70B ops per gene.
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
func evaluateWindow(strat *Sigmoid, gene domain.Gene, window domain.CrucibleWindow, friction domain.FrictionParams, fatalMDD, initialUSDT float64) (resultpkg.CrucibleResult, *resultpkg.SharpeStats, error) {
	if len(window.Bars) == 0 {
		return resultpkg.CrucibleResult{}, nil, fmt.Errorf("evaluateWindow: window %q has no bars", window.Name)
	}
	if window.WarmupLen >= len(window.Bars) {
		return resultpkg.CrucibleResult{}, nil, fmt.Errorf(
			"evaluateWindow: window %q WarmupLen=%d >= len(Bars)=%d",
			window.Name, window.WarmupLen, len(window.Bars),
		)
	}

	p := strategy.PortfolioSnapshot{USDT: initialUSDT}
	var rs json.RawMessage // empty = cold start, see decodeRuntimeState
	var lastProcessedBarTime int64

	// Trailing closes/timestamps. Pre-allocated to the cap and
	// shifted in-place via copy() once full, so the backing array
	// never exceeds stepHistoryCap regardless of window size.
	closesBuf := make([]float64, 0, stepHistoryCap)
	timestampsBuf := make([]int64, 0, stepHistoryCap)

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

	for i, bar := range window.Bars {
		// Append to trailing history, shifting in-place when full.
		if len(closesBuf) == stepHistoryCap {
			copy(closesBuf, closesBuf[1:])
			closesBuf[stepHistoryCap-1] = bar.Close
			copy(timestampsBuf, timestampsBuf[1:])
			timestampsBuf[stepHistoryCap-1] = bar.OpenTime
		} else {
			closesBuf = append(closesBuf, bar.Close)
			timestampsBuf = append(timestampsBuf, bar.OpenTime)
		}

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

		input := strategy.StrategyInput{
			NowMs:                bar.OpenTime,
			Closes:               closesBuf,
			Timestamps:           timestampsBuf,
			Portfolio:            p,
			Chromosome:           gene,
			LastProcessedBarTime: lastProcessedBarTime,
			RuntimeState:         rs,
		}
		out, err := strat.Step(input)
		if err != nil {
			return resultpkg.CrucibleResult{}, nil, fmt.Errorf(
				"evaluateWindow: window %q bar %d (ts=%d): %w",
				window.Name, i, bar.OpenTime, err,
			)
		}

		// Gap bars: discard orders so no fake trades; RuntimeState
		// still evolves through Step() so peak window etc. stay
		// continuous.
		if bar.IsGap {
			out.MacroOrders = nil
			out.MicroOrders = nil
			out.ReleaseIntents = nil
		}

		p = applyStrategyOutput(p, out, bar.Close, friction)
		rs = out.RuntimeState
		lastProcessedBarTime = bar.OpenTime
	}

	if fatal {
		reason := fmt.Sprintf("drawdown_%.2f", observedMDD)
		return resultpkg.CrucibleResult{
			Window:        window.Name,
			Score:         resultpkg.SliceScore{Fatal: true, Value: nil},
			FatalReason:   &reason,
			FatalAtBarTS:  &fatalBarTS,
			FatalMDDValue: &observedMDD,
			BarsEvaluated: barsScored,
		}, nil, nil
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
		reason := "nav_non_positive"
		ts := finalBar.OpenTime
		one := 1.0
		return resultpkg.CrucibleResult{
			Window:        window.Name,
			Score:         resultpkg.SliceScore{Fatal: true, Value: nil},
			FatalReason:   &reason,
			FatalAtBarTS:  &ts,
			FatalMDDValue: &one,
			BarsEvaluated: barsScored,
		}, nil, nil
	}

	// Final return spans the last scored bar's orders being settled
	// against navFinal (which captures post-last-order inventory at
	// the last close). Adds one entry to logReturns so the series
	// covers `barsScored` periods total.
	if prevPreNav > 0 {
		logReturns = append(logReturns, math.Log(navFinal/prevPreNav))
	}
	stats := verification.ComputeSharpeStats(logReturns)

	score := math.Log(navFinal / navAtWarmupStart)
	return resultpkg.CrucibleResult{
		Window:        window.Name,
		Score:         resultpkg.SliceScore{Fatal: false, Value: &score},
		BarsEvaluated: barsScored,
	}, &stats, nil
}
