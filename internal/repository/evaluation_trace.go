// EvaluationTrace persistence — one row per individual evaluation inside
// a GA run. Phase 1.5: feeds Optuna-dashboard population-scale analytics
// that winner-only gene_records can't support.
//
// Volume is high (typical 6000 rows / task) but row payload is small;
// CreateInBatches keeps the round-trips bounded. Engine wiring lives
// in internal/saas/epoch/service.go via EngineConfig.OnGenerationEvaluated.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// EvaluationTraceRepo wraps a *gorm.DB. The evaluation_traces table is
// provisioned by store.NewDB's AutoMigrate.
type EvaluationTraceRepo struct {
	db *gorm.DB
}

func NewEvaluationTraceRepo(db *gorm.DB) *EvaluationTraceRepo {
	return &EvaluationTraceRepo{db: db}
}

// BuildRows is the pure-function conversion from engine output to store
// rows. Separate from BulkInsert so a future unit test can verify the
// mapping without a database.
func BuildRows(
	taskID string,
	generation int,
	pop []domain.Gene,
	scores []resultpkg.ScoreTotal,
	raws []*resultpkg.RawEvaluateResult,
	fingerprints []string,
) ([]store.EvaluationTrace, error) {
	if taskID == "" {
		return nil, errors.New("BuildRows: empty taskID")
	}
	n := len(pop)
	if len(scores) != n || len(raws) != n || len(fingerprints) != n {
		return nil, fmt.Errorf("BuildRows: slice len mismatch pop=%d scores=%d raws=%d fp=%d",
			n, len(scores), len(raws), len(fingerprints))
	}

	rows := make([]store.EvaluationTrace, 0, n)
	for i := 0; i < n; i++ {
		geneBlob, err := json.Marshal(pop[i])
		if err != nil {
			return nil, fmt.Errorf("BuildRows: marshal gene[%d]: %w", i, err)
		}

		var windowsBlob []byte
		if raws[i] != nil && len(raws[i].Windows) > 0 {
			b, err := json.Marshal(raws[i].Windows)
			if err != nil {
				return nil, fmt.Errorf("BuildRows: marshal windows[%d]: %w", i, err)
			}
			windowsBlob = b
		}

		row := store.EvaluationTrace{
			TaskID:             taskID,
			Generation:         generation,
			IndividualIdx:      i,
			GeneJSON:           geneBlob,
			ScoreTotal:         scores[i].Value,
			ScoreRaw:           scores[i].ScoreRaw,
			ConsistencyPenalty: scores[i].ConsistencyPenalty,
			Fatal:              scores[i].Fatal,
			FatalReason:        scores[i].Reason,
			WindowScoresJSON:   windowsBlob,
			Fingerprint:        fingerprints[i],
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// BulkInsert writes rows in batches of 200 (one generation's worth at
// the typical PopSize). Returns the underlying gorm error verbatim.
func (r *EvaluationTraceRepo) BulkInsert(ctx context.Context, rows []store.EvaluationTrace) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).CreateInBatches(rows, 200).Error
}
