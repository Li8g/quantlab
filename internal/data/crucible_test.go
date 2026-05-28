package data

import (
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
)

// makeDailyBars builds n bars at 1-day spacing starting at startMs.
// Daily granularity keeps the tests readable; the function under test is
// granularity-agnostic (works at any consistent interval).
func makeDailyBars(n int, startMs int64) []domain.Bar {
	out := make([]domain.Bar, n)
	for i := 0; i < n; i++ {
		out[i] = domain.Bar{OpenTime: startMs + int64(i)*DayMs, Close: 100}
	}
	return out
}

func intPtr(v int) *int { return &v }

func TestBuildCrucible_AllFourWindowsPresent(t *testing.T) {
	// 4000 days >= 1825 (5y) + 1200 (warmup) = 3025, comfortable for all four.
	bars := makeDailyBars(4000, 1_700_000_000_000)
	is, oos, err := BuildCrucibleWindows(bars, 1200, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if oos != nil {
		t.Errorf("oos = %v, want nil when oosDays is nil", oos)
	}
	if len(is) != 4 {
		t.Fatalf("len(is) = %d, want 4", len(is))
	}
	for i, want := range []resultpkg.WindowName{
		resultpkg.Window6M, resultpkg.Window2Y, resultpkg.Window5Y, resultpkg.Window10Y,
	} {
		if is[i].Name != want {
			t.Errorf("is[%d].Name = %s, want %s", i, is[i].Name, want)
		}
	}
}

func TestBuildCrucible_DropsWindowsThatDoNotFit(t *testing.T) {
	// 500 days IS: 6m (183+1200=1383) — drop. 2y, 5y — drop.
	// 10y: needs evalDays + warmupDays > 0 and ≤ isDays.
	//      With warmupDays=1200 > isDays=500 → also drop.
	bars := makeDailyBars(500, 1_700_000_000_000)
	is, _, err := BuildCrucibleWindows(bars, 1200, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(is) != 0 {
		t.Errorf("len(is) = %d, want 0 (all windows too short)", len(is))
	}
}

func TestBuildCrucible_PartialFit(t *testing.T) {
	// 1500 days IS, warmup=1200:
	//   6m: 183+1200=1383 ≤ 1500 ✅
	//   2y: 730+1200=1930 > 1500 ❌
	//   5y: ❌
	//   10y: eval = 1500-1200 = 300, total 1500 ≤ 1500 ✅
	bars := makeDailyBars(1500, 1_700_000_000_000)
	is, _, err := BuildCrucibleWindows(bars, 1200, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(is) != 2 {
		t.Fatalf("len(is) = %d, want 2 (6m + 10y)", len(is))
	}
	if is[0].Name != resultpkg.Window6M || is[1].Name != resultpkg.Window10Y {
		t.Errorf("got [%s, %s], want [6m, 10y]", is[0].Name, is[1].Name)
	}
}

func TestBuildCrucible_OOSAnchoredHoldout(t *testing.T) {
	bars := makeDailyBars(4000, 1_700_000_000_000)
	oosDays := 60
	warmupDays := 1200
	is, oos, err := BuildCrucibleWindows(bars, warmupDays, &oosDays)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if oos == nil {
		t.Fatal("oos = nil")
	}
	if oos.Name != resultpkg.WindowOOS {
		t.Errorf("oos.Name = %s, want %s", oos.Name, resultpkg.WindowOOS)
	}
	// StartTS is the eval-start (post-warmup), still the trailing oosDays anchor.
	wantOosStart := bars[len(bars)-1].OpenTime - int64(oosDays)*DayMs
	if oos.StartTS < wantOosStart {
		t.Errorf("oos.StartTS = %d < want >= %d", oos.StartTS, wantOosStart)
	}
	// All IS windows must end before OOS evaluation starts.
	for _, w := range is {
		if w.EndTS >= oos.StartTS {
			t.Errorf("future-leakage: IS %s EndTS=%d >= OOS StartTS=%d",
				w.Name, w.EndTS, oos.StartTS)
		}
	}
	// WarmupLen ≈ warmupDays (give or take one bar for sort.Search alignment).
	// On strict daily bars the count should land exactly at warmupDays.
	if oos.WarmupLen < warmupDays-1 || oos.WarmupLen > warmupDays+1 {
		t.Errorf("oos.WarmupLen = %d, want ≈%d (warmupDays prefix)",
			oos.WarmupLen, warmupDays)
	}
	// The first EVAL bar must align with StartTS.
	if oos.Bars[oos.WarmupLen].OpenTime != oos.StartTS {
		t.Errorf("oos.Bars[WarmupLen].OpenTime = %d, want StartTS=%d",
			oos.Bars[oos.WarmupLen].OpenTime, oos.StartTS)
	}
}

func TestBuildCrucible_OOSDroppedWhenWarmupTruncated(t *testing.T) {
	// 200 bars total, warmupDays=180, oosDays=60. Pre-OOS bars ≈ 140,
	// less than warmup → drop OOS (oos = nil) per the "strict" rule.
	// IS gets the entire series since OOS wasn't constructed.
	bars := makeDailyBars(200, 1_700_000_000_000)
	oosDays := 60
	is, oos, err := BuildCrucibleWindows(bars, 180, &oosDays)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if oos != nil {
		t.Errorf("oos = %+v, want nil (pre-OOS history too short for warmup)", oos)
	}
	// Sanity: IS still tries to construct what it can from the full
	// series. No window may fit either — both is and oos being empty
	// is acceptable here; we only assert oos was dropped.
	_ = is
}

func TestBuildCrucible_WarmupPrecedesEval(t *testing.T) {
	bars := makeDailyBars(4000, 1_700_000_000_000)
	is, _, err := BuildCrucibleWindows(bars, 1200, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range is {
		if w.WarmupLen < 0 || w.WarmupLen > len(w.Bars) {
			t.Errorf("%s: WarmupLen=%d out of [0,%d]", w.Name, w.WarmupLen, len(w.Bars))
		}
		// First eval bar's OpenTime should be >= StartTS.
		if w.WarmupLen < len(w.Bars) {
			firstEval := w.Bars[w.WarmupLen]
			if firstEval.OpenTime < w.StartTS {
				t.Errorf("%s: first-eval bar OpenTime %d < StartTS %d",
					w.Name, firstEval.OpenTime, w.StartTS)
			}
		}
		// Warmup bars (if any) must precede StartTS.
		if w.WarmupLen > 0 {
			lastWarmup := w.Bars[w.WarmupLen-1]
			if lastWarmup.OpenTime >= w.StartTS {
				t.Errorf("%s: last warmup bar %d >= StartTS %d",
					w.Name, lastWarmup.OpenTime, w.StartTS)
			}
		}
	}
}

func TestBuildCrucible_RejectsBadInput(t *testing.T) {
	good := makeDailyBars(2000, 1_700_000_000_000)

	cases := []struct {
		name        string
		bars        []domain.Bar
		warmupDays  int
		oosDays     *int
		wantErrSubs string
	}{
		{"empty bars", nil, 1200, nil, "empty"},
		{"negative warmup", good, -1, nil, "warmupDays"},
		{"oosDays zero", good, 1200, intPtr(0), "oosDays"},
		{
			"oosDays consumes all",
			good, 1200, intPtr(10_000), "consumes",
		},
		{
			"non-ascending bars",
			func() []domain.Bar {
				b := makeDailyBars(10, 1_700_000_000_000)
				b[5].OpenTime = b[4].OpenTime // duplicate timestamp
				return b
			}(),
			1200, nil, "not strictly ascending",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := BuildCrucibleWindows(c.bars, c.warmupDays, c.oosDays)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if c.wantErrSubs != "" && !contains(err.Error(), c.wantErrSubs) {
				t.Errorf("err = %q, want substring %q", err.Error(), c.wantErrSubs)
			}
		})
	}
}

func TestBuildCrucible_BarsContainsWarmupPlusEval(t *testing.T) {
	// For a 6m window: len(Bars) ≈ 183 + 1200 days of bars (here daily = 1383).
	bars := makeDailyBars(4000, 1_700_000_000_000)
	is, _, err := BuildCrucibleWindows(bars, 1200, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[resultpkg.WindowName]int{
		resultpkg.Window6M:  183 + 1200,
		resultpkg.Window2Y:  730 + 1200,
		resultpkg.Window5Y:  1825 + 1200,
		resultpkg.Window10Y: 4000, // entire IS (eval = 4000-1200 = 2800, warmup 1200)
	}
	for _, w := range is {
		// Allow ±1 fuzz from day-boundary search semantics.
		got := len(w.Bars)
		diff := got - want[w.Name]
		if diff < -1 || diff > 1 {
			t.Errorf("%s: len(Bars)=%d, want ≈%d (±1)", w.Name, got, want[w.Name])
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
