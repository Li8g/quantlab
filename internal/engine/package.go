// ChallengerResultPackage construction for the GA engine. Source-of-
// truth: docs/Coding-plan-dev-phases-prompts_v3_2_2.md Phase 5B step 5
// + §II-3.2 (five-layer package) + §I-3.13 (reproducibility metadata).
//
// Engine-side responsibility (per §M14):
//  1. Build EvaluationLayer from the strategy's RawEvaluateResult +
//     the engine's ScoreTotal.
//  2. Compose ReproducibilityMetadata + GAConfigSnapshot from
//     EngineConfig + a per-Epoch BuildContext (TaskID, audit-trail
//     strings, plan/bars hashes).
//  3. Hand all of the above to strategy.EncodeResult, which stamps
//     core.StrategyID + ChampionGene + Promote.DecisionStatus =
//     pending.
//
// Verification + Diagnostics are emitted empty here. Each lights up
// in later commits (5B-sharpe writes Verification.DSRSummary;
// 5B-fatal writes Diagnostics.FatalAuditSamples; 5C-plan writes
// the real PlanHash / BarsHash; 5D writes OOSResult).
package engine

import (
	"encoding/json"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
)

// BuildContext bundles the task-bound metadata that the engine itself
// does not know. The SaaS Epoch service (Phase 5D) fills it in from
// the EvolutionTask row + cmd-startup build constants. Engine tests
// fill it in by hand.
//
// Fields are grouped by concern:
//   - Routing:    ChallengerID, Pair
//   - Task knobs: TestMode, OosDays, FatalAuditSampleRate
//   - Repro:      DataVersion … BarsHash
//
// Empty-string defaults are tolerated by resultpkg.Validate (the
// version strings are mandatory there, but DataVersion / engine-version
// strings are not). Phase 5C-plan will replace placeholder hashes with
// real ones computed from the EvaluablePlan + bars.
type BuildContext struct {
	ChallengerID string
	Pair         string

	TestMode             bool
	OosDays              *int
	FatalAuditSampleRate *float64

	DataVersion       string
	EngineVersion     string
	StrategyVersion   string
	HardwareSignature string
	GoVersion         string
	BuildID           string
	PlanHash          string
	BarsHash          string

	// DSRSummary is the optional pre-marshalled JSON payload for
	// VerificationLayer.DSRSummary. The SaaS Epoch service computes
	// it via verification.ComputeDSR after SharpeBank.Stats reports
	// N ≥ MinTrialsForDSR, then marshals verification.DSRSummary
	// here. Empty ⇒ Verification.DSRSummary stays unset.
	DSRSummary json.RawMessage

	// OOSPayload is the optional pre-marshalled OOSResult from the
	// Phase 5D Anchored Holdout runner (verification.RunOOS). The
	// SaaS Epoch service marshals verification.RunOOS's *OOSResult
	// via verification.MarshalOOSPayload after RunEpoch returns and
	// passes the bytes here. Empty ⇒ Verification.OOSResult stays
	// at the engine default {Status: not_run}. Non-empty overrides
	// the default; malformed JSON surfaces as an EncodeResult error.
	OOSPayload json.RawMessage

	// FatalAuditSamples is the §I-3.12 fatal-audit pick collected by
	// RunEpoch. The caller plumbs EpochResult.FatalAuditSamples
	// verbatim; BuildChallengerPackage writes it onto
	// DiagnosticsLayer.FatalAuditSamples when non-empty.
	FatalAuditSamples []resultpkg.AuditSampleSummary
}

// BuildChallengerPackage assembles the five-layer package for one
// gene. Verification + Diagnostics layers are emitted empty; later
// phase commits populate them.
//
// Inputs:
//   - strat / plan: the running strategy + its plan. plan supplies
//     Spawn (for SpawnPoint), Friction (for GAConfigSnapshot taker/
//     slippage values mirroring the EFFECTIVE friction).
//   - bestGene / bestRaw / bestScore: outputs from RunEpoch's
//     final-generation winner. bestRaw.FrictionActual is what the
//     EvaluationLayer.FrictionActual mirrors verbatim; bestRaw.Windows
//     is what WindowScores carries.
//   - cfg: EngineConfig (GA knobs feed GAConfigSnapshot; EpochSeed
//     feeds ReproducibilityMetadata).
//   - bc: BuildContext (task-bound fields + audit-trail strings).
//
// Returns the constructed package or an EncodeResult error. The caller
// is responsible for persisting via internal/repository.
func BuildChallengerPackage(
	strat strategy.EvolvableStrategy,
	plan *domain.EvaluablePlan,
	bestGene domain.Gene,
	bestRaw *resultpkg.RawEvaluateResult,
	bestScore resultpkg.ScoreTotal,
	cfg EngineConfig,
	bc BuildContext,
) (resultpkg.ChallengerResultPackage, error) {
	eval := &resultpkg.EvaluationLayer{
		WindowScores:   bestRaw.Windows,
		ScoreTotal:     bestScore,
		FrictionActual: bestRaw.FrictionActual,
	}
	verif := &resultpkg.VerificationLayer{
		// OOSResult defaults to NotRun. When the SaaS Epoch service
		// supplies OOSPayload (Phase 5D Anchored Holdout output), it
		// overrides this default; otherwise NotRun is the honest
		// "OOS verification was not attempted" state and keeps the
		// VerificationStatus enum validated.
		OOSResult: resultpkg.OOSResult{Status: resultpkg.VerificationStatusNotRun},
	}
	if len(bc.OOSPayload) > 0 {
		var oos resultpkg.OOSResult
		if err := json.Unmarshal(bc.OOSPayload, &oos); err != nil {
			return resultpkg.ChallengerResultPackage{}, err
		}
		verif.OOSResult = oos
	}
	if len(bc.DSRSummary) > 0 {
		verif.DSRSummary = bc.DSRSummary
	}
	diag := &resultpkg.DiagnosticsLayer{}
	if len(bc.FatalAuditSamples) > 0 {
		diag.FatalAuditSamples = bc.FatalAuditSamples
	}

	repro := resultpkg.ReproducibilityMetadata{
		EpochSeed:          cfg.EpochSeed,
		DataVersion:        bc.DataVersion,
		EngineVersion:      bc.EngineVersion,
		StrategyVersion:    bc.StrategyVersion,
		SchemaVersion:      resultpkg.SchemaVersionV533,
		FitnessVersion:     resultpkg.FitnessVersionV1RawStd,
		FingerprintVersion: resultpkg.FingerprintVersionV1,
		HardwareSignature:  bc.HardwareSignature,
		GoVersion:          bc.GoVersion,
		BuildID:            bc.BuildID,
		PlanHash:           bc.PlanHash,
		BarsHash:           bc.BarsHash,
	}

	ga := resultpkg.GAConfigSnapshot{
		StrategyID:           strat.StrategyID(),
		Pair:                 bc.Pair,
		PopSize:              cfg.PopSize,
		MaxGenerations:       cfg.MaxGenerations,
		EliteRatio:           cfg.EliteRatio,
		FatalMDD:             plan.FatalMDD,
		TakerFeeBPS:          plan.Friction.TakerFeeBPS,
		SlippageBPS:          plan.Friction.SlippageBPS,
		SpawnMode:            plan.Spawn.SpawnMode,
		TestMode:             bc.TestMode,
		OosDays:              bc.OosDays,
		FatalAuditSampleRate: bc.FatalAuditSampleRate,
	}

	return strat.EncodeResult(bestGene, plan.Spawn, repro, ga, eval, verif, diag)
}
