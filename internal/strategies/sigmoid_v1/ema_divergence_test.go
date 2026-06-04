package sigmoid_v1

import (
	"math"
	"math/rand"
	"testing"

	"quantlab/internal/quant"
)

// TestEMADivergence_WindowedVsIncremental measures the backlog "constraint B"
// grey zone (see docs/decision-ga-reproducibility-constraint.md): the
// production signal recomputes a *windowed* EMA over a sliding stepHistoryCap
// buffer with a rolling cold-start each bar (quant.EMA(buf, period)[last]),
// whereas the #6 incremental rewrite would carry an *infinite-history* EMA
// from bar 0 (O(1)/bar). Those are different mathematical quantities; this
// quantifies by how much — at the EMA level and propagated into the signal's
// price-deviation term (the only signal term that depends on EMA_long).
//
// This is a MEASUREMENT harness, not a correctness gate: it prints the delta
// distribution (run with `-v`). The soft assertion only catches a wildly
// wrong result (NaN or >5%). The real ScoreTotal/trajectory delta needs the
// #6 seam through EvaluateWindow and is measured when #6 is actually built —
// this is the cheap pre-evidence that de-risks starting it.
func TestEMADivergence_WindowedVsIncremental(t *testing.T) {
	const (
		nBars  = 5000
		burnIn = stepHistoryCap + 100 // steady state: sliding buffer full + margin
		seed   = int64(42)
	)
	closes := syntheticCloses(nBars, seed)
	c := defaultChromosome()

	for _, period := range []int{50, 100, 200, 300} {
		win := windowedEMALast(closes, period, stepHistoryCap)
		inc := incrementalEMA(closes, period)

		var maxRel, sumRel, maxSignalTermDelta float64
		var cnt int
		for i := burnIn; i < nBars; i++ {
			if inc[i] == 0 {
				continue
			}
			rel := math.Abs(win[i]-inc[i]) / math.Abs(inc[i])
			if rel > maxRel {
				maxRel = rel
			}
			sumRel += rel
			cnt++

			// Propagate into signal: signal = A1*priceDeviation + (EMA-free
			// terms). priceDeviation = (close - EMA_long)/EMA_long, so the
			// signal delta from the EMA change is exactly A1*(pd_win - pd_inc).
			pdWin := (closes[i] - win[i]) / win[i]
			pdInc := (closes[i] - inc[i]) / inc[i]
			if d := math.Abs(c.A1 * (pdWin - pdInc)); d > maxSignalTermDelta {
				maxSignalTermDelta = d
			}
		}
		meanRel := sumRel / float64(cnt)
		t.Logf("EMA period=%3d  rel-delta: max=%.3e mean=%.3e  |  max signal Δ (A1·Δpd)=%.3e",
			period, maxRel, meanRel, maxSignalTermDelta)

		if math.IsNaN(maxRel) || maxRel > 0.05 {
			t.Errorf("period=%d: EMA rel-delta max=%.3e out of sane bound (NaN or >5%%)", period, maxRel)
		}
	}
}

// windowedEMALast replicates the production hot path: a sliding buffer capped
// at `cap` (evaluate_window.go), and at each bar quant.EMA(buffer, period)[last].
// Before the buffer fills it grows from bar 0, so early bars match incremental;
// the divergence only appears in steady state once the buffer slides.
func windowedEMALast(closes []float64, period, cap int) []float64 {
	out := make([]float64, len(closes))
	buf := make([]float64, 0, cap)
	for i, x := range closes {
		if len(buf) == cap {
			copy(buf, buf[1:])
			buf[cap-1] = x
		} else {
			buf = append(buf, x)
		}
		ema := quant.EMA(buf, period)
		out[i] = ema[len(ema)-1]
	}
	return out
}

// incrementalEMA carries the same recurrence quant.EMA uses, from bar 0 with
// no window reset — the O(1)/bar form #6 would adopt.
func incrementalEMA(closes []float64, period int) []float64 {
	out := make([]float64, len(closes))
	if len(closes) == 0 {
		return out
	}
	alpha := 2.0 / (float64(period) + 1.0)
	out[0] = closes[0]
	for i := 1; i < len(closes); i++ {
		out[i] = alpha*closes[i] + (1.0-alpha)*out[i-1]
	}
	return out
}

// syntheticCloses builds a deterministic log-normal random walk in BTC's
// range (~0.1% per-bar vol, no drift) so the divergence reflects a realistic
// price path without needing the klines DB.
func syntheticCloses(n int, seed int64) []float64 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float64, n)
	price := 50000.0
	for i := range out {
		price *= math.Exp(r.NormFloat64() * 0.001)
		out[i] = price
	}
	return out
}
