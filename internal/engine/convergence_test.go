package engine

import (
	"testing"

	"quantlab/internal/resultpkg"
)

// ----- helpers -----

func scoreOf(v float64) resultpkg.ScoreTotal {
	val := v
	return resultpkg.ScoreTotal{Fatal: false, Value: &val}
}

func fatalScore() resultpkg.ScoreTotal {
	return resultpkg.ScoreTotal{Fatal: true}
}

// rampCfg is a self-contained EngineConfig fixture with non-zero
// ramp + patience parameters so the unit tests can exercise both
// the stagnation and the improvement branches without depending on
// DefaultConfig drift.
func rampCfg() EngineConfig {
	return EngineConfig{
		MutationProbability:    0.10,
		MutationScale:          1.0,
		MutationProbabilityMax: 0.40,
		MutationScaleMax:       2.0,
		MutationRampFactor:     2.0,
		EarlyStopPatience:      3,
		EarlyStopMinDelta:      0.01,
	}
}

// ----- isImprovement -----

func TestConvergence_FirstNonFatalIsImprovement(t *testing.T) {
	s := newConvergenceState(rampCfg())
	if !s.isImprovement(scoreOf(0.5), 0.01) {
		t.Error("first non-Fatal observation should count as improvement")
	}
}

func TestConvergence_FatalIsNeverImprovement(t *testing.T) {
	s := newConvergenceState(rampCfg())
	if s.isImprovement(fatalScore(), 0.01) {
		t.Error("Fatal score must not register as improvement")
	}
}

func TestConvergence_NonFatalBeatsPriorFatal(t *testing.T) {
	s := newConvergenceState(rampCfg())
	// Observe Fatal first — state stays at bestSoFarFatal=true.
	s.observe(fatalScore(), rampCfg())
	if !s.isImprovement(scoreOf(-100), 0.01) {
		t.Error("any non-Fatal score must beat prior Fatal")
	}
}

func TestConvergence_StrictDeltaRequired(t *testing.T) {
	s := newConvergenceState(rampCfg())
	s.observe(scoreOf(1.0), rampCfg())
	// +0.005 < MinDelta 0.01 → not an improvement.
	if s.isImprovement(scoreOf(1.005), 0.01) {
		t.Error("delta below threshold counted as improvement")
	}
	// +0.02 > 0.01 → improvement.
	if !s.isImprovement(scoreOf(1.02), 0.01) {
		t.Error("delta above threshold not counted")
	}
}

// ----- observe: counter + ramp -----

func TestConvergence_ImprovementResetsCounterAndRamp(t *testing.T) {
	cfg := rampCfg()
	s := newConvergenceState(cfg)
	// observe(1.0) counts as IMPROVEMENT (no prior best) — establishes
	// baseline. The two subsequent observe(1.0) calls are the
	// stagnations that drive the counter to 2.
	s.observe(scoreOf(1.0), cfg) // baseline (improvement)
	s.observe(scoreOf(1.0), cfg) // counter 1
	s.observe(scoreOf(1.0), cfg) // counter 2
	if s.noImproveCount != 2 {
		t.Fatalf("noImproveCount = %d, want 2", s.noImproveCount)
	}
	if s.mutProb <= cfg.MutationProbability {
		t.Errorf("mutProb = %v, want > base %v (ramped)", s.mutProb, cfg.MutationProbability)
	}
	// Improvement: everything resets.
	stop := s.observe(scoreOf(2.0), cfg)
	if stop {
		t.Error("improvement should not signal stop")
	}
	if s.noImproveCount != 0 {
		t.Errorf("after improvement: noImproveCount = %d, want 0", s.noImproveCount)
	}
	if s.mutProb != cfg.MutationProbability {
		t.Errorf("after improvement: mutProb = %v, want base %v", s.mutProb, cfg.MutationProbability)
	}
	if s.mutScale != cfg.MutationScale {
		t.Errorf("after improvement: mutScale = %v, want base %v", s.mutScale, cfg.MutationScale)
	}
}

func TestConvergence_RampCappedAtMax(t *testing.T) {
	cfg := rampCfg()
	// Factor 2.0, base 0.10 → after enough stagnations: 0.10 → 0.20 → 0.40 (cap).
	s := newConvergenceState(cfg)
	for i := 0; i < 10; i++ {
		s.observe(scoreOf(1.0), cfg)
	}
	if s.mutProb != cfg.MutationProbabilityMax {
		t.Errorf("mutProb = %v, want cap %v", s.mutProb, cfg.MutationProbabilityMax)
	}
	if s.mutScale != cfg.MutationScaleMax {
		t.Errorf("mutScale = %v, want cap %v", s.mutScale, cfg.MutationScaleMax)
	}
}

func TestConvergence_RampSkippedWhenFactorLEQOne(t *testing.T) {
	cfg := rampCfg()
	cfg.MutationRampFactor = 1.0
	s := newConvergenceState(cfg)
	for i := 0; i < 5; i++ {
		s.observe(scoreOf(1.0), cfg)
	}
	if s.mutProb != cfg.MutationProbability {
		t.Errorf("factor=1 should not ramp: mutProb = %v", s.mutProb)
	}
}

// ----- observe: early-stop -----

func TestConvergence_EarlyStopFiresAtPatience(t *testing.T) {
	cfg := rampCfg() // EarlyStopPatience = 3
	s := newConvergenceState(cfg)
	// First observation: improvement (no prior best). No stop.
	if stop := s.observe(scoreOf(1.0), cfg); stop {
		t.Error("first observation should not stop")
	}
	// Stagnations 1, 2: counter = 1, 2 — below patience.
	for i := 1; i <= 2; i++ {
		if stop := s.observe(scoreOf(1.0), cfg); stop {
			t.Errorf("stop fired at counter=%d, want at %d", i, cfg.EarlyStopPatience)
		}
	}
	// Stagnation 3: counter == patience → stop.
	if stop := s.observe(scoreOf(1.0), cfg); !stop {
		t.Error("stop did not fire at patience")
	}
}

func TestConvergence_EarlyStopDisabledWhenPatienceZero(t *testing.T) {
	cfg := rampCfg()
	cfg.EarlyStopPatience = 0
	s := newConvergenceState(cfg)
	for i := 0; i < 100; i++ {
		if stop := s.observe(scoreOf(1.0), cfg); stop {
			t.Fatalf("patience=0 should never stop, but did at i=%d", i)
		}
	}
}

func TestConvergence_ImprovementBeforePatienceResetsCounter(t *testing.T) {
	cfg := rampCfg() // patience 3
	s := newConvergenceState(cfg)
	s.observe(scoreOf(1.0), cfg) // baseline
	s.observe(scoreOf(1.0), cfg) // counter 1
	s.observe(scoreOf(1.0), cfg) // counter 2
	// At counter 2, improve → reset to 0.
	if stop := s.observe(scoreOf(1.5), cfg); stop {
		t.Error("improvement should not stop")
	}
	if s.noImproveCount != 0 {
		t.Errorf("counter not reset: %d", s.noImproveCount)
	}
	// Now need 3 more stagnations to trigger stop.
	for i := 1; i <= 2; i++ {
		if stop := s.observe(scoreOf(1.5), cfg); stop {
			t.Errorf("premature stop after reset at counter=%d", i)
		}
	}
	if stop := s.observe(scoreOf(1.5), cfg); !stop {
		t.Error("stop did not fire after fresh patience window")
	}
}
