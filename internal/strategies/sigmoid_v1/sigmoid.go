// Package sigmoid_v1 implements the first real EvolvableStrategy for
// QuantLab. Source of truth: docs/strategies/sigmoid_v1.md.
//
// Phase 4 is split across four commits matching the spec:
//
//   4a  this file + chromosome.go: GA-contract verbs (Segments, Sample,
//       Clamp, Validate, Crossover, Mutate, Fingerprint, MinEvalBars,
//       StrategyID). The remaining five verbs (Evaluate, ReviewBacktest,
//       EncodeResult, DecodeElite, NewAdapter) are stubbed and return
//       errPhase4Pending so callers learn loudly which milestone is
//       gating them.
//
//   4b  pure-function math: market state, signal synthesis, sigmoid, macro
//       engine, deadBTC release.
//
//   4c  Step() integration + the four remaining encoding verbs.
//
//   4d  Adapter (Reset/Evaluate/Close) + asset-accounting simulator.
//
// CLAUDE.md "two-layer hard boundary": engine code only sees the verbs
// declared in internal/strategy.EvolvableStrategy. The Chromosome type
// and segment constants live in this package and never escape.
package sigmoid_v1

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

// StrategyID is the stable identifier persisted on EvolutionTask and
// GeneRecord rows. Changing this value is a migration event (§13 of the
// spec mandates a new strategy ID on structural changes).
const StrategyID = "sigmoid_v1"

// Phase 4d closed out the last of the EvolvableStrategy stubs; nothing
// in this package now returns "verb not implemented yet" anymore. The
// errPhase4Pending sentinel that lived here through 4a–4c has been
// retired alongside its last caller.

// Sigmoid is the strategy instance. It owns one read-only knob:
// barIntervalMs — the wall-clock interval one bar represents, in
// milliseconds. The value is injected at construction (task creation
// path supplies it from EvolutionTask.interval) and pinned for the
// instance lifetime; mixing intervals within a Champion lineage is
// forbidden by spec §13.
type Sigmoid struct {
	barIntervalMs int64
}

// Compile-time conformance to the engine-facing interfaces. The Adapter
// is stubbed for Phase 4a and replaced in 4d.
var (
	_ strategy.EvolvableStrategy = (*Sigmoid)(nil)
	_ strategy.Adapter           = (*sigmoidAdapter)(nil)
)

// New constructs a Sigmoid pinned to the given bar interval. Pass:
//
//	60_000      for 1m bars
//	3_600_000   for 1h bars
//	86_400_000  for 1d bars
//
// Caller decodes the symbol+interval string into milliseconds; we keep
// the conversion out of this package so it lives in one place (most
// likely cmd/saas wire-up).
//
// Panics on a non-positive interval — that's a programming error in the
// wire-up, not a runtime condition worth a returned error.
func New(barIntervalMs int64) *Sigmoid {
	if barIntervalMs <= 0 {
		panic(fmt.Sprintf("sigmoid_v1.New: barIntervalMs must be > 0, got %d", barIntervalMs))
	}
	return &Sigmoid{barIntervalMs: barIntervalMs}
}

func (s *Sigmoid) StrategyID() string { return StrategyID }

func (s *Sigmoid) Segments() []domain.SegmentInfo { return segmentInfos() }

// MinEvalBars implements spec §8.2 exactly:
//
//	max(navPeakDurationMs/barIntervalMs, MaxChromosomePeriod) + 1.
func (s *Sigmoid) MinEvalBars() int {
	const navPeakDurationMs = int64(30) * 24 * 60 * 60 * 1000
	navPeakBars := int(navPeakDurationMs / s.barIntervalMs)
	if navPeakBars > MaxChromosomePeriod {
		return navPeakBars + 1
	}
	return MaxChromosomePeriod + 1
}

// Sample draws each dimension uniformly within its bounds, then Clamps
// (the only post-Clamp constraint is the integer-rounding + short<long
// fix-up, which uniform sampling doesn't guarantee).
func (s *Sigmoid) Sample(rng *rand.Rand) domain.Gene {
	g := make(domain.Gene, GeneDim)
	for i := 0; i < GeneDim; i++ {
		lo, hi := bounds[i][0], bounds[i][1]
		g[i] = lo + rng.Float64()*(hi-lo)
	}
	return s.Clamp(g)
}

// Clamp implements spec §4.3 three-step repair:
//
//	1. Bound-clip every dimension to [lo, hi].
//	2. feature_periods: round to int, then enforce short < long.
//	3. No cross-segment constraints in v1.
//
// Result is guaranteed to pass Validate (the engine relies on
// Validate(Clamp(g)) == nil).
func (s *Sigmoid) Clamp(g domain.Gene) domain.Gene {
	// Pad short / truncate long inputs to the canonical GeneDim. This
	// lets callers hand-construct partial genes in tests without
	// fighting a length check before Clamp runs.
	out := make(domain.Gene, GeneDim)
	copy(out, g)

	// Step 1: per-dim bound clipping.
	for i := 0; i < GeneDim; i++ {
		out[i] = clampOne(out[i], bounds[i][0], bounds[i][1])
	}

	// Step 2a: feature_periods → integer-valued.
	out[geneDimEMAShortPeriod] = math.Round(out[geneDimEMAShortPeriod])
	out[geneDimEMALongPeriod] = math.Round(out[geneDimEMALongPeriod])
	out[geneDimMAVShortPeriod] = math.Round(out[geneDimMAVShortPeriod])
	out[geneDimMAVLongPeriod] = math.Round(out[geneDimMAVLongPeriod])

	// Step 2b: short < long. If short ≥ long, push long up one tick
	// (and re-clamp to its upper bound — so if short hits its own upper
	// bound we may end up with short == long at the bound, which the
	// next Clamp pass will catch).
	if out[geneDimEMAShortPeriod] >= out[geneDimEMALongPeriod] {
		out[geneDimEMALongPeriod] = clampOne(
			out[geneDimEMAShortPeriod]+1,
			minEMALongPeriod, maxEMALongPeriod,
		)
	}
	if out[geneDimMAVShortPeriod] >= out[geneDimMAVLongPeriod] {
		out[geneDimMAVLongPeriod] = clampOne(
			out[geneDimMAVShortPeriod]+1,
			minMAVLongPeriod, maxMAVLongPeriod,
		)
	}

	return out
}

// Validate returns nil iff Gene satisfies §4.4 hard constraints.
func (s *Sigmoid) Validate(g domain.Gene) error {
	if len(g) != GeneDim {
		return fmt.Errorf("sigmoid_v1: gene dim = %d, want %d", len(g), GeneDim)
	}
	for i := 0; i < GeneDim; i++ {
		if g[i] < bounds[i][0] || g[i] > bounds[i][1] {
			return fmt.Errorf("sigmoid_v1: gene[%d]=%g out of [%g,%g]",
				i, g[i], bounds[i][0], bounds[i][1])
		}
	}
	if g[geneDimEMAShortPeriod] >= g[geneDimEMALongPeriod] {
		return fmt.Errorf("sigmoid_v1: ema_short_period (%g) >= ema_long_period (%g)",
			g[geneDimEMAShortPeriod], g[geneDimEMALongPeriod])
	}
	if g[geneDimMAVShortPeriod] >= g[geneDimMAVLongPeriod] {
		return fmt.Errorf("sigmoid_v1: mav_short_period (%g) >= mav_long_period (%g)",
			g[geneDimMAVShortPeriod], g[geneDimMAVLongPeriod])
	}
	return nil
}

// Crossover does block-orthogonal segment swap, then Clamp + Validate. On
// validation failure (which Clamp should prevent but we keep the
// safety-net path so the contract is honoured), fall back to a parent
// clone — the engine observes the fallback via diagnostics, never via a
// returned error.
func (s *Sigmoid) Crossover(p1, p2 domain.Gene, rng *rand.Rand) domain.Gene {
	child := make(domain.Gene, GeneDim)
	for _, seg := range s.Segments() {
		src := p1
		if rng.Float64() < 0.5 {
			src = p2
		}
		for _, idx := range seg.Dimensions {
			child[idx] = src[idx]
		}
	}
	child = s.Clamp(child)
	if err := s.Validate(child); err != nil {
		if rng.Float64() < 0.5 {
			return append(domain.Gene(nil), p1...)
		}
		return append(domain.Gene(nil), p2...)
	}
	return child
}

// Mutate: per-dim Bernoulli(prob) * Gaussian(0, GeneStep * scale).
// All clamping is deferred to the final Clamp pass so individual
// dimensions can drift past their bounds during perturbation without
// invalidating the move.
func (s *Sigmoid) Mutate(g domain.Gene, prob, scale float64, rng *rand.Rand) domain.Gene {
	child := append(domain.Gene(nil), g...)
	if len(child) < GeneDim {
		child = append(child, make(domain.Gene, GeneDim-len(child))...)
	}
	for _, seg := range s.Segments() {
		for localIdx, geneIdx := range seg.Dimensions {
			if rng.Float64() < prob {
				delta := rng.NormFloat64() * seg.GeneStep[localIdx] * scale
				child[geneIdx] += delta
			}
		}
	}
	return s.Clamp(child)
}

// Fingerprint mirrors the toy strategy's algorithm verbatim so the
// quantization-then-FNV scheme is consistent across strategies:
// per-segment quantize → FNV-1a-64 over IEEE-754 little-endian bytes →
// lower-hex 16 chars.
func (s *Sigmoid) Fingerprint(g domain.Gene) string {
	h := fnv.New64a()
	var buf [8]byte
	for _, seg := range s.Segments() {
		for localIdx, geneIdx := range seg.Dimensions {
			step := seg.QuantizationStep[localIdx]
			q := math.Round(g[geneIdx]/step) * step
			binary.LittleEndian.PutUint64(buf[:], math.Float64bits(q))
			h.Write(buf[:])
		}
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

// ===== Phase 4d cascade verb =====

// Evaluate is the engine-facing entry point. It mirrors toy.go's
// pattern of a thin wrapper around the adapter — NewAdapter + Reset
// + Evaluate — so callers that don't want to manage Adapter
// lifetimes (one-shot scoring, integration tests) can drive a single
// gene through the four-window cascade with one call.
//
// Production worker pools that batch many genes through one adapter
// should call NewAdapter / Reset / Evaluate directly to amortise the
// adapter allocation.
func (s *Sigmoid) Evaluate(_ context.Context, gene domain.Gene, plan *domain.EvaluablePlan) (*resultpkg.RawEvaluateResult, error) {
	a, err := s.NewAdapter(plan)
	if err != nil {
		return nil, err
	}
	if err := a.Reset(plan); err != nil {
		return nil, err
	}
	return a.Evaluate(gene)
}

// ===== Phase 4c encoding verbs =====

// ReviewBacktest is a prototype no-op per phase plan §5A and §10 of
// the sigmoid_v1 spec. Returning (nil, nil) matches the toy strategy
// convention; the engine treats both forms identically and stores no
// ReviewSummary on the result package.
//
// A real implementation would replay the full history under the
// elected gene and emit alpha breakdown / DSR / stress diagnostics.
// That's Audit-phase work, deferred per the upstream `[INVENTED v1]`
// note on EvolvableStrategy.ReviewBacktest.
func (s *Sigmoid) ReviewBacktest(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.ReviewSummary, error) {
	return nil, nil
}

// EncodeResult stitches the engine-supplied layers into the five-layer
// ChallengerResultPackage. The strategy owns:
//
//   - Encoding `gene` into core.ChampionGene. Sigmoid uses the same
//     scheme as toy.go (JSON-marshalled []float64) per the prototype
//     contract that Encoding must equal resultpkg.GeneEncodingJSON.
//   - Stamping core.StrategyID.
//   - Initialising promote.DecisionStatus = DecisionStatusPending so
//     the human Promote workflow has the right starting state.
//
// Everything else is taken verbatim from the engine inputs:
// SpawnPoint, ReproducibilityMetadata, GAConfig, and the three
// already-assembled layers (eval / verif / diag). Schema /
// fitness / fingerprint version mirror ReproducibilityMetadata so
// resultpkg validate.go's cross-field equality checks pass.
//
// nil eval / verif / diag pointers are treated as "this layer is
// empty" rather than as errors — the engine may legitimately call
// EncodeResult before all layers are populated (e.g. when a Fatal
// short-circuits before verification runs).
func (s *Sigmoid) EncodeResult(
	gene domain.Gene,
	spawn resultpkg.SpawnPointPayload,
	repro resultpkg.ReproducibilityMetadata,
	gaConfig resultpkg.GAConfigSnapshot,
	eval *resultpkg.EvaluationLayer,
	verif *resultpkg.VerificationLayer,
	diag *resultpkg.DiagnosticsLayer,
) (resultpkg.ChallengerResultPackage, error) {
	payload, err := json.Marshal(gene)
	if err != nil {
		return resultpkg.ChallengerResultPackage{}, fmt.Errorf("sigmoid_v1: encode gene: %w", err)
	}
	pkg := resultpkg.ChallengerResultPackage{
		Core: resultpkg.ResultCore{
			StrategyID: s.StrategyID(),
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

// DecodeElite reverses EncodeResult's gene serialization. The strategy
// rejects encodings it does not understand (only JSON is legal in v1
// per spec §13) and re-runs Validate on the decoded gene so that a
// corrupted result package can't slip a stale or out-of-range Gene
// back into a future evaluation.
func (s *Sigmoid) DecodeElite(blob resultpkg.ChampionGenePayload) (domain.Gene, error) {
	if blob.Encoding != resultpkg.GeneEncodingJSON {
		return nil, fmt.Errorf("sigmoid_v1: unsupported gene encoding %q", blob.Encoding)
	}
	var g domain.Gene
	if err := json.Unmarshal(blob.Payload, &g); err != nil {
		return nil, fmt.Errorf("sigmoid_v1: decode gene: %w", err)
	}
	if err := s.Validate(g); err != nil {
		return nil, fmt.Errorf("sigmoid_v1: validate decoded gene: %w", err)
	}
	return g, nil
}

func (s *Sigmoid) NewAdapter(plan *domain.EvaluablePlan) (strategy.Adapter, error) {
	return &sigmoidAdapter{strat: s, plan: plan}, nil
}

// sigmoidAdapter is the per-worker evaluation handle. It holds a
// pointer to the parent Sigmoid (read-only) and a plan that the
// engine refreshes via Reset before every Evaluate. v1 keeps no
// gene-derived caches between Evaluate calls — evaluateWindow starts
// with a cold-start Portfolio + empty RuntimeState — so Reset only
// has to swap the plan pointer. Adding caches later requires
// expanding Reset to clear them per the §5.6 isolation contract.
type sigmoidAdapter struct {
	strat *Sigmoid
	plan  *domain.EvaluablePlan
}

// Reset honours the §5.6 contract by re-pointing the adapter at the
// latest plan and clearing any gene-derived state. v1 has none to
// clear; the call is effectively `a.plan = plan`.
func (a *sigmoidAdapter) Reset(plan *domain.EvaluablePlan) error {
	a.plan = plan
	return nil
}

// Evaluate runs the four-window cascade in the canonical
// 6m→2y→5y→10y order, regardless of the order plan.Windows happens
// to be in. Each iteration:
//
//   - find the matching plan.Windows entry by name (skip if absent);
//   - if a prior window already Fataled, append a cascade-skip
//     CrucibleResult with the appropriate SkippedBy enum;
//   - otherwise invoke evaluateWindow and stamp the result;
//   - if the result is Fatal, set the cascade marker for subsequent
//     windows.
//
// Returns *RawEvaluateResult. ScoreTotal is engine-only — see the
// strategy.evolvable.go package-doc rationale.
func (a *sigmoidAdapter) Evaluate(gene domain.Gene) (*resultpkg.RawEvaluateResult, error) {
	if a.plan == nil {
		return nil, fmt.Errorf("sigmoid_v1: Adapter.Evaluate called before Reset (no plan)")
	}
	byName := make(map[resultpkg.WindowName]domain.CrucibleWindow, len(a.plan.Windows))
	for _, w := range a.plan.Windows {
		byName[w.Name] = w
	}

	results := make([]resultpkg.CrucibleResult, 0, 4)
	var totalBars int
	var cascadeFrom resultpkg.SkippedBy // "" until a Fatal triggers cascade
	// AllWindowsInEvalOrder() yields 6m→2y→5y→10y. Stats are kept
	// only from the last non-Fatal window in that order, which is by
	// construction the longest non-Fatal window — §I-4.2 "T = 回测
	// horizon" picks the longest available horizon.
	var longestStats *resultpkg.SharpeStats

	for _, name := range resultpkg.AllWindowsInEvalOrder() {
		w, ok := byName[name]
		if !ok {
			continue
		}
		if cascadeFrom != "" {
			skip := cascadeFrom
			results = append(results, resultpkg.CrucibleResult{
				Window:    name,
				Score:     resultpkg.SliceScore{Fatal: false, Value: nil},
				SkippedBy: &skip,
			})
			continue
		}
		res, stats, err := evaluateWindow(a.strat, gene, w, a.plan.Friction)
		if err != nil {
			return nil, fmt.Errorf("sigmoid_v1: window %q: %w", name, err)
		}
		results = append(results, res)
		totalBars += res.BarsEvaluated
		if stats != nil {
			longestStats = stats
		}
		if res.Score.Fatal {
			switch name {
			case resultpkg.Window6M:
				cascadeFrom = resultpkg.SkippedByCascadeFrom6M
			case resultpkg.Window2Y:
				cascadeFrom = resultpkg.SkippedByCascadeFrom2Y
			case resultpkg.Window5Y:
				cascadeFrom = resultpkg.SkippedByCascadeFrom5Y
				// A 10y Fatal has nothing to cascade into; leave
				// cascadeFrom empty.
			}
		}
	}

	return &resultpkg.RawEvaluateResult{
		Windows: results,
		FrictionActual: resultpkg.FrictionActual{
			TakerFeeBPS: a.plan.Friction.TakerFeeBPS,
			SlippageBPS: a.plan.Friction.SlippageBPS,
		},
		BarsEvaluated:      totalBars,
		LongestWindowStats: longestStats,
	}, nil
}

func (a *sigmoidAdapter) Close() error { return nil }
