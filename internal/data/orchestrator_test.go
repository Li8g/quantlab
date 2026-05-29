package data

import (
	"testing"
	"time"
)

func TestIntervalMs(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1m", 60_000, true},
		{"5m", 300_000, true},
		{"1h", 3_600_000, true},
		{"1d", 86_400_000, true},
		{"bogus", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := IntervalMs(c.in)
			if c.ok && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if !c.ok && err == nil {
				t.Errorf("err = nil, want error")
			}
			if got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestMonthRange(t *testing.T) {
	cases := []struct {
		name       string
		start, end time.Time
		want       []yearMonth
	}{
		{
			"single month",
			time.Date(2025, 1, 5, 0, 0, 0, 0, time.UTC),
			time.Date(2025, 1, 25, 0, 0, 0, 0, time.UTC),
			[]yearMonth{{2025, 1}},
		},
		{
			"two adjacent months",
			time.Date(2025, 1, 28, 0, 0, 0, 0, time.UTC),
			time.Date(2025, 2, 2, 0, 0, 0, 0, time.UTC),
			[]yearMonth{{2025, 1}, {2025, 2}},
		},
		{
			"span year boundary",
			time.Date(2024, 11, 15, 0, 0, 0, 0, time.UTC),
			time.Date(2025, 2, 10, 0, 0, 0, 0, time.UTC),
			[]yearMonth{{2024, 11}, {2024, 12}, {2025, 1}, {2025, 2}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := monthRange(c.start, c.end)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("[%d] got %v, want %v", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestFilterByOpenTime(t *testing.T) {
	rows := []KlineRow{
		{OpenTime: 100}, {OpenTime: 200}, {OpenTime: 300}, {OpenTime: 400},
	}
	got := filterByOpenTime(rows, 200, 300)
	if len(got) != 2 || got[0].OpenTime != 200 || got[1].OpenTime != 300 {
		t.Errorf("got %v, want [200, 300]", got)
	}
	if filterByOpenTime(rows, 500, 1000) != nil && len(filterByOpenTime(rows, 500, 1000)) != 0 {
		t.Error("expected empty result")
	}
}

func TestComputeGaps_NoGap(t *testing.T) {
	// Consecutive 1m bars: no gap.
	openTimes := []int64{60_000, 120_000, 180_000, 240_000}
	gaps := computeGaps(openTimes, 60_000, "BTCUSDT", "1m")
	if len(gaps) != 0 {
		t.Errorf("got %d gaps, want 0", len(gaps))
	}
}

func TestComputeGaps_SingleGap(t *testing.T) {
	// Gap from 120000..180000 (2 missing bars).
	openTimes := []int64{60_000, 120_000, 240_000, 300_000}
	gaps := computeGaps(openTimes, 60_000, "BTCUSDT", "1m")
	if len(gaps) != 1 {
		t.Fatalf("got %d gaps, want 1", len(gaps))
	}
	g := gaps[0]
	// gap_start = 120000 + 60000 = 180000
	// gap_end   = 240000 - 1 = 239999
	if g.GapStartMs != 180_000 || g.GapEndMs != 239_999 {
		t.Errorf("gap = [%d, %d], want [180000, 239999]", g.GapStartMs, g.GapEndMs)
	}
	if g.Symbol != "BTCUSDT" || g.Interval != "1m" {
		t.Errorf("symbol/interval not propagated: %+v", g)
	}
}

func TestComputeGaps_MultipleGaps(t *testing.T) {
	openTimes := []int64{0, 60_000, 240_000, 300_000, 600_000}
	// gap1: between idx1 (60000) and idx2 (240000) — span 60001..239999
	// gap2: between idx3 (300000) and idx4 (600000) — span 360000..599999
	gaps := computeGaps(openTimes, 60_000, "BTCUSDT", "1m")
	if len(gaps) != 2 {
		t.Fatalf("got %d gaps, want 2", len(gaps))
	}
}

func TestComputeGaps_EmptyOrSingleTime(t *testing.T) {
	if len(computeGaps(nil, 60_000, "x", "1m")) != 0 {
		t.Error("empty input must produce zero gaps")
	}
	if len(computeGaps([]int64{42}, 60_000, "x", "1m")) != 0 {
		t.Error("single-time input must produce zero gaps")
	}
}
