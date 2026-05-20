// EvolvableStrategy and Adapter interfaces — the 14-verb hard boundary
// between the engine layer and any concrete strategy (Phase 5A).
//
// Reference: docs/进化计算引擎.md §5 (14-verb interface table); phase plan
// docs/Coding-plan-dev-phases-prompts_v3_2_2.md Phase 5A.
//
// Type-level invariant (v3.2 M14 / v5.4.1 P09):
// RawEvaluateResult deliberately omits ScoreTotal. The strategy layer
// physically cannot write the aggregate score; engine code calls
// fitness.AggregateScoreTotal on the strategy's RawEvaluateResult and
// stitches the resulting ScoreTotal into EvaluationLayer in RunEpoch.
// See §5.7 of the source-of-truth doc.
package strategy

import (
	"context"
	"math/rand"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
)

// EvolvableStrategy is the 14-verb contract every strategy must satisfy.
// The engine layer holds only this interface and never reaches into a
// concrete strategy's package (CLAUDE.md "two-layer hard boundary").
//
// Determinism / isolation requirements that the engine relies on:
//   - Sample / Clamp / Crossover / Mutate / Fingerprint are pure functions
//     of their inputs plus rng (no global mutable state, no wall-clock).
//   - Evaluate may not launch goroutines; float accumulations must be serial.
//   - Mutate's `scale` is a global GeneStep multiplier independent of `prob`
//     (§5.4); strategies must respect both knobs separately.
type EvolvableStrategy interface {
	// StrategyID returns the stable identifier persisted on EvolutionTask
	// and GeneRecord rows. Changing this value is a migration event.
	StrategyID() string

	// Segments returns the same slice in the same order throughout the
	// strategy's lifetime (used for block crossover, fingerprint
	// quantization, and per-dimension mutation step).
	Segments() []domain.SegmentInfo

	// Sample draws a Gene uniformly from the legal space, calling Clamp
	// before returning so the caller never sees an out-of-bounds value.
	Sample(rng *rand.Rand) domain.Gene

	// Clamp repairs out-of-range values AND structural constraints
	// (weight normalization, exclusivity, discrete-tier mapping, integer
	// rounding). See §5.2.
	Clamp(gene domain.Gene) domain.Gene

	// Validate reports whether a Gene satisfies all hard constraints.
	// Engine code may rely on Validate(Clamp(g)) == nil for any input.
	Validate(gene domain.Gene) error

	// Crossover performs block-orthogonal crossover per Segments(): for
	// each Segment, flip a coin to inherit from p1 or p2 wholesale (no
	// per-dimension mixing). The implementation must internally fall back
	// to a clone of one parent if the result fails Validate; the engine
	// observes the crossover_fallback event via a diagnostics hook, not
	// via a returned error.
	Crossover(p1, p2 domain.Gene, rng *rand.Rand) domain.Gene

	// Mutate applies independent Bernoulli(prob) per dimension; on a hit
	// the perturbation is NormFloat64() * GeneStep[i] * scale. Result must
	// pass through Clamp before return.
	Mutate(gene domain.Gene, prob, scale float64, rng *rand.Rand) domain.Gene

	// Fingerprint quantizes per SegmentInfo.QuantizationStep then hashes
	// (FNV-1a-64 lower-hex). Used for population dedup and CompareFitness
	// double-Fatal tie-break (must be deterministic for equivalent genes).
	Fingerprint(gene domain.Gene) string

	// Evaluate runs the cascade short-circuit across the plan's windows
	// in the fixed 6m→2y→5y→10y order (resultpkg.AllWindowsInEvalOrder).
	// On Fatal in window N, subsequent windows are recorded with SkippedBy
	// and Value=nil.
	//
	// Returns *RawEvaluateResult which deliberately has no ScoreTotal
	// field — see this file's package-doc header.
	Evaluate(ctx context.Context, gene domain.Gene, plan *domain.EvaluablePlan) (*resultpkg.RawEvaluateResult, error)

	// ReviewBacktest is a post-Promote full-history audit replay; the
	// engine never feeds its output back into GA decisions. Prototype
	// strategies may return (nil, nil).
	//
	// [INVENTED v1 — Part III-2 (Audit phase) will formalize signature
	// and result fields. Using *EvaluablePlan for symmetry with Evaluate;
	// the engine can build a single all-history window when calling.]
	ReviewBacktest(ctx context.Context, gene domain.Gene, plan *domain.EvaluablePlan) (*resultpkg.ReviewSummary, error)

	// EncodeResult stitches the engine-supplied layers into the five-layer
	// ChallengerResultPackage. The strategy is responsible for:
	//   - Encoding `gene` into core.ChampionGene (only the strategy knows
	//     its gene JSON schema; encoding MUST equal GeneEncodingJSON during
	//     the prototype phase).
	//   - Setting core.StrategyID from StrategyID().
	//   - Initializing promote.DecisionStatus = DecisionStatusPending.
	// Everything else is taken verbatim from the engine inputs.
	//
	// The phase plan suggested a narrower 5-arg form but it omits
	// ReproducibilityMetadata and GAConfigSnapshot, both of which the
	// strategy needs to populate core. Expanded here.
	EncodeResult(
		gene domain.Gene,
		spawn resultpkg.SpawnPointPayload,
		repro resultpkg.ReproducibilityMetadata,
		gaConfig resultpkg.GAConfigSnapshot,
		eval *resultpkg.EvaluationLayer,
		verif *resultpkg.VerificationLayer,
		diag *resultpkg.DiagnosticsLayer,
	) (resultpkg.ChallengerResultPackage, error)

	// DecodeElite reverses EncodeResult's gene serialization, lifting a
	// historical Champion back into a runnable Gene.
	// Implementations should reject Encoding != GeneEncodingJSON.
	DecodeElite(blob resultpkg.ChampionGenePayload) (domain.Gene, error)

	// MinEvalBars is the lower bound on bars (including warmup) required
	// for a stable evaluation: longest EMA window + shortest statistic
	// stabilization period. Engine uses this to fatal a window early when
	// EvaluablePlan does not supply enough bars.
	MinEvalBars() int

	// NewAdapter creates a strategy-side Adapter for one worker. Each
	// worker owns its Adapter for the Epoch's lifetime; the engine calls
	// adapter.Reset(plan) before every Evaluate (§5.6).
	NewAdapter(plan *domain.EvaluablePlan) (Adapter, error)
}

// Adapter is the per-worker evaluation handle returned by NewAdapter.
// Implementations must satisfy the Reset isolation contract spelled out
// in §5.6 / §5.7: clearing position state, indicator caches, trade-history
// caches, and any consecutive-loss counters; allowed to retain only data
// that is independent of the current Gene (K-line buffers, DCABaseline
// caches, preallocated scratch buffers that are zeroed on first write).
//
// TestAdapterResetIsolation enforces this contract empirically.
type Adapter interface {
	// Reset is invoked by the engine before every Evaluate call.
	// Incomplete Reset breaks evaluation-order invariance and therefore
	// breaks the tolerance-level determinism contract.
	Reset(plan *domain.EvaluablePlan) error

	// Evaluate runs one Gene against the plan. Returns *RawEvaluateResult
	// (no ScoreTotal — see package doc); aggregation is the engine's job.
	Evaluate(gene domain.Gene) (*resultpkg.RawEvaluateResult, error)

	// Close releases any resources owned by this Adapter (no-op for most
	// pure-Go strategies). Called once per worker at Epoch teardown.
	Close() error
}
