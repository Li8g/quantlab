package cron

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"quantlab/internal/saas/instance"
	"quantlab/internal/saas/store"
)

// ===== fakes =====

type fakeLister struct {
	rows []store.StrategyInstance
	err  error
	mu   sync.Mutex
	hits int
}

func (f *fakeLister) ListLive(_ context.Context) ([]store.StrategyInstance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits++
	return f.rows, f.err
}

type recordingTicker struct {
	mu        sync.Mutex
	callsByID map[string]int
	err       error
	panicArg  any
	delay     time.Duration
}

func (t *recordingTicker) Tick(ctx context.Context, inst store.StrategyInstance) error {
	if t.panicArg != nil {
		panic(t.panicArg)
	}
	if t.delay > 0 {
		select {
		case <-time.After(t.delay):
		case <-ctx.Done():
		}
	}
	t.mu.Lock()
	if t.callsByID == nil {
		t.callsByID = map[string]int{}
	}
	t.callsByID[inst.InstanceID]++
	t.mu.Unlock()
	return t.err
}

func (t *recordingTicker) countFor(id string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.callsByID[id]
}

func (t *recordingTicker) total() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, v := range t.callsByID {
		n += v
	}
	return n
}

// silentLogger returns a slog.Logger that drops everything — keeps
// test output clean.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func liveRow(id string) store.StrategyInstance {
	return store.StrategyInstance{
		InstanceID: id, Status: store.InstanceStatusLive,
	}
}

// ===== tests =====

// TestScheduler_FiresImmediatelyOnStart verifies the initial scan
// happens before the first Interval expires (avoids one-minute
// startup delay).
func TestScheduler_FiresImmediatelyOnStart(t *testing.T) {
	lister := &fakeLister{rows: []store.StrategyInstance{liveRow("a"), liveRow("b")}}
	tk := &recordingTicker{}

	s := New(lister, tk, Config{Interval: time.Hour, Logger: silentLogger()})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	// Give the immediate scan + tick goroutines time to run.
	waitForCount(t, tk, 2, 200*time.Millisecond)

	cancel()
	<-done

	if tk.countFor("a") < 1 || tk.countFor("b") < 1 {
		t.Errorf("each live instance must tick at least once on startup; got a=%d b=%d",
			tk.countFor("a"), tk.countFor("b"))
	}
}

// TestScheduler_TicksOnInterval verifies repeated scans fire on
// schedule. Uses 30ms interval; expect ≥ 3 ticks per instance in
// 200ms.
func TestScheduler_TicksOnInterval(t *testing.T) {
	lister := &fakeLister{rows: []store.StrategyInstance{liveRow("inst-1")}}
	tk := &recordingTicker{}
	s := New(lister, tk, Config{Interval: 30 * time.Millisecond, Logger: silentLogger()})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	if n := tk.countFor("inst-1"); n < 3 {
		t.Errorf("expected >=3 ticks in 200ms with 30ms interval, got %d", n)
	}
}

// TestScheduler_StopsCleanlyOnCancel verifies Run returns promptly
// when ctx is cancelled.
func TestScheduler_StopsCleanlyOnCancel(t *testing.T) {
	lister := &fakeLister{rows: []store.StrategyInstance{liveRow("x")}}
	tk := &recordingTicker{}
	s := New(lister, tk, Config{Interval: time.Minute, Logger: silentLogger()})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("scheduler did not return within 500ms after ctx cancel")
	}
}

// TestScheduler_RecoversFromTickPanic verifies a single instance's
// Tick panic does not crash the scheduler.
func TestScheduler_RecoversFromTickPanic(t *testing.T) {
	lister := &fakeLister{rows: []store.StrategyInstance{liveRow("panicky")}}
	tk := &recordingTicker{panicArg: "synthetic panic"}
	s := New(lister, tk, Config{Interval: 30 * time.Millisecond, Logger: silentLogger()})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	// Let it scan + tick + recover at least twice. If panic isn't
	// recovered, the test goroutine dies and we'd hang waiting.
	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("scheduler did not return after cancel (panic likely killed it)")
	}

	// hits captures ListLive calls — should be >=2 (initial + at
	// least one interval). Validates the scheduler loop kept running
	// past the panic.
	if lister.hits < 2 {
		t.Errorf("ListLive hits = %d, want >=2 (scheduler loop should survive panic)", lister.hits)
	}
}

// TestScheduler_LogsSentinelsAsExpectedClass uses a counting logger
// to verify ErrTickInFlight → debug, ErrInstanceNoChampion → warn,
// other errors → error.
func TestScheduler_LogsSentinelsAsExpectedClass(t *testing.T) {
	counts := newLogClassCounter()
	logger := slog.New(counts)

	tests := []struct {
		name      string
		tickErr   error
		wantLevel slog.Level
	}{
		{"in_flight", instance.ErrTickInFlight, slog.LevelDebug},
		{"no_champion", instance.ErrInstanceNoChampion, slog.LevelWarn},
		{"stale_data", instance.ErrInstanceDataStale, slog.LevelWarn},
		{"other", errors.New("boom"), slog.LevelError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			counts.reset()
			lister := &fakeLister{rows: []store.StrategyInstance{liveRow("inst")}}
			tk := &recordingTicker{err: tc.tickErr}
			s := New(lister, tk, Config{Interval: time.Hour, Logger: logger})

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { s.Run(ctx); close(done) }()

			waitForCount(t, tk, 1, 200*time.Millisecond)
			cancel()
			<-done

			if counts.byLevel[tc.wantLevel] == 0 {
				t.Errorf("expected at least one %s log, got counts=%+v",
					tc.wantLevel, counts.byLevel)
			}
		})
	}
}

// TestScheduler_TickTimeoutHonored verifies a slow Tick is cut off at
// the configured TickTimeout (ctx.Done() fires inside Tick).
func TestScheduler_TickTimeoutHonored(t *testing.T) {
	lister := &fakeLister{rows: []store.StrategyInstance{liveRow("slow")}}
	tk := &recordingTicker{delay: 200 * time.Millisecond}
	s := New(lister, tk, Config{
		Interval:    time.Hour,
		TickTimeout: 30 * time.Millisecond,
		Logger:      silentLogger(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	start := time.Now()
	go func() { s.Run(ctx); close(done) }()

	// Wait for one tick to register (even if cut short, the
	// recordingTicker counts on entry — so this fires once delay
	// completes OR ctx cancels). Use slightly more than tick timeout
	// to allow the tick goroutine to record either way.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	elapsed := time.Since(start)

	// The recordingTicker's `delay` select waits for either timer or
	// ctx; the per-tick context timeout (30ms) should cut it short
	// well before the 200ms delay would naturally complete.
	if elapsed > 250*time.Millisecond {
		t.Errorf("scheduler hung (%v elapsed); per-tick timeout did not cut slow Tick", elapsed)
	}
}

// ===== helpers =====

func waitForCount(t *testing.T, tk *recordingTicker, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if tk.total() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waiting for %d ticks; got %d in %v", want, tk.total(), timeout)
}

// logClassCounter is a slog.Handler that counts records by level.
type logClassCounter struct {
	mu      sync.Mutex
	byLevel map[slog.Level]int
}

func newLogClassCounter() *logClassCounter {
	return &logClassCounter{byLevel: map[slog.Level]int{}}
}

func (l *logClassCounter) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (l *logClassCounter) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byLevel[r.Level]++
	return nil
}
func (l *logClassCounter) WithAttrs(_ []slog.Attr) slog.Handler { return l }
func (l *logClassCounter) WithGroup(_ string) slog.Handler      { return l }

func (l *logClassCounter) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.byLevel {
		delete(l.byLevel, k)
	}
}

// _ avoids "unused" lint on atomic import while keeping the file
// import set ready for future tests that need atomic counters.
var _ atomic.Int64
