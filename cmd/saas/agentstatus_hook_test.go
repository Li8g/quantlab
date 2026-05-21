package main

import (
	"context"
	"sync"
	"testing"

	"quantlab/internal/saas/agentstatus"
	"quantlab/internal/saas/wshub"
)

// captureReporter records calls so the hook test can assert routing
// without spinning up Redis.
type captureReporter struct {
	mu      sync.Mutex
	sets    []captureSet
	deletes []string
}

type captureSet struct {
	accountID string
	status    agentstatus.Status
}

func (c *captureReporter) Set(_ context.Context, accountID string, s agentstatus.Status) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sets = append(c.sets, captureSet{accountID: accountID, status: s})
	return nil
}
func (c *captureReporter) Get(_ context.Context, _ string) (agentstatus.Status, bool, error) {
	return agentstatus.Status{}, false, nil
}
func (c *captureReporter) Delete(_ context.Context, accountID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deletes = append(c.deletes, accountID)
	return nil
}

func TestMakeConnectionStateHook_NonDisconnectedCallsSet(t *testing.T) {
	r := &captureReporter{}
	hook := makeConnectionStateHook(r)

	for _, state := range []string{"authed", "ready", "stale"} {
		if err := hook(context.Background(), wshub.ConnectionStateEvent{
			AccountID: "ACCT",
			AgentID:   "AGT",
			State:     state,
			LastMsgID: "M-" + state,
			NowMs:     12345,
		}); err != nil {
			t.Fatalf("hook(%s): %v", state, err)
		}
	}

	if len(r.deletes) != 0 {
		t.Errorf("unexpected deletes: %v", r.deletes)
	}
	if len(r.sets) != 3 {
		t.Fatalf("sets len = %d, want 3 (one per non-disconnect state)", len(r.sets))
	}
	wantStates := []agentstatus.ConnectionState{
		agentstatus.StateAuthed,
		agentstatus.StateReady,
		agentstatus.StateStale,
	}
	for i, want := range wantStates {
		if r.sets[i].status.ConnectionState != want {
			t.Errorf("sets[%d].ConnectionState = %q, want %q",
				i, r.sets[i].status.ConnectionState, want)
		}
		if r.sets[i].status.LastSeenMs != 12345 {
			t.Errorf("sets[%d].LastSeenMs = %d, want 12345",
				i, r.sets[i].status.LastSeenMs)
		}
	}
}

func TestMakeConnectionStateHook_DisconnectedCallsDelete(t *testing.T) {
	r := &captureReporter{}
	hook := makeConnectionStateHook(r)

	if err := hook(context.Background(), wshub.ConnectionStateEvent{
		AccountID: "ACCT-X",
		AgentID:   "AGT",
		State:     "disconnected",
	}); err != nil {
		t.Fatalf("hook: %v", err)
	}
	if len(r.sets) != 0 {
		t.Errorf("expected zero Sets on disconnected, got %d", len(r.sets))
	}
	if len(r.deletes) != 1 || r.deletes[0] != "ACCT-X" {
		t.Errorf("deletes = %v, want [ACCT-X]", r.deletes)
	}
}
