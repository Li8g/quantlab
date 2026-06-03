package epoch

import (
	"context"
	"sync"
	"testing"
	"time"

	"quantlab/internal/api"
	"quantlab/internal/fitness"
)

// lockFor and the (strategy, pair) mutex are the only Service knobs
// safely testable without a live DB. CreateAndRunTask happy-path
// coverage is deferred to a //go:build integration test (DB required).

func TestService_lockFor_SameTupleReturnsSameMutex(t *testing.T) {
	s := &Service{locks: map[string]*sync.Mutex{}}
	a := s.lockFor("sigmoid_v1", "BTCUSDT")
	b := s.lockFor("sigmoid_v1", "BTCUSDT")
	if a != b {
		t.Errorf("lockFor(same) returned different mutexes: %p vs %p", a, b)
	}
}

func TestService_lockFor_DifferentTupleReturnsDifferentMutex(t *testing.T) {
	s := &Service{locks: map[string]*sync.Mutex{}}
	a := s.lockFor("sigmoid_v1", "BTCUSDT")
	b := s.lockFor("sigmoid_v1", "ETHUSDT")
	c := s.lockFor("toy", "BTCUSDT")
	if a == b || a == c || b == c {
		t.Errorf("different tuples must yield different mutexes; a=%p b=%p c=%p", a, b, c)
	}
}

func TestResolveDefaults_NilFieldsUseBase(t *testing.T) {
	base := DefaultDefaults()
	got := resolveDefaults(base, api.CreateEvolutionTaskRequest{})
	if got != base {
		t.Errorf("nil request overrides should leave Defaults untouched\n  got=%+v\n  want=%+v", got, base)
	}
}

func TestResolveDefaults_NonNilFieldsOverride(t *testing.T) {
	base := DefaultDefaults()
	warmup := 180
	lotStep := 0.001
	lotMin := 0.005
	initialUSDT := 5_000.0
	req := api.CreateEvolutionTaskRequest{
		WarmupDays:  &warmup,
		LotStep:     &lotStep,
		LotMin:      &lotMin,
		InitialUSDT: &initialUSDT,
		DCA: &api.DCAConfigRequest{
			InitialCapital: 7_500,
			MonthlyInject:  250,
		},
	}
	got := resolveDefaults(base, req)
	want := Defaults{
		WarmupDays:  180,
		LotStep:     0.001,
		LotMin:      0.005,
		InitialUSDT: 5_000,
		DCA:         fitness.GhostDCAConfig{InitialCapital: 7_500, MonthlyInject: 250},
	}
	if got != want {
		t.Errorf("override mismatch\n  got=%+v\n  want=%+v", got, want)
	}
}

func TestResolveDefaults_PartialOverride(t *testing.T) {
	base := DefaultDefaults()
	warmup := 90
	req := api.CreateEvolutionTaskRequest{WarmupDays: &warmup}
	got := resolveDefaults(base, req)
	if got.WarmupDays != 90 {
		t.Errorf("WarmupDays = %d, want 90 (overridden)", got.WarmupDays)
	}
	if got.LotStep != base.LotStep || got.LotMin != base.LotMin ||
		got.InitialUSDT != base.InitialUSDT || got.DCA != base.DCA {
		t.Errorf("unrelated fields drifted: got=%+v want_baseline=%+v", got, base)
	}
}

func TestService_lockFor_TryLockBehaviour(t *testing.T) {
	s := &Service{locks: map[string]*sync.Mutex{}}
	mu := s.lockFor("sigmoid_v1", "BTCUSDT")
	if !mu.TryLock() {
		t.Fatal("first TryLock should succeed on a fresh mutex")
	}
	if mu.TryLock() {
		t.Error("second TryLock on a held mutex should fail")
	}
	mu.Unlock()
	if !mu.TryLock() {
		t.Error("TryLock after Unlock should succeed")
	}
	mu.Unlock()
}

// Shutdown with no in-flight epochs must cancel baseCtx (so any future
// run would abort) and return promptly.
func TestService_Shutdown_CancelsBaseCtxAndReturns(t *testing.T) {
	baseCtx, stop := context.WithCancel(context.Background())
	s := &Service{baseCtx: baseCtx, stop: stop, locks: map[string]*sync.Mutex{}}
	if s.baseCtx.Err() != nil {
		t.Fatal("baseCtx must not be cancelled before Shutdown")
	}

	done := make(chan struct{})
	go func() { s.Shutdown(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not return with no in-flight epochs")
	}
	if s.baseCtx.Err() == nil {
		t.Error("Shutdown must cancel baseCtx")
	}
}

// Shutdown must honor its ctx deadline and return even when an epoch
// goroutine never finishes — the startup sweep is the backstop for the
// abandoned task row.
func TestService_Shutdown_RespectsDeadlineWhenEpochHangs(t *testing.T) {
	baseCtx, stop := context.WithCancel(context.Background())
	s := &Service{baseCtx: baseCtx, stop: stop, locks: map[string]*sync.Mutex{}}
	s.wg.Add(1) // simulate a run() goroutine that never reaches wg.Done
	defer s.wg.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { s.Shutdown(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Shutdown ignored its deadline while an epoch was in-flight")
	}
}
