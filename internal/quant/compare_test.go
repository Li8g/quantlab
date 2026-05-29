package quant

import (
	"testing"

	"quantlab/internal/resultpkg"
)

func f64(v float64) *float64 { return &v }

func scoreVal(v float64) resultpkg.ScoreTotal {
	return resultpkg.ScoreTotal{Fatal: false, Value: f64(v)}
}
func scoreFatal() resultpkg.ScoreTotal { return resultpkg.ScoreTotal{Fatal: true, Value: nil} }

// TestCompareFitnessNilSafe is priority test #11.
// Verifies CompareFitness handles all Fatal/Normal combinations without
// panicking, and that the ordering contract is correct.
func TestCompareFitnessNilSafe(t *testing.T) {
	cases := []struct {
		name     string
		a, b     resultpkg.ScoreTotal
		wantSign int // -1, 0, or +1
	}{
		{"normal_beats_fatal", scoreVal(1.5), scoreFatal(), -1},
		{"fatal_loses_to_normal", scoreFatal(), scoreVal(1.5), +1},
		{"double_fatal_tie", scoreFatal(), scoreFatal(), 0},
		{"higher_normal_wins", scoreVal(2.0), scoreVal(1.0), -1},
		{"lower_normal_loses", scoreVal(1.0), scoreVal(2.0), +1},
		{"equal_normal_tie", scoreVal(1.0), scoreVal(1.0), 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CompareFitness(tc.a, tc.b)
			switch tc.wantSign {
			case -1:
				if got >= 0 {
					t.Errorf("want negative, got %d", got)
				}
			case +1:
				if got <= 0 {
					t.Errorf("want positive, got %d", got)
				}
			case 0:
				if got != 0 {
					t.Errorf("want zero, got %d", got)
				}
			}
		})
	}
}

// TestCompareFitnessAntiSymmetry verifies CompareFitness(a,b) == -CompareFitness(b,a)
// for all pairs (required for sort correctness).
func TestCompareFitnessAntiSymmetry(t *testing.T) {
	pairs := []struct {
		a, b resultpkg.ScoreTotal
	}{
		{scoreVal(2.0), scoreVal(1.0)},
		{scoreVal(1.0), scoreVal(2.0)},
		{scoreVal(1.0), scoreVal(1.0)},
		{scoreFatal(), scoreVal(1.0)},
		{scoreVal(1.0), scoreFatal()},
		{scoreFatal(), scoreFatal()},
	}
	for _, p := range pairs {
		ab := CompareFitness(p.a, p.b)
		ba := CompareFitness(p.b, p.a)
		if sign(ab) != -sign(ba) {
			t.Errorf("anti-symmetry broken: Compare(a,b)=%d, Compare(b,a)=%d", ab, ba)
		}
	}
}

func sign(x int) int {
	if x < 0 {
		return -1
	}
	if x > 0 {
		return 1
	}
	return 0
}
