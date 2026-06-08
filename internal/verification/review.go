// Reproducibility replay verification (ReviewBacktest / backlog A1).
//
// RunReview answers one question for an audit reviewer: "Given only the
// persisted ChallengerResultPackage, can I rebuild the inputs and
// reproduce the recorded IS ScoreTotal?" It is the reproducibility half
// of the §10.1 #7 (TestReplayWithinTolerance) contract, applied to a
// real challenger rather than a unit fixture.
//
// Two independent integrity gates, in order:
//
//  1. Hash gate — the caller rebuilds the EvaluablePlan from the task
//     row + server defaults and passes the rebuilt plan_hash / bars_hash.
//     If either disagrees with the recorded reproducibility_metadata,
//     the inputs are no longer the same bytes that produced the package;
//     we report Status=mismatch WITHOUT replaying (the replay would be
//     meaningless). This is what gives the recorded plan_hash / bars_hash
//     teeth — until now they were stored but never checked.
//
//  2. Replay gate — re-evaluate bestGene on the (rebuilt) plan via the
//     same NewAdapter→Reset→Evaluate isolation contract the engine uses,
//     re-aggregate with fitness.AggregateScoreTotal, and compare the
//     fingerprint (exact) and ScoreTotal (within ReviewScoreTolerance)
//     against the recorded values.
//
// Status semantics:
//
//	ok       — hashes match AND fingerprint matches AND score within tolerance.
//	mismatch — any gate failed; Notes carries recorded-vs-replayed detail.
//
// Scope: this replays the IS crucible windows only (DataScope="is-windows").
// A full-history audit dimension (alpha breakdown / DSR / stress) is
// deferred — see backlog A1/B. Determinism is the same hard invariant the
// engine relies on; RunReview adds no goroutines and aggregates serially.
//
// Like RunOOS, this runs out-of-band AFTER engine.RunEpoch returns and
// never feeds back into GA decisions or the IS ScoreTotal already written.
// A non-nil Go error means an Adapter/strategy contract violation
// (NewAdapter/Reset/Evaluate failed in a way the contract forbids), not a
// reproducibility verdict — those are carried in ReviewSummary.Status.
package verification

import (
	"encoding/json"
	"fmt"
	"math"

	"quantlab/internal/domain"
	"quantlab/internal/fitness"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
)

// ReviewScoreTolerance is the absolute slack allowed between the recorded
// and replayed ScoreTotal.Value. Replay on the same plan+gene is
// deterministic and normally bit-identical (serial float accumulation),
// but the recorded score may have been produced on different hardware
// (reproducibility_metadata.hardware_signature records this boundary as
// soft, not a hard constraint), so we allow a tiny epsilon rather than
// demanding bit equality.
const ReviewScoreTolerance = 1e-9

// ReviewExpectation carries the recorded values a replay is checked
// against — lifted from the persisted package's core /
// reproducibility_metadata / evaluation layers by the caller.
type ReviewExpectation struct {
	Score       resultpkg.ScoreTotal
	Fingerprint string
	PlanHash    string
	BarsHash    string
}

// RunReview replays bestGene on plan and compares the outcome against
// expect. plan is the REBUILT plan (the caller reconstructs it from the
// task row + defaults); rebuiltPlanHash / rebuiltBarsHash are that
// rebuild's hashes, checked against expect's recorded hashes before any
// replay happens.
//
// weights / lambdaCons must match what the engine used (engine.WindowWeights()
// and EngineConfig.LambdaCons) so the re-aggregation reproduces BestScore.
// The caller supplies them rather than RunReview reaching into engine, to
// keep this package free of an engine import.
func RunReview(
	strat strategy.EvolvableStrategy,
	plan *domain.EvaluablePlan,
	bestGene domain.Gene,
	expect ReviewExpectation,
	rebuiltPlanHash, rebuiltBarsHash string,
	weights map[resultpkg.WindowName]float64,
	lambdaCons float64,
) (*resultpkg.ReviewSummary, error) {
	scope := "is-windows"
	res := &resultpkg.ReviewSummary{DataScope: &scope}

	// Gate 1: hash. Rebuilt inputs must reproduce the recorded hashes,
	// else the bytes differ and replaying tells us nothing.
	if expect.PlanHash != rebuiltPlanHash || expect.BarsHash != rebuiltBarsHash {
		notes := fmt.Sprintf(
			"hash drift: plan_hash recorded=%s rebuilt=%s; bars_hash recorded=%s rebuilt=%s",
			expect.PlanHash, rebuiltPlanHash, expect.BarsHash, rebuiltBarsHash,
		)
		res.Status = resultpkg.VerificationStatusMismatch
		res.Notes = &notes
		return res, nil
	}

	// Gate 2a: fingerprint. Pure function of the gene; a mismatch means
	// the persisted gene/fingerprint pair is internally inconsistent.
	replayFp := strat.Fingerprint(bestGene)
	if replayFp != expect.Fingerprint {
		notes := fmt.Sprintf("fingerprint mismatch: recorded=%s replayed=%s", expect.Fingerprint, replayFp)
		res.Status = resultpkg.VerificationStatusMismatch
		res.Notes = &notes
		return res, nil
	}

	// Gate 2b: replay the IS windows under the §5.6 isolation contract
	// (fresh Adapter, Reset before Evaluate), then re-aggregate.
	adapter, err := strat.NewAdapter(plan)
	if err != nil {
		return nil, fmt.Errorf("verification.RunReview: NewAdapter: %w", err)
	}
	defer adapter.Close()
	if err := adapter.Reset(plan); err != nil {
		return nil, fmt.Errorf("verification.RunReview: Reset: %w", err)
	}
	raw, err := adapter.Evaluate(bestGene)
	if err != nil {
		return nil, fmt.Errorf("verification.RunReview: Evaluate: %w", err)
	}
	if err := raw.ValidateForIS(); err != nil {
		return nil, fmt.Errorf("verification.RunReview: invalid raw: %w", err)
	}
	replayScore := fitness.AggregateScoreTotal(
		raw.Windows, weights, lambdaCons, resultpkg.FitnessVersionV1RawStd,
	)

	if ok, detail := scoreMatches(expect.Score, replayScore); !ok {
		notes := "score mismatch: " + detail
		res.Status = resultpkg.VerificationStatusMismatch
		res.Notes = &notes
		return res, nil
	}

	notes := fmt.Sprintf("reproduced: fingerprint + score within tol=%g over is-windows", ReviewScoreTolerance)
	res.Status = resultpkg.VerificationStatusOK
	res.Notes = &notes
	return res, nil
}

// scoreMatches compares two ScoreTotal values for replay equivalence.
// Fatal-state must agree exactly; non-Fatal values must be within
// ReviewScoreTolerance. Nil-safe per the SliceScore three-state contract
// (Value is nil iff Fatal). Returns a human-readable detail on mismatch.
func scoreMatches(recorded, replayed resultpkg.ScoreTotal) (bool, string) {
	if recorded.Fatal != replayed.Fatal {
		return false, fmt.Sprintf("fatal recorded=%v replayed=%v", recorded.Fatal, replayed.Fatal)
	}
	if recorded.Fatal {
		// Both Fatal — no Value to compare; that's a reproduced match.
		return true, ""
	}
	if recorded.Value == nil || replayed.Value == nil {
		// Non-Fatal with nil Value violates the ScoreTotal contract;
		// treat as a mismatch rather than panic.
		return false, fmt.Sprintf("non-fatal nil value: recorded=%v replayed=%v", recorded.Value, replayed.Value)
	}
	delta := math.Abs(*recorded.Value - *replayed.Value)
	if delta > ReviewScoreTolerance {
		return false, fmt.Sprintf("recorded=%.12g replayed=%.12g delta=%.3g > tol=%g",
			*recorded.Value, *replayed.Value, delta, ReviewScoreTolerance)
	}
	return true, ""
}

// MarshalReviewPayload mirrors MarshalOOSPayload: stuff the result into
// engine.BuildContext.ReviewPayload (json.RawMessage, to keep engine
// decoupled from verification types). Returns nil + nil when res is nil.
func MarshalReviewPayload(res *resultpkg.ReviewSummary) (json.RawMessage, error) {
	if res == nil {
		return nil, nil
	}
	return json.Marshal(res)
}
