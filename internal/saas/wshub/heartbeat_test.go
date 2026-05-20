package wshub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"quantlab/internal/saas/agentauth"
	"quantlab/internal/wire"
)

func newHeartbeatHub(t *testing.T, clock Clock, onStale func(ctx context.Context, accountID string) error) (*Hub, string, string) {
	t.Helper()
	svc, _, token, _, accountID := authFixture(t)
	hub := New(svc, Config{
		Clock:            clock,
		PingInterval:     time.Hour, // never auto-fire; tests drive via clock.fire()
		PongTimeout:      time.Hour,
		PingMisses:       3,
		AuthTimeout:      2 * time.Second,
		StateSyncTimeout: 2 * time.Second,
		WriteTimeout:     2 * time.Second,
		MsgIDFn:          stableMsgID(),
		NowFn:            clock.Now,
		OnStale:          onStale,
	})
	return hub, token, accountID
}

func TestHeartbeat_PingPongHappyPath(t *testing.T) {
	clk := newFakeClock(time.Unix(1700000000, 0))
	hub, token, accountID := newHeartbeatHub(t, clk, nil)
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	driveHandshake(t, pc, accountID, token)
	waitReady(t, hub, accountID)

	// Fire one ping tick; expect a ping frame on the wire.
	clk.fire()
	env := pc.clientReadEnv(t)
	if env.Type != wire.TypePing {
		t.Fatalf("expected ping, got %q", env.Type)
	}
	// Respond with matching pong.
	pc.clientSend(t, encodeForClient(t, wire.TypePong, accountID, wire.Pong{
		EchoMsgID:         env.MsgID,
		AgentNowMs:        time.Now().UnixMilli(),
		ExchangeReachable: true,
	}))

	// Allow the read pump to process the pong.
	time.Sleep(20 * time.Millisecond)

	// Fire another tick; pong cleared, so this should send another ping
	// (NOT close STALE).
	clk.fire()
	env2 := pc.clientReadEnv(t)
	if env2.Type != wire.TypePing {
		t.Fatalf("expected second ping, got %q", env2.Type)
	}
}

func TestHeartbeat_StaleAfterThreeMisses(t *testing.T) {
	clk := newFakeClock(time.Unix(1700000000, 0))
	var staleCount atomic.Int32
	hub, token, accountID := newHeartbeatHub(t, clk, func(ctx context.Context, _ string) error {
		staleCount.Add(1)
		return nil
	})
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	driveHandshake(t, pc, accountID, token)
	waitReady(t, hub, accountID)

	// Tick 1: ping sent, no pong → outstanding marker set.
	clk.fire()
	if env := pc.clientReadEnv(t); env.Type != wire.TypePing {
		t.Fatalf("tick 1: expected ping, got %q", env.Type)
	}
	// Tick 2: outstanding still true → miss=1, then SEND next ping.
	clk.fire()
	if env := pc.clientReadEnv(t); env.Type != wire.TypePing {
		t.Fatalf("tick 2: expected ping, got %q", env.Type)
	}
	// Tick 3: still outstanding → miss=2.
	clk.fire()
	if env := pc.clientReadEnv(t); env.Type != wire.TypePing {
		t.Fatalf("tick 3: expected ping, got %q", env.Type)
	}
	// Tick 4: miss=3, threshold reached → STALE, conn closes.
	clk.fire()

	// Wait for OnStale callback or close.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if staleCount.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if staleCount.Load() == 0 {
		t.Errorf("OnStale not fired after 3 misses")
	}
}

func TestHeartbeat_PongClearsMissCounter(t *testing.T) {
	clk := newFakeClock(time.Unix(1700000000, 0))
	hub, token, accountID := newHeartbeatHub(t, clk, nil)
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	driveHandshake(t, pc, accountID, token)
	waitReady(t, hub, accountID)

	// Fire 5 ping rounds, answering each one. STALE should never trigger
	// even though we tick beyond pingMisses.
	for i := 0; i < 5; i++ {
		clk.fire()
		env := pc.clientReadEnv(t)
		if env.Type != wire.TypePing {
			t.Fatalf("tick %d: expected ping, got %q", i, env.Type)
		}
		pc.clientSend(t, encodeForClient(t, wire.TypePong, accountID, wire.Pong{
			EchoMsgID:         env.MsgID,
			AgentNowMs:        time.Now().UnixMilli(),
			ExchangeReachable: true,
		}))
		time.Sleep(10 * time.Millisecond) // let read pump process pong
	}

	// Confirm connection still alive and Ready.
	cn, err := hub.Registry().Get(accountID)
	if err != nil {
		t.Fatalf("conn unregistered after 5 successful rounds: %v", err)
	}
	if !cn.IsReady() {
		t.Errorf("conn no longer Ready")
	}
}

// silence unused-import warnings if we ever drop a heartbeat case
var _ = agentauth.ErrRevoked
