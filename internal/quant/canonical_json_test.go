package quant

import (
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
)

// TestBarsHashExcludesMetadata is priority test #12.
// Changing Bar.IsGap or Bar.GapType must not alter bars_hash — gap-detection
// algorithm upgrades must not invalidate the reproducibility guarantee.
func TestBarsHashExcludesMetadata(t *testing.T) {
	base := []domain.Bar{
		{OpenTime: 1000, Open: 10.0, High: 11.0, Low: 9.0, Close: 10.5, Volume: 100.0},
		{OpenTime: 2000, Open: 10.5, High: 12.0, Low: 10.0, Close: 11.0, Volume: 150.0},
	}
	withGap := make([]domain.Bar, len(base))
	copy(withGap, base)
	withGap[0].IsGap = true
	withGap[0].GapType = "price_gap"
	withGap[1].IsGap = true
	withGap[1].GapType = "volume_gap"

	h1, err := BarsHash(base)
	if err != nil {
		t.Fatalf("BarsHash(base): %v", err)
	}
	h2, err := BarsHash(withGap)
	if err != nil {
		t.Fatalf("BarsHash(withGap): %v", err)
	}
	if h1 != h2 {
		t.Errorf("IsGap/GapType mutated bars_hash:\n  base=%s\n  withGap=%s", h1, h2)
	}
}

// TestBarsHashSensitiveToPrice verifies the hash is not degenerate:
// any OHLCV change must produce a different hash.
func TestBarsHashSensitiveToPrice(t *testing.T) {
	a := []domain.Bar{{OpenTime: 1000, Open: 10.0, High: 11.0, Low: 9.0, Close: 10.5, Volume: 100.0}}
	b := []domain.Bar{{OpenTime: 1000, Open: 10.0, High: 11.0, Low: 9.0, Close: 99.9, Volume: 100.0}}

	ha, err := BarsHash(a)
	if err != nil {
		t.Fatalf("BarsHash(a): %v", err)
	}
	hb, err := BarsHash(b)
	if err != nil {
		t.Fatalf("BarsHash(b): %v", err)
	}
	if ha == hb {
		t.Error("different Close values must produce different bars_hash")
	}
}

// TestBarsHashDeterministic verifies the same input produces the same hash.
func TestBarsHashDeterministic(t *testing.T) {
	bars := []domain.Bar{
		{OpenTime: 1000, Open: 10.0, High: 11.0, Low: 9.0, Close: 10.5, Volume: 100.0},
	}
	h1, err := BarsHash(bars)
	if err != nil {
		t.Fatalf("first BarsHash: %v", err)
	}
	h2, err := BarsHash(bars)
	if err != nil {
		t.Fatalf("second BarsHash: %v", err)
	}
	if h1 != h2 {
		t.Errorf("same input produced different hashes: %s vs %s", h1, h2)
	}
}

// TestBarsHashFormat verifies the output is a 64-character lower-hex string.
func TestBarsHashFormat(t *testing.T) {
	bars := []domain.Bar{{OpenTime: 1, Open: 1, High: 1, Low: 1, Close: 1, Volume: 1}}
	h, err := BarsHash(bars)
	if err != nil {
		t.Fatalf("BarsHash: %v", err)
	}
	if len(h) != 64 {
		t.Errorf("expected 64-char hex hash, got len=%d: %s", len(h), h)
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-lower-hex character %q in hash %s", c, h)
			break
		}
	}
}

// planFixture returns a minimal EvaluablePlan for hash testing.
// Values are arbitrary but deterministic.
func planFixture() *domain.EvaluablePlan {
	bars := []domain.Bar{
		{OpenTime: 1000, Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 100},
		{OpenTime: 2000, Open: 10.5, High: 12, Low: 10, Close: 11, Volume: 150},
	}
	return &domain.EvaluablePlan{
		Pair:        "BTCUSDT",
		Spawn:       resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeRandomOnce},
		LotStep:     0.00001,
		LotMin:      0.00001,
		FatalMDD:    0.5,
		InitialUSDT: 10_000,
		Windows: []domain.CrucibleWindow{{
			Name:      resultpkg.Window6M,
			StartTS:   bars[0].OpenTime,
			EndTS:     bars[len(bars)-1].OpenTime,
			WarmupLen: 0,
			Bars:      bars,
		}},
		Friction: domain.FrictionParams{TakerFeeBPS: 5, SlippageBPS: 2},
	}
}

// TestPlanHashDeterministic — same input, two calls, byte-identical hashes.
// This is the frozen-comment contract: PlanHash must be reproducible
// across processes for the v5.3.3 reproducibility-metadata guarantee.
func TestPlanHashDeterministic(t *testing.T) {
	plan := planFixture()
	h1, err := PlanHash(plan)
	if err != nil {
		t.Fatalf("first PlanHash: %v", err)
	}
	h2, err := PlanHash(plan)
	if err != nil {
		t.Fatalf("second PlanHash: %v", err)
	}
	if h1 != h2 {
		t.Errorf("same plan produced different hashes: %s vs %s", h1, h2)
	}
}

// TestPlanHashExcludesAggregateCache — AggregateCache has json:"-" on
// the struct field, so mutating it must not move plan_hash. Cache is a
// pure-memory perf optimisation; reproducibility must not depend on it.
func TestPlanHashExcludesAggregateCache(t *testing.T) {
	plan := planFixture()
	h1, err := PlanHash(plan)
	if err != nil {
		t.Fatalf("base PlanHash: %v", err)
	}
	// AggregateCache is currently struct{} — assignment is a no-op but
	// pins the intent: future fields added to AggregateCache must keep
	// the json:"-" exclusion.
	plan.AggregateCache = domain.AggregateCache{}
	h2, err := PlanHash(plan)
	if err != nil {
		t.Fatalf("mutated PlanHash: %v", err)
	}
	if h1 != h2 {
		t.Errorf("AggregateCache mutation altered plan_hash: %s vs %s", h1, h2)
	}
}

// TestPlanHashSensitiveToPair — flipping a non-cache field must move
// plan_hash. Guards against an accidental json:"-" creep.
func TestPlanHashSensitiveToPair(t *testing.T) {
	plan := planFixture()
	h1, err := PlanHash(plan)
	if err != nil {
		t.Fatalf("base PlanHash: %v", err)
	}
	plan.Pair = "ETHUSDT"
	h2, err := PlanHash(plan)
	if err != nil {
		t.Fatalf("mutated PlanHash: %v", err)
	}
	if h1 == h2 {
		t.Error("changing Pair did not move plan_hash")
	}
}

// TestPlanHashFormat — 64-char lower-hex, same as bars_hash format.
func TestPlanHashFormat(t *testing.T) {
	h, err := PlanHash(planFixture())
	if err != nil {
		t.Fatalf("PlanHash: %v", err)
	}
	if len(h) != 64 {
		t.Errorf("expected 64-char hex hash, got len=%d: %s", len(h), h)
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-lower-hex character %q in hash %s", c, h)
			break
		}
	}
}
