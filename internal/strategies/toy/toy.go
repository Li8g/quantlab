// Package toy is a PLACEHOLDER EvolvableStrategy used exclusively to
// validate the 14-verb interface contract (Phase 5A) and exercise the
// downstream GA engine (Phase 5B/5C) end-to-end before any real strategy
// exists.
//
// ┌──────────────────────────────────────────────────────────────────────┐
// │  [TOY v1 — DELETE OR REPLACE before any real Promote workflow ships. │
// │                                                                      │
// │  Replacement plan:                                                   │
// │    1. Implement the sigmoid-balance strategy per                     │
// │       docs/策略数学引擎.md §5 (核心公式) and §7 (chromosome).         │
// │    2. New package: internal/strategies/sigmoid/ (or similar).        │
// │    3. Switch the cmd/saas wire-up from `toy.New()` to the real one.  │
// │    4. Remove this entire package directory.                          │
// │                                                                      │
// │  Search markers (grep before shipping):                              │
// │    - "TOY v1"                                                        │
// │    - "toy-validation"   (StrategyID)                                 │
// │    - internal/strategies/toy/   (whole package)                      │
// └──────────────────────────────────────────────────────────────────────┘
//
// Gene semantics (deliberately trivial):
//   gene[0] ∈ [0, 1],  target = 0.42  (segment "alpha", IsCritical=true)
//   gene[1] ∈ [-1, 1], target = -0.3  (segment "beta",  IsCritical=false)
//
// Fitness landscape:
//   score = -(|gene[0] - 0.42| + |gene[1] - (-0.3)|)  ∈ (-∞, 0]
//
// The toy is plan-independent: it ignores EvaluablePlan.Windows[*].Bars
// and returns the same SliceScore for every window. This is acceptable
// because the toy's purpose is to test GA mechanics (convergence,
// determinism, isolation), not strategy behaviour.
package toy

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
)

const (
	// StrategyID is deliberately distinct from any plausible real strategy
	// identifier so leakage into production data is grep-detectable.
	// [TOY v1]
	StrategyID = "toy-validation"

	targetAlpha = 0.42
	targetBeta  = -0.3

	minAlpha = 0.0
	maxAlpha = 1.0
	minBeta  = -1.0
	maxBeta  = 1.0

	geneDimAlpha = 0
	geneDimBeta  = 1
	geneDim      = 2
)

// Toy implements strategy.EvolvableStrategy with a stateless 2-segment,
// 2-dimensional landscape. [TOY v1 — placeholder; see package doc.]
type Toy struct{}

// New constructs a Toy. Stateless; safe to share across workers (per-
// worker Adapter holds the only mutable state, which is also empty).
func New() *Toy { return &Toy{} }

// Compile-time conformance assertions.
var (
	_ strategy.EvolvableStrategy = (*Toy)(nil)
	_ strategy.Adapter           = (*toyAdapter)(nil)
)

func (t *Toy) StrategyID() string { return StrategyID }

func (t *Toy) Segments() []domain.SegmentInfo {
	return []domain.SegmentInfo{
		{
			Name:             "alpha",
			Dimensions:       []int{geneDimAlpha},
			QuantizationStep: []float64{0.001},
			GeneStep:         []float64{0.05},
			IsCritical:       true,
			Description:      "toy v1: target=0.42, range=[0,1]",
		},
		{
			Name:             "beta",
			Dimensions:       []int{geneDimBeta},
			QuantizationStep: []float64{0.001},
			GeneStep:         []float64{0.05},
			IsCritical:       false,
			Description:      "toy v1: target=-0.3, range=[-1,1]",
		},
	}
}

func (t *Toy) Sample(rng *rand.Rand) domain.Gene {
	g := domain.Gene{
		minAlpha + rng.Float64()*(maxAlpha-minAlpha),
		minBeta + rng.Float64()*(maxBeta-minBeta),
	}
	return t.Clamp(g)
}

// Clamp pads/truncates to expected dim then clips per-dim ranges, so
// Validate(Clamp(g)) == nil for any input (per EvolvableStrategy contract).
func (t *Toy) Clamp(g domain.Gene) domain.Gene {
	out := make(domain.Gene, geneDim)
	copy(out, g)
	out[geneDimAlpha] = clip(out[geneDimAlpha], minAlpha, maxAlpha)
	out[geneDimBeta] = clip(out[geneDimBeta], minBeta, maxBeta)
	return out
}

func (t *Toy) Validate(g domain.Gene) error {
	if len(g) != geneDim {
		return fmt.Errorf("toy: gene dim = %d, want %d", len(g), geneDim)
	}
	if g[geneDimAlpha] < minAlpha || g[geneDimAlpha] > maxAlpha {
		return fmt.Errorf("toy: alpha %g out of [%g,%g]", g[geneDimAlpha], minAlpha, maxAlpha)
	}
	if g[geneDimBeta] < minBeta || g[geneDimBeta] > maxBeta {
		return fmt.Errorf("toy: beta %g out of [%g,%g]", g[geneDimBeta], minBeta, maxBeta)
	}
	return nil
}

// Crossover: for each segment, flip a 50/50 coin to inherit from p1 or p2
// wholesale. The fallback path (validate-failure → parent clone) is wired
// in so real strategies can use this as a reference; the toy itself never
// hits the fallback because Clamp always produces a valid child.
func (t *Toy) Crossover(p1, p2 domain.Gene, rng *rand.Rand) domain.Gene {
	child := make(domain.Gene, geneDim)
	for _, seg := range t.Segments() {
		from := p1
		if rng.Float64() < 0.5 {
			from = p2
		}
		for _, idx := range seg.Dimensions {
			child[idx] = from[idx]
		}
	}
	child = t.Clamp(child)
	if err := t.Validate(child); err != nil {
		if rng.Float64() < 0.5 {
			return append(domain.Gene(nil), p1...)
		}
		return append(domain.Gene(nil), p2...)
	}
	return child
}

// Mutate: independent Bernoulli(prob) per dim; perturbation is
// NormFloat64() * GeneStep[i] * scale; result clamped before return.
func (t *Toy) Mutate(g domain.Gene, prob, scale float64, rng *rand.Rand) domain.Gene {
	child := append(domain.Gene(nil), g...)
	for _, seg := range t.Segments() {
		for localIdx, geneIdx := range seg.Dimensions {
			if rng.Float64() < prob {
				delta := rng.NormFloat64() * seg.GeneStep[localIdx] * scale
				child[geneIdx] += delta
			}
		}
	}
	return t.Clamp(child)
}

// Fingerprint: per-segment quantize then FNV-1a-64 over little-endian
// IEEE-754 bytes; lower-hex 16 chars.
func (t *Toy) Fingerprint(g domain.Gene) string {
	h := fnv.New64a()
	var buf [8]byte
	for _, seg := range t.Segments() {
		for localIdx, geneIdx := range seg.Dimensions {
			step := seg.QuantizationStep[localIdx]
			q := math.Round(g[geneIdx]/step) * step
			binary.LittleEndian.PutUint64(buf[:], math.Float64bits(q))
			h.Write(buf[:])
		}
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

// Evaluate ignores plan.Windows[i].Bars (toy is plan-independent) and
// returns the same SliceScore for every window. ScoreTotal is NOT set —
// engine fills it via fitness.AggregateScoreTotal (§5.7).
func (t *Toy) Evaluate(
	_ context.Context, g domain.Gene, plan *domain.EvaluablePlan,
) (*resultpkg.RawEvaluateResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("toy: nil plan")
	}
	if err := t.Validate(g); err != nil {
		return nil, err
	}
	score := -(math.Abs(g[geneDimAlpha]-targetAlpha) + math.Abs(g[geneDimBeta]-targetBeta))
	windows := make([]resultpkg.CrucibleResult, 0, len(plan.Windows))
	for _, w := range plan.Windows {
		v := score
		windows = append(windows, resultpkg.CrucibleResult{
			Window: w.Name,
			Score: resultpkg.SliceScore{
				Fatal: false,
				Value: &v,
			},
			BarsEvaluated: len(w.Bars),
		})
	}
	return &resultpkg.RawEvaluateResult{
		Windows: windows,
		FrictionActual: resultpkg.FrictionActual{
			TakerFeeBPS: plan.Friction.TakerFeeBPS,
			SlippageBPS: plan.Friction.SlippageBPS,
		},
	}, nil
}

// ReviewBacktest is a prototype no-op per phase plan §5A.
func (t *Toy) ReviewBacktest(
	_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan,
) (*resultpkg.ReviewSummary, error) {
	return nil, nil
}

// EncodeResult stitches engine-supplied layers into the five-layer package.
// Toy fills core.ChampionGene (its private JSON encoding) and seeds
// promote.DecisionStatus = pending; engine inputs are taken verbatim.
func (t *Toy) EncodeResult(
	g domain.Gene,
	spawn resultpkg.SpawnPointPayload,
	repro resultpkg.ReproducibilityMetadata,
	gaConfig resultpkg.GAConfigSnapshot,
	eval *resultpkg.EvaluationLayer,
	verif *resultpkg.VerificationLayer,
	diag *resultpkg.DiagnosticsLayer,
) (resultpkg.ChallengerResultPackage, error) {
	payload, err := json.Marshal(g)
	if err != nil {
		return resultpkg.ChallengerResultPackage{}, fmt.Errorf("toy: encode gene: %w", err)
	}
	pkg := resultpkg.ChallengerResultPackage{
		Core: resultpkg.ResultCore{
			StrategyID: t.StrategyID(),
			ChampionGene: resultpkg.ChampionGenePayload{
				Encoding: resultpkg.GeneEncodingJSON,
				Payload:  payload,
			},
			SpawnPoint:              spawn,
			ReproducibilityMetadata: repro,
			GAConfig:                gaConfig,
			SchemaVersion:           repro.SchemaVersion,
			FitnessVersion:          repro.FitnessVersion,
			FingerprintVersion:      repro.FingerprintVersion,
		},
		Promote: resultpkg.PromoteLayer{DecisionStatus: resultpkg.DecisionStatusPending},
	}
	if eval != nil {
		pkg.Evaluation = *eval
	}
	if verif != nil {
		pkg.Verification = *verif
	}
	if diag != nil {
		pkg.Diagnostics = *diag
	}
	return pkg, nil
}

func (t *Toy) DecodeElite(blob resultpkg.ChampionGenePayload) (domain.Gene, error) {
	if blob.Encoding != resultpkg.GeneEncodingJSON {
		return nil, fmt.Errorf("toy: unsupported encoding %q", blob.Encoding)
	}
	var g domain.Gene
	if err := json.Unmarshal(blob.Payload, &g); err != nil {
		return nil, fmt.Errorf("toy: decode gene: %w", err)
	}
	if err := t.Validate(g); err != nil {
		return nil, fmt.Errorf("toy: validate decoded gene: %w", err)
	}
	return g, nil
}

// MinEvalBars: toy ignores bars entirely; return 1 so the engine accepts
// any non-empty window. Real strategies should return
// longestEMAWindow + shortestStatStabilizationPeriod (per §4.4).
func (t *Toy) MinEvalBars() int { return 1 }

func (t *Toy) NewAdapter(plan *domain.EvaluablePlan) (strategy.Adapter, error) {
	return &toyAdapter{strat: t, plan: plan}, nil
}

// toyAdapter is stateless. Reset is a no-op since there is nothing to
// clear. [TOY v1] Real strategies clear position state, indicator caches,
// trade-history caches, and consecutive-loss counters here.
type toyAdapter struct {
	strat *Toy
	plan  *domain.EvaluablePlan
}

func (a *toyAdapter) Reset(plan *domain.EvaluablePlan) error {
	a.plan = plan
	return nil
}

func (a *toyAdapter) Evaluate(g domain.Gene) (*resultpkg.RawEvaluateResult, error) {
	return a.strat.Evaluate(context.Background(), g, a.plan)
}

func (a *toyAdapter) Close() error { return nil }

func clip(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
