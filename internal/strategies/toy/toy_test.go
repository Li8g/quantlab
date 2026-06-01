// [TOY v1] Tests exercise the 14-verb EvolvableStrategy contract end-to-
// end. They double as a smoke test for the interface design itself: if
// these compile and pass, the Phase 5A interface shape is sound.
package toy

import (
	"context"
	"math"
	"math/rand"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
)

func TestToy_SampleProducesValidGene(t *testing.T) {
	s := New()
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 100; i++ {
		g := s.Sample(rng)
		if err := s.Validate(g); err != nil {
			t.Fatalf("iter %d: Sample produced invalid gene %v: %v", i, g, err)
		}
	}
}

func TestToy_ClampClipsOutOfRange(t *testing.T) {
	s := New()
	g := s.Clamp(domain.Gene{2.0, -2.0})
	if g[geneDimAlpha] != maxAlpha || g[geneDimBeta] != minBeta {
		t.Errorf("Clamp(2,-2) = %v, want [%g,%g]", g, maxAlpha, minBeta)
	}
	// Wrong-dim input must still produce a valid result (contract).
	g2 := s.Clamp(domain.Gene{0.5})
	if err := s.Validate(g2); err != nil {
		t.Errorf("Clamp on short input: Validate err = %v, want nil", err)
	}
}

func TestToy_Validate(t *testing.T) {
	s := New()
	cases := []struct {
		name    string
		g       domain.Gene
		wantErr bool
	}{
		{"valid mid", domain.Gene{0.5, 0}, false},
		{"valid edges", domain.Gene{0, -1}, false},
		{"alpha too high", domain.Gene{1.5, 0}, true},
		{"beta too low", domain.Gene{0.5, -2}, true},
		{"wrong dim", domain.Gene{0.5}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.Validate(c.g)
			if (err != nil) != c.wantErr {
				t.Errorf("Validate(%v) err=%v, wantErr=%v", c.g, err, c.wantErr)
			}
		})
	}
}

func TestToy_CrossoverChildSegmentsFromOneParent(t *testing.T) {
	s := New()
	rng := rand.New(rand.NewSource(42))
	p1 := domain.Gene{0.1, -0.7}
	p2 := domain.Gene{0.9, 0.7}
	for i := 0; i < 50; i++ {
		c := s.Crossover(p1, p2, rng)
		if c[geneDimAlpha] != 0.1 && c[geneDimAlpha] != 0.9 {
			t.Errorf("iter %d: alpha %g not from either parent", i, c[geneDimAlpha])
		}
		if c[geneDimBeta] != -0.7 && c[geneDimBeta] != 0.7 {
			t.Errorf("iter %d: beta %g not from either parent", i, c[geneDimBeta])
		}
	}
}

func TestToy_MutateResultIsClamped(t *testing.T) {
	s := New()
	rng := rand.New(rand.NewSource(7))
	g := domain.Gene{0.99, 0.99}
	for i := 0; i < 100; i++ {
		// prob=1 + scale=100 guarantees big deltas that would overflow
		// without Clamp. We assert all results stay in-range.
		m := s.Mutate(g, 1.0, 100.0, rng)
		if err := s.Validate(m); err != nil {
			t.Fatalf("iter %d: Mutate produced invalid gene %v: %v", i, m, err)
		}
	}
}

func TestToy_FingerprintDeterministic(t *testing.T) {
	s := New()
	g := domain.Gene{0.42, -0.3}
	//lint:ignore SA4000 deliberate: same gene must hash identically (determinism check)
	if s.Fingerprint(g) != s.Fingerprint(g) {
		t.Error("Fingerprint nondeterministic")
	}
	if s.Fingerprint(g) == s.Fingerprint(domain.Gene{0.5, 0}) {
		t.Error("different genes produced same fingerprint")
	}
}

func TestToy_FingerprintQuantizationCollision(t *testing.T) {
	s := New()
	// QuantizationStep is 0.001; values within step round to same bucket.
	if s.Fingerprint(domain.Gene{0.4201, -0.3001}) != s.Fingerprint(domain.Gene{0.4202, -0.3002}) {
		t.Error("expected sub-quantum genes to collide")
	}
}

func TestToy_EvaluateAtOptimum(t *testing.T) {
	s := New()
	plan := miniPlan(t)
	raw, err := s.Evaluate(context.Background(), domain.Gene{targetAlpha, targetBeta}, plan)
	if err != nil {
		t.Fatalf("Evaluate err: %v", err)
	}
	if len(raw.Windows) != len(plan.Windows) {
		t.Fatalf("Windows len = %d, want %d", len(raw.Windows), len(plan.Windows))
	}
	for _, w := range raw.Windows {
		if w.Score.Fatal {
			t.Errorf("window %s fatal at optimum gene", w.Window)
		}
		if w.Score.Value == nil {
			t.Fatalf("window %s nil value", w.Window)
		}
		if math.Abs(*w.Score.Value) > 1e-12 {
			t.Errorf("window %s score = %g, want ~0 at optimum", w.Window, *w.Score.Value)
		}
	}
}

func TestToy_EvaluateRejectsInvalidGene(t *testing.T) {
	s := New()
	plan := miniPlan(t)
	if _, err := s.Evaluate(context.Background(), domain.Gene{1.5, 0}, plan); err == nil {
		t.Error("Evaluate accepted out-of-range gene")
	}
	if _, err := s.Evaluate(context.Background(), domain.Gene{0.5, 0}, nil); err == nil {
		t.Error("Evaluate accepted nil plan")
	}
}

func TestToy_EncodeDecodeRoundtrip(t *testing.T) {
	s := New()
	g := domain.Gene{0.42, -0.3}
	pkg, err := s.EncodeResult(g,
		resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeRandomOnce},
		resultpkg.ReproducibilityMetadata{
			SchemaVersion:      resultpkg.SchemaVersionV533,
			FitnessVersion:     resultpkg.FitnessVersionV1RawStd,
			FingerprintVersion: resultpkg.FingerprintVersionV1,
		},
		resultpkg.GAConfigSnapshot{StrategyID: s.StrategyID(), Pair: "BTCUSDT"},
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("EncodeResult err: %v", err)
	}
	if pkg.Core.StrategyID != StrategyID {
		t.Errorf("StrategyID = %q, want %q", pkg.Core.StrategyID, StrategyID)
	}
	if pkg.Core.ChampionGene.Encoding != resultpkg.GeneEncodingJSON {
		t.Errorf("Encoding = %q, want %q", pkg.Core.ChampionGene.Encoding, resultpkg.GeneEncodingJSON)
	}
	if pkg.Promote.DecisionStatus != resultpkg.DecisionStatusPending {
		t.Errorf("DecisionStatus = %q, want %q", pkg.Promote.DecisionStatus, resultpkg.DecisionStatusPending)
	}
	decoded, err := s.DecodeElite(pkg.Core.ChampionGene)
	if err != nil {
		t.Fatalf("DecodeElite err: %v", err)
	}
	if !geneEqual(g, decoded) {
		t.Errorf("round-trip mismatch: %v vs %v", g, decoded)
	}
}

func TestToy_DecodeEliteRejectsBadEncoding(t *testing.T) {
	s := New()
	_, err := s.DecodeElite(resultpkg.ChampionGenePayload{Encoding: "base64", Payload: []byte(`"abc"`)})
	if err == nil {
		t.Error("DecodeElite accepted unsupported encoding")
	}
}

func TestToyAdapter_BasicLifecycle(t *testing.T) {
	s := New()
	plan := miniPlan(t)
	a, err := s.NewAdapter(plan)
	if err != nil {
		t.Fatalf("NewAdapter err: %v", err)
	}
	defer func() {
		if err := a.Close(); err != nil {
			t.Errorf("Close err: %v", err)
		}
	}()
	if err := a.Reset(plan); err != nil {
		t.Fatalf("Reset err: %v", err)
	}
	raw, err := a.Evaluate(domain.Gene{0.5, 0})
	if err != nil {
		t.Fatalf("Evaluate err: %v", err)
	}
	if len(raw.Windows) != len(plan.Windows) {
		t.Errorf("Windows len = %d, want %d", len(raw.Windows), len(plan.Windows))
	}
}

func TestToy_EvaluateDeterministic(t *testing.T) {
	// Same (gene, plan) must produce identical Value for every window.
	// Phase-5A pre-implementation of TestEvaluateDeterministic from §10.1.
	s := New()
	plan := miniPlan(t)
	g := domain.Gene{0.3, -0.5}
	r1, err := s.Evaluate(context.Background(), g, plan)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.Evaluate(context.Background(), g, plan)
	if err != nil {
		t.Fatal(err)
	}
	for i := range r1.Windows {
		if *r1.Windows[i].Score.Value != *r2.Windows[i].Score.Value {
			t.Errorf("window %d nondeterministic: %g vs %g", i,
				*r1.Windows[i].Score.Value, *r2.Windows[i].Score.Value)
		}
	}
}

func miniPlan(t *testing.T) *domain.EvaluablePlan {
	t.Helper()
	windows := make([]domain.CrucibleWindow, 0, 4)
	for _, name := range resultpkg.AllWindowsInEvalOrder() {
		windows = append(windows, domain.CrucibleWindow{
			Name: name,
			Bars: []domain.Bar{{OpenTime: 1, Close: 100}},
		})
	}
	return &domain.EvaluablePlan{
		Pair:     "BTCUSDT",
		Spawn:    resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeRandomOnce},
		LotStep:  0.0001,
		LotMin:   0.001,
		FatalMDD: 0.5,
		Windows:  windows,
		Friction: domain.FrictionParams{TakerFeeBPS: 10, SlippageBPS: 5},
	}
}

func geneEqual(a, b domain.Gene) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
