package main

import (
	"context"
	"log/slog"
	"testing"

	"quantlab/internal/saas/store"
	"quantlab/internal/wire"
)

func TestMaxFlaggedDriftBps(t *testing.T) {
	drifts := []driftResult{
		{Asset: "USDT", DriftBps: 999, Flagged: false}, // big but unflagged (dust) — ignored
		{Asset: "BTC", DriftBps: 120, Flagged: true},
		{Asset: "ETH", DriftBps: 340, Flagged: true},
	}
	if got := maxFlaggedDriftBps(drifts); got != 340 {
		t.Errorf("maxFlaggedDriftBps = %v, want 340 (largest flagged)", got)
	}
	if got := maxFlaggedDriftBps(nil); got != 0 {
		t.Errorf("maxFlaggedDriftBps(nil) = %v, want 0", got)
	}
}

func TestNextDriftStreak(t *testing.T) {
	for _, tc := range []struct {
		name       string
		maxBps     float64
		prev, want int
	}{
		{"below line resets", 50, 1, 0},
		{"below line lifts sentinel", 50, killedSentinel, 0},
		{"first breach", 300, 0, 1},
		{"second breach reaches threshold", 300, 1, freezeDebounceReports},
		{"sentinel suppresses while drifting", 300, killedSentinel, killedSentinel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextDriftStreak(tc.maxBps, tc.prev); got != tc.want {
				t.Errorf("nextDriftStreak(%v,%d) = %d, want %d", tc.maxBps, tc.prev, got, tc.want)
			}
		})
	}
}

// fakeKiller records SendKillSwitch calls for the auto-freeze trigger test.
type fakeKiller struct {
	calls []wire.KillSwitch
	err   error
}

func (f *fakeKiller) SendKillSwitch(_ string, ks wire.KillSwitch) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, ks)
	return nil
}

// fakeAuditor records AuditLog inserts for the action-trail assertions.
type fakeAuditor struct{ rows []*store.AuditLog }

func (f *fakeAuditor) Insert(_ context.Context, e *store.AuditLog) error {
	f.rows = append(f.rows, e)
	return nil
}

func TestMaybeAutoFreeze_DebouncesAndLatches(t *testing.T) {
	fk := &fakeKiller{}
	fa := &fakeAuditor{}
	h := &agentMessageHandler{
		driftStreak: map[string]int{},
		killer:      fk,
		auditor:     fa,
		logger:      slog.Default(),
	}
	const acct = "01HKACCT00000000000000000A"
	breach := []driftResult{{Asset: "BTC", DriftBps: 300, Flagged: true}} // > 200 freeze line
	clean := []driftResult{{Asset: "BTC", DriftBps: 10, Flagged: true}}   // < 200

	h.maybeAutoFreeze(context.Background(), acct, breach) // streak 1 — no fire (debounce N=2)
	if len(fk.calls) != 0 {
		t.Fatalf("fired after 1 breach, want debounce; calls=%d", len(fk.calls))
	}
	h.maybeAutoFreeze(context.Background(), acct, breach) // streak 2 — FIRE
	if len(fk.calls) != 1 {
		t.Fatalf("did not fire on 2nd consecutive breach; calls=%d", len(fk.calls))
	}
	if fk.calls[0].Reason != wire.KillSwitchDiscrepancyDetected {
		t.Errorf("kill reason = %q, want discrepancy_detected", fk.calls[0].Reason)
	}
	h.maybeAutoFreeze(context.Background(), acct, breach) // sentinel — must NOT re-fire
	if len(fk.calls) != 1 {
		t.Errorf("re-fired while still drifting (no sentinel suppression); calls=%d", len(fk.calls))
	}

	// Drift clears, then recurs → must re-arm and fire again.
	h.maybeAutoFreeze(context.Background(), acct, clean)  // reset
	h.maybeAutoFreeze(context.Background(), acct, breach) // 1
	h.maybeAutoFreeze(context.Background(), acct, breach) // 2 → fire
	if len(fk.calls) != 2 {
		t.Errorf("did not re-arm after drift cleared; calls=%d, want 2", len(fk.calls))
	}

	// Each fire records one instance.kill audit row (actor=system, auto).
	if len(fa.rows) != 2 {
		t.Fatalf("audit rows = %d, want 2 (one per fire)", len(fa.rows))
	}
	if fa.rows[0].Action != store.AuditActionInstanceKill {
		t.Errorf("audit action = %q, want instance.kill", fa.rows[0].Action)
	}
	if fa.rows[0].Actor != "system" {
		t.Errorf("audit actor = %q, want system (auto trigger)", fa.rows[0].Actor)
	}
}

func TestMaybeAutoFreeze_NoKillerIsNoop(t *testing.T) {
	h := &agentMessageHandler{driftStreak: map[string]int{}, logger: slog.Default()} // killer nil
	breach := []driftResult{{Asset: "BTC", DriftBps: 300, Flagged: true}}
	// Must not panic and must be harmless when auto-freeze is unwired.
	h.maybeAutoFreeze(context.Background(), "acct", breach)
	h.maybeAutoFreeze(context.Background(), "acct", breach)
}
