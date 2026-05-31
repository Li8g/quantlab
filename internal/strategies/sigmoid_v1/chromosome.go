// Chromosome layout for sigmoid_v1. Authoritative spec: docs/strategies/
// sigmoid_v1.md §4. Gene indices, value ranges, defaults, and segment
// definitions are MIRRORED here from that document — any discrepancy is a
// bug to be reconciled by editing the doc first.
//
// 13 dimensions, 5 segments. See §4.1 for the dimension table, §4.2 for
// segment layout (block-crossover boundaries + per-dim Fingerprint and
// Mutate steps).
//
// The mirror is no longer discipline-only: doc_layout_test.go parses §4.1
// and §4.2 out of the markdown and fails the build on any drift between
// this file and the spec.
package sigmoid_v1

import (
	"fmt"
	"math"

	"quantlab/internal/domain"
	"quantlab/internal/quant"
)

// Gene indices (§4.1 table). Use these constants instead of literals when
// reading/writing into a domain.Gene to keep the layout auditable.
const (
	geneDimA1                       = 0
	geneDimA2                       = 1
	geneDimA3                       = 2
	geneDimBeta                     = 3
	geneDimGamma                    = 4
	geneDimEMAShortPeriod           = 5
	geneDimEMALongPeriod            = 6
	geneDimMAVShortPeriod           = 7
	geneDimMAVLongPeriod            = 8
	geneDimQuietThreshold           = 9
	geneDimMicroReservePct          = 10
	geneDimMacroInjectUSD           = 11
	geneDimReleaseDrawdownThreshold = 12

	GeneDim = 13
)

// Bounds (§4.1 table). Pairs are (min, max). All inclusive.
const (
	minA1, maxA1                                             = -1.0, 1.0
	minA2, maxA2                                             = -1.0, 1.0
	minA3, maxA3                                             = -1.0, 1.0
	minBeta, maxBeta                                         = 0.5, 5.0
	minGamma, maxGamma                                       = 0.0, 3.0
	minEMAShortPeriod, maxEMAShortPeriod                     = 5.0, 100.0
	minEMALongPeriod, maxEMALongPeriod                       = 50.0, 300.0
	minMAVShortPeriod, maxMAVShortPeriod                     = 5.0, 50.0
	minMAVLongPeriod, maxMAVLongPeriod                       = 30.0, 250.0
	minQuietThreshold, maxQuietThreshold                     = 0.3, 1.2
	minMicroReservePct, maxMicroReservePct                   = 0.05, 0.5
	minMacroInjectUSD, maxMacroInjectUSD                     = 10.0, 1000.0
	minReleaseDrawdownThreshold, maxReleaseDrawdownThreshold = 0.1, 0.5
)

// MaxChromosomePeriod is the largest "period" bound across all
// integer-valued period dimensions. Used by MinEvalBars per spec §8.2 to
// guarantee enough warmup bars regardless of the realised Gene.
const MaxChromosomePeriod = 300 // ema_long upper bound; mav_long is 250

// Chromosome is the typed view of a domain.Gene. Conversion is via Decode
// and Encode; the engine layer only ever sees the opaque []float64.
type Chromosome struct {
	A1                       float64
	A2                       float64
	A3                       float64
	Beta                     float64
	Gamma                    float64
	EMAShortPeriod           int
	EMALongPeriod            int
	MAVShortPeriod           int
	MAVLongPeriod            int
	QuietThreshold           float64
	MicroReservePct          float64
	MacroInjectUSD           float64
	ReleaseDrawdownThreshold float64
}

// DecodeChromosome interprets a Gene of length GeneDim. Caller must have
// passed gene through Clamp first; this function only checks length and
// converts. Integer-valued fields use math.Round on the float — this is
// safe given the caller's Clamp invariant.
func DecodeChromosome(g domain.Gene) (Chromosome, error) {
	if len(g) != GeneDim {
		return Chromosome{}, fmt.Errorf("sigmoid_v1: gene dim = %d, want %d", len(g), GeneDim)
	}
	return Chromosome{
		A1:                       g[geneDimA1],
		A2:                       g[geneDimA2],
		A3:                       g[geneDimA3],
		Beta:                     g[geneDimBeta],
		Gamma:                    g[geneDimGamma],
		EMAShortPeriod:           int(math.Round(g[geneDimEMAShortPeriod])),
		EMALongPeriod:            int(math.Round(g[geneDimEMALongPeriod])),
		MAVShortPeriod:           int(math.Round(g[geneDimMAVShortPeriod])),
		MAVLongPeriod:            int(math.Round(g[geneDimMAVLongPeriod])),
		QuietThreshold:           g[geneDimQuietThreshold],
		MicroReservePct:          g[geneDimMicroReservePct],
		MacroInjectUSD:           g[geneDimMacroInjectUSD],
		ReleaseDrawdownThreshold: g[geneDimReleaseDrawdownThreshold],
	}, nil
}

// EncodeChromosome is the inverse of DecodeChromosome; used by tests and by
// any future "construct a known Gene" path (e.g. seeding the GA from a
// hand-tuned starting point).
func EncodeChromosome(c Chromosome) domain.Gene {
	g := make(domain.Gene, GeneDim)
	g[geneDimA1] = c.A1
	g[geneDimA2] = c.A2
	g[geneDimA3] = c.A3
	g[geneDimBeta] = c.Beta
	g[geneDimGamma] = c.Gamma
	g[geneDimEMAShortPeriod] = float64(c.EMAShortPeriod)
	g[geneDimEMALongPeriod] = float64(c.EMALongPeriod)
	g[geneDimMAVShortPeriod] = float64(c.MAVShortPeriod)
	g[geneDimMAVLongPeriod] = float64(c.MAVLongPeriod)
	g[geneDimQuietThreshold] = c.QuietThreshold
	g[geneDimMicroReservePct] = c.MicroReservePct
	g[geneDimMacroInjectUSD] = c.MacroInjectUSD
	g[geneDimReleaseDrawdownThreshold] = c.ReleaseDrawdownThreshold
	return g
}

// defaultChromosome returns the spec §4.1 default-value column. Used by
// Sample() if RNG ever produces a degenerate genome (zero-length sample);
// also serves as the starting point for Phase 4b/4c regression tests.
func defaultChromosome() Chromosome {
	return Chromosome{
		A1:                       0.5,
		A2:                       0.3,
		A3:                       0.2,
		Beta:                     2.0,
		Gamma:                    0.5,
		EMAShortPeriod:           20,
		EMALongPeriod:            100,
		MAVShortPeriod:           10,
		MAVLongPeriod:            60,
		QuietThreshold:           0.7,
		MicroReservePct:          0.25,
		MacroInjectUSD:           100,
		ReleaseDrawdownThreshold: 0.3,
	}
}

// segmentInfos is the spec §4.2 segment layout. It MUST satisfy the
// EvolvableStrategy contract: stable order, every gene dimension covered
// exactly once, and 2-10 dims per segment.
//
// We compute this once at package init via a function (rather than a
// package-level var literal) so subsequent QuantizationStep / GeneStep
// changes are textually adjacent to the dimension list — easier to audit.
func segmentInfos() []domain.SegmentInfo {
	return []domain.SegmentInfo{
		{
			Name:             "signal_weights",
			Dimensions:       []int{geneDimA1, geneDimA2, geneDimA3},
			QuantizationStep: []float64{0.05, 0.05, 0.05},
			GeneStep:         []float64{0.2, 0.2, 0.2},
			IsCritical:       true,
			Description:      "信号合成权重 a1, a2, a3 (v1 不归一)",
		},
		{
			Name:             "micro_dynamics",
			Dimensions:       []int{geneDimBeta, geneDimGamma},
			QuantizationStep: []float64{0.1, 0.1},
			GeneStep:         []float64{0.3, 0.2},
			IsCritical:       true,
			Description:      "Sigmoid β 与库存偏置 γ",
		},
		{
			Name: "feature_periods",
			Dimensions: []int{
				geneDimEMAShortPeriod, geneDimEMALongPeriod,
				geneDimMAVShortPeriod, geneDimMAVLongPeriod,
			},
			QuantizationStep: []float64{1, 1, 1, 1},
			GeneStep:         []float64{5, 10, 5, 10},
			IsCritical:       true,
			Description:      "EMA 与 MAV 的短长周期; Clamp 后强制 short < long",
		},
		{
			Name:             "state_thresholds",
			Dimensions:       []int{geneDimQuietThreshold, geneDimMicroReservePct},
			QuantizationStep: []float64{0.05, 0.02},
			GeneStep:         []float64{0.1, 0.05},
			IsCritical:       false,
			Description:      "市场状态阈值与现金保留比例",
		},
		{
			Name:             "macro_release",
			Dimensions:       []int{geneDimMacroInjectUSD, geneDimReleaseDrawdownThreshold},
			QuantizationStep: []float64{10, 0.02},
			GeneStep:         []float64{50, 0.05},
			IsCritical:       false,
			Description:      "月度注入金额与 DeadBTC 释放回撤阈值",
		},
	}
}

// clampOne applies the spec §4.3 Step-1 bound clipping for a single dim.
// Kept tiny and inlined into clampGene below; pulled out only because
// callers writing fixture-style tests want it.
func clampOne(v, lo, hi float64) float64 {
	return quant.ClipFloat64(v, lo, hi)
}

// bounds is the lo/hi pair for every gene dimension; indexed by geneDim*
// constants. Populated once; same source-of-truth as the spec §4.1 table.
var bounds = [GeneDim][2]float64{
	geneDimA1:                       {minA1, maxA1},
	geneDimA2:                       {minA2, maxA2},
	geneDimA3:                       {minA3, maxA3},
	geneDimBeta:                     {minBeta, maxBeta},
	geneDimGamma:                    {minGamma, maxGamma},
	geneDimEMAShortPeriod:           {minEMAShortPeriod, maxEMAShortPeriod},
	geneDimEMALongPeriod:            {minEMALongPeriod, maxEMALongPeriod},
	geneDimMAVShortPeriod:           {minMAVShortPeriod, maxMAVShortPeriod},
	geneDimMAVLongPeriod:            {minMAVLongPeriod, maxMAVLongPeriod},
	geneDimQuietThreshold:           {minQuietThreshold, maxQuietThreshold},
	geneDimMicroReservePct:          {minMicroReservePct, maxMicroReservePct},
	geneDimMacroInjectUSD:           {minMacroInjectUSD, maxMacroInjectUSD},
	geneDimReleaseDrawdownThreshold: {minReleaseDrawdownThreshold, maxReleaseDrawdownThreshold},
}
