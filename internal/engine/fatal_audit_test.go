package engine_test

import (
	"context"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/engine"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
	"quantlab/internal/strategies/toy"
)

// fatalToy wraps toy.Toy but forces every Evaluate to return a Fatal
// cascade. Used to drive 100% Fatal populations through RunEpoch so
// the fatal-audit sampler exercises its only code path. Delegates GA
// verbs (Sample/Mutate/etc.) to toy.Toy verbatim — only Evaluate
// changes.
type fatalToy struct{ inner *toy.Toy }

func (f *fatalToy) StrategyID() string             { return "fatal-toy" }
func (f *fatalToy) Segments() []domain.SegmentInfo { return f.inner.Segments() }
func (f *fatalToy) Sample(r *rand.Rand) domain.Gene {
	return f.inner.Sample(r)
}
func (f *fatalToy) Clamp(g domain.Gene) domain.Gene  { return f.inner.Clamp(g) }
func (f *fatalToy) Validate(g domain.Gene) error     { return f.inner.Validate(g) }
func (f *fatalToy) Fingerprint(g domain.Gene) string { return f.inner.Fingerprint(g) }
func (f *fatalToy) Crossover(p1, p2 domain.Gene, r *rand.Rand) domain.Gene {
	return f.inner.Crossover(p1, p2, r)
}
func (f *fatalToy) Mutate(g domain.Gene, p, s float64, r *rand.Rand) domain.Gene {
	return f.inner.Mutate(g, p, s, r)
}
func (f *fatalToy) MinEvalBars() int { return f.inner.MinEvalBars() }
func (f *fatalToy) ReviewBacktest(
	ctx context.Context, g domain.Gene, p *domain.EvaluablePlan,
) (*resultpkg.ReviewSummary, error) {
	return f.inner.ReviewBacktest(ctx, g, p)
}
func (f *fatalToy) DecodeElite(b resultpkg.ChampionGenePayload) (domain.Gene, error) {
	return f.inner.DecodeElite(b)
}
func (f *fatalToy) EncodeResult(
	g domain.Gene, sp resultpkg.SpawnPointPayload, repro resultpkg.ReproducibilityMetadata,
	ga resultpkg.GAConfigSnapshot, ev *resultpkg.EvaluationLayer,
	vr *resultpkg.VerificationLayer, dg *resultpkg.DiagnosticsLayer,
) (resultpkg.ChallengerResultPackage, error) {
	return f.inner.EncodeResult(g, sp, repro, ga, ev, vr, dg)
}
func (f *fatalToy) NewAdapter(_ *domain.EvaluablePlan) (strategy.Adapter, error) {
	return &fatalToyAdapter{}, nil
}
func (f *fatalToy) Evaluate(
	_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan,
) (*resultpkg.RawEvaluateResult, error) {
	return fatalCascade(), nil
}

type fatalToyAdapter struct{}

func (a *fatalToyAdapter) Reset(_ *domain.EvaluablePlan) error { return nil }
func (a *fatalToyAdapter) Evaluate(_ domain.Gene) (*resultpkg.RawEvaluateResult, error) {
	return fatalCascade(), nil
}
func (a *fatalToyAdapter) Close() error { return nil }

func fatalCascade() *resultpkg.RawEvaluateResult {
	reason := "test-forced-fatal"
	ts := int64(1)
	mdd := 0.6
	windows := []resultpkg.CrucibleResult{
		{
			Window:        resultpkg.Window6M,
			Score:         resultpkg.SliceScore{Fatal: true, Value: nil},
			FatalReason:   &reason,
			FatalAtBarTS:  &ts,
			FatalMDDValue: &mdd,
			BarsEvaluated: 1,
		},
	}
	for _, name := range []resultpkg.WindowName{resultpkg.Window2Y, resultpkg.Window5Y, resultpkg.Window10Y} {
		skip := resultpkg.SkippedByCascadeFrom6M
		windows = append(windows, resultpkg.CrucibleResult{
			Window:    name,
			Score:     resultpkg.SliceScore{Fatal: false, Value: nil},
			SkippedBy: &skip,
		})
	}
	return &resultpkg.RawEvaluateResult{
		Windows:        windows,
		FrictionActual: resultpkg.FrictionActual{},
		BarsEvaluated:  1,
	}
}

func TestFatalAudit_RateZeroProducesNoSamples(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 8
	cfg.MaxGenerations = 2
	cfg.EpochSeed = 11
	cfg.FatalAuditSampleRate = 0

	eng := engine.New(&fatalToy{inner: toy.New()}, cfg)
	result, err := eng.RunEpoch(context.Background(), miniPlan())
	if err != nil {
		t.Fatalf("RunEpoch: %v", err)
	}
	if len(result.FatalAuditSamples) != 0 {
		t.Errorf("rate=0 produced %d samples, want 0", len(result.FatalAuditSamples))
	}
}

func TestFatalAudit_RateOneSamplesEveryFatalGene(t *testing.T) {
	// With FatalAuditSampleRate=1.0 every Fatal candidate is sampled.
	// Every fatalToy evaluation is Fatal, so total samples =
	// PopSize × number-of-evaluatePopulation-calls = PopSize ×
	// MaxGenerations (since early-stop has nothing to detect on a
	// flat-Fatal landscape; the loop runs to MaxGen-1 + the initial
	// eval, which the engine collapses to MaxGen total calls).
	cfg := engine.DefaultConfig()
	cfg.PopSize = 6
	cfg.MaxGenerations = 3
	cfg.EpochSeed = 13
	cfg.FatalAuditSampleRate = 1.0
	cfg.EarlyStopPatience = 0 // disable early-stop so the loop runs full

	eng := engine.New(&fatalToy{inner: toy.New()}, cfg)
	result, err := eng.RunEpoch(context.Background(), miniPlan())
	if err != nil {
		t.Fatalf("RunEpoch: %v", err)
	}
	want := cfg.PopSize * cfg.MaxGenerations
	if len(result.FatalAuditSamples) != want {
		t.Errorf("rate=1.0 with PopSize=%d MaxGen=%d: got %d samples, want %d",
			cfg.PopSize, cfg.MaxGenerations, len(result.FatalAuditSamples), want)
	}

	// SampleID format check: "g{evalCallNum}.{fingerprint}". The
	// initial population is evalCallNum=0; per-gen rebuilds are 1..N.
	seenPrefixes := make(map[string]bool)
	for _, s := range result.FatalAuditSamples {
		if !strings.HasPrefix(s.SampleID, "g") {
			t.Errorf("SampleID %q missing 'g' prefix", s.SampleID)
			continue
		}
		dot := strings.Index(s.SampleID, ".")
		if dot < 2 {
			t.Errorf("SampleID %q missing generation/fingerprint separator", s.SampleID)
			continue
		}
		seenPrefixes[s.SampleID[:dot]] = true
		if !s.ScoreTotal.Fatal {
			t.Errorf("sample %q: ScoreTotal.Fatal=false, want true", s.SampleID)
		}
		if len(s.WindowScores) != 4 {
			t.Errorf("sample %q: %d windows, want 4", s.SampleID, len(s.WindowScores))
		}
	}
	if got := len(seenPrefixes); got != cfg.MaxGenerations {
		t.Errorf("seen generation prefixes = %d, want %d (one per evaluatePopulation call)",
			got, cfg.MaxGenerations)
	}
}

func TestFatalAudit_DeterministicAcrossRuns(t *testing.T) {
	// Same EpochSeed + FatalAuditSampleRate → identical sample lists.
	// The sampleRng is seeded from masterRng.Int63() on the main
	// goroutine, and the sampler runs in idx-order on the main
	// goroutine — neither path is affected by worker scheduling.
	cfg := engine.DefaultConfig()
	cfg.PopSize = 10
	cfg.MaxGenerations = 3
	cfg.EpochSeed = 23
	cfg.FatalAuditSampleRate = 0.5
	cfg.EarlyStopPatience = 0

	run := func() []resultpkg.AuditSampleSummary {
		eng := engine.New(&fatalToy{inner: toy.New()}, cfg)
		r, err := eng.RunEpoch(context.Background(), miniPlan())
		if err != nil {
			t.Fatalf("RunEpoch: %v", err)
		}
		return r.FatalAuditSamples
	}
	a := run()
	b := run()
	if len(a) != len(b) {
		t.Fatalf("sample count drift: %d vs %d", len(a), len(b))
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("non-deterministic FatalAuditSamples:\n  a=%s\n  b=%s", ja, jb)
	}
}

// TestFatalAudit_BuildContextPropagatesToDiagnostics verifies the
// engine.BuildContext → DiagnosticsLayer edge that the SaaS Epoch
// service uses. Same shape as TestBuildChallengerPackage_DSRSummary;
// non-empty input lands on diag.FatalAuditSamples, empty leaves it
// unset.
func TestFatalAudit_BuildContextPropagatesToDiagnostics(t *testing.T) {
	score := 1.0
	raw := &resultpkg.RawEvaluateResult{
		Windows:        []resultpkg.CrucibleResult{{Window: resultpkg.Window6M, Score: resultpkg.SliceScore{Value: &score}}},
		FrictionActual: resultpkg.FrictionActual{},
	}
	st := resultpkg.ScoreTotal{Value: &score}
	plan := &domain.EvaluablePlan{FatalMDD: 0.5, Friction: domain.FrictionParams{}}

	samples := []resultpkg.AuditSampleSummary{{
		SampleID:   "g0.test-fp",
		ScoreTotal: resultpkg.ScoreTotal{Fatal: true},
	}}
	bc := engine.BuildContext{Pair: "BTCUSDT", FatalAuditSamples: samples}

	pkg, err := engine.BuildChallengerPackage(
		toy.New(), plan, domain.Gene{0, 0}, raw, st, engine.DefaultConfig(), bc,
	)
	if err != nil {
		t.Fatalf("BuildChallengerPackage: %v", err)
	}
	if len(pkg.Diagnostics.FatalAuditSamples) != 1 ||
		pkg.Diagnostics.FatalAuditSamples[0].SampleID != "g0.test-fp" {
		t.Errorf("FatalAuditSamples did not propagate: %+v", pkg.Diagnostics.FatalAuditSamples)
	}

	bc.FatalAuditSamples = nil
	pkg2, err := engine.BuildChallengerPackage(
		toy.New(), plan, domain.Gene{0, 0}, raw, st, engine.DefaultConfig(), bc,
	)
	if err != nil {
		t.Fatalf("BuildChallengerPackage (empty samples): %v", err)
	}
	if len(pkg2.Diagnostics.FatalAuditSamples) != 0 {
		t.Errorf("empty samples should leave field unset, got %d entries",
			len(pkg2.Diagnostics.FatalAuditSamples))
	}
}

