package wshub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"quantlab/internal/wire"
)

// driveHandshake walks the Agent side of §4.2:
//
//	send hello → recv auth_required → send auth → recv auth_ok → recv state_sync_request → send state_sync_response
//
// All messages use the same accountID. Returns the (auth_ok, state_sync_request)
// envelopes so the caller can assert on payloads.
func driveHandshake(t *testing.T, pc *pipeConn, accountID, token string) (wire.Envelope, wire.Envelope) {
	t.Helper()

	// 1. hello
	pc.clientSend(t, encodeForClient(t, wire.TypeHello, accountID, wire.Hello{
		AgentVersion:  "test-0.0.1",
		AccountID:     accountID,
		SchemaVersion: wire.SchemaVersion,
	}))

	// 2. expect auth_required
	if got := pc.clientReadEnv(t); got.Type != wire.TypeAuthRequired {
		t.Fatalf("expected auth_required, got %q", got.Type)
	}

	// 3. auth
	pc.clientSend(t, encodeForClient(t, wire.TypeAuth, accountID, wire.Auth{Token: token}))

	// 4. expect auth_ok
	authOK := pc.clientReadEnv(t)
	if authOK.Type != wire.TypeAuthOK {
		t.Fatalf("expected auth_ok, got %q payload=%s", authOK.Type, authOK.Payload)
	}

	// 5. expect state_sync_request
	ssReq := pc.clientReadEnv(t)
	if ssReq.Type != wire.TypeStateSyncRequest {
		t.Fatalf("expected state_sync_request, got %q", ssReq.Type)
	}

	// 6. state_sync_response
	pc.clientSend(t, encodeForClient(t, wire.TypeStateSyncResponse, accountID, wire.StateSyncResponse{
		ReportedAtMs: time.Now().UnixMilli(),
		Positions:    []wire.Position{},
		OpenOrders:   []wire.OpenOrder{},
	}))

	return authOK, ssReq
}

// newTestHub builds a Hub with bcrypt MinCost auth, fakeClock, stable msg
// id, and infinite-ish timeouts (so a slow test doesn't trip auth_timeout).
func newTestHub(t *testing.T) (*Hub, *fakeTokenStore, string, string) {
	t.Helper()
	svc, fs, token, _, accountID := authFixture(t)
	hub := New(svc, Config{
		PingInterval:     50 * time.Millisecond, // fast for tests
		PongTimeout:      50 * time.Millisecond,
		PingMisses:       3,
		AuthTimeout:      2 * time.Second,
		StateSyncTimeout: 2 * time.Second,
		WriteTimeout:     2 * time.Second,
		Clock:            newFakeClock(time.Unix(1700000000, 0)),
		MsgIDFn:          stableMsgID(),
		NowFn:            func() time.Time { return time.Unix(1700000000, 0) },
	})
	return hub, fs, token, accountID
}

func runConnInBg(hub *Hub, pc *pipeConn) (context.CancelFunc, *sync.WaitGroup) {
	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		hub.runConn(ctx, pc)
	}()
	return cancel, wg
}

func TestHandshake_HappyPath(t *testing.T) {
	hub, _, token, accountID := newTestHub(t)
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	authOK, _ := driveHandshake(t, pc, accountID, token)

	okPayload, err := wire.DecodePayload[wire.AuthOK](authOK)
	if err != nil {
		t.Fatalf("decode auth_ok: %v", err)
	}
	if okPayload.AgentID == "" {
		t.Errorf("auth_ok.agent_id empty")
	}
	// Connection should now be Ready and registered.
	// Allow a tiny moment for the connection goroutine to set phaseReady
	// after our state_sync_response is processed.
	waitReady(t, hub, accountID)
}

// waitReady polls the registry for a Ready connection on accountID. Caps
// at 1s to avoid hanging tests.
func waitReady(t *testing.T, hub *Hub, accountID string) {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		cn, err := hub.Registry().Get(accountID)
		if err == nil && cn.IsReady() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("connection not Ready within 1s")
}

func TestHandshake_RejectsBadSchema(t *testing.T) {
	hub, _, _, accountID := newTestHub(t)
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	pc.clientSend(t, encodeForClient(t, wire.TypeHello, accountID, wire.Hello{
		AgentVersion:  "test-0.0.1",
		AccountID:     accountID,
		SchemaVersion: "v9.9.9",
	}))

	// auth_fail expected (and then close)
	got := pc.clientReadEnv(t)
	if got.Type != wire.TypeAuthFail {
		t.Fatalf("expected auth_fail, got %q", got.Type)
	}
	fail, err := wire.DecodePayload[wire.AuthFail](got)
	if err != nil {
		t.Fatalf("decode auth_fail: %v", err)
	}
	if fail.Code != wire.AuthFailSchemaMismatch {
		t.Errorf("auth_fail.code = %q, want schema_mismatch", fail.Code)
	}
}

func TestHandshake_RejectsBadToken(t *testing.T) {
	hub, _, _, accountID := newTestHub(t)
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	pc.clientSend(t, encodeForClient(t, wire.TypeHello, accountID, wire.Hello{
		AgentVersion:  "test-0.0.1",
		AccountID:     accountID,
		SchemaVersion: wire.SchemaVersion,
	}))
	_ = pc.clientReadEnv(t) // auth_required

	pc.clientSend(t, encodeForClient(t, wire.TypeAuth, accountID, wire.Auth{
		Token: "agt_not_a_real_token_1234567890",
	}))

	got := pc.clientReadEnv(t)
	if got.Type != wire.TypeAuthFail {
		t.Fatalf("expected auth_fail, got %q", got.Type)
	}
	fail, _ := wire.DecodePayload[wire.AuthFail](got)
	if fail.Code != wire.AuthFailInvalidToken {
		t.Errorf("auth_fail.code = %q, want invalid_token", fail.Code)
	}
}

func TestHandshake_RejectsAccountMismatch(t *testing.T) {
	hub, _, token, _ := newTestHub(t)
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	// hello claims a different accountID than the token binds.
	pc.clientSend(t, encodeForClient(t, wire.TypeHello, "01HKWRONGACCT0000000000000A", wire.Hello{
		AgentVersion:  "test-0.0.1",
		AccountID:     "01HKWRONGACCT0000000000000A",
		SchemaVersion: wire.SchemaVersion,
	}))
	_ = pc.clientReadEnv(t) // auth_required
	pc.clientSend(t, encodeForClient(t, wire.TypeAuth, "01HKWRONGACCT0000000000000A", wire.Auth{Token: token}))

	// Wait for auth_fail; allow up to a couple of envelopes if the
	// server pushes something else first.
	for i := 0; i < 3; i++ {
		got := pc.clientReadEnv(t)
		if got.Type == wire.TypeAuthFail {
			fail, _ := wire.DecodePayload[wire.AuthFail](got)
			if fail.Code != wire.AuthFailAccountMismatch {
				t.Errorf("auth_fail.code = %q, want account_mismatch", fail.Code)
			}
			return
		}
	}
	t.Fatalf("never received auth_fail")
}

// TestHandshake_RejectsRevokedToken exercises the revoked-token path.
func TestHandshake_RejectsRevokedToken(t *testing.T) {
	svc, _, token, agentID, accountID := authFixture(t)
	if err := svc.Revoke(context.Background(), agentID, time.Now()); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	hub := New(svc, Config{
		Clock:        newFakeClock(time.Unix(1700000000, 0)),
		PingInterval: 50 * time.Millisecond, PongTimeout: 50 * time.Millisecond,
		PingMisses: 3, AuthTimeout: 2 * time.Second,
		StateSyncTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
		MsgIDFn: stableMsgID(),
		NowFn:   func() time.Time { return time.Unix(1700000000, 0) },
	})
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	pc.clientSend(t, encodeForClient(t, wire.TypeHello, accountID, wire.Hello{
		AccountID: accountID, SchemaVersion: wire.SchemaVersion,
	}))
	_ = pc.clientReadEnv(t) // auth_required
	pc.clientSend(t, encodeForClient(t, wire.TypeAuth, accountID, wire.Auth{Token: token}))

	got := pc.clientReadEnv(t)
	if got.Type != wire.TypeAuthFail {
		t.Fatalf("expected auth_fail, got %q", got.Type)
	}
	fail, _ := wire.DecodePayload[wire.AuthFail](got)
	if fail.Code != wire.AuthFailRevoked {
		t.Errorf("auth_fail.code = %q, want revoked", fail.Code)
	}
}

// TestHandshake_AuthTimeout exercises the auth_timeout path: agent
// connects but never sends `auth`. The connection should close on its
// own.
func TestHandshake_AuthTimeout(t *testing.T) {
	svc, _, _, _, accountID := authFixture(t)
	hub := New(svc, Config{
		Clock:        newFakeClock(time.Unix(1700000000, 0)),
		PingInterval: 50 * time.Millisecond, PongTimeout: 50 * time.Millisecond,
		PingMisses:       3,
		AuthTimeout:      100 * time.Millisecond, // very short
		StateSyncTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
		MsgIDFn: stableMsgID(),
		NowFn:   func() time.Time { return time.Unix(1700000000, 0) },
	})
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); wg.Wait() }()

	pc.clientSend(t, encodeForClient(t, wire.TypeHello, accountID, wire.Hello{
		AccountID: accountID, SchemaVersion: wire.SchemaVersion,
	}))
	_ = pc.clientReadEnv(t) // auth_required

	// Don't send auth; wait for conn to die. ReadFrame on the agent
	// side should eventually error.
	deadline := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case _, ok := <-pc.clientReads:
			if !ok {
				break loop // conn closed
			}
			// drain any extra frames
		case <-deadline:
			t.Fatalf("conn did not close within 500ms after auth timeout")
		}
	}
}

var _ = errors.Is // keep import used across files
