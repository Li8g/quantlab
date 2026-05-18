// Convergence-rescue layer 1 for the GA engine. Source-of-truth:
// docs/Coding-plan-dev-phases-prompts_v3_2_2.md §I-3.12 + §I-5
// default constants.
//
// Mutation ramp + early-stop share one "consecutive no-improvement"
// counter. A non-trivial improvement (> EarlyStopMinDelta) resets
// both the counter and the ramped mutation parameters back to their
// base values. Stagnation increments the counter, multiplies
// mutProb / mutScale by MutationRampFactor (capped at the
// configured Max), and triggers early-stop once the counter reaches
// EarlyStopPatience.
//
// Setting EarlyStopPatience = 0 disables the whole subsystem;
// mutation stays at the base values forever and the generation loop
// never breaks early.
package engine

import (
	"math"

	"quantlab/internal/resultpkg"
)

// convergenceState is the mutable per-Epoch convergence book-keeping.
// Layout-only (no methods that take EngineConfig by value-ref) so it
// can be constructed cheaply once per RunEpoch and observed by tests.
type convergenceState struct {
	// noImproveCount is the number of consecutive generations whose
	// best score did not exceed bestSoFar by at least
	// EarlyStopMinDelta. Resets to 0 on any genuine improvement.
	noImproveCount int

	// bestSoFar tracks the best score seen across all generations.
	// Stored as a *float64 so the "no best yet" / "current best
	// Fatal" case is encoded explicitly (nil = unset). Fatal scores
	// never become bestSoFar — they live in a separate Fatal flag
	// because compare semantics differ.
	bestSoFar    *float64
	bestSoFarFatal bool

	// mutProb / mutScale are the live mutation parameters threaded
	// into produceNextGeneration. Start at the configured base, ramp
	// up on stagnation, reset on improvement.
	mutProb  float64
	mutScale float64
}

// newConvergenceState returns the initial state for one RunEpoch.
// All ramp/patience knobs read from cfg; the function does not
// retain a cfg pointer.
func newConvergenceState(cfg EngineConfig) *convergenceState {
	return &convergenceState{
		mutProb:        cfg.MutationProbability,
		mutScale:       cfg.MutationScale,
		bestSoFarFatal: true, // no best yet → behave as if prior was Fatal
	}
}

// observe is called by RunEpoch right after the per-generation sort
// with the current best individual's ScoreTotal. It updates the
// internal state in-place and returns shouldStop=true when the
// patience threshold has been reached.
//
// shouldStop is conservative: a returned true means the caller MUST
// break out of the generation loop. A returned false leaves the
// decision to the loop's MaxGenerations bound.
func (s *convergenceState) observe(current resultpkg.ScoreTotal, cfg EngineConfig) (shouldStop bool) {
	improved := s.isImprovement(current, cfg.EarlyStopMinDelta)
	if improved {
		// Latch the new best, reset counters + ramp.
		if !current.Fatal && current.Value != nil {
			v := *current.Value
			s.bestSoFar = &v
			s.bestSoFarFatal = false
		}
		s.noImproveCount = 0
		s.mutProb = cfg.MutationProbability
		s.mutScale = cfg.MutationScale
		return false
	}
	// Stagnation path.
	s.noImproveCount++
	// Ramp mutation if a Max + Factor are configured. A Max of 0 or
	// a Factor ≤ 1.0 disables ramping for that knob.
	if cfg.MutationRampFactor > 1.0 {
		if cfg.MutationProbabilityMax > 0 {
			s.mutProb = math.Min(s.mutProb*cfg.MutationRampFactor, cfg.MutationProbabilityMax)
		}
		if cfg.MutationScaleMax > 0 {
			s.mutScale = math.Min(s.mutScale*cfg.MutationRampFactor, cfg.MutationScaleMax)
		}
	}
	// Early-stop trigger.
	if cfg.EarlyStopPatience > 0 && s.noImproveCount >= cfg.EarlyStopPatience {
		return true
	}
	return false
}

// isImprovement returns true iff `current` strictly improves on the
// stored bestSoFar by at least minDelta. Fatal handling:
//
//   - prior Fatal + current Normal → improvement (any non-Fatal is
//     better than Fatal under CompareFitness)
//   - prior Normal + current Fatal → not an improvement (elite
//     preservation makes this unreachable in practice)
//   - both Fatal → not an improvement
//
// minDelta ≤ 0 collapses to "any strictly-greater value counts" via
// the > comparison.
func (s *convergenceState) isImprovement(current resultpkg.ScoreTotal, minDelta float64) bool {
	if current.Fatal || current.Value == nil {
		return false
	}
	if s.bestSoFarFatal || s.bestSoFar == nil {
		return true
	}
	return *current.Value > *s.bestSoFar+minDelta
}
