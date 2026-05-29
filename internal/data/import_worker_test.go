package data

import (
	"context"
	"errors"
	"testing"
	"time"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// fakeJobStore records the terminal transition for assertions and lets a
// test script the queued jobs + the cancel flag.
type fakeJobStore struct {
	queue        []*store.ImportJob
	running      []string
	monthCalls   [][2]int // (done, total)
	cancelAfter  int      // RecordMonth returns cancel=true once done >= this (0 = never)
	finalStatus  resultpkg.TaskStatus
	finalRows    int64
	finalGaps    int
	finalReason  *string
	recordMonthN int
}

func (f *fakeJobStore) NextQueued(context.Context) (*store.ImportJob, error) {
	if len(f.queue) == 0 {
		return nil, nil
	}
	j := f.queue[0]
	f.queue = f.queue[1:]
	return j, nil
}
func (f *fakeJobStore) MarkRunning(_ context.Context, jobID string, _ time.Time) error {
	f.running = append(f.running, jobID)
	return nil
}
func (f *fakeJobStore) RecordMonth(_ context.Context, _ string, done, total int) (bool, error) {
	f.recordMonthN++
	f.monthCalls = append(f.monthCalls, [2]int{done, total})
	return f.cancelAfter != 0 && done >= f.cancelAfter, nil
}
func (f *fakeJobStore) Finish(_ context.Context, _ string, status resultpkg.TaskStatus, rows int64, gaps int, reason *string, _ time.Time) error {
	f.finalStatus, f.finalRows, f.finalGaps, f.finalReason = status, rows, gaps, reason
	return nil
}

func runOneJob(t *testing.T, fs *fakeJobStore, fn ImportFunc) {
	t.Helper()
	job := &store.ImportJob{JobID: "imp_x", Symbol: "BTCUSDT", Interval: "1h", StartMs: 1, EndMs: 2}
	w := NewImportWorker(fs, fn, nil)
	w.runJob(context.Background(), job)
}

func TestImportWorker_Succeeded(t *testing.T) {
	fs := &fakeJobStore{}
	fn := func(_ context.Context, _, _ string, _, _ time.Time, _ func(int, int) bool) (*ImportSummary, error) {
		return &ImportSummary{RowsInserted: 1500, GapsDetected: 3}, nil
	}
	runOneJob(t, fs, fn)
	if fs.finalStatus != resultpkg.TaskStatusSucceeded {
		t.Errorf("status = %q, want succeeded", fs.finalStatus)
	}
	if fs.finalRows != 1500 || fs.finalGaps != 3 {
		t.Errorf("summary = rows %d gaps %d, want 1500/3", fs.finalRows, fs.finalGaps)
	}
	if fs.finalReason != nil {
		t.Errorf("reason = %v, want nil", *fs.finalReason)
	}
}

func TestImportWorker_Failed_PreservesProgress(t *testing.T) {
	fs := &fakeJobStore{}
	fn := func(_ context.Context, _, _ string, _, _ time.Time, _ func(int, int) bool) (*ImportSummary, error) {
		// partial progress THEN error — summary must still be recorded.
		return &ImportSummary{RowsInserted: 400, GapsDetected: 1}, errors.New("month 2017-03: download zip: 404")
	}
	runOneJob(t, fs, fn)
	if fs.finalStatus != resultpkg.TaskStatusFailed {
		t.Errorf("status = %q, want failed", fs.finalStatus)
	}
	if fs.finalRows != 400 {
		t.Errorf("rows = %d, want 400 (progress before failure preserved)", fs.finalRows)
	}
	if fs.finalReason == nil || *fs.finalReason == "" {
		t.Error("want a failure_reason recorded")
	}
}

func TestImportWorker_Cancelled(t *testing.T) {
	fs := &fakeJobStore{cancelAfter: 2} // RecordMonth flags cancel at month 2
	// fn drives onMonth like the orchestrator would, returning
	// ErrImportCancelled when onMonth says to stop.
	fn := func(_ context.Context, _, _ string, _, _ time.Time, onMonth func(int, int) bool) (*ImportSummary, error) {
		total := 5
		sum := &ImportSummary{}
		for done := 1; done <= total; done++ {
			sum.RowsInserted += 100
			if onMonth(done, total) {
				return sum, ErrImportCancelled
			}
		}
		return sum, nil
	}
	runOneJob(t, fs, fn)
	if fs.finalStatus != resultpkg.TaskStatusCancelled {
		t.Errorf("status = %q, want cancelled", fs.finalStatus)
	}
	// Stopped at month 2 → 2 RecordMonth calls, 200 rows preserved.
	if fs.recordMonthN != 2 {
		t.Errorf("RecordMonth calls = %d, want 2 (stopped at boundary)", fs.recordMonthN)
	}
	if fs.finalRows != 200 {
		t.Errorf("rows = %d, want 200 (months done before cancel kept)", fs.finalRows)
	}
}

func TestImportWorker_MarksRunningBeforeImport(t *testing.T) {
	fs := &fakeJobStore{}
	fn := func(_ context.Context, _, _ string, _, _ time.Time, _ func(int, int) bool) (*ImportSummary, error) {
		return &ImportSummary{}, nil
	}
	runOneJob(t, fs, fn)
	if len(fs.running) != 1 || fs.running[0] != "imp_x" {
		t.Errorf("MarkRunning calls = %v, want [imp_x]", fs.running)
	}
}
