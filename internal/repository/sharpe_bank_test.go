package repository

import (
	"math"
	"testing"
)

// ----- computeBankStats (the pure reducer) -----

func TestComputeBankStats_EmptySlice(t *testing.T) {
	s := computeBankStats(nil)
	if s.N != 0 || s.SharpeMean != 0 || s.SharpeVariance != 0 {
		t.Errorf("empty input: %+v, want zero-valued struct", s)
	}
}

func TestComputeBankStats_SingleEntry(t *testing.T) {
	// One sample: variance is 0 (no spread), mean equals the sample.
	s := computeBankStats([]float64{1.5})
	if s.N != 1 {
		t.Errorf("N = %d, want 1", s.N)
	}
	if math.Abs(s.SharpeMean-1.5) > 1e-12 {
		t.Errorf("Mean = %v, want 1.5", s.SharpeMean)
	}
	if s.SharpeVariance != 0 {
		t.Errorf("Variance = %v, want 0 (single sample)", s.SharpeVariance)
	}
}

func TestComputeBankStats_KnownVariance(t *testing.T) {
	// xs = [1, 2, 3, 4, 5]; mean = 3; population variance = 2.
	s := computeBankStats([]float64{1, 2, 3, 4, 5})
	if math.Abs(s.SharpeMean-3) > 1e-12 {
		t.Errorf("Mean = %v, want 3", s.SharpeMean)
	}
	if math.Abs(s.SharpeVariance-2.0) > 1e-12 {
		t.Errorf("Variance = %v, want 2.0 (population)", s.SharpeVariance)
	}
}

func TestComputeBankStats_AllIdentical(t *testing.T) {
	// Five identical Sharpes → variance 0 → DSR caller will reject
	// (variance ≤ 0 guard in ComputeDSR returns NaN). This test
	// pins the upstream "no NaN before DSR" contract.
	s := computeBankStats([]float64{1.5, 1.5, 1.5, 1.5, 1.5})
	if s.N != 5 {
		t.Errorf("N = %d, want 5", s.N)
	}
	if s.SharpeVariance != 0 {
		t.Errorf("Variance = %v, want 0 (identical Sharpes)", s.SharpeVariance)
	}
}
