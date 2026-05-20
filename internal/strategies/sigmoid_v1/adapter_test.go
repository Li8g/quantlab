package sigmoid_v1

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
)

// fourWindowPlan returns an EvaluablePlan with all four canonical
// windows populated using the supplied bar series. Same bars in
// every window — the test asserts cascade behaviour, not per-window
// numerical realism.
func fourWindowPlan(bars []domain.Bar, warmup int, friction domain.FrictionParams) *domain.EvaluablePlan {
	mk := func(name resultpkg.WindowName) domain.CrucibleWindow {
		return domain.CrucibleWindow{
			Name:      name,
			StartTS:   bars[0].OpenTime,
			EndTS:     bars[len(bars)-1].OpenTime,
			WarmupLen: warmup,
			Bars:      bars,
		}
	}
	return &domain.EvaluablePlan{
		Pair: "BTCUSDT",
		Windows: []domain.CrucibleWindow{
			mk(resultpkg.Window6M),
			mk(resultpkg.Window2Y),
			mk(resultpkg.Window5Y),
			mk(resultpkg.Window10Y),
		},
		FatalMDD:    0.5,
		InitialUSDT: 10_000,
		Friction:    friction,
	}
}

// fatalPlan builds a plan where the 6m window's bar series ends in a
// 99.5% price crash that triggers evaluateWindow's Fatal path.
// Subsequent windows use flat bars so they would normally score
// non-Fatal — proving the cascade-skip via SkippedBy enums.
func fatalPlan() *domain.EvaluablePlan {
	c := defaultChromosome()
	c.MacroInjectUSD = 12_000
	_ = c // gene fixture is built per-test via stepTestGene or below

	// 6m window: 20 flat + 60 ramp down to $0.5 → Fatal.
	pre := flatBars(20, 100, windowTestRefMs)
	post := rampBars(60, 100, 0.5, pre[len(pre)-1].OpenTime+barIntervalDays)
	fatalBars := append(pre, post...)

	// Other windows: 80 flat (no Fatal possible).
	flat := flatBars(80, 100, windowTestRefMs)

	mk := func(name resultpkg.WindowName, b []domain.Bar) domain.CrucibleWindow {
		return domain.CrucibleWindow{
			Name: name, WarmupLen: 5, Bars: b,
			StartTS: b[0].OpenTime, EndTS: b[len(b)-1].OpenTime,
		}
	}
	return &domain.EvaluablePlan{
		Pair: "BTCUSDT",
		Windows: []domain.CrucibleWindow{
			mk(resultpkg.Window6M, fatalBars),
			mk(resultpkg.Window2Y, flat),
			mk(resultpkg.Window5Y, flat),
			mk(resultpkg.Window10Y, flat),
		},
		FatalMDD:    0.5,
		InitialUSDT: 10_000,
	}
}

// fatalGene returns a Chromosome encoded gene where macroInjectUSD =
// 12_000 (out-of-bounds but Step()-accepted) so the crash scenario
// in fatalPlan actually generates enough BTC exposure to wipe NAV by
// 50%+.
func fatalGene() domain.Gene {
	c := defaultChromosome()
	c.EMALongPeriod = 10
	c.EMAShortPeriod = 5
	c.MAVLongPeriod = 8
	c.MAVShortPeriod = 4
	c.MacroInjectUSD = 12_000
	c.MicroReservePct = 0.05
	return EncodeChromosome(c)
}

// ----- TestEvaluateDeterministic -----

func TestEvaluate_DeterministicAcrossCalls(t *testing.T) {
	s := windowTestSigmoid()
	gene := stepTestGene()
	bars := flatBars(80, 100, windowTestRefMs)
	plan := fourWindowPlan(bars, 5, domain.FrictionParams{TakerFeeBPS: 5, SlippageBPS: 2})

	r1, err := s.Evaluate(context.Background(), gene, plan)
	if err != nil {
		t.Fatalf("first Evaluate: %v", err)
	}
	r2, err := s.Evaluate(context.Background(), gene, plan)
	if err != nil {
		t.Fatalf("second Evaluate: %v", err)
	}
	j1, _ := json.Marshal(r1)
	j2, _ := json.Marshal(r2)
	if !bytes.Equal(j1, j2) {
		t.Errorf("non-deterministic RawEvaluateResult\n  r1=%s\n  r2=%s", j1, j2)
	}
}

// ----- TestEvaluateOrderInvariance -----

func TestEvaluate_PlanWindowOrderInvariance(t *testing.T) {
	// The cascade always iterates resultpkg.AllWindowsInEvalOrder().
	// Permuting plan.Windows must produce byte-equal results.
	s := windowTestSigmoid()
	gene := stepTestGene()
	bars := flatBars(80, 100, windowTestRefMs)

	canonical := fourWindowPlan(bars, 5, domain.FrictionParams{})
	reverse := fourWindowPlan(bars, 5, domain.FrictionParams{})
	// Reverse the windows slice (10y, 5y, 2y, 6m).
	for i, j := 0, len(reverse.Windows)-1; i < j; i, j = i+1, j-1 {
		reverse.Windows[i], reverse.Windows[j] = reverse.Windows[j], reverse.Windows[i]
	}

	rCan, err := s.Evaluate(context.Background(), gene, canonical)
	if err != nil {
		t.Fatalf("canonical-order Evaluate: %v", err)
	}
	rRev, err := s.Evaluate(context.Background(), gene, reverse)
	if err != nil {
		t.Fatalf("reverse-order Evaluate: %v", err)
	}
	j1, _ := json.Marshal(rCan)
	j2, _ := json.Marshal(rRev)
	if !bytes.Equal(j1, j2) {
		t.Errorf("order-dependent RawEvaluateResult\n  canonical=%s\n  reverse=%s", j1, j2)
	}
}

// ----- TestAdapterResetIsolation -----

func TestAdapterResetIsolation(t *testing.T) {
	// Same Adapter, three Evaluate calls. The first and third use
	// gene A with the same plan; the second uses gene B. After the
	// detour through gene B, gene A's RawEvaluateResult must be
	// byte-identical to the first call — proving no per-gene state
	// leaked between Reset/Evaluate cycles.
	s := windowTestSigmoid()
	geneA := stepTestGene()
	cB := defaultChromosome()
	cB.EMALongPeriod = 10
	cB.EMAShortPeriod = 5
	cB.MAVLongPeriod = 8
	cB.MAVShortPeriod = 4
	cB.A1 = 0.5 // diverge from A
	geneB := EncodeChromosome(cB)

	bars := flatBars(80, 100, windowTestRefMs)
	plan := fourWindowPlan(bars, 5, domain.FrictionParams{})

	a, err := s.NewAdapter(plan)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	if err := a.Reset(plan); err != nil {
		t.Fatalf("Reset #1: %v", err)
	}
	r1, err := a.Evaluate(geneA)
	if err != nil {
		t.Fatalf("Evaluate A #1: %v", err)
	}
	if err := a.Reset(plan); err != nil {
		t.Fatalf("Reset #2: %v", err)
	}
	if _, err := a.Evaluate(geneB); err != nil {
		t.Fatalf("Evaluate B: %v", err)
	}
	if err := a.Reset(plan); err != nil {
		t.Fatalf("Reset #3: %v", err)
	}
	r3, err := a.Evaluate(geneA)
	if err != nil {
		t.Fatalf("Evaluate A #3: %v", err)
	}

	j1, _ := json.Marshal(r1)
	j3, _ := json.Marshal(r3)
	if !bytes.Equal(j1, j3) {
		t.Errorf("Reset isolation broken: A #1 != A #3\n  #1=%s\n  #3=%s", j1, j3)
	}
}

func TestAdapter_EvaluateWithoutResetErrors(t *testing.T) {
	// Defensive contract: Adapter built with nil plan can't Evaluate.
	// NewAdapter with a non-nil plan covers the happy path; this
	// asserts the explicit "you must Reset first" guard for the
	// future case where engine constructs an empty Adapter.
	s := windowTestSigmoid()
	a := &sigmoidAdapter{strat: s, plan: nil}
	if _, err := a.Evaluate(stepTestGene()); err == nil {
		t.Error("nil plan: want error, got nil")
	}
}

// ----- TestCascadeShortCircuit -----

func TestEvaluate_CascadeShortCircuitFrom6M(t *testing.T) {
	s := windowTestSigmoid()
	plan := fatalPlan()
	gene := fatalGene()

	res, err := s.Evaluate(context.Background(), gene, plan)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res.Windows) != 4 {
		t.Fatalf("len(Windows) = %d, want 4", len(res.Windows))
	}

	// Window[0] = 6m → must be Fatal.
	w6m := res.Windows[0]
	if w6m.Window != resultpkg.Window6M {
		t.Errorf("Windows[0] = %q, want 6m (canonical order)", w6m.Window)
	}
	if !w6m.Score.Fatal {
		t.Errorf("6m window: Fatal=false, want true (%+v)", w6m)
	}

	// Window[1..3] = 2y, 5y, 10y → cascade-skipped with the same
	// SkippedBy = SkippedByCascadeFrom6M.
	for i, want := range []resultpkg.WindowName{resultpkg.Window2Y, resultpkg.Window5Y, resultpkg.Window10Y} {
		w := res.Windows[i+1]
		if w.Window != want {
			t.Errorf("Windows[%d] = %q, want %q", i+1, w.Window, want)
		}
		if w.Score.Fatal {
			t.Errorf("%q: Fatal=true, want false (cascade-skip semantics)", want)
		}
		if w.Score.Value != nil {
			t.Errorf("%q: Value=%v, want nil (cascade-skip)", want, *w.Score.Value)
		}
		if w.SkippedBy == nil {
			t.Errorf("%q: SkippedBy=nil, want non-nil", want)
			continue
		}
		if *w.SkippedBy != resultpkg.SkippedByCascadeFrom6M {
			t.Errorf("%q: SkippedBy=%q, want %q", want, *w.SkippedBy, resultpkg.SkippedByCascadeFrom6M)
		}
	}
}

func TestEvaluate_FrictionActualReflectsPlan(t *testing.T) {
	s := windowTestSigmoid()
	bars := flatBars(80, 100, windowTestRefMs)
	fp := domain.FrictionParams{TakerFeeBPS: 7.5, SlippageBPS: 3.5}
	plan := fourWindowPlan(bars, 5, fp)

	res, err := s.Evaluate(context.Background(), stepTestGene(), plan)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.FrictionActual.TakerFeeBPS != 7.5 || res.FrictionActual.SlippageBPS != 3.5 {
		t.Errorf("FrictionActual = %+v, want {7.5, 3.5}", res.FrictionActual)
	}
}

func TestEvaluate_BarsEvaluatedAccumulates(t *testing.T) {
	s := windowTestSigmoid()
	bars := flatBars(80, 100, windowTestRefMs)
	plan := fourWindowPlan(bars, 5, domain.FrictionParams{})
	res, err := s.Evaluate(context.Background(), stepTestGene(), plan)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// 4 windows × (80 - 5) = 300 scored bars. Skipped windows
	// contribute 0 to BarsEvaluated. Here no window is skipped so
	// total is the full sum.
	if res.BarsEvaluated != 4*(80-5) {
		t.Errorf("BarsEvaluated = %d, want %d", res.BarsEvaluated, 4*(80-5))
	}
}

// TestEvaluate_LongestWindowStatsFromLastNonFatal pins the §I-4.2
// invariant that LongestWindowStats reflects the longest non-Fatal
// window's return series. With four equal-length windows evaluated
// in 6m→2y→5y→10y order, the 10y window (last in canonical order)
// is the producer.
func TestEvaluate_LongestWindowStatsFromLastNonFatal(t *testing.T) {
	s := windowTestSigmoid()
	bars := flatBars(80, 100, windowTestRefMs)
	plan := fourWindowPlan(bars, 5, domain.FrictionParams{})

	res, err := s.Evaluate(context.Background(), stepTestGene(), plan)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.LongestWindowStats == nil {
		t.Fatal("LongestWindowStats=nil, want populated (all 4 windows non-Fatal)")
	}
	// HorizonT equals scored bars of the longest non-Fatal window
	// (the 10y window in canonical order). Same bars in every window
	// here, so it's bars-warmup.
	wantHorizonT := len(bars) - 5
	if res.LongestWindowStats.HorizonT != wantHorizonT {
		t.Errorf("LongestWindowStats.HorizonT=%d, want %d",
			res.LongestWindowStats.HorizonT, wantHorizonT)
	}
}

// TestEvaluate_LongestWindowStatsNilOnFirstWindowFatal pins the
// degenerate case: 6m goes Fatal so no window completes. Stats
// stays nil — the SaaS layer skips SharpeBank for this challenger.
func TestEvaluate_LongestWindowStatsNilOnFirstWindowFatal(t *testing.T) {
	s := windowTestSigmoid()
	plan := fatalPlan()
	gene := fatalGene()

	res, err := s.Evaluate(context.Background(), gene, plan)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !res.Windows[0].Score.Fatal {
		t.Fatalf("precondition: 6m must be Fatal, got %+v", res.Windows[0])
	}
	if res.LongestWindowStats != nil {
		t.Errorf("LongestWindowStats=%+v, want nil (all windows Fatal-or-skipped)",
			*res.LongestWindowStats)
	}
}
