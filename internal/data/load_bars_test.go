package data

import "testing"

func TestIntervalToMs_Whitelist(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"1m", 60_000},
		{"5m", 5 * 60_000},
		{"15m", 15 * 60_000},
		{"1h", 60 * 60_000},
		{"4h", 4 * 60 * 60_000},
		{"1d", 24 * 60 * 60_000},
	}
	for _, c := range cases {
		got, err := IntervalToMs(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %d, want %d", c.in, got, c.want)
		}
	}
}

func TestIntervalToMs_RejectsUnknown(t *testing.T) {
	for _, bad := range []string{"", "30s", "2h", "1w", "bogus"} {
		if _, err := IntervalToMs(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}
