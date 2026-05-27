// Package engine implements the GA evolution loop (Phase 5B minimum).
//
// What is included (toy-convergence scope):
//   - Population init via strategy.Sample
//   - Parallel evaluation with one Adapter per worker; Reset is called
//     before every Evaluate (per docs/进化计算引擎.md §5.6 / §5.7).
//   - Population-level operations (sort, tournament, crossover, mutate)
//     run serially on the main goroutine using the master RNG.
//   - CompareFitness sort via sort.SliceStable with fingerprint
//     tiebreaker for double-Fatal pairs (per phase plan §1619).
//   - ScoreTotal aggregation delegated to fitness.AggregateScoreTotal.
//
// What is NOT included (deferred to Phase 5.5 / 5D):
//   - Mutation ramp, early-stop, diversity rescue (layers 1 & 2).
//   - Repository writes, SharpeBank, DSR, Fatal-audit sampling.
//   - Full ChallengerResultPackage assembly (returned as EpochResult only).
//   - Worker-local RNG isolation (not needed by deterministic toy; revisit
//     when strategies with stochastic Evaluate exist).
//   - EvaluablePlan construction from real K-line data (Phase 5C/1.5).
package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"sort"
	"strconv"
	"sync"

	"quantlab/internal/domain"
	"quantlab/internal/fitness"
	"quantlab/internal/quant"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
)

// EngineConfig holds the per-Epoch knobs. Defaults match Part I §I-5.
//
// Mutation ramp + early-stop (§I-3.12 layer 1):
//   - When the per-generation best fails to improve by EarlyStopMinDelta,
//     a no-improvement counter increments AND the current mutation
//     probability/scale are multiplied by MutationRampFactor, clamped to
//     MutationProbabilityMax / MutationScaleMax.
//   - Any subsequent improvement resets the counter to 0 and the
//     mutation parameters back to their base values.
//   - When the counter reaches EarlyStopPatience, the generation loop
//     exits before MaxGenerations.
//
// Setting EarlyStopPatience = 0 disables early-stop and the ramp; the
// engine falls back to fixed-mutation behaviour and runs all
// MaxGenerations.
type EngineConfig struct {
	PopSize             int
	MaxGenerations      int
	EliteRatio          float64
	TournamentSize      int
	MutationProbability float64
	MutationScale       float64
	LambdaCons          float64
	EpochSeed           int64

	// Layer 1 convergence rescue (§I-3.12). Zero values for any of the
	// ramp/patience fields disable the corresponding behaviour, which
	// is what the v3.1 "prototype phase only requires layer 1" line
	// allows callers to opt out of for unit tests.
	MutationProbabilityMax float64
	MutationScaleMax       float64
	MutationRampFactor     float64
	EarlyStopPatience      int
	EarlyStopMinDelta      float64

	// FatalAuditSampleRate gates §I-3.12 fatal-audit sampling. For
	// every gene that aggregates to ScoreTotal.Fatal=true, a per-gene
	// Bernoulli(rate) consumes one sampleRng tick to decide whether
	// to capture an AuditSampleSummary into EpochResult.FatalAuditSamples.
	// 0 disables sampling entirely; values >= 1 capture every Fatal
	// gene. Default 0.05 matches the spec's "5% 抽样" wording.
	FatalAuditSampleRate float64

	// OnProgress is called after the per-generation sort with the
	// best individual. Optional.
	OnProgress func(gen int, bestFp string, best resultpkg.ScoreTotal)

	// OnGenerationEvaluated fires once per generation after the pop has
	// been evaluated and fingerprinted, before the per-generation sort.
	// Receives the full (pop, scores, raws, fingerprints) slices in
	// index order so the caller can persist a per-individual audit
	// trail (see saas/epoch wires this into EvaluationTraceRepo). The
	// slices reference engine-owned memory; the caller must not mutate.
	// Optional.
	OnGenerationEvaluated func(
		gen int,
		pop []domain.Gene,
		scores []resultpkg.ScoreTotal,
		raws []*resultpkg.RawEvaluateResult,
		fingerprints []string,
	)
}

// DefaultConfig returns Part I §I-5 frozen defaults.
func DefaultConfig() EngineConfig {
	return EngineConfig{
		PopSize:                300,
		MaxGenerations:         25,
		EliteRatio:             0.05,
		TournamentSize:         3,
		MutationProbability:    0.15,
		MutationScale:          1.0,
		LambdaCons:             0.3,
		EpochSeed:              1,
		MutationProbabilityMax: 0.55,
		MutationScaleMax:       3.0,
		MutationRampFactor:     1.25,
		EarlyStopPatience:      5,
		EarlyStopMinDelta:      0.001,
		FatalAuditSampleRate:   0.05,
	}
}

// WindowWeights returns the fixed window weights (no renormalization
// downstream — see fitness.AggregateScoreTotal).
func WindowWeights() map[resultpkg.WindowName]float64 {
	return map[resultpkg.WindowName]float64{
		resultpkg.Window6M:  0.10,
		resultpkg.Window2Y:  0.20,
		resultpkg.Window5Y:  0.30,
		resultpkg.Window10Y: 0.40,
	}
}

// EpochResult is what RunEpoch returns. Phase 5D wraps this into a
// full ChallengerResultPackage via engine.BuildChallengerPackage; the
// SaaS Epoch service supplies the BuildContext + persists via
// internal/repository.ChallengerRepo.
//
// BestRawEvaluate is the per-window raw evaluation of BestGene,
// re-computed on the worker pool's first adapter at the end of
// RunEpoch. The recompute step is required because evaluatePopulation
// discards each gene's *RawEvaluateResult immediately after
// fitness.AggregateScoreTotal — we'd have to keep PopSize × MaxGen
// raws around otherwise. Re-evaluating one gene at the end is
// strictly cheaper, and Adapter.Evaluate is contractually pure of
// (gene, plan) (§5.5) so the result matches what produced BestScore.
type EpochResult struct {
	BestGene        domain.Gene
	BestScore       resultpkg.ScoreTotal
	BestFingerprint string
	BestRawEvaluate *resultpkg.RawEvaluateResult
	Generations     int

	// FatalAuditSamples accumulates the §I-3.12 fatal-audit pick across
	// every evaluatePopulation call in this Epoch (initial + per-
	// generation). Sampling is deterministic given EpochSeed +
	// FatalAuditSampleRate: a dedicated sampleRng (seeded from masterRng
	// once on the main goroutine) ticks once per Fatal candidate in
	// index order. Empty when FatalAuditSampleRate == 0 or no Fatal
	// genes appeared.
	FatalAuditSamples []resultpkg.AuditSampleSummary
}

// Engine drives a single EvolvableStrategy through one or more Epochs.
type Engine struct {
	strat strategy.EvolvableStrategy
	cfg   EngineConfig
}

func New(strat strategy.EvolvableStrategy, cfg EngineConfig) *Engine {
	return &Engine{strat: strat, cfg: cfg}
}

// RunEpoch runs MaxGenerations of GA against plan and returns the best
// individual after the final sort.
func (e *Engine) RunEpoch(ctx context.Context, plan *domain.EvaluablePlan) (*EpochResult, error) {
	if plan == nil {
		return nil, errors.New("engine: nil plan")
	}
	if e.cfg.PopSize < 2 {
		return nil, fmt.Errorf("engine: PopSize=%d, need >= 2", e.cfg.PopSize)
	}
	if e.cfg.MaxGenerations < 1 {
		return nil, fmt.Errorf("engine: MaxGenerations=%d, need >= 1", e.cfg.MaxGenerations)
	}

	// Crucible windows must each carry at least MinEvalBars rows — the
	// data-layer plan builder only checks day-span sufficiency, so a
	// fine-grained interval (e.g. 1m) on a thin bar series can yield a
	// window that fits in calendar days yet starves the strategy's
	// internal lookback. Failing fast here turns a silently-spinning
	// epoch into a clean failure with a fixable reason.
	minBars := e.strat.MinEvalBars()
	for _, w := range plan.Windows {
		if len(w.Bars) < minBars {
			return nil, fmt.Errorf(
				"engine: crucible window %q has %d bars, below MinEvalBars=%d for strategy %q",
				w.Name, len(w.Bars), minBars, e.strat.StrategyID(),
			)
		}
	}

	masterRng := rand.New(rand.NewSource(e.cfg.EpochSeed))

	// sampleRng is independent of the GA loop's masterRng — its only
	// consumer is the fatal-audit sampler. Seeding from masterRng.Int63
	// (one tick) keeps the sample selection deterministic per EpochSeed
	// without ever returning to masterRng (so the GA dynamics stay
	// unaffected even when sampling fires).
	sampleRng := rand.New(rand.NewSource(masterRng.Int63()))
	var auditSamples []resultpkg.AuditSampleSummary

	pop := make([]domain.Gene, e.cfg.PopSize)
	for i := range pop {
		pop[i] = e.strat.Sample(masterRng)
	}

	numWorkers := runtime.NumCPU()
	if numWorkers > e.cfg.PopSize {
		numWorkers = e.cfg.PopSize
	}
	adapters := make([]strategy.Adapter, 0, numWorkers)
	for i := 0; i < numWorkers; i++ {
		a, err := e.strat.NewAdapter(plan)
		if err != nil {
			for _, prev := range adapters {
				_ = prev.Close()
			}
			return nil, fmt.Errorf("engine: NewAdapter[%d]: %w", i, err)
		}
		adapters = append(adapters, a)
	}
	defer func() {
		for _, a := range adapters {
			_ = a.Close()
		}
	}()

	scores, raws, err := e.evaluatePopulation(ctx, plan, pop, adapters)
	if err != nil {
		return nil, err
	}
	auditSamples = e.collectFatalSamples(auditSamples, pop, scores, raws, sampleRng, 0)

	var (
		bestIdx    int
		bestFp     string
		conv       = newConvergenceState(e.cfg)
		actualGens int
	)
	for gen := 0; gen < e.cfg.MaxGenerations; gen++ {
		fingerprints := make([]string, len(pop))
		for i := range pop {
			fingerprints[i] = e.strat.Fingerprint(pop[i])
		}
		if e.cfg.OnGenerationEvaluated != nil {
			e.cfg.OnGenerationEvaluated(gen, pop, scores, raws, fingerprints)
		}

		order := makeOrder(len(pop))
		sort.SliceStable(order, func(i, j int) bool {
			return compareWithFp(
				scores[order[i]], scores[order[j]],
				fingerprints[order[i]], fingerprints[order[j]],
			) < 0
		})
		bestIdx = order[0]
		bestFp = fingerprints[bestIdx]
		actualGens = gen + 1

		if e.cfg.OnProgress != nil {
			e.cfg.OnProgress(gen, bestFp, scores[bestIdx])
		}

		// Layer 1 convergence rescue (§I-3.12): observe the new
		// best, ramp mutation params on stagnation, early-stop on
		// patience exhaustion. Updates conv in-place.
		shouldStop := conv.observe(scores[bestIdx], e.cfg)
		if shouldStop {
			break
		}

		// On the final generation we don't need to build the next one.
		if gen == e.cfg.MaxGenerations-1 {
			break
		}

		next := e.produceNextGeneration(pop, scores, fingerprints, order, masterRng, conv.mutProb, conv.mutScale)
		pop = next
		scores, raws, err = e.evaluatePopulation(ctx, plan, pop, adapters)
		if err != nil {
			return nil, err
		}
		auditSamples = e.collectFatalSamples(auditSamples, pop, scores, raws, sampleRng, gen+1)
	}

	// Recover the best gene's *RawEvaluateResult so the SaaS Epoch
	// service (Phase 5D) can build a ChallengerResultPackage without
	// the engine having to retain PopSize × MaxGen raws in memory.
	// Adapter.Evaluate is pure of (gene, plan) per §5.5, so the
	// re-evaluation produces the same Windows that produced
	// scores[bestIdx]. We reuse the first worker's adapter (Reset
	// first to honour the §5.6 isolation contract).
	if err := adapters[0].Reset(plan); err != nil {
		return nil, fmt.Errorf("engine: re-evaluate best: reset: %w", err)
	}
	bestRaw, err := adapters[0].Evaluate(pop[bestIdx])
	if err != nil {
		return nil, fmt.Errorf("engine: re-evaluate best: evaluate: %w", err)
	}

	return &EpochResult{
		BestGene:          append(domain.Gene(nil), pop[bestIdx]...),
		BestScore:         scores[bestIdx],
		BestFingerprint:   bestFp,
		BestRawEvaluate:   bestRaw,
		Generations:       actualGens,
		FatalAuditSamples: auditSamples,
	}, nil
}

// collectFatalSamples is the §I-3.12 sampling kernel called after every
// evaluatePopulation pass. It runs on the main goroutine in idx order
// and consumes one sampleRng.Float64() per Fatal candidate, so the SET
// of samples is fully deterministic given (EpochSeed, FatalAuditSampleRate)
// — independent of worker scheduling.
//
// Rate ≤ 0 short-circuits to a zero-cost no-op (sampleRng untouched).
// Rate ≥ 1 captures every Fatal candidate.
//
// SampleID format "g{evalCallNum}.{fingerprint}" disambiguates samples
// across generations when the same fingerprint reappears (elite copy +
// untouched-by-Mutate offspring is plausible). evalCallNum=0 is the
// initial population's evaluation.
func (e *Engine) collectFatalSamples(
	dst []resultpkg.AuditSampleSummary,
	pop []domain.Gene,
	scores []resultpkg.ScoreTotal,
	raws []*resultpkg.RawEvaluateResult,
	rng *rand.Rand,
	evalCallNum int,
) []resultpkg.AuditSampleSummary {
	if e.cfg.FatalAuditSampleRate <= 0 {
		return dst
	}
	for idx := 0; idx < len(pop); idx++ {
		if !scores[idx].Fatal {
			continue
		}
		if rng.Float64() >= e.cfg.FatalAuditSampleRate {
			continue
		}
		var windows []resultpkg.CrucibleResult
		if raws[idx] != nil {
			windows = raws[idx].Windows
		}
		dst = append(dst, resultpkg.AuditSampleSummary{
			SampleID:     "g" + strconv.Itoa(evalCallNum) + "." + e.strat.Fingerprint(pop[idx]),
			ScoreTotal:   scores[idx],
			WindowScores: windows,
		})
	}
	return dst
}

// evaluatePopulation spreads pop across len(adapters) workers. Each worker
// owns one Adapter for the full pass; the engine calls Reset(plan) before
// every Evaluate, satisfying the §5.6 isolation contract.
//
// Determinism: scores[i] / raws[i] are written only by the worker handling
// i; no cross-index races. Worker-pickup order does not affect final
// scores because Adapter.Evaluate is required to be a pure function of
// (gene, plan) (§5.5).
//
// raws is returned alongside scores so the main goroutine can run the
// §I-3.12 fatal-audit sampler in deterministic idx order. Each raws[i]
// holds the per-gene cascade output (Windows + FrictionActual +
// LongestWindowStats); the slice survives only until the next
// evaluatePopulation call in RunEpoch.
func (e *Engine) evaluatePopulation(
	ctx context.Context,
	plan *domain.EvaluablePlan,
	pop []domain.Gene,
	adapters []strategy.Adapter,
) ([]resultpkg.ScoreTotal, []*resultpkg.RawEvaluateResult, error) {
	scores := make([]resultpkg.ScoreTotal, len(pop))
	raws := make([]*resultpkg.RawEvaluateResult, len(pop))
	weights := WindowWeights()

	jobs := make(chan int, len(pop))
	for i := range pop {
		jobs <- i
	}
	close(jobs)

	errCh := make(chan error, len(adapters))
	var wg sync.WaitGroup
	for _, adapter := range adapters {
		wg.Add(1)
		go func(adapter strategy.Adapter) {
			defer wg.Done()
			for idx := range jobs {
				if err := ctx.Err(); err != nil {
					errCh <- err
					return
				}
				if err := adapter.Reset(plan); err != nil {
					errCh <- fmt.Errorf("adapter.Reset: %w", err)
					return
				}
				raw, err := adapter.Evaluate(pop[idx])
				if err != nil {
					errCh <- fmt.Errorf("adapter.Evaluate[%d]: %w", idx, err)
					return
				}
				scores[idx] = fitness.AggregateScoreTotal(
					raw.Windows, weights, e.cfg.LambdaCons,
					resultpkg.FitnessVersionV1RawStd,
				)
				raws[idx] = raw
			}
		}(adapter)
	}
	wg.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		return nil, nil, err
	}
	return scores, raws, nil
}

// produceNextGeneration builds the next population: deep-copied elites
// followed by Tournament → Crossover → Mutate offspring filling the rest.
// All operations use the master RNG and run on the calling goroutine.
//
// mutProb / mutScale come from the convergence-state ramp (§I-3.12 layer
// 1) rather than the static cfg values, so a stagnating GA explores more
// aggressively as patience drops.
func (e *Engine) produceNextGeneration(
	pop []domain.Gene,
	scores []resultpkg.ScoreTotal,
	fingerprints []string,
	order []int,
	rng *rand.Rand,
	mutProb, mutScale float64,
) []domain.Gene {
	n := len(pop)
	nElite := int(float64(n) * e.cfg.EliteRatio)
	if nElite < 1 {
		nElite = 1
	}
	if nElite > n {
		nElite = n
	}

	next := make([]domain.Gene, 0, n)
	for i := 0; i < nElite; i++ {
		next = append(next, append(domain.Gene(nil), pop[order[i]]...))
	}
	for len(next) < n {
		p1 := e.tournamentSelect(rng, scores, fingerprints)
		p2 := e.tournamentSelect(rng, scores, fingerprints)
		child := e.strat.Crossover(pop[p1], pop[p2], rng)
		child = e.strat.Mutate(child, mutProb, mutScale, rng)
		next = append(next, child)
	}
	return next
}

// tournamentSelect picks TournamentSize random indices and returns the
// best by compareWithFp.
func (e *Engine) tournamentSelect(
	rng *rand.Rand,
	scores []resultpkg.ScoreTotal,
	fingerprints []string,
) int {
	best := rng.Intn(len(scores))
	for k := 1; k < e.cfg.TournamentSize; k++ {
		cand := rng.Intn(len(scores))
		if compareWithFp(scores[cand], scores[best], fingerprints[cand], fingerprints[best]) < 0 {
			best = cand
		}
	}
	return best
}

// compareWithFp extends quant.CompareFitness with a fingerprint tiebreaker
// for the double-Fatal case (per phase plan §1619). Stable across runs
// regardless of input order, which sort.SliceStable alone does not give.
func compareWithFp(a, b resultpkg.ScoreTotal, aFp, bFp string) int {
	if a.Fatal && b.Fatal {
		switch {
		case aFp < bFp:
			return -1
		case aFp > bFp:
			return 1
		default:
			return 0
		}
	}
	return quant.CompareFitness(a, b)
}

func makeOrder(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}
