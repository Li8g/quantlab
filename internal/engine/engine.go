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

	// OnProgress is called after the per-generation sort with the
	// best individual. Optional.
	OnProgress func(gen int, bestFp string, best resultpkg.ScoreTotal)
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

// EpochResult is what RunEpoch returns. Phase 5D will wrap this into a
// full ChallengerResultPackage; for now it is the minimum the engine
// needs to expose to validate end-to-end GA behaviour.
type EpochResult struct {
	BestGene        domain.Gene
	BestScore       resultpkg.ScoreTotal
	BestFingerprint string
	Generations     int
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

	masterRng := rand.New(rand.NewSource(e.cfg.EpochSeed))

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

	scores, err := e.evaluatePopulation(ctx, plan, pop, adapters)
	if err != nil {
		return nil, err
	}

	var (
		bestIdx int
		bestFp  string
		conv    = newConvergenceState(e.cfg)
		actualGens int
	)
	for gen := 0; gen < e.cfg.MaxGenerations; gen++ {
		fingerprints := make([]string, len(pop))
		for i := range pop {
			fingerprints[i] = e.strat.Fingerprint(pop[i])
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
		scores, err = e.evaluatePopulation(ctx, plan, pop, adapters)
		if err != nil {
			return nil, err
		}
	}

	return &EpochResult{
		BestGene:        append(domain.Gene(nil), pop[bestIdx]...),
		BestScore:       scores[bestIdx],
		BestFingerprint: bestFp,
		Generations:     actualGens,
	}, nil
}

// evaluatePopulation spreads pop across len(adapters) workers. Each worker
// owns one Adapter for the full pass; the engine calls Reset(plan) before
// every Evaluate, satisfying the §5.6 isolation contract.
//
// Determinism: scores[i] is written only by the worker handling i; no
// cross-index races. Worker-pickup order does not affect final scores
// because Adapter.Evaluate is required to be a pure function of (gene,
// plan) (§5.5).
func (e *Engine) evaluatePopulation(
	ctx context.Context,
	plan *domain.EvaluablePlan,
	pop []domain.Gene,
	adapters []strategy.Adapter,
) ([]resultpkg.ScoreTotal, error) {
	scores := make([]resultpkg.ScoreTotal, len(pop))
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
			}
		}(adapter)
	}
	wg.Wait()
	close(errCh)
	if err := <-errCh; err != nil {
		return nil, err
	}
	return scores, nil
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
