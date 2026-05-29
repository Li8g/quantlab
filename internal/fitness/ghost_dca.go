// Ghost DCA dual-baseline simulator (Part I §I-3.10).
//
// Two passive buy-and-hold baselines that any active strategy must beat to
// earn a positive raw window score:
//
//   - SimulateGhostDCAMonthly: lump-sum each calendar-month-start at UTC.
//   - SimulateGhostDCAWeekly:  one inject every 7 elapsed days from bar[0],
//     sized MonthlyInject / 4.33 to roughly match
//     monthly cash outflow.
//
// Both share the same kernel (simulateDCA) and the same friction path
// (quant.ApplyBuyFriction). InitialCapital is invested at bar[0]; periodic
// injections follow the cadence above. After each buy the simulator holds
// 0 cash (all-in DCA) — NAV at any bar i is qty * bars[i].Close.
//
// ROI calculation:
//   - Default: Modified Dietz (linearly time-weighted contributions).
//   - Fallback: precise TWR (chain-linked sub-period returns), triggered if
//     any single post-initial injection exceeds 10% of pre-injection NAV.
//     The 10% threshold is the framework doc's hand-off point.
//
// MaxDrawdown is peak-to-trough on the per-bar NAV series.
package fitness

import (
	"time"

	"quantlab/internal/domain"
	"quantlab/internal/quant"
)

// GhostDCAConfig parameterises one DCA baseline run.
type GhostDCAConfig struct {
	InitialCapital float64 // USDT invested at bar[0]; 0 disables seed
	MonthlyInject  float64 // USDT injected per calendar month (or scaled weekly)
}

// GhostDCAResult is the four-tuple returned by both Monthly and Weekly.
type GhostDCAResult struct {
	FinalEquity   float64 // NAV at bars[len-1].Close
	TotalInjected float64 // sum of InitialCapital + all periodic injections actually applied
	MaxDrawdown   float64 // (peak-trough)/peak, max over NAV path; [0,1]
	ROI           float64 // Modified Dietz, or TWR if any post-init injection > 10% pre-NAV
}

// dietzTwrSwitchRatio is the framework threshold: a single sub-period
// injection larger than 10% of pre-injection NAV makes Modified Dietz
// inaccurate; we fall back to chain-linked TWR.
const dietzTwrSwitchRatio = 0.10

// weeklyInjectDivisor splits a monthly cash budget into weekly tranches.
// 4.33 ≈ 52/12 weeks per month — matches the framework doc literally.
const weeklyInjectDivisor = 4.33

// SimulateGhostDCAMonthly runs the calendar-month-start cadence DCA.
// Trigger: first bar whose UTC calendar month differs from the previous bar.
func SimulateGhostDCAMonthly(cfg GhostDCAConfig, bars []domain.Bar, fp domain.FrictionParams) GhostDCAResult {
	monthly := func(prevTimeMs, curTimeMs int64, _ int) (bool, float64) {
		prev := time.UnixMilli(prevTimeMs).UTC()
		cur := time.UnixMilli(curTimeMs).UTC()
		if cur.Year() != prev.Year() || cur.Month() != prev.Month() {
			return true, cfg.MonthlyInject
		}
		return false, 0
	}
	return simulateDCA(cfg.InitialCapital, bars, fp, monthly)
}

// SimulateGhostDCAWeekly runs the rolling-7-days cadence DCA.
// Trigger: first bar where (cur - bar0)/7d differs from (prev - bar0)/7d.
func SimulateGhostDCAWeekly(cfg GhostDCAConfig, bars []domain.Bar, fp domain.FrictionParams) GhostDCAResult {
	if len(bars) == 0 {
		return GhostDCAResult{}
	}
	startMs := bars[0].OpenTime
	const weekMs = int64(7 * 24 * 60 * 60 * 1000)
	dust := cfg.MonthlyInject / weeklyInjectDivisor

	weekly := func(prevTimeMs, curTimeMs int64, _ int) (bool, float64) {
		if (prevTimeMs-startMs)/weekMs != (curTimeMs-startMs)/weekMs {
			return true, dust
		}
		return false, 0
	}
	return simulateDCA(cfg.InitialCapital, bars, fp, weekly)
}

// injectionEvent records one cash flow for post-hoc ROI accounting.
type injectionEvent struct {
	idx       int     // bar index where the buy executed
	timeMs    int64   // bar.OpenTime
	amount    float64 // USD injected
	navBefore float64 // NAV at bars[idx].Close before this injection's buy (= 0 for idx=0)
	navAfter  float64 // NAV at bars[idx].Close after this injection's buy
}

// triggerFn returns (shouldInject, amountUSD) for the transition from
// bars[i-1] to bars[i]. The first invocation is at i=1 (bar[0] is seeded
// with InitialCapital separately).
type triggerFn func(prevTimeMs, curTimeMs int64, idx int) (bool, float64)

// simulateDCA is the shared kernel for both Monthly and Weekly. It walks
// the bars exactly once, records NAV at every bar, applies friction to
// every buy via quant.ApplyBuyFriction, and returns the GhostDCAResult.
func simulateDCA(initialCapital float64, bars []domain.Bar, fp domain.FrictionParams, trigger triggerFn) GhostDCAResult {
	n := len(bars)
	if n == 0 {
		return GhostDCAResult{}
	}

	var qty float64
	var totalInjected float64
	nav := make([]float64, n)
	injections := make([]injectionEvent, 0, n/30+1)

	// Bar 0: seed with InitialCapital.
	if initialCapital > 0 && bars[0].Close > 0 {
		filled, _, _ := quant.ApplyBuyFriction(initialCapital, bars[0].Close, fp)
		qty += filled
		totalInjected += initialCapital
		injections = append(injections, injectionEvent{
			idx:       0,
			timeMs:    bars[0].OpenTime,
			amount:    initialCapital,
			navBefore: 0,
			navAfter:  qty * bars[0].Close,
		})
	}
	nav[0] = qty * bars[0].Close

	// Bars 1..n-1: NAV is qty*close pre-injection; if this bar triggers,
	// inject and re-mark NAV post-buy.
	for i := 1; i < n; i++ {
		navBefore := qty * bars[i].Close
		fire, amount := trigger(bars[i-1].OpenTime, bars[i].OpenTime, i)
		if fire && amount > 0 && bars[i].Close > 0 {
			filled, _, _ := quant.ApplyBuyFriction(amount, bars[i].Close, fp)
			qty += filled
			totalInjected += amount
			navAfter := qty * bars[i].Close
			injections = append(injections, injectionEvent{
				idx:       i,
				timeMs:    bars[i].OpenTime,
				amount:    amount,
				navBefore: navBefore,
				navAfter:  navAfter,
			})
			nav[i] = navAfter
		} else {
			nav[i] = navBefore
		}
	}

	final := nav[n-1]
	mdd := maxDrawdown(nav)
	roi := computeROI(injections, bars, final)

	return GhostDCAResult{
		FinalEquity:   final,
		TotalInjected: totalInjected,
		MaxDrawdown:   mdd,
		ROI:           roi,
	}
}

// maxDrawdown returns the largest (peak - trough) / peak ratio observed
// on the NAV path. Always in [0, 1]. Returns 0 for an empty/single point.
func maxDrawdown(nav []float64) float64 {
	if len(nav) == 0 {
		return 0
	}
	peak := nav[0]
	var mdd float64
	for _, v := range nav {
		if v > peak {
			peak = v
		}
		if peak > 0 {
			dd := (peak - v) / peak
			if dd > mdd {
				mdd = dd
			}
		}
	}
	return mdd
}

// computeROI picks Modified Dietz or TWR based on the largest post-initial
// injection ratio. The very first injection (at bar[0], navBefore=0) is
// always excluded from this check — it has no meaningful ratio.
func computeROI(injections []injectionEvent, bars []domain.Bar, final float64) float64 {
	if len(injections) == 0 {
		return 0
	}
	useTWR := false
	for _, inj := range injections[1:] {
		if inj.navBefore > 0 && inj.amount/inj.navBefore > dietzTwrSwitchRatio {
			useTWR = true
			break
		}
	}
	if useTWR {
		return twr(injections, final)
	}
	return modifiedDietz(injections, bars, final)
}

// modifiedDietz computes the linearly time-weighted return.
//
// ROI = (E - Σ C_i) / Σ (w_i · C_i),  where w_i = (T - t_i) / T,
// t_i is the elapsed time from bar[0] to injection i, and T is the total
// elapsed time. This treats B=0 (no pre-existing balance) — appropriate
// for Ghost DCA which starts from nothing.
func modifiedDietz(injections []injectionEvent, bars []domain.Bar, final float64) float64 {
	if len(injections) == 0 || len(bars) < 2 {
		return 0
	}
	startMs := bars[0].OpenTime
	endMs := bars[len(bars)-1].OpenTime
	T := float64(endMs - startMs)
	if T <= 0 {
		// Single-instant series — degenerate. Define ROI as the simple
		// (E - C) / C for safety.
		var sumC float64
		for _, inj := range injections {
			sumC += inj.amount
		}
		if sumC == 0 {
			return 0
		}
		return (final - sumC) / sumC
	}

	var sumC, wSumC float64
	for _, inj := range injections {
		t := float64(inj.timeMs - startMs)
		w := (T - t) / T
		sumC += inj.amount
		wSumC += w * inj.amount
	}
	if wSumC == 0 {
		return 0
	}
	return (final - sumC) / wSumC
}

// twr computes the chain-linked Time-Weighted Return.
//
// Each sub-period starts at injection k (post-buy NAV = inj[k].navAfter)
// and ends at the moment just before injection k+1 (= inj[k+1].navBefore),
// or at `final` for the last sub-period.
//
//	r_k = end_k / start_k - 1
//	TWR = Π (1 + r_k) - 1
func twr(injections []injectionEvent, final float64) float64 {
	if len(injections) == 0 {
		return 0
	}
	growth := 1.0
	for k := 0; k < len(injections); k++ {
		start := injections[k].navAfter
		var end float64
		if k+1 < len(injections) {
			end = injections[k+1].navBefore
		} else {
			end = final
		}
		if start <= 0 {
			continue
		}
		growth *= end / start
	}
	return growth - 1
}
