package sigmoid_v1

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
	"time"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
)

// barIntervalDays = 1 day; used as the per-bar spacing for windows.
// Chosen so monthly macro triggers fire on a manageable bar count.
const barIntervalDays = int64(24) * 60 * 60 * 1000

// flatBars builds n consecutive 1d bars all at the same price,
// starting at start. Each bar's OpenTime increments by 1 day.
func flatBars(n int, price float64, start int64) []domain.Bar {
	out := make([]domain.Bar, n)
	for i := range out {
		out[i] = domain.Bar{
			OpenTime: start + int64(i)*barIntervalDays,
			Open:     price,
			High:     price,
			Low:      price,
			Close:    price,
			Volume:   1,
		}
	}
	return out
}

// rampBars builds n bars that linearly transition from startPrice
// to endPrice. Useful for engineering MDD scenarios.
func rampBars(n int, startPrice, endPrice float64, start int64) []domain.Bar {
	out := make([]domain.Bar, n)
	step := (endPrice - startPrice) / float64(n-1)
	for i := range out {
		p := startPrice + step*float64(i)
		out[i] = domain.Bar{
			OpenTime: start + int64(i)*barIntervalDays,
			Open:     p,
			High:     p,
			Low:      p,
			Close:    p,
			Volume:   1,
		}
	}
	return out
}

// windowTestRefMs anchors all evaluate_window fixtures at 2024-01-01
// UTC so monthly macro triggers in the strategy are predictable.
var windowTestRefMs = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

func windowTestSigmoid() *Sigmoid { return New(barIntervalDays) }

func TestEvaluateWindow_FlatPriceProducesNonFatalNearZeroScore(t *testing.T) {
	s := windowTestSigmoid()
	gene := stepTestGene()
	bars := flatBars(80, 100, windowTestRefMs)
	w := domain.CrucibleWindow{
		Name:      resultpkg.Window6M,
		StartTS:   bars[0].OpenTime,
		EndTS:     bars[len(bars)-1].OpenTime,
		WarmupLen: 20,
		Bars:      bars,
	}
	res, stats, err := evaluateWindow(s, gene, w, domain.FrictionParams{})
	if err != nil {
		t.Fatalf("evaluateWindow: %v", err)
	}
	if res.Score.Fatal {
		t.Fatalf("flat price: Fatal=true, want false (res=%+v)", res)
	}
	if res.Score.Value == nil {
		t.Fatal("flat price: Value=nil, want non-nil")
	}
	// Flat price + no friction → no trading profit/loss. Score should
	// be ~0; loose tolerance because micro orders may fire small amounts
	// driven by signal noise on the centred volRatio = 0.
	if math.Abs(*res.Score.Value) > 0.05 {
		t.Errorf("flat price: |score|=%v, want ~0", *res.Score.Value)
	}
	if res.BarsEvaluated != len(bars)-w.WarmupLen {
		t.Errorf("BarsEvaluated=%d, want %d (len - warmup)",
			res.BarsEvaluated, len(bars)-w.WarmupLen)
	}
	if stats == nil {
		t.Error("non-Fatal evaluation: stats=nil, want SharpeStats populated")
	} else if stats.HorizonT != res.BarsEvaluated {
		t.Errorf("stats.HorizonT=%d, want BarsEvaluated=%d", stats.HorizonT, res.BarsEvaluated)
	}
}

func TestEvaluateWindow_FatalOnDeepDrawdown(t *testing.T) {
	s := windowTestSigmoid()
	// Craft a gene that DCAs aggressively into BTC, then crash the
	// price so the resulting NAV drops > 50% from the post-buy peak.
	// macroInjectUSD = 12_000 is out of chromosome bounds but Step()
	// doesn't Validate; this is fine for the evaluator test.
	c := defaultChromosome()
	c.EMALongPeriod = 10
	c.EMAShortPeriod = 5
	c.MAVLongPeriod = 8
	c.MAVShortPeriod = 4
	c.MacroInjectUSD = 12_000 // half-deadline = $6_000 spend
	c.MicroReservePct = 0.05  // low reserve → plenty of macro headroom
	gene := EncodeChromosome(c)

	// Bars: 20 flat at $100 (warmup builds DCA exposure), then ramp
	// down to $0.5 (99.5% price crash → near-total BTC wipeout).
	pre := flatBars(20, 100, windowTestRefMs)
	post := rampBars(60, 100, 0.5, pre[len(pre)-1].OpenTime+barIntervalDays)
	bars := append(pre, post...)

	w := domain.CrucibleWindow{
		Name:      resultpkg.Window6M,
		StartTS:   bars[0].OpenTime,
		EndTS:     bars[len(bars)-1].OpenTime,
		WarmupLen: 5,
		Bars:      bars,
	}
	res, stats, err := evaluateWindow(s, gene, w, domain.FrictionParams{})
	if err != nil {
		t.Fatalf("evaluateWindow: %v", err)
	}
	if !res.Score.Fatal {
		t.Fatalf("crash: Fatal=false, want true (res=%+v)", res)
	}
	if res.Score.Value != nil {
		t.Errorf("Fatal SliceScore: Value=%v, want nil", *res.Score.Value)
	}
	if res.FatalReason == nil || res.FatalAtBarTS == nil || res.FatalMDDValue == nil {
		t.Errorf("Fatal CrucibleResult missing diagnostics: %+v", res)
	}
	if res.FatalMDDValue != nil && *res.FatalMDDValue < fatalMDDThreshold {
		t.Errorf("FatalMDDValue=%v, want >= %v", *res.FatalMDDValue, fatalMDDThreshold)
	}
	// Fatal short-circuits the loop, so BarsEvaluated < total scored.
	if res.BarsEvaluated >= len(bars)-w.WarmupLen {
		t.Errorf("BarsEvaluated=%d, want < %d (early break on Fatal)",
			res.BarsEvaluated, len(bars)-w.WarmupLen)
	}
	if stats != nil {
		t.Errorf("Fatal evaluation: stats=%+v, want nil", *stats)
	}
}

func TestEvaluateWindow_GapBarsProduceNoTrades(t *testing.T) {
	s := windowTestSigmoid()
	// Mark every other bar as IsGap. The strategy may try to fire a
	// macro on those bars (cold start deadline), but the evaluator
	// must discard those orders. So no DeadBTC should accumulate
	// across a gap-only series — except via non-gap bars.
	//
	// To isolate the gap rule, make EVERY bar a gap. No trades →
	// portfolio stays at the cold-start cash position → log return
	// stays ~0 (no friction either).
	bars := flatBars(40, 100, windowTestRefMs)
	for i := range bars {
		bars[i].IsGap = true
		bars[i].GapType = "synthetic"
	}
	w := domain.CrucibleWindow{
		Name:      resultpkg.Window6M,
		StartTS:   bars[0].OpenTime,
		EndTS:     bars[len(bars)-1].OpenTime,
		WarmupLen: 5,
		Bars:      bars,
	}
	res, _, err := evaluateWindow(s, stepTestGene(), w, domain.FrictionParams{})
	if err != nil {
		t.Fatalf("evaluateWindow: %v", err)
	}
	if res.Score.Fatal {
		t.Fatalf("all-gap window: Fatal=true, want false (res=%+v)", res)
	}
	// All gaps → no trading → final NAV == initial USDT == 10_000.
	// Score = log(10_000/10_000) = 0 exactly.
	if res.Score.Value == nil || math.Abs(*res.Score.Value) > 1e-12 {
		t.Errorf("all-gap window: score=%v, want exactly 0 (no trades)", res.Score.Value)
	}
}

func TestEvaluateWindow_Deterministic(t *testing.T) {
	s := windowTestSigmoid()
	gene := stepTestGene()
	// Mix of flat + ramp so the strategy actually trades.
	pre := flatBars(20, 100, windowTestRefMs)
	post := rampBars(40, 100, 120, pre[len(pre)-1].OpenTime+barIntervalDays)
	bars := append(pre, post...)
	w := domain.CrucibleWindow{
		Name: resultpkg.Window6M, WarmupLen: 5, Bars: bars,
		StartTS: bars[0].OpenTime, EndTS: bars[len(bars)-1].OpenTime,
	}
	fp := domain.FrictionParams{TakerFeeBPS: 5, SlippageBPS: 2}

	r1, s1, err := evaluateWindow(s, gene, w, fp)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	r2, s2, err := evaluateWindow(s, gene, w, fp)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	j1, _ := json.Marshal(r1)
	j2, _ := json.Marshal(r2)
	if !bytes.Equal(j1, j2) {
		t.Errorf("non-deterministic CrucibleResult\n  r1=%s\n  r2=%s", j1, j2)
	}
	js1, _ := json.Marshal(s1)
	js2, _ := json.Marshal(s2)
	if !bytes.Equal(js1, js2) {
		t.Errorf("non-deterministic SharpeStats\n  s1=%s\n  s2=%s", js1, js2)
	}
}

func TestEvaluateWindow_LongSeriesRespectsHistoryCap(t *testing.T) {
	// 2000 bars > stepHistoryCap (900) — ensures the trailing-window
	// shift logic doesn't error out and Step() still receives a
	// well-formed input slice.
	s := windowTestSigmoid()
	bars := rampBars(2000, 100, 105, windowTestRefMs)
	w := domain.CrucibleWindow{
		Name: resultpkg.Window6M, WarmupLen: 100, Bars: bars,
		StartTS: bars[0].OpenTime, EndTS: bars[len(bars)-1].OpenTime,
	}
	res, _, err := evaluateWindow(s, stepTestGene(), w, domain.FrictionParams{})
	if err != nil {
		t.Fatalf("evaluateWindow on 2000-bar window: %v", err)
	}
	if res.BarsEvaluated < 100 {
		t.Errorf("BarsEvaluated=%d, want >= 100 (no truncation)", res.BarsEvaluated)
	}
}

func TestEvaluateWindow_EmptyBarsErrors(t *testing.T) {
	_, _, err := evaluateWindow(
		windowTestSigmoid(), stepTestGene(),
		domain.CrucibleWindow{Name: resultpkg.Window6M, Bars: nil},
		domain.FrictionParams{},
	)
	if err == nil {
		t.Error("empty bars: want error, got nil")
	}
}

func TestEvaluateWindow_WarmupGEQLenErrors(t *testing.T) {
	bars := flatBars(10, 100, windowTestRefMs)
	_, _, err := evaluateWindow(
		windowTestSigmoid(), stepTestGene(),
		domain.CrucibleWindow{
			Name: resultpkg.Window6M, Bars: bars, WarmupLen: 10,
		},
		domain.FrictionParams{},
	)
	if err == nil {
		t.Error("WarmupLen >= len(Bars): want error, got nil")
	}
}

func TestEvaluateWindow_PropagatesStepError(t *testing.T) {
	// We can force a Step() error via malformed RuntimeState, but
	// evaluateWindow constructs RuntimeState from scratch. The other
	// reachable error path is a wrong-dim Chromosome — pass a short
	// gene.
	short := domain.Gene{0.1, 0.2, 0.3}
	bars := flatBars(20, 100, windowTestRefMs)
	w := domain.CrucibleWindow{
		Name: resultpkg.Window6M, WarmupLen: 5, Bars: bars,
		StartTS: bars[0].OpenTime, EndTS: bars[len(bars)-1].OpenTime,
	}
	_, _, err := evaluateWindow(windowTestSigmoid(), short, w, domain.FrictionParams{})
	if err == nil {
		t.Error("wrong-dim chromosome: want propagated error, got nil")
	}
}
