package data

import (
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/fitness"
	"quantlab/internal/quant"
	"quantlab/internal/resultpkg"
)

// planBars returns 4000 daily bars with a deterministic price ramp so
// the Ghost DCA simulator has a non-flat path and produces non-zero
// FinalEquity. 4000 days comfortably accommodates all four IS windows.
func planBars(n int, startMs int64) []domain.Bar {
	out := make([]domain.Bar, n)
	price := 100.0
	for i := 0; i < n; i++ {
		out[i] = domain.Bar{
			OpenTime: startMs + int64(i)*DayMs,
			Open:     price,
			High:     price + 1,
			Low:      price - 1,
			Close:    price,
			Volume:   1000,
		}
		price *= 1.0001 // ~0.01%/day → modest uptrend
	}
	return out
}

func defaultPlanOpts() PlanOptions {
	return PlanOptions{
		Pair:       "BTCUSDT",
		Spawn:      resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeRandomOnce},
		WarmupDays: 100,
		Friction:   domain.FrictionParams{TakerFeeBPS: 5, SlippageBPS: 2},
		LotStep:    0.00001,
		LotMin:     0.00001,
		DCA: fitness.GhostDCAConfig{
			InitialCapital: 10_000,
			MonthlyInject:  0,
		},
	}
}

func TestBuildEvaluablePlan_PopulatesAllLayers(t *testing.T) {
	bars := planBars(4000, 1_700_000_000_000)
	plan, planHash, barsHash, err := BuildEvaluablePlan(bars, defaultPlanOpts())
	if err != nil {
		t.Fatalf("BuildEvaluablePlan: %v", err)
	}
	if plan.Pair != "BTCUSDT" {
		t.Errorf("Pair = %q, want BTCUSDT", plan.Pair)
	}
	if len(plan.Windows) != 4 {
		t.Errorf("Windows len = %d, want 4 (all IS windows fit)", len(plan.Windows))
	}
	if plan.DCABaselines.Monthly.FinalEquity <= 0 {
		t.Errorf("Monthly DCA FinalEquity=%v, want > 0", plan.DCABaselines.Monthly.FinalEquity)
	}
	if plan.DCABaselines.Weekly.FinalEquity <= 0 {
		t.Errorf("Weekly DCA FinalEquity=%v, want > 0", plan.DCABaselines.Weekly.FinalEquity)
	}
	if len(planHash) != 64 || len(barsHash) != 64 {
		t.Errorf("hash lengths: plan=%d bars=%d, both want 64", len(planHash), len(barsHash))
	}

	// Cross-check: returned hashes match what quant.PlanHash /
	// quant.BarsHash compute against the plan + input bars.
	wantBarsHash, err := quant.BarsHash(bars)
	if err != nil {
		t.Fatalf("quant.BarsHash: %v", err)
	}
	if barsHash != wantBarsHash {
		t.Errorf("returned barsHash=%s, quant.BarsHash=%s", barsHash, wantBarsHash)
	}
	wantPlanHash, err := quant.PlanHash(plan)
	if err != nil {
		t.Fatalf("quant.PlanHash: %v", err)
	}
	if planHash != wantPlanHash {
		t.Errorf("returned planHash=%s, quant.PlanHash=%s", planHash, wantPlanHash)
	}
}

func TestBuildEvaluablePlan_Deterministic(t *testing.T) {
	bars := planBars(4000, 1_700_000_000_000)
	opts := defaultPlanOpts()

	_, ph1, bh1, err := BuildEvaluablePlan(bars, opts)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	_, ph2, bh2, err := BuildEvaluablePlan(bars, opts)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if ph1 != ph2 {
		t.Errorf("plan_hash drifted across builds: %s vs %s", ph1, ph2)
	}
	if bh1 != bh2 {
		t.Errorf("bars_hash drifted across builds: %s vs %s", bh1, bh2)
	}
}

func TestBuildEvaluablePlan_GapMetadataDoesNotMoveBarsHash(t *testing.T) {
	// Mirror canonical_json_test's TestBarsHashExcludesMetadata but at
	// the BuildEvaluablePlan layer — gap-detection upgrades must not
	// invalidate reproducibility metadata for an in-flight plan.
	bars := planBars(4000, 1_700_000_000_000)
	withGap := make([]domain.Bar, len(bars))
	copy(withGap, bars)
	for i := 0; i < len(withGap); i += 100 {
		withGap[i].IsGap = true
		withGap[i].GapType = "synthetic"
	}

	opts := defaultPlanOpts()
	_, _, bh1, err := BuildEvaluablePlan(bars, opts)
	if err != nil {
		t.Fatalf("base build: %v", err)
	}
	_, _, bh2, err := BuildEvaluablePlan(withGap, opts)
	if err != nil {
		t.Fatalf("gap build: %v", err)
	}
	if bh1 != bh2 {
		t.Errorf("gap metadata leaked into bars_hash: %s vs %s", bh1, bh2)
	}
}

func TestBuildEvaluablePlan_OOSExcludedFromDCABaselines(t *testing.T) {
	// DCA baselines must reflect only the IS period — OOS bars must
	// not leak into the baseline equity path. We compare baselines
	// computed with and without OOS configured; with OOS, the
	// baseline runs over fewer bars and so produces different
	// FinalEquity (lower, since the uptrend's tail is excluded).
	bars := planBars(4000, 1_700_000_000_000)
	optsNoOOS := defaultPlanOpts()
	planNo, _, _, err := BuildEvaluablePlan(bars, optsNoOOS)
	if err != nil {
		t.Fatalf("no-oos build: %v", err)
	}

	oos := 500
	optsWithOOS := defaultPlanOpts()
	optsWithOOS.OosDays = &oos
	planWith, _, _, err := BuildEvaluablePlan(bars, optsWithOOS)
	if err != nil {
		t.Fatalf("with-oos build: %v", err)
	}

	if planNo.DCABaselines.Monthly.FinalEquity == planWith.DCABaselines.Monthly.FinalEquity {
		t.Errorf("OOS truncation did not change Monthly DCA: both = %v",
			planNo.DCABaselines.Monthly.FinalEquity)
	}
	if planWith.OosWindow == nil {
		t.Error("with-oos build returned nil OosWindow")
	}
}

func TestBuildEvaluablePlan_PriceChangeMovesBothHashes(t *testing.T) {
	// Sanity: a real input change must move both bars_hash and
	// plan_hash. Guards against an accidental no-op assembler.
	bars := planBars(4000, 1_700_000_000_000)
	_, ph1, bh1, err := BuildEvaluablePlan(bars, defaultPlanOpts())
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	// Mutate one bar's Close.
	mut := make([]domain.Bar, len(bars))
	copy(mut, bars)
	mut[1000].Close *= 2
	_, ph2, bh2, err := BuildEvaluablePlan(mut, defaultPlanOpts())
	if err != nil {
		t.Fatalf("mut: %v", err)
	}
	if bh1 == bh2 {
		t.Error("Close change did not move bars_hash")
	}
	if ph1 == ph2 {
		t.Error("Close change did not move plan_hash (bars embedded in windows)")
	}
}

func TestBuildEvaluablePlan_BadInputPropagatesError(t *testing.T) {
	_, _, _, err := BuildEvaluablePlan(nil, defaultPlanOpts())
	if err == nil {
		t.Error("nil bars: want error, got nil")
	}
}
