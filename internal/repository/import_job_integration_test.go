//go:build integration

// Integration tests for ImportJobRepo against a live Postgres instance —
// the partial unique index (uq_import_jobs_active, decision 1), the
// status transitions, cancel gating, and the orphan sweep. Mirror of
// trade_integration_test.go. Run:
//
//	go test -tags=integration ./internal/repository/ \
//	    -args -config=/absolute/path/to/config.yaml
//
// reuses the configPath flag defined in challenger_integration_test.go.
package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"quantlab/internal/api"
	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

func TestImportJobRepo(t *testing.T) {
	cfg, err := config.Load(*configPath)
	if err != nil {
		t.Fatalf("load config %s: %v", *configPath, err)
	}
	db, err := store.NewDB(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	repo := NewImportJobRepo(db)
	ctx := context.Background()

	const sym, itv = "ITUSDT", "1h"
	// Unscoped → hard delete: ImportJob embeds gorm.Model (soft-delete),
	// and the job_id unique index counts soft-deleted rows, so a plain
	// Delete would leave the job_ids occupied and break re-runs.
	cleanup := func() { _ = db.Unscoped().Where("symbol = ?", sym).Delete(&store.ImportJob{}).Error }
	cleanup()
	t.Cleanup(cleanup)

	mk := func(jobID string) *store.ImportJob {
		return &store.ImportJob{JobID: jobID, Symbol: sym, Interval: itv, StartMs: 1, EndMs: 2, Status: resultpkg.TaskStatusQueued}
	}

	// First queued job inserts fine.
	if err := repo.Create(ctx, mk("imp-it-1")); err != nil {
		t.Fatalf("Create #1: %v", err)
	}

	// Second active job for the same (symbol, interval) → ErrImportActive
	// (partial unique index, decision 1).
	if err := repo.Create(ctx, mk("imp-it-2")); !errors.Is(err, api.ErrImportActive) {
		t.Fatalf("Create #2 err = %v, want ErrImportActive", err)
	}

	// NextQueued returns the queued one.
	nq, err := repo.NextQueued(ctx)
	if err != nil || nq == nil || nq.JobID != "imp-it-1" {
		t.Fatalf("NextQueued = %+v, %v; want imp-it-1", nq, err)
	}

	// queued → running. Index covers running too, so same pair still blocked.
	if err := repo.MarkRunning(ctx, "imp-it-1", time.Now()); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := repo.Create(ctx, mk("imp-it-3")); !errors.Is(err, api.ErrImportActive) {
		t.Fatalf("Create while running err = %v, want ErrImportActive", err)
	}

	// RecordMonth writes progress + reads back cancel flag (false here).
	cancel, err := repo.RecordMonth(ctx, "imp-it-1", 5, 12)
	if err != nil || cancel {
		t.Fatalf("RecordMonth = %v, %v; want false, nil", cancel, err)
	}

	// SetCancelRequested on an active job → ok=true; RecordMonth now true.
	ok, err := repo.SetCancelRequested(ctx, "imp-it-1")
	if err != nil || !ok {
		t.Fatalf("SetCancelRequested(active) = %v, %v; want true, nil", ok, err)
	}
	cancel, err = repo.RecordMonth(ctx, "imp-it-1", 6, 12)
	if err != nil || !cancel {
		t.Fatalf("RecordMonth after cancel = %v, %v; want true, nil", cancel, err)
	}

	// Finish(succeeded) → terminal. Same pair can now be re-imported.
	if err := repo.Finish(ctx, "imp-it-1", resultpkg.TaskStatusSucceeded, 1500, 3, nil, time.Now()); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	got, err := repo.Get(ctx, "imp-it-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != resultpkg.TaskStatusSucceeded || got.RowsInserted != 1500 || got.MonthsDone != 6 {
		t.Errorf("finished job = %+v, want succeeded/1500 rows/6 months", got)
	}
	// Terminal job → SetCancelRequested returns ok=false (409 path).
	if ok, _ := repo.SetCancelRequested(ctx, "imp-it-1"); ok {
		t.Error("SetCancelRequested(terminal) = true, want false")
	}
	// Re-import allowed once terminal (index only blocks active).
	if err := repo.Create(ctx, mk("imp-it-4")); err != nil {
		t.Fatalf("Create after terminal: %v", err)
	}

	// SweepOrphans fails any leftover running job. Mark imp-it-4 running,
	// then sweep.
	if err := repo.MarkRunning(ctx, "imp-it-4", time.Now()); err != nil {
		t.Fatalf("MarkRunning #4: %v", err)
	}
	n, err := repo.SweepOrphans(ctx)
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if n < 1 {
		t.Errorf("SweepOrphans count = %d, want >= 1", n)
	}
	swept, _ := repo.Get(ctx, "imp-it-4")
	if swept.Status != resultpkg.TaskStatusFailed || swept.FailureReason == nil {
		t.Errorf("swept job = %+v, want failed with reason", swept)
	}
}
