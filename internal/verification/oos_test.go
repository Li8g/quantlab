package verification

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"strings"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/fitness"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
)

// ----- stub strategy & adapter -----
//
// stubOOSStrategy returns a deterministic CrucibleResult on every
// Evaluate call. The score / Fatal / err knobs let each test exercise
// one RunOOS branch in isolation.
type stubOOSStrategy struct {
	score         float64 // log-return for the single OOS window
	fatal         bool
	fatalReason   string
	evalErr       error
	newAdapterErr error
	resetErr      error
	resetCalls    int
	// RunStress knobs: stressReturns is the series the adapter attaches
	// to LongestWindowReturns, but ONLY when the plan asked for it —
	// gotCaptureReturns records the CaptureReturns flag Reset observed,
	// so tests can assert RunStress actually set it.
	stressReturns     []float64
	gotCaptureReturns bool
}

func (s *stubOOSStrategy) StrategyID() string                                           { return "stub_oos" }
func (s *stubOOSStrategy) Segments() []domain.SegmentInfo                               { return nil }
func (s *stubOOSStrategy) Sample(*rand.Rand) domain.Gene                                { return domain.Gene{0} }
func (s *stubOOSStrategy) Clamp(g domain.Gene) domain.Gene                              { return g }
func (s *stubOOSStrategy) Validate(domain.Gene) error                                   { return nil }
func (s *stubOOSStrategy) Fingerprint(domain.Gene) string                               { return "stub" }
func (s *stubOOSStrategy) Crossover(p1, _ domain.Gene, _ *rand.Rand) domain.Gene        { return p1 }
func (s *stubOOSStrategy) Mutate(g domain.Gene, _, _ float64, _ *rand.Rand) domain.Gene { return g }
func (s *stubOOSStrategy) MinEvalBars() int                                             { return 1 }
func (s *stubOOSStrategy) Evaluate(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.RawEvaluateResult, error) {
	return nil, errors.New("stubOOSStrategy.Evaluate not used by RunOOS path")
}
func (s *stubOOSStrategy) ReviewBacktest(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.ReviewSummary, error) {
	return nil, nil
}
func (s *stubOOSStrategy) DecodeElite(resultpkg.ChampionGenePayload) (domain.Gene, error) {
	return nil, errors.New("not used")
}
func (s *stubOOSStrategy) EncodeResult(
	_ domain.Gene, _ resultpkg.SpawnPointPayload, _ resultpkg.ReproducibilityMetadata,
	_ resultpkg.GAConfigSnapshot, _ *resultpkg.EvaluationLayer, _ *resultpkg.VerificationLayer,
	_ *resultpkg.DiagnosticsLayer,
) (resultpkg.ChallengerResultPackage, error) {
	return resultpkg.ChallengerResultPackage{}, errors.New("not used")
}
func (s *stubOOSStrategy) NewAdapter(_ *domain.EvaluablePlan) (strategy.Adapter, error) {
	if s.newAdapterErr != nil {
		return nil, s.newAdapterErr
	}
	return &stubOOSAdapter{parent: s}, nil
}

var _ strategy.EvolvableStrategy = (*stubOOSStrategy)(nil)

type stubOOSAdapter struct {
	parent *stubOOSStrategy
}

func (a *stubOOSAdapter) Reset(plan *domain.EvaluablePlan) error {
	a.parent.resetCalls++
	if plan != nil {
		a.parent.gotCaptureReturns = plan.CaptureReturns
	}
	return a.parent.resetErr
}
func (a *stubOOSAdapter) Close() error { return nil }
func (a *stubOOSAdapter) Evaluate(_ domain.Gene) (*resultpkg.RawEvaluateResult, error) {
	if a.parent.evalErr != nil {
		return nil, a.parent.evalErr
	}
	cr := resultpkg.CrucibleResult{
		Window:        resultpkg.Window6M,
		BarsEvaluated: 100,
	}
	if a.parent.fatal {
		reason := a.parent.fatalReason
		cr.Score = resultpkg.SliceScore{Fatal: true, Value: nil}
		cr.FatalReason = &reason
	} else {
		v := a.parent.score
		cr.Score = resultpkg.SliceScore{Fatal: false, Value: &v}
	}
	raw := &resultpkg.RawEvaluateResult{Windows: []resultpkg.CrucibleResult{cr}}
	if a.parent.gotCaptureReturns {
		raw.LongestWindowReturns = a.parent.stressReturns
	}
	return raw, nil
}

// ----- bar fixture helpers -----

// flatBars produces n daily bars with constant Close=100 starting at
// startMs. Suitable for DCA baseline determinism: zero return → DCA ROI
// equals the running cash drag from injections (zero with default cfg).
func flatBars(n int, startMs int64) []domain.Bar {
	const dayMs = int64(24 * 60 * 60 * 1000)
	out := make([]domain.Bar, n)
	for i := range out {
		out[i] = domain.Bar{
			OpenTime: startMs + int64(i)*dayMs,
			Open:     100, High: 100, Low: 100, Close: 100, Volume: 1,
		}
	}
	return out
}

// planWithOOS builds the minimum EvaluablePlan that RunOOS reads.
func planWithOOS(bars []domain.Bar) *domain.EvaluablePlan {
	return &domain.EvaluablePlan{
		Pair:        "BTCUSDT",
		LotStep:     0.00001,
		LotMin:      0.00001,
		FatalMDD:    0.5,
		InitialUSDT: 10_000,
		Windows:     nil,
		OosWindow: &domain.CrucibleWindow{
			Name:      resultpkg.WindowOOS,
			StartTS:   bars[0].OpenTime,
			EndTS:     bars[len(bars)-1].OpenTime,
			WarmupLen: 0,
			Bars:      bars,
		},
		Friction: domain.FrictionParams{TakerFeeBPS: 0, SlippageBPS: 0},
	}
}

func defaultDCA() fitness.GhostDCAConfig {
	return fitness.GhostDCAConfig{InitialCapital: 10_000, MonthlyInject: 0}
}

// ----- tests -----

func TestRunOOS_InsufficientData_NilOosWindow(t *testing.T) {
	plan := &domain.EvaluablePlan{Pair: "BTCUSDT"} // no OosWindow
	got, err := RunOOS(context.Background(), &stubOOSStrategy{}, plan, domain.Gene{0}, defaultDCA())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != resultpkg.VerificationStatusInsufficientData {
		t.Errorf("Status = %q, want insufficient_data", got.Status)
	}
	if got.DecisionColor != nil {
		t.Errorf("DecisionColor = %v, want nil (insufficient_data ⇒ gray on front-end)", got.DecisionColor)
	}
	if got.OOSAlphaMonthly != nil || got.OOSAlphaWeekly != nil {
		t.Error("alpha fields should be nil for insufficient_data")
	}
}

func TestRunOOS_InsufficientData_SpanTooShort(t *testing.T) {
	// 89 daily bars → 88-day span, below 90-day floor.
	bars := flatBars(89, 0)
	plan := planWithOOS(bars)
	got, err := RunOOS(context.Background(), &stubOOSStrategy{score: 0.1}, plan, domain.Gene{0}, defaultDCA())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != resultpkg.VerificationStatusInsufficientData {
		t.Errorf("Status = %q, want insufficient_data", got.Status)
	}
}

func TestRunOOS_FloorBoundary_ExactlyMinDays(t *testing.T) {
	// 91 daily bars → 90-day span, meets the floor → status=ok.
	bars := flatBars(91, 0)
	plan := planWithOOS(bars)
	// score=0 means strat return == 0 == DCA return (flat bars, zero
	// injection) → alpha=0 → yellow.
	got, err := RunOOS(context.Background(), &stubOOSStrategy{score: 0}, plan, domain.Gene{0}, defaultDCA())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != resultpkg.VerificationStatusOK {
		t.Errorf("Status = %q, want ok", got.Status)
	}
	if got.DecisionColor == nil || *got.DecisionColor != resultpkg.DecisionColorYellow {
		t.Errorf("DecisionColor = %v, want yellow", got.DecisionColor)
	}
}

func TestRunOOS_Fatal_RedAndFailed(t *testing.T) {
	bars := flatBars(180, 0)
	plan := planWithOOS(bars)
	stub := &stubOOSStrategy{fatal: true, fatalReason: "drawdown_0.55"}
	got, err := RunOOS(context.Background(), stub, plan, domain.Gene{0}, defaultDCA())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != resultpkg.VerificationStatusFailed {
		t.Errorf("Status = %q, want failed", got.Status)
	}
	if got.DecisionColor == nil || *got.DecisionColor != resultpkg.DecisionColorRed {
		t.Errorf("DecisionColor = %v, want red", got.DecisionColor)
	}
	if got.Notes == nil || *got.Notes == "" {
		t.Error("Notes should carry fatal reason")
	}
	if got.OOSAlphaMonthly != nil || got.OOSAlphaWeekly != nil {
		t.Error("alpha fields must remain nil when Fatal")
	}
}

func TestRunOOS_Green_StrongAlpha(t *testing.T) {
	// 365 daily bars ≈ 1 year. score=log(1.20) ⇒ strat_ann = +20%.
	// DCA on flat bars with zero injection ⇒ ROI=0 ⇒ dca_ann=0.
	// alpha_monthly = +20% ≥ +5% AND alpha_weekly = +20% ≥ 0 ⇒ green.
	bars := flatBars(365, 0)
	plan := planWithOOS(bars)
	stub := &stubOOSStrategy{score: math.Log(1.20)}
	got, err := RunOOS(context.Background(), stub, plan, domain.Gene{0}, defaultDCA())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != resultpkg.VerificationStatusOK {
		t.Fatalf("Status = %q, want ok", got.Status)
	}
	if got.DecisionColor == nil || *got.DecisionColor != resultpkg.DecisionColorGreen {
		t.Errorf("DecisionColor = %v, want green", got.DecisionColor)
	}
	if got.OOSAlphaMonthly == nil || math.Abs(*got.OOSAlphaMonthly-0.20) > 0.01 {
		t.Errorf("alpha_monthly_ann = %v, want ≈0.20", got.OOSAlphaMonthly)
	}
}

func TestRunOOS_Red_StrongUnderperformance(t *testing.T) {
	// score=log(0.90) ⇒ strat_ann ≈ -10% ⇒ alpha_monthly_ann ≈ -10%
	// ≤ -3% ⇒ red.
	bars := flatBars(365, 0)
	plan := planWithOOS(bars)
	stub := &stubOOSStrategy{score: math.Log(0.90)}
	got, err := RunOOS(context.Background(), stub, plan, domain.Gene{0}, defaultDCA())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != resultpkg.VerificationStatusOK {
		t.Fatalf("Status = %q, want ok", got.Status)
	}
	if got.DecisionColor == nil || *got.DecisionColor != resultpkg.DecisionColorRed {
		t.Errorf("DecisionColor = %v, want red", got.DecisionColor)
	}
}

func TestRunOOS_Yellow_DefaultPool(t *testing.T) {
	// score=log(1.02) ⇒ strat_ann = +2%, dca=0 ⇒ alpha=+2% ∈ (-3%, +5%) ⇒ yellow.
	bars := flatBars(365, 0)
	plan := planWithOOS(bars)
	stub := &stubOOSStrategy{score: math.Log(1.02)}
	got, err := RunOOS(context.Background(), stub, plan, domain.Gene{0}, defaultDCA())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.DecisionColor == nil || *got.DecisionColor != resultpkg.DecisionColorYellow {
		t.Errorf("DecisionColor = %v, want yellow", got.DecisionColor)
	}
}

func TestClassifyOOSColor_BoundaryConditions(t *testing.T) {
	cases := []struct {
		name       string
		alphaMonth float64
		alphaWeek  float64
		want       resultpkg.DecisionColor
	}{
		// Green requires BOTH conditions.
		{"green: just above both", +0.05, 0.0, resultpkg.DecisionColorGreen},
		{"green: monthly ok, weekly fail", +0.05, -0.01, resultpkg.DecisionColorYellow},
		{"green: monthly fail, weekly ok", +0.04, +0.10, resultpkg.DecisionColorYellow},
		// Red triggers on monthly alone.
		{"red: exactly at floor", -0.03, +0.10, resultpkg.DecisionColorRed},
		{"red: well below", -0.10, -0.10, resultpkg.DecisionColorRed},
		// Yellow boundaries.
		{"yellow: zero alpha", 0, 0, resultpkg.DecisionColorYellow},
		{"yellow: just above red floor", -0.029, 0, resultpkg.DecisionColorYellow},
		{"yellow: just below green threshold", +0.049, 0, resultpkg.DecisionColorYellow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyOOSColor(c.alphaMonth, c.alphaWeek)
			if got != c.want {
				t.Errorf("classify(%v, %v) = %q, want %q", c.alphaMonth, c.alphaWeek, got, c.want)
			}
		})
	}
}

func TestAnnualizeROI(t *testing.T) {
	cases := []struct {
		roi   float64
		years float64
		want  float64
	}{
		{0, 1, 0},
		{0.20, 1, 0.20},            // 1y +20% total ⇒ +20% ann
		{1.0, 2, math.Sqrt(2) - 1}, // 100% over 2y ⇒ (1+1)^(1/2)-1 ≈ 0.414
		{0.10, 0.5, 0.21},          // 6 months +10% ⇒ ann ≈ 21%
		{-0.99, 1, -0.99},          // near-total loss in 1y
		{-1.5, 1, -1},              // 1+roi ≤ 0 guard ⇒ clamps to -1
	}
	for _, c := range cases {
		got := annualizeROI(c.roi, c.years)
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("annualizeROI(%v, %v) = %v, want ≈%v", c.roi, c.years, got, c.want)
		}
	}
}

func TestRunOOS_AdapterDiscipline_ResetCalled(t *testing.T) {
	bars := flatBars(180, 0)
	plan := planWithOOS(bars)
	stub := &stubOOSStrategy{score: 0}
	_, err := RunOOS(context.Background(), stub, plan, domain.Gene{0}, defaultDCA())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if stub.resetCalls != 1 {
		t.Errorf("Reset called %d times, want 1 (§5.6 isolation contract)", stub.resetCalls)
	}
}

func TestRunOOS_WithWarmupPrefix_ScoresFromEvalStart(t *testing.T) {
	// 280 daily bars: first 100 are "warmup", last 180 are eval.
	// The stub strategy returns a fixed score regardless, but RunOOS
	// must (a) measure span from EVAL bars only — 180-day span passes
	// the 90-day floor; (b) run DCA on EVAL bars only — confirmed by
	// alpha sign matching a 1y annualization of the score.
	allBars := flatBars(280, 0)
	plan := &domain.EvaluablePlan{
		Pair:        "BTCUSDT",
		LotStep:     0.00001,
		LotMin:      0.00001,
		FatalMDD:    0.5,
		InitialUSDT: 10_000,
		OosWindow: &domain.CrucibleWindow{
			Name:      resultpkg.WindowOOS,
			StartTS:   allBars[100].OpenTime,
			EndTS:     allBars[len(allBars)-1].OpenTime,
			WarmupLen: 100,
			Bars:      allBars,
		},
		Friction: domain.FrictionParams{},
	}
	stub := &stubOOSStrategy{score: math.Log(1.10)} // +10% over eval period
	got, err := RunOOS(context.Background(), stub, plan, domain.Gene{0}, defaultDCA())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != resultpkg.VerificationStatusOK {
		t.Fatalf("Status=%q, want ok; notes=%v", got.Status, got.Notes)
	}
	// 180 eval days ≈ 0.493 years; (1+0.10)^(1/0.493)-1 ≈ 0.21.
	if got.OOSAlphaMonthly == nil || math.Abs(*got.OOSAlphaMonthly-0.21) > 0.02 {
		t.Errorf("alpha_monthly_ann = %v, want ≈0.21 (180-day +10%% annualized)",
			got.OOSAlphaMonthly)
	}
	// Notes must report the warmup_len, not "warmup_len=0".
	if got.Notes == nil || !strings.Contains(*got.Notes, "warmup_len=100") {
		t.Errorf("Notes does not mention warmup_len=100; got %v", got.Notes)
	}
}

func TestRunOOS_InvalidWarmupLen(t *testing.T) {
	// WarmupLen ≥ len(Bars) → invariant violation, returns Go error.
	bars := flatBars(100, 0)
	plan := &domain.EvaluablePlan{
		OosWindow: &domain.CrucibleWindow{
			Name:      resultpkg.WindowOOS,
			WarmupLen: 100, // == len(Bars), no eval bars left
			Bars:      bars,
		},
	}
	_, err := RunOOS(context.Background(), &stubOOSStrategy{}, plan, domain.Gene{0}, defaultDCA())
	if err == nil {
		t.Fatal("expected error for WarmupLen >= len(Bars), got nil")
	}
}

func TestRunOOS_NewAdapterError(t *testing.T) {
	bars := flatBars(180, 0)
	plan := planWithOOS(bars)
	stub := &stubOOSStrategy{newAdapterErr: errors.New("boom")}
	_, err := RunOOS(context.Background(), stub, plan, domain.Gene{0}, defaultDCA())
	if err == nil || err.Error() == "" {
		t.Fatalf("expected error from NewAdapter, got %v", err)
	}
}

func TestMarshalOOSPayload_NilSafe(t *testing.T) {
	b, err := MarshalOOSPayload(nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if b != nil {
		t.Errorf("payload = %v, want nil", b)
	}
}

func TestMarshalOOSPayload_RoundTrip(t *testing.T) {
	alpha := 0.07
	color := resultpkg.DecisionColorGreen
	notes := "test"
	src := &resultpkg.OOSResult{
		Status:          resultpkg.VerificationStatusOK,
		OOSAlphaMonthly: &alpha,
		DecisionColor:   &color,
		Notes:           &notes,
	}
	b, err := MarshalOOSPayload(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got resultpkg.OOSResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != resultpkg.VerificationStatusOK {
		t.Errorf("Status mismatch: %v", got.Status)
	}
	if got.OOSAlphaMonthly == nil || *got.OOSAlphaMonthly != 0.07 {
		t.Errorf("OOSAlphaMonthly = %v, want 0.07", got.OOSAlphaMonthly)
	}
}
