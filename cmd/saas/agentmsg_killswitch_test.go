package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"quantlab/internal/saas/store"
	"quantlab/internal/wire"
)

// flakyAuditor fails its first `fails` Insert calls, then succeeds — used to
// prove the B1 latch write is load-bearing (a failed latch must not latch a
// freeze that didn't persist).
type flakyAuditor struct {
	rows  []*store.AuditLog
	fails int
}

func (f *flakyAuditor) Insert(_ context.Context, e *store.AuditLog) error {
	if f.fails > 0 {
		f.fails--
		return errors.New("audit insert failed")
	}
	f.rows = append(f.rows, e)
	return nil
}

// managedSet builds a managed-asset set for the scoped auto-freeze tests.
func managedSet(assets ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(assets))
	for _, a := range assets {
		m[a] = struct{}{}
	}
	return m
}

func TestMaxFlaggedDriftBps(t *testing.T) {
	drifts := []driftResult{
		{Asset: "USDT", DriftBps: 999, Flagged: false}, // big but unflagged (dust) — ignored
		{Asset: "BTC", DriftBps: 120, Flagged: true},
		{Asset: "ETH", DriftBps: 340, Flagged: true},
	}
	managed := managedSet("USDT", "BTC", "ETH")
	if got := maxFlaggedDriftBps(drifts, managed); got != 340 {
		t.Errorf("maxFlaggedDriftBps = %v, want 340 (largest flagged)", got)
	}
	if got := maxFlaggedDriftBps(nil, managed); got != 0 {
		t.Errorf("maxFlaggedDriftBps(nil) = %v, want 0", got)
	}
}

// TestMaxFlaggedDriftBps_ScopedToManaged is the core guard for the testnet
// faucet false-positive: a huge flagged drift on an UNMANAGED asset (a
// faucet coin the instance never trades) must NOT count toward the freeze,
// while drift on a managed asset does.
func TestMaxFlaggedDriftBps_ScopedToManaged(t *testing.T) {
	drifts := []driftResult{
		{Asset: "ACH", DriftBps: 10000, Flagged: true},  // faucet coin — unmanaged
		{Asset: "AEVO", DriftBps: 10000, Flagged: true}, // faucet coin — unmanaged
		{Asset: "BTC", DriftBps: 120, Flagged: true},    // managed
	}
	managed := managedSet("BTC", "USDT")
	if got := maxFlaggedDriftBps(drifts, managed); got != 120 {
		t.Errorf("maxFlaggedDriftBps = %v, want 120 (faucet coins must be ignored)", got)
	}
	// Nothing managed drifts → no freeze even with huge faucet drift.
	onlyFaucet := []driftResult{{Asset: "ACH", DriftBps: 10000, Flagged: true}}
	if got := maxFlaggedDriftBps(onlyFaucet, managed); got != 0 {
		t.Errorf("maxFlaggedDriftBps = %v, want 0 (only unmanaged drift)", got)
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
		{"second breach reaches threshold", 300, 1, defaultFreezeDebounceReports},
		{"sentinel suppresses while drifting", 300, killedSentinel, killedSentinel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextDriftStreak(tc.maxBps, tc.prev, defaultFreezeToleranceBps); got != tc.want {
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
	managed := managedSet("BTC")
	breach := []driftResult{{Asset: "BTC", DriftBps: 300, Flagged: true}} // > 200 freeze line
	clean := []driftResult{{Asset: "BTC", DriftBps: 10, Flagged: true}}   // < 200

	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // streak 1 — no fire (debounce N=2)
	if len(fk.calls) != 0 {
		t.Fatalf("fired after 1 breach, want debounce; calls=%d", len(fk.calls))
	}
	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // streak 2 — FIRE
	if len(fk.calls) != 1 {
		t.Fatalf("did not fire on 2nd consecutive breach; calls=%d", len(fk.calls))
	}
	if fk.calls[0].Reason != wire.KillSwitchDiscrepancyDetected {
		t.Errorf("kill reason = %q, want discrepancy_detected", fk.calls[0].Reason)
	}
	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // sentinel — must NOT re-fire
	if len(fk.calls) != 1 {
		t.Errorf("re-fired while still drifting (no sentinel suppression); calls=%d", len(fk.calls))
	}

	// Drift clears, then recurs → must re-arm and fire again.
	h.maybeAutoFreeze(context.Background(), acct, clean, managed)  // reset
	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // 1
	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // 2 → fire
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

// TestClearDriftStreak_ReArmsAutoFreeze proves the §5.13 v2 resume
// re-arm: after a fire latches the killedSentinel, ClearDriftStreak lifts
// it so a STILL-drifting account auto-freezes again WITHOUT needing a
// clean report to intervene (which the sentinel would otherwise demand).
func TestClearDriftStreak_ReArmsAutoFreeze(t *testing.T) {
	fk := &fakeKiller{}
	h := &agentMessageHandler{
		driftStreak: map[string]int{},
		killer:      fk,
		logger:      slog.Default(),
	}
	const acct = "01HKACCT00000000000000000A"
	managed := managedSet("BTC")
	breach := []driftResult{{Asset: "BTC", DriftBps: 300, Flagged: true}}

	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // 1
	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // 2 → fire (latches sentinel)
	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // sentinel suppresses
	if len(fk.calls) != 1 {
		t.Fatalf("setup: calls=%d, want 1 (fired once, then suppressed)", len(fk.calls))
	}

	// Resume re-arms — no clean report in between.
	h.ClearDriftStreak(acct)
	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // 1
	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // 2 → fire again
	if len(fk.calls) != 2 {
		t.Errorf("did not re-fire after ClearDriftStreak; calls=%d, want 2", len(fk.calls))
	}
}

// TestMaybeAutoFreeze_HonorsConfiguredThresholds proves the config overrides
// (effFreeze*) take effect: a lower line + debounce=1 fires on the FIRST
// breach at a drift the default 200bps/N=2 would not have frozen on.
func TestMaybeAutoFreeze_HonorsConfiguredThresholds(t *testing.T) {
	fk := &fakeKiller{}
	h := &agentMessageHandler{
		driftStreak:           map[string]int{},
		killer:                fk,
		logger:                slog.Default(),
		freezeToleranceBps:    100, // below default 200
		freezeDebounceReports: 1,   // fire immediately, no debounce
	}
	managed := managedSet("BTC")
	breach := []driftResult{{Asset: "BTC", DriftBps: 150, Flagged: true}} // >100 line, <200 default

	h.maybeAutoFreeze(context.Background(), "01HKACCT00000000000000000A", breach, managed)
	if len(fk.calls) != 1 {
		t.Fatalf("configured line=100/debounce=1 did not fire on first 150bps breach; calls=%d", len(fk.calls))
	}
}

// TestMaybeAutoFreeze_LatchesWhenPushFails pins B1: when the live kill_switch
// push fails (agent offline, or a connection tearing down), the durable freeze
// latch (the audit row IsAccountFrozen reads back) MUST still be persisted, and
// the sentinel MUST still latch so the freeze isn't re-fired — the agent will
// re-enter HALTED via auth_ok.Frozen on reconnect.
func TestMaybeAutoFreeze_LatchesWhenPushFails(t *testing.T) {
	fk := &fakeKiller{err: errors.New("agent offline")}
	fa := &fakeAuditor{}
	h := &agentMessageHandler{
		driftStreak: map[string]int{},
		killer:      fk,
		auditor:     fa,
		logger:      slog.Default(),
	}
	const acct = "01HKACCT00000000000000000A"
	managed := managedSet("BTC")
	breach := []driftResult{{Asset: "BTC", DriftBps: 300, Flagged: true}}

	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // 1
	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // 2 → fire: latch ok, push fails

	if len(fa.rows) != 1 {
		t.Fatalf("freeze latch not persisted when push failed; audit rows=%d, want 1", len(fa.rows))
	}
	if fa.rows[0].Action != store.AuditActionInstanceKill {
		t.Errorf("latch audit action = %q, want instance.kill", fa.rows[0].Action)
	}

	// Sentinel must suppress repeats despite the push failure — the durable
	// latch + reconnect freeze is the guarantee, not a retried push.
	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // 3
	if len(fa.rows) != 1 {
		t.Errorf("re-latched after a failed push (no sentinel suppression); audit rows=%d, want 1", len(fa.rows))
	}
}

// TestMaybeAutoFreeze_LatchFailureStaysArmed pins the inverse: if the latch
// WRITE itself fails, no freeze is pushed and the streak stays armed (no
// sentinel), so a later report with a recovered store still freezes.
func TestMaybeAutoFreeze_LatchFailureStaysArmed(t *testing.T) {
	fk := &fakeKiller{}
	fa := &flakyAuditor{fails: 1} // first latch write fails, then recovers
	h := &agentMessageHandler{
		driftStreak: map[string]int{},
		killer:      fk,
		auditor:     fa,
		logger:      slog.Default(),
	}
	const acct = "01HKACCT00000000000000000A"
	managed := managedSet("BTC")
	breach := []driftResult{{Asset: "BTC", DriftBps: 300, Flagged: true}}

	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // 1
	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // 2 → latch FAILS
	if len(fk.calls) != 0 {
		t.Fatalf("pushed a freeze whose latch failed to persist; calls=%d, want 0", len(fk.calls))
	}
	if len(fa.rows) != 0 {
		t.Fatalf("audit rows=%d, want 0 (the latch write failed)", len(fa.rows))
	}

	// Streak stayed armed (not sentinel) → next breach with a working store fires.
	h.maybeAutoFreeze(context.Background(), acct, breach, managed) // 3 → latch ok, fire
	if len(fk.calls) != 1 {
		t.Errorf("did not retry freeze after the latch recovered; calls=%d, want 1", len(fk.calls))
	}
	if len(fa.rows) != 1 {
		t.Errorf("audit rows=%d, want 1 (the successful retry)", len(fa.rows))
	}
}

func TestMaybeAutoFreeze_NoKillerIsNoop(t *testing.T) {
	h := &agentMessageHandler{driftStreak: map[string]int{}, logger: slog.Default()} // killer nil
	breach := []driftResult{{Asset: "BTC", DriftBps: 300, Flagged: true}}
	// Must not panic and must be harmless when auto-freeze is unwired.
	h.maybeAutoFreeze(context.Background(), "acct", breach, managedSet("BTC"))
	h.maybeAutoFreeze(context.Background(), "acct", breach, managedSet("BTC"))
}
