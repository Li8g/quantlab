// EvolutionTask persistence for the SaaS Epoch service. Source-of-truth:
// docs/Coding-plan-dev-phases-prompts_v3_2_2.md Phase 5D step 1-4 +
// CLAUDE.md "EvolutionTask audit fields" (M16).
//
// Lifecycle: Create → MarkStarted → MarkSucceeded / MarkFailed. The
// HTTP layer reads via Get to populate EvolutionTaskStatusResponse.
//
// Audit-trail integrity: M16 mandates that RequestedTakerFeeBPS and
// RequestedSlippageBPS preserve the user's *original* friction intent.
// TestMode coercion (zeroing them in effective friction) happens at
// the Epoch service / engine layer, NOT here — Create writes the
// request values verbatim.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"quantlab/internal/api"
	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// EvolutionTaskRepo wraps a *gorm.DB and exposes the CRUD verbs the
// SaaS Epoch service uses across one task's lifetime.
type EvolutionTaskRepo struct {
	db *gorm.DB
}

// NewEvolutionTaskRepo wraps a *gorm.DB. The evolution_tasks table is
// expected to exist (store.NewDB's AutoMigrate provisions it).
func NewEvolutionTaskRepo(db *gorm.DB) *EvolutionTaskRepo {
	return &EvolutionTaskRepo{db: db}
}

// Create inserts a new evolution_tasks row in Queued state. The
// resulting row's TaskID equals the taskID argument; epochSeed is
// the caller-supplied deterministic seed for the engine. Returns the
// underlying gorm error verbatim so callers can detect duplicate
// task_id violations.
func (r *EvolutionTaskRepo) Create(
	ctx context.Context,
	taskID string,
	epochSeed int64,
	req api.CreateEvolutionTaskRequest,
) error {
	if taskID == "" {
		return errors.New("repository.EvolutionTaskRepo.Create: empty taskID")
	}
	row := buildEvolutionTask(taskID, epochSeed, req)
	return r.db.WithContext(ctx).Create(&row).Error
}

// List returns up to limit recently-created tasks, newest first.
// limit ≤ 0 falls through to all rows (callers should cap externally).
// Used by GET /api/v1/evolution/tasks for the index page.
func (r *EvolutionTaskRepo) List(ctx context.Context, limit int) ([]store.EvolutionTask, error) {
	q := r.db.WithContext(ctx).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []store.EvolutionTask
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Get fetches the row by task_id. Returns gorm.ErrRecordNotFound when
// no matching row exists — callers can map this to HTTP 404.
func (r *EvolutionTaskRepo) Get(ctx context.Context, taskID string) (*store.EvolutionTask, error) {
	if taskID == "" {
		return nil, errors.New("repository.EvolutionTaskRepo.Get: empty taskID")
	}
	var row store.EvolutionTask
	if err := r.db.WithContext(ctx).Where("task_id = ?", taskID).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// MarkStarted flips Status to Running and stamps StartedAt = now. No-op
// if the task is already in a terminal state (succeeded/failed/cancelled);
// the caller should refuse to start in that case.
func (r *EvolutionTaskRepo) MarkStarted(ctx context.Context, taskID string) error {
	if taskID == "" {
		return errors.New("repository.EvolutionTaskRepo.MarkStarted: empty taskID")
	}
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).
		Model(&store.EvolutionTask{}).
		Where("task_id = ?", taskID).
		Updates(map[string]interface{}{
			"status":     resultpkg.TaskStatusRunning,
			"started_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("repository.EvolutionTaskRepo.MarkStarted: task %q not found", taskID)
	}
	return nil
}

// MarkSucceeded transitions the task to Succeeded and records the
// produced challenger + best aggregate score. CurrentGeneration is
// the EpochResult.Generations value (actual generations executed,
// post-early-stop). bestScore is the pointer ScoreTotal.Value — nil
// is permitted for the rare "all-Fatal Epoch but ran to completion"
// path (handled as a separate failure mode in practice).
func (r *EvolutionTaskRepo) MarkSucceeded(
	ctx context.Context,
	taskID, challengerID string,
	currentGeneration int,
	bestScore *float64,
) error {
	if taskID == "" {
		return errors.New("repository.EvolutionTaskRepo.MarkSucceeded: empty taskID")
	}
	if challengerID == "" {
		return errors.New("repository.EvolutionTaskRepo.MarkSucceeded: empty challengerID")
	}
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).
		Model(&store.EvolutionTask{}).
		Where("task_id = ?", taskID).
		Updates(map[string]interface{}{
			"status":             resultpkg.TaskStatusSucceeded,
			"finished_at":        now,
			"challenger_id":      challengerID,
			"best_score":         bestScore,
			"current_generation": currentGeneration,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("repository.EvolutionTaskRepo.MarkSucceeded: task %q not found", taskID)
	}
	return nil
}

// MarkFailed stamps Status=failed and writes the failure reason.
// reason is required; empty values reject so the audit trail never
// contains a "failed with no explanation" row.
func (r *EvolutionTaskRepo) MarkFailed(ctx context.Context, taskID, reason string) error {
	if taskID == "" {
		return errors.New("repository.EvolutionTaskRepo.MarkFailed: empty taskID")
	}
	if reason == "" {
		return errors.New("repository.EvolutionTaskRepo.MarkFailed: empty reason")
	}
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).
		Model(&store.EvolutionTask{}).
		Where("task_id = ?", taskID).
		Updates(map[string]interface{}{
			"status":         resultpkg.TaskStatusFailed,
			"finished_at":    now,
			"failure_reason": reason,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("repository.EvolutionTaskRepo.MarkFailed: task %q not found", taskID)
	}
	return nil
}

// SweepOrphans resets every task still in a non-terminal state
// (queued or running) to failed, stamping a restart reason. Run once at
// startup before the HTTP server accepts traffic.
//
// Unlike import jobs — which a background worker re-picks from queued —
// epoch tasks are spawned by a detached goroutine at request time and
// have no resume path: a queued row whose goroutine never reached
// MarkStarted, and a running row whose goroutine died mid-epoch, are
// both orphaned by a process exit. Sweeping both closes the "task stuck
// running forever" invariant the in-process WaitGroup can't (it dies
// with the process). Returns the number of rows reset.
func (r *EvolutionTaskRepo) SweepOrphans(ctx context.Context) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&store.EvolutionTask{}).
		Where("status IN ?", []resultpkg.TaskStatus{
			resultpkg.TaskStatusQueued,
			resultpkg.TaskStatusRunning,
		}).
		Updates(map[string]interface{}{
			"status":         resultpkg.TaskStatusFailed,
			"finished_at":    time.Now().UTC(),
			"failure_reason": "interrupted by server restart",
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// buildEvolutionTask is the pure-function mapper from api request to
// store row. Extracted so unit tests can pin column population
// without a DB. RequestedTakerFeeBPS / RequestedSlippageBPS preserve
// user intent verbatim (M16) — TestMode coercion happens upstream of
// effective friction, not here.
func buildEvolutionTask(
	taskID string,
	epochSeed int64,
	req api.CreateEvolutionTaskRequest,
) store.EvolutionTask {
	return store.EvolutionTask{
		TaskID:               taskID,
		StrategyID:           req.StrategyID,
		Pair:                 req.Pair,
		Interval:             req.Interval,
		Status:               resultpkg.TaskStatusQueued,
		CurrentGeneration:    0,
		RequestedTakerFeeBPS: req.TakerFeeBPS,
		RequestedSlippageBPS: req.SlippageBPS,
		TestMode:             req.TestMode,
		SpawnMode:            req.SpawnMode,
		OosDays:              req.OosDays,
		FatalAuditSampleRate: req.FatalAuditSampleRate,
		EpochSeed:            epochSeed,
	}
}
