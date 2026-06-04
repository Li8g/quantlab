package wshub

import (
	"testing"
	"time"

	"quantlab/internal/wire"
)

// newTestHubEnv mirrors newTestHub but configures the backlog ⑥
// environment-consistency assertion.
func newTestHubEnv(t *testing.T, expectedEnv string, reject bool) (*Hub, string, string) {
	t.Helper()
	svc, _, token, _, accountID := authFixture(t)
	hub := New(svc, Config{
		PingInterval:        50 * time.Millisecond,
		PongTimeout:         50 * time.Millisecond,
		PingMisses:          3,
		AuthTimeout:         2 * time.Second,
		StateSyncTimeout:    2 * time.Second,
		WriteTimeout:        2 * time.Second,
		ExpectedEnvironment: expectedEnv,
		RejectEnvMismatch:   reject,
		Clock:               newFakeClock(time.Unix(1700000000, 0)),
		MsgIDFn:             stableMsgID(),
		NowFn:               func() time.Time { return time.Unix(1700000000, 0) },
	})
	return hub, token, accountID
}

// driveHandshakeEnv sends hello (with Environment) + auth and returns the
// next server frame (auth_ok on accept, auth_fail on env reject).
func driveHandshakeEnv(t *testing.T, pc *pipeConn, accountID, token, env string) wire.Envelope {
	t.Helper()
	pc.clientSend(t, encodeForClient(t, wire.TypeHello, accountID, wire.Hello{
		AgentVersion:  "test-0.0.1",
		AccountID:     accountID,
		SchemaVersion: wire.SchemaVersion,
		Environment:   env,
	}))
	if got := pc.clientReadEnv(t); got.Type != wire.TypeAuthRequired {
		t.Fatalf("expected auth_required, got %q", got.Type)
	}
	pc.clientSend(t, encodeForClient(t, wire.TypeAuth, accountID, wire.Auth{Token: token}))
	return pc.clientReadEnv(t)
}

func TestHandshake_EnvMatchPasses(t *testing.T) {
	hub, token, accountID := newTestHubEnv(t, wire.EnvironmentMainnet, true)
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	got := driveHandshakeEnv(t, pc, accountID, token, wire.EnvironmentMainnet)
	if got.Type != wire.TypeAuthOK {
		t.Fatalf("matching env should pass; got %q payload=%s", got.Type, got.Payload)
	}
}

func TestHandshake_EnvMismatchProdRejects(t *testing.T) {
	hub, token, accountID := newTestHubEnv(t, wire.EnvironmentMainnet, true) // reject = prod
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	got := driveHandshakeEnv(t, pc, accountID, token, wire.EnvironmentTestnet)
	if got.Type != wire.TypeAuthFail {
		t.Fatalf("env mismatch on prod should auth_fail; got %q", got.Type)
	}
	fail, err := wire.DecodePayload[wire.AuthFail](got)
	if err != nil {
		t.Fatalf("decode auth_fail: %v", err)
	}
	if fail.Code != wire.AuthFailEnvironmentMismatch {
		t.Errorf("auth_fail.code = %q, want environment_mismatch", fail.Code)
	}
}

func TestHandshake_EnvMismatchDevWarnsButProceeds(t *testing.T) {
	hub, token, accountID := newTestHubEnv(t, wire.EnvironmentMainnet, false) // no reject = dev/lab
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	got := driveHandshakeEnv(t, pc, accountID, token, wire.EnvironmentTestnet)
	if got.Type != wire.TypeAuthOK {
		t.Fatalf("env mismatch on dev should warn-only and proceed; got %q", got.Type)
	}
}

func TestHandshake_EnvEmptyReportedSkips(t *testing.T) {
	hub, token, accountID := newTestHubEnv(t, wire.EnvironmentMainnet, true)
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	// Empty Environment (pre-⑥ agent) → assertion skipped → handshake ok.
	got := driveHandshakeEnv(t, pc, accountID, token, "")
	if got.Type != wire.TypeAuthOK {
		t.Fatalf("empty reported env should skip the assertion; got %q", got.Type)
	}
}
