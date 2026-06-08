package verification

import (
	"math"
	"testing"

	"quantlab/internal/quant"
)

// TestComputeSharpeStats_FeedsDSRWithoutNaN exercises the integration between
// quant.ComputeSharpeStats and ComputeDSR. NaN propagating from either helper
// into the other would surface here.
func TestComputeSharpeStats_FeedsDSRWithoutNaN(t *testing.T) {
	r := make([]float64, 200)
	for i := range r {
		r[i] = 0.0008 + 0.012*math.Sin(float64(i)*0.4)
	}
	s := quant.ComputeSharpeStats(r)
	dsr := ComputeDSR(s.ObservedSharpe, 0.2, 10, s.HorizonT, s.Skew, s.ExcessKurt)
	if math.IsNaN(dsr) {
		t.Errorf("DSR = NaN from healthy stats %+v", s)
	}
}
