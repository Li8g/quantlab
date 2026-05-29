// import_worker.go — single serial background worker that drains queued
// ImportJobs and runs them through the Orchestrator (docs/phase9-data-
// import-v1.md §2.4). One goroutine: polls for the oldest queued job,
// runs it to completion, repeats. Combined with the per-pair partial
// unique index, serial execution makes concurrent-import correctness
// trivially hold (no two jobs run at once).
package data

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// DefaultImportPollInterval is how often the worker checks for queued jobs
// when idle. Imports are low-frequency (a human kicks them off), so a few
// seconds of latency to pick up a new job is fine.
const DefaultImportPollInterval = 2 * time.Second

// JobStore is the slice of ImportJobRepo the worker needs. Declared here
// (not imported from repository) so the data package stays free of a
// repository dependency — repository.ImportJobRepo satisfies it.
type JobStore interface {
	NextQueued(ctx context.Context) (*store.ImportJob, error)
	MarkRunning(ctx context.Context, jobID string, startedAt time.Time) error
	RecordMonth(ctx context.Context, jobID string, done, total int) (cancelRequested bool, err error)
	Finish(ctx context.Context, jobID string, status resultpkg.TaskStatus, rowsInserted int64, gapsDetected int, failureReason *string, finishedAt time.Time) error
}

// ImportFunc runs one import to completion, calling onMonth at each month
// boundary (onMonth returning true requests cancel → ErrImportCancelled).
// Abstracted so the worker's state machine is testable without the
// network; OrchestratorImportFunc is the production binding.
type ImportFunc func(ctx context.Context, symbol, interval string, start, end time.Time, onMonth func(done, total int) bool) (*ImportSummary, error)

// OrchestratorImportFunc adapts an *Orchestrator to ImportFunc by setting
// its frozen OnMonth hook around each ImportSymbol call. Safe under the
// single serial worker (one job at a time → no OnMonth races).
func OrchestratorImportFunc(o *Orchestrator) ImportFunc {
	return func(ctx context.Context, symbol, interval string, start, end time.Time, onMonth func(done, total int) bool) (*ImportSummary, error) {
		o.OnMonth = onMonth
		defer func() { o.OnMonth = nil }()
		return o.ImportSymbol(ctx, symbol, interval, start, end)
	}
}

// ImportWorker drains queued ImportJobs serially.
type ImportWorker struct {
	jobs   JobStore
	run    ImportFunc
	poll   time.Duration
	logger *slog.Logger
}

func NewImportWorker(jobs JobStore, run ImportFunc, logger *slog.Logger) *ImportWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &ImportWorker{jobs: jobs, run: run, poll: DefaultImportPollInterval, logger: logger}
}

// Run blocks until ctx is cancelled, draining all queued jobs then
// sleeping one poll interval. Intended to run in its own goroutine.
func (w *ImportWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		// Drain everything queued before sleeping.
		for {
			if ctx.Err() != nil {
				return
			}
			job, err := w.jobs.NextQueued(ctx)
			if err != nil {
				w.logger.Error("import_worker: poll queued", "err", err)
				break
			}
			if job == nil {
				break
			}
			w.runJob(ctx, job)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runJob executes one job end-to-end and writes its terminal status.
func (w *ImportWorker) runJob(ctx context.Context, job *store.ImportJob) {
	if err := w.jobs.MarkRunning(ctx, job.JobID, time.Now()); err != nil {
		w.logger.Error("import_worker: mark running", "job_id", job.JobID, "err", err)
		return
	}
	w.logger.Info("import_worker: started",
		"job_id", job.JobID, "symbol", job.Symbol, "interval", job.Interval)

	// Month-boundary hook: persist progress + relay the cancel flag.
	onMonth := func(done, total int) bool {
		cancel, err := w.jobs.RecordMonth(ctx, job.JobID, done, total)
		if err != nil {
			w.logger.Warn("import_worker: record month", "job_id", job.JobID, "err", err)
			return false
		}
		return cancel
	}

	start := time.UnixMilli(job.StartMs).UTC()
	end := time.UnixMilli(job.EndMs).UTC()
	summary, err := w.run(ctx, job.Symbol, job.Interval, start, end, onMonth)

	var rows int64
	var gaps int
	if summary != nil {
		rows, gaps = summary.RowsInserted, summary.GapsDetected
	}
	finishedAt := time.Now()

	switch {
	case errors.Is(err, ErrImportCancelled):
		w.finish(ctx, job.JobID, resultpkg.TaskStatusCancelled, rows, gaps, nil, finishedAt)
		w.logger.Info("import_worker: cancelled", "job_id", job.JobID)
	case err != nil:
		reason := err.Error()
		w.finish(ctx, job.JobID, resultpkg.TaskStatusFailed, rows, gaps, &reason, finishedAt)
		w.logger.Error("import_worker: failed", "job_id", job.JobID, "err", err)
	default:
		w.finish(ctx, job.JobID, resultpkg.TaskStatusSucceeded, rows, gaps, nil, finishedAt)
		w.logger.Info("import_worker: succeeded",
			"job_id", job.JobID, "rows_inserted", rows, "gaps_detected", gaps)
	}
}

func (w *ImportWorker) finish(
	ctx context.Context, jobID string, status resultpkg.TaskStatus,
	rows int64, gaps int, reason *string, at time.Time,
) {
	if err := w.jobs.Finish(ctx, jobID, status, rows, gaps, reason, at); err != nil {
		// A failed terminal write leaves the row `running`; the startup
		// orphan sweep is the backstop (it will mark it failed).
		w.logger.Error("import_worker: finish", "job_id", jobID, "status", status, "err", err)
	}
}
