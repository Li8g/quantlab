// import_job.go — ImportJobRepo persists async kline-import jobs
// (docs/phase9-data-import-v1.md). The HTTP layer uses Create/Get/List/
// SetCancelRequested; the single background worker uses NextQueued/
// MarkRunning/RecordMonth/Finish; SweepOrphans runs once at startup.
package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"quantlab/internal/api"
	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

type ImportJobRepo struct {
	db *gorm.DB
}

func NewImportJobRepo(db *gorm.DB) *ImportJobRepo {
	return &ImportJobRepo{db: db}
}

// isUniqueViolation detects a Postgres unique-constraint violation without
// depending on gorm's TranslateError (not enabled globally). Matches the
// gorm sentinel, the SQLSTATE, the generic message, and our index name.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "uq_import_jobs_active") ||
		strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key value")
}

// Create inserts a queued job. A concurrent active job for the same
// (symbol, interval) trips the partial unique index → ErrImportActive.
func (r *ImportJobRepo) Create(ctx context.Context, j *store.ImportJob) error {
	if j == nil {
		return errors.New("repository.ImportJobRepo.Create: nil job")
	}
	err := r.db.WithContext(ctx).Create(j).Error
	if isUniqueViolation(err) {
		return api.ErrImportActive
	}
	return err
}

// Get fetches a job by its public JobID.
func (r *ImportJobRepo) Get(ctx context.Context, jobID string) (*store.ImportJob, error) {
	var j store.ImportJob
	if err := r.db.WithContext(ctx).Where("job_id = ?", jobID).First(&j).Error; err != nil {
		return nil, err
	}
	return &j, nil
}

// List returns recent jobs, newest first. No filters in v1 (open Q3).
func (r *ImportJobRepo) List(ctx context.Context, limit int) ([]store.ImportJob, error) {
	var rows []store.ImportJob
	if err := r.db.WithContext(ctx).
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// NextQueued returns the oldest queued job, or (nil, nil) when none.
// Single-worker serial model (§2.4): no row-locking needed.
func (r *ImportJobRepo) NextQueued(ctx context.Context) (*store.ImportJob, error) {
	var j store.ImportJob
	err := r.db.WithContext(ctx).
		Where("status = ?", resultpkg.TaskStatusQueued).
		Order("created_at ASC").
		Limit(1).
		First(&j).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// MarkRunning transitions queued → running and stamps StartedAt.
func (r *ImportJobRepo) MarkRunning(ctx context.Context, jobID string, startedAt time.Time) error {
	return r.db.WithContext(ctx).
		Model(&store.ImportJob{}).
		Where("job_id = ?", jobID).
		Updates(map[string]any{
			"status":     resultpkg.TaskStatusRunning,
			"started_at": startedAt,
		}).Error
}

// RecordMonth writes month progress and reports whether cancellation was
// requested — the OnMonth hook's single round-trip (write progress, read
// the cancel flag back).
func (r *ImportJobRepo) RecordMonth(ctx context.Context, jobID string, done, total int) (bool, error) {
	if err := r.db.WithContext(ctx).
		Model(&store.ImportJob{}).
		Where("job_id = ?", jobID).
		Updates(map[string]any{"months_done": done, "months_total": total}).Error; err != nil {
		return false, err
	}
	var cancel bool
	err := r.db.WithContext(ctx).
		Model(&store.ImportJob{}).
		Select("cancel_requested").
		Where("job_id = ?", jobID).
		Scan(&cancel).Error
	return cancel, err
}

// Finish writes a terminal status (succeeded/failed/cancelled) with the
// summary counts, optional failure reason, and FinishedAt. rows/gaps are
// recorded even on failure so the user sees progress before the error.
func (r *ImportJobRepo) Finish(
	ctx context.Context, jobID string, status resultpkg.TaskStatus,
	rowsInserted int64, gapsDetected int, failureReason *string, finishedAt time.Time,
) error {
	return r.db.WithContext(ctx).
		Model(&store.ImportJob{}).
		Where("job_id = ?", jobID).
		Updates(map[string]any{
			"status":         status,
			"rows_inserted":  rowsInserted,
			"gaps_detected":  gapsDetected,
			"failure_reason": failureReason,
			"finished_at":    finishedAt,
		}).Error
}

// SetCancelRequested flags an active job for cancellation. Returns
// (false, nil) when the job is in a terminal state (caller maps to 409);
// the WHERE clause makes it a no-op rather than a clobber.
func (r *ImportJobRepo) SetCancelRequested(ctx context.Context, jobID string) (bool, error) {
	res := r.db.WithContext(ctx).
		Model(&store.ImportJob{}).
		Where("job_id = ? AND status IN ?", jobID,
			[]resultpkg.TaskStatus{resultpkg.TaskStatusQueued, resultpkg.TaskStatusRunning}).
		Update("cancel_requested", true)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// SweepOrphans fails every job left running by a crashed worker. Liveness-
// critical (§2.1): an orphaned running row holds the unique index and
// blocks all new imports of that pair. Run once at startup. Returns the
// number of jobs swept.
func (r *ImportJobRepo) SweepOrphans(ctx context.Context) (int64, error) {
	reason := "interrupted by restart"
	res := r.db.WithContext(ctx).
		Model(&store.ImportJob{}).
		Where("status = ?", resultpkg.TaskStatusRunning).
		Updates(map[string]any{
			"status":         resultpkg.TaskStatusFailed,
			"failure_reason": &reason,
			"finished_at":    time.Now(),
		})
	return res.RowsAffected, res.Error
}
