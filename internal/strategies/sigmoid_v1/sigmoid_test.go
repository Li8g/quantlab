package sigmoid_v1

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"quantlab/internal/domain"
)

// newRNG returns a deterministic RNG for table-style tests. Each call
// gets a fresh stream from the same seed, so test ordering doesn't
// leak between cases.
func newRNG(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// ----- StrategyID + Segments structural tests -----

func TestStrategyID(t *testing.T) {
	s := New(60_000)
	if s.StrategyID() != "sigmoid_v1" {
		t.Errorf("StrategyID = %q, want sigmoid_v1", s.StrategyID())
	}
}

func TestSegmentsCoverage(t *testing.T) {
	s := New(60_000)
	segs := s.Segments()

	seen := make([]int, 0, GeneDim)
	for _, seg := range segs {
		if len(seg.Dimensions) != len(seg.QuantizationStep) ||
			len(seg.Dimensions) != len(seg.GeneStep) {
			t.Errorf("segment %q: len mismatch dims=%d qstep=%d gstep=%d",
				seg.Name, len(seg.Dimensions), len(seg.QuantizationStep), len(seg.GeneStep))
		}
		if len(seg.Dimensions) < 2 || len(seg.Dimensions) > 10 {
			t.Errorf("segment %q: %d dims, expected 2-10 (spec §7.4)",
				seg.Name, len(seg.Dimensions))
		}
		seen = append(seen, seg.Dimensions...)
	}
	sort.Ints(seen)

	if len(seen) != GeneDim {
		t.Fatalf("Σ dims = %d, want %d (every dim exactly once)", len(seen), GeneDim)
	}
	for i := 0; i < GeneDim; i++ {
		if seen[i] != i {
			t.Errorf("dim %d missing or duplicated (seen[%d]=%d)", i, i, seen[i])
		}
	}
}

func TestSegmentsStableOrder(t *testing.T) {
	s := New(60_000)
	a := s.Segments()
	b := s.Segments()
	if !reflect.DeepEqual(a, b) {
		t.Error("Segments() returned a different slice on consecutive calls")
	}
	want := []string{"signal_weights", "micro_dynamics", "feature_periods", "state_thresholds", "macro_release"}
	for i, seg := range a {
		if seg.Name != want[i] {
			t.Errorf("segment[%d].Name = %q, want %q", i, seg.Name, want[i])
		}
	}
}

// ----- Chromosome round-trip -----

func TestChromosomeRoundTrip(t *testing.T) {
	src := defaultChromosome()
	g := EncodeChromosome(src)
	got, err := DecodeChromosome(g)
	if err != nil {
		t.Fatalf("DecodeChromosome: %v", err)
	}
	if got != src {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", got, src)
	}
}

func TestDecodeChromosome_WrongDim(t *testing.T) {
	_, err := DecodeChromosome(domain.Gene{1, 2, 3})
	if err == nil {
		t.Error("DecodeChromosome on wrong-length gene should error")
	}
}

// ----- Clamp -----

func TestSampleProducesValidGene(t *testing.T) {
	s := New(60_000)
	rng := newRNG(42)
	for i := 0; i < 200; i++ {
		g := s.Sample(rng)
		if err := s.Validate(g); err != nil {
			t.Fatalf("Sample[%d] failed Validate: %v\n  gene=%v", i, err, g)
		}
	}
}

func TestChromosomeClampIdempotent(t *testing.T) {
	s := New(60_000)
	rng := newRNG(7)
	for i := 0; i < 100; i++ {
		// Out-of-bounds raw gene: each dim sampled from 3× its legal range.
		g := make(domain.Gene, GeneDim)
		for j := 0; j < GeneDim; j++ {
			lo, hi := bounds[j][0], bounds[j][1]
			width := hi - lo
			g[j] = lo - width + rng.Float64()*3*width
		}
		once := s.Clamp(g)
		twice := s.Clamp(once)
		if !reflect.DeepEqual(once, twice) {
			t.Fatalf("Clamp not idempotent at iter %d:\n  once  %v\n  twice %v", i, once, twice)
		}
	}
}

func TestChromosomeValidateAfterClamp(t *testing.T) {
	s := New(60_000)
	rng := newRNG(11)
	for i := 0; i < 500; i++ {
		g := make(domain.Gene, GeneDim)
		for j := 0; j < GeneDim; j++ {
			lo, hi := bounds[j][0], bounds[j][1]
			width := hi - lo
			g[j] = lo - width + rng.Float64()*3*width
		}
		clamped := s.Clamp(g)
		if err := s.Validate(clamped); err != nil {
			t.Fatalf("Validate(Clamp(g)) failed at iter %d: %v\n  raw=%v\n  clamped=%v",
				i, err, g, clamped)
		}
	}
}

func TestClampPadsShortInput(t *testing.T) {
	s := New(60_000)
	clamped := s.Clamp(domain.Gene{0.1, 0.2}) // only 2 of 13 dims
	if len(clamped) != GeneDim {
		t.Errorf("len after Clamp = %d, want %d", len(clamped), GeneDim)
	}
	if err := s.Validate(clamped); err != nil {
		t.Errorf("padded gene failed Validate: %v", err)
	}
}

func TestClampEnforcesShortLessThanLong(t *testing.T) {
	s := New(60_000)
	// Construct a gene where ema_short = ema_long = 100, mav_short = mav_long = 60.
	c := defaultChromosome()
	c.EMAShortPeriod = 100
	c.EMALongPeriod = 100
	c.MAVShortPeriod = 60
	c.MAVLongPeriod = 60
	clamped := s.Clamp(EncodeChromosome(c))
	dec, err := DecodeChromosome(clamped)
	if err != nil {
		t.Fatal(err)
	}
	if dec.EMAShortPeriod >= dec.EMALongPeriod {
		t.Errorf("ema short %d not < long %d", dec.EMAShortPeriod, dec.EMALongPeriod)
	}
	if dec.MAVShortPeriod >= dec.MAVLongPeriod {
		t.Errorf("mav short %d not < long %d", dec.MAVShortPeriod, dec.MAVLongPeriod)
	}
}

func TestClampRoundsPeriodsToIntegers(t *testing.T) {
	s := New(60_000)
	c := defaultChromosome()
	g := EncodeChromosome(c)
	g[geneDimEMAShortPeriod] = 20.4
	g[geneDimEMALongPeriod] = 99.6
	clamped := s.Clamp(g)
	if clamped[geneDimEMAShortPeriod] != 20 {
		t.Errorf("ema_short rounded to %v, want 20", clamped[geneDimEMAShortPeriod])
	}
	if clamped[geneDimEMALongPeriod] != 100 {
		t.Errorf("ema_long rounded to %v, want 100", clamped[geneDimEMALongPeriod])
	}
}

// ----- Validate -----

func TestValidateRejectsOutOfBounds(t *testing.T) {
	s := New(60_000)
	c := defaultChromosome()

	g := EncodeChromosome(c)
	g[geneDimBeta] = 99 // way out of [0.5, 5]
	if err := s.Validate(g); err == nil {
		t.Error("Validate accepted beta=99")
	}

	g2 := EncodeChromosome(c)
	g2[geneDimA1] = -2.5 // out of [-1, 1]
	if err := s.Validate(g2); err == nil {
		t.Error("Validate accepted A1=-2.5")
	}
}

func TestValidateRejectsShortGEQLong(t *testing.T) {
	s := New(60_000)
	c := defaultChromosome()
	c.EMAShortPeriod = 100
	c.EMALongPeriod = 100
	if err := s.Validate(EncodeChromosome(c)); err == nil {
		t.Error("Validate accepted ema_short==ema_long")
	}
}

func TestSignalWeightsRangeRespected(t *testing.T) {
	s := New(60_000)
	rng := newRNG(99)
	for i := 0; i < 1000; i++ {
		g := s.Sample(rng)
		for _, idx := range []int{geneDimA1, geneDimA2, geneDimA3} {
			if g[idx] < -1.0 || g[idx] > 1.0 {
				t.Errorf("iter %d: a[%d]=%v out of [-1, 1]", i, idx, g[idx])
			}
		}
	}
}

// ----- Crossover -----

func TestCrossoverProducesValidGene(t *testing.T) {
	s := New(60_000)
	rng := newRNG(101)
	for i := 0; i < 200; i++ {
		p1 := s.Sample(rng)
		p2 := s.Sample(rng)
		c := s.Crossover(p1, p2, rng)
		if err := s.Validate(c); err != nil {
			t.Fatalf("iter %d: Crossover child invalid: %v", i, err)
		}
	}
}

// TestCrossoverBlockFidelity (CLAUDE.md §10.1 #6): for every segment, the
// child's segment values must byte-equal one of the two parents' values
// in that segment. This is the test that v1 protects by NOT doing L2
// normalisation in Clamp — see spec §4.3.
func TestCrossoverBlockFidelity(t *testing.T) {
	s := New(60_000)
	rng := newRNG(202)
	segs := s.Segments()

	for i := 0; i < 200; i++ {
		p1 := s.Sample(rng)
		p2 := s.Sample(rng)
		child := s.Crossover(p1, p2, rng)

		for _, seg := range segs {
			fromP1, fromP2 := true, true
			for _, idx := range seg.Dimensions {
				if child[idx] != p1[idx] {
					fromP1 = false
				}
				if child[idx] != p2[idx] {
					fromP2 = false
				}
			}
			if !fromP1 && !fromP2 {
				t.Errorf("iter %d seg %q: child segment matches neither parent\n  p1=%v\n  p2=%v\n  child=%v",
					i, seg.Name, segSubset(p1, seg.Dimensions),
					segSubset(p2, seg.Dimensions),
					segSubset(child, seg.Dimensions))
			}
		}
	}
}

func segSubset(g domain.Gene, idxs []int) []float64 {
	out := make([]float64, len(idxs))
	for i, k := range idxs {
		out[i] = g[k]
	}
	return out
}

// ----- Mutate -----

func TestMutateProducesValidGene(t *testing.T) {
	s := New(60_000)
	rng := newRNG(303)
	for i := 0; i < 200; i++ {
		g := s.Sample(rng)
		m := s.Mutate(g, 0.5, 1.0, rng)
		if err := s.Validate(m); err != nil {
			t.Fatalf("iter %d: mutated invalid: %v", i, err)
		}
	}
}

func TestMutateProb0IsNoOp(t *testing.T) {
	s := New(60_000)
	rng := newRNG(404)
	g := s.Sample(rng)
	m := s.Mutate(g, 0, 1, rng)
	if !reflect.DeepEqual(g, m) {
		t.Errorf("Mutate prob=0 changed gene:\n  in  %v\n  out %v", g, m)
	}
}

func TestMutateProb1ChangesSomeDim(t *testing.T) {
	s := New(60_000)
	rng := newRNG(505)
	g := s.Sample(rng)
	m := s.Mutate(g, 1.0, 1.0, rng)
	changed := false
	for i := 0; i < GeneDim; i++ {
		if g[i] != m[i] {
			changed = true
			break
		}
	}
	if !changed {
		t.Errorf("Mutate prob=1 changed nothing:\n  in  %v\n  out %v", g, m)
	}
}

// ----- Fingerprint -----

func TestFingerprintDeterministic(t *testing.T) {
	s := New(60_000)
	rng := newRNG(606)
	for i := 0; i < 50; i++ {
		g := s.Sample(rng)
		if s.Fingerprint(g) != s.Fingerprint(g) {
			t.Fatalf("iter %d: Fingerprint not deterministic", i)
		}
	}
}

func TestFingerprintQuantizationStable(t *testing.T) {
	s := New(60_000)
	c := defaultChromosome()
	g1 := EncodeChromosome(c)
	// Nudge A1 below its quantization step (0.05 / 4 = 0.0125 < 0.025 half-bucket).
	g2 := append(domain.Gene(nil), g1...)
	g2[geneDimA1] += 0.0125
	if s.Fingerprint(g1) != s.Fingerprint(g2) {
		t.Errorf("Fingerprint changed under sub-quantization jitter")
	}
}

func TestFingerprintBucketChange(t *testing.T) {
	s := New(60_000)
	c := defaultChromosome()
	g1 := EncodeChromosome(c)
	g2 := append(domain.Gene(nil), g1...)
	// Move A1 a full bucket forward.
	g2[geneDimA1] += 0.05
	if s.Fingerprint(g1) == s.Fingerprint(g2) {
		t.Errorf("Fingerprint stable across a full quantization step (expected change)")
	}
}

// ----- MinEvalBars -----

func TestMinEvalBarsTable(t *testing.T) {
	cases := []struct {
		name          string
		barIntervalMs int64
		want          int
	}{
		{"1m", 60_000, 43_201},
		{"5m", 5 * 60_000, 8_641},
		{"1h", 60 * 60_000, 721},
		{"4h", 4 * 60 * 60_000, 301}, // bound by maxChromosomePeriod
		{"1d", 24 * 60 * 60_000, 301},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(tc.barIntervalMs)
			if got := s.MinEvalBars(); got != tc.want {
				t.Errorf("MinEvalBars = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNewPanicsOnNonPositiveInterval(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	New(0)
}
