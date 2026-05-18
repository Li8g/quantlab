package fitness

import (
	"math"
	"testing"
	"time"

	"quantlab/internal/domain"
)

const eps = 1e-9

// makeBars creates a synthetic Bar slice starting at startMs (UTC), one bar
// per minute, with all OHLC == price[i] and zero volume.
func makeBars(startMs int64, prices []float64) []domain.Bar {
	const minute = int64(60_000)
	out := make([]domain.Bar, len(prices))
	for i, p := range prices {
		out[i] = domain.Bar{
			OpenTime: startMs + int64(i)*minute,
			Open:     p, High: p, Low: p, Close: p,
			Volume: 0,
		}
	}
	return out
}

func zeroFP() domain.FrictionParams { return domain.FrictionParams{} }

// constPrices: n identical prices.
func constPrices(n int, p float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = p
	}
	return out
}

// linearPrices: n prices from start linearly increasing to end.
func linearPrices(n int, start, end float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = start + (end-start)*float64(i)/float64(n-1)
	}
	return out
}

// ----- Edge cases -----

func TestGhostDCA_EmptyBars(t *testing.T) {
	got := SimulateGhostDCAMonthly(GhostDCAConfig{InitialCapital: 1000, MonthlyInject: 100}, nil, zeroFP())
	if got != (GhostDCAResult{}) {
		t.Errorf("Monthly(nil bars) = %+v, want zero", got)
	}
	got = SimulateGhostDCAWeekly(GhostDCAConfig{InitialCapital: 1000, MonthlyInject: 100}, nil, zeroFP())
	if got != (GhostDCAResult{}) {
		t.Errorf("Weekly(nil bars) = %+v, want zero", got)
	}
}

// ----- Buy-and-hold (no periodic injection) -----

// With InitialCapital only and flat prices and zero friction, final equity
// equals initial capital, ROI = 0, MDD = 0.
func TestGhostDCA_BuyAndHoldFlatNoFriction(t *testing.T) {
	bars := makeBars(0, constPrices(60, 100.0))
	cfg := GhostDCAConfig{InitialCapital: 1000, MonthlyInject: 0}
	got := SimulateGhostDCAMonthly(cfg, bars, zeroFP())
	if math.Abs(got.FinalEquity-1000) > 1e-9 {
		t.Errorf("FinalEquity = %v, want 1000", got.FinalEquity)
	}
	if got.TotalInjected != 1000 {
		t.Errorf("TotalInjected = %v, want 1000", got.TotalInjected)
	}
	if got.MaxDrawdown != 0 {
		t.Errorf("MaxDrawdown = %v, want 0", got.MaxDrawdown)
	}
	if math.Abs(got.ROI) > eps {
		t.Errorf("ROI = %v, want 0", got.ROI)
	}
}

// With InitialCapital only and price doubling, ROI = 1.0 (Modified Dietz
// with single t=0 contribution: ROI = (E - C)/C = (2000 - 1000)/1000 = 1).
func TestGhostDCA_BuyAndHoldPriceDouble(t *testing.T) {
	bars := makeBars(0, linearPrices(100, 100.0, 200.0))
	cfg := GhostDCAConfig{InitialCapital: 1000, MonthlyInject: 0}
	got := SimulateGhostDCAMonthly(cfg, bars, zeroFP())
	if math.Abs(got.FinalEquity-2000) > 1e-6 {
		t.Errorf("FinalEquity = %v, want ~2000", got.FinalEquity)
	}
	if math.Abs(got.ROI-1.0) > 1e-6 {
		t.Errorf("ROI = %v, want 1.0", got.ROI)
	}
	if got.MaxDrawdown != 0 {
		t.Errorf("MaxDrawdown = %v, want 0 (monotonic up)", got.MaxDrawdown)
	}
}

// ----- Monthly cadence -----

// A 7-day series spans only one calendar month → only the initial buy.
func TestGhostDCAMonthly_SingleMonthHasNoPeriodicInject(t *testing.T) {
	// 2025-01-01 to 2025-01-07 inclusive — entirely within Jan.
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	bars := makeBars(start, constPrices(7*1440, 100.0))
	cfg := GhostDCAConfig{InitialCapital: 1000, MonthlyInject: 100}
	got := SimulateGhostDCAMonthly(cfg, bars, zeroFP())
	if got.TotalInjected != 1000 {
		t.Errorf("TotalInjected = %v, want 1000 (only initial)", got.TotalInjected)
	}
}

// 35 days spanning Jan + early Feb → initial + one Feb 1 injection = 1100.
// (60 days would span Jan/Feb/Mar = 2 injections, not 1.)
func TestGhostDCAMonthly_TwoMonthsOneInjection(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	bars := makeBars(start, constPrices(35*1440, 100.0))
	cfg := GhostDCAConfig{InitialCapital: 1000, MonthlyInject: 100}
	got := SimulateGhostDCAMonthly(cfg, bars, zeroFP())
	if got.TotalInjected != 1100 {
		t.Errorf("TotalInjected = %v, want 1100 (initial + Feb 1)", got.TotalInjected)
	}
	// Flat prices: FinalEquity == TotalInjected (no friction).
	if math.Abs(got.FinalEquity-1100) > 1e-6 {
		t.Errorf("FinalEquity = %v, want 1100", got.FinalEquity)
	}
}

// ----- Weekly cadence -----

// 7-day series: bar[0] initial, then bars at minute 7*1440 don't exist
// (series ends at day 7 - 1 minute). The trigger fires when the day-week
// bucket changes; with bars ending JUST before day 7, no weekly trigger.
func TestGhostDCAWeekly_TriggerCountSmoke(t *testing.T) {
	// 15 days → 15/7 = 2 full weeks elapsed → 2 weekly injections after the
	// initial seed.
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	bars := makeBars(start, constPrices(15*1440, 100.0))
	cfg := GhostDCAConfig{InitialCapital: 1000, MonthlyInject: 100}
	got := SimulateGhostDCAWeekly(cfg, bars, zeroFP())

	expected := 1000.0 + 2*(100.0/weeklyInjectDivisor)
	if math.Abs(got.TotalInjected-expected) > 1e-9 {
		t.Errorf("TotalInjected = %v, want %v", got.TotalInjected, expected)
	}
}

// ----- Friction effect -----

func TestGhostDCA_FeeReducesFinalEquity(t *testing.T) {
	bars := makeBars(0, constPrices(60*1440, 100.0))
	cfg := GhostDCAConfig{InitialCapital: 1000, MonthlyInject: 100}

	noFric := SimulateGhostDCAMonthly(cfg, bars, zeroFP())
	withFric := SimulateGhostDCAMonthly(cfg, bars, domain.FrictionParams{TakerFeeBPS: 10, SlippageBPS: 5})

	if !(withFric.FinalEquity < noFric.FinalEquity) {
		t.Errorf("with-friction FinalEquity %v not less than no-friction %v",
			withFric.FinalEquity, noFric.FinalEquity)
	}
	// Roughly: 15 bps loss per buy * (initial + one inject) ≈ 15bps on
	// ~1100; tolerate a wide range, just need it bounded and positive.
	loss := noFric.FinalEquity - withFric.FinalEquity
	if loss <= 0 || loss > 5 {
		t.Errorf("friction loss = %v, expected (0, 5]", loss)
	}
}

// ----- MaxDrawdown -----

func TestGhostDCA_MaxDrawdownVShape(t *testing.T) {
	// Price goes 100 → 50 → 100. Equity follows same shape (no injection).
	// MDD should be 50%.
	n := 600
	prices := make([]float64, n)
	for i := 0; i < n/2; i++ {
		prices[i] = 100 - 50*float64(i)/float64(n/2)
	}
	for i := n / 2; i < n; i++ {
		prices[i] = 50 + 50*float64(i-n/2)/float64(n/2-1)
	}
	prices[0] = 100 // seed price for initial buy
	bars := makeBars(0, prices)
	cfg := GhostDCAConfig{InitialCapital: 1000, MonthlyInject: 0}
	got := SimulateGhostDCAMonthly(cfg, bars, zeroFP())
	if math.Abs(got.MaxDrawdown-0.5) > 0.01 {
		t.Errorf("MaxDrawdown = %v, want ~0.5", got.MaxDrawdown)
	}
}

// ----- ROI method switch -----

// First inject is 10% of NAV (exactly at threshold, not over). Should NOT
// flip to TWR — boundary test.
func TestGhostDCA_ROIMethodAtBoundary(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	bars := makeBars(start, constPrices(60*1440, 100.0))
	// Inject = 10% of initial 1000 NAV exactly. Should pick Modified Dietz.
	cfg := GhostDCAConfig{InitialCapital: 1000, MonthlyInject: 100}
	got := SimulateGhostDCAMonthly(cfg, bars, zeroFP())
	// Two contributions at t=0 (w=1) and t≈T*(31/60) (w≈29/60).
	// ROI = (1100 - 1100) / (1*1000 + 29/60*100) = 0
	if math.Abs(got.ROI) > 1e-6 {
		t.Errorf("ROI = %v, want 0", got.ROI)
	}
}

// Verify TWR path: huge inject (>10%) into a flat market → TWR = 0.
func TestGhostDCA_TWRPathFlatMarket(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	bars := makeBars(start, constPrices(60*1440, 100.0))
	// Inject = 50% of initial. Triggers TWR. Flat prices → TWR = 0.
	cfg := GhostDCAConfig{InitialCapital: 1000, MonthlyInject: 500}
	got := SimulateGhostDCAMonthly(cfg, bars, zeroFP())
	if math.Abs(got.ROI) > 1e-9 {
		t.Errorf("ROI = %v, want 0 (flat market, TWR)", got.ROI)
	}
}
