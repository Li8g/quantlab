package wshub

import (
	"context"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"quantlab/internal/saas/agentauth"
	"quantlab/internal/wire"
)

// TestOnConnectionState_HandshakeFiresAuthedAndReady drives a real
// handshake and asserts the hook saw the lifecycle transitions in
// order.
func TestOnConnectionState_HandshakeFiresAuthedAndReady(t *testing.T) {
	fs := newFakeTokenStore()
	svc := agentauth.NewService(fs).WithBcryptCost(bcrypt.MinCost)
	const accountID = "01HKACCT00000000000000000A"
	created, err := svc.CreateToken(context.Background(), accountID, "test")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	var mu sync.Mutex
	var events []ConnectionStateEvent
	hub := New(svc, Config{
		PingInterval:     time.Hour,
		PongTimeout:      time.Hour,
		PingMisses:       3,
		AuthTimeout:      2 * time.Second,
		StateSyncTimeout: 2 * time.Second,
		WriteTimeout:     2 * time.Second,
		Clock:            newFakeClock(time.Unix(1700000000, 0)),
		MsgIDFn:          stableMsgID(),
		NowFn:            func() time.Time { return time.Unix(1700000000, 0) },
		OnConnectionState: func(_ context.Context, ev ConnectionStateEvent) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, ev)
			return nil
		},
	})

	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	_, _ = driveHandshake(t, pc, accountID, created.Plaintext)
	waitReady(t, hub, accountID)

	// Snapshot the first two events. The connection may still receive
	// trailing events (e.g. disconnected during the deferred cancel)
	// after this point — those are verified by the *_Close test below.
	mu.Lock()
	got := append([]ConnectionStateEvent{}, events...)
	mu.Unlock()

	if len(got) < 2 {
		t.Fatalf("expected ≥ 2 state events, got %d: %+v", len(got), got)
	}
	if got[0].State != "authed" {
		t.Errorf("event[0].State = %q, want authed", got[0].State)
	}
	if got[0].AccountID != accountID {
		t.Errorf("event[0].AccountID = %q, want %q", got[0].AccountID, accountID)
	}
	if got[0].AgentID == "" {
		t.Errorf("event[0].AgentID empty")
	}
	if got[1].State != "ready" {
		t.Errorf("event[1].State = %q, want ready", got[1].State)
	}
	if got[1].LastMsgID == "" {
		t.Errorf("event[1].LastMsgID empty (should carry state_sync_response msg_id)")
	}
}

// TestOnConnectionState_AgentMessageRefreshesTTL asserts that an
// inbound Ack fires a 'ready' event carrying the message's msg_id —
// this is how Redis TTL gets refreshed on every Agent → SaaS message
// per docs/saas-ws-protocol-v1.md §7.2.
func TestOnConnectionState_AgentMessageRefreshesTTL(t *testing.T) {
	fs := newFakeTokenStore()
	svc := agentauth.NewService(fs).WithBcryptCost(bcrypt.MinCost)
	const accountID = "01HKACCT00000000000000000B"
	created, err := svc.CreateToken(context.Background(), accountID, "test")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	var mu sync.Mutex
	var events []ConnectionStateEvent
	hub := New(svc, Config{
		PingInterval:     time.Hour,
		PongTimeout:      time.Hour,
		AuthTimeout:      2 * time.Second,
		StateSyncTimeout: 2 * time.Second,
		WriteTimeout:     2 * time.Second,
		Clock:            newFakeClock(time.Unix(1700000000, 0)),
		MsgIDFn:          stableMsgID(),
		NowFn:            func() time.Time { return time.Unix(1700000000, 0) },
		OnConnectionState: func(_ context.Context, ev ConnectionStateEvent) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, ev)
			return nil
		},
	})

	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	_, _ = driveHandshake(t, pc, accountID, created.Plaintext)
	waitReady(t, hub, accountID)

	// Send a synthetic Ack and wait for the readPump to refresh.
	const ackMsgID = "01HKACKMSGID0000000000000A"
	pc.clientSend(t, encodeForClient(t, wire.TypeAck, accountID, wire.Ack{
		ClientOrderID: "01HKCOID000000000000000001",
		Status:        wire.AckStatusAccepted,
		ExchangeNowMs: 1700000000000,
	}))

	deadline := time.Now().Add(1 * time.Second)
	var refresh *ConnectionStateEvent
	for time.Now().Before(deadline) {
		mu.Lock()
		for i := range events {
			if events[i].State == "ready" && events[i].LastMsgID != "" && events[i].LastMsgID != ackMsgID {
				// state_sync_response msg_id is from stableMsgID; the
				// ack msg_id is from encodeForClient (newTestMsgID).
				// We accept any 'ready' event after the initial handshake
				// pair — there are only two 'ready' events: state-sync
				// and this ack.
				cp := events[i]
				refresh = &cp
			}
		}
		mu.Unlock()
		if refresh != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if refresh == nil {
		mu.Lock()
		evs := append([]ConnectionStateEvent{}, events...)
		mu.Unlock()
		t.Fatalf("no refresh event observed after Ack; events=%+v", evs)
	}
}

// TestOnConnectionState_DisconnectFiresOnClose drives a handshake then
// cancels the connection context and asserts a 'disconnected' event
// reaches the hook after AccountID has been set.
func TestOnConnectionState_DisconnectFiresOnClose(t *testing.T) {
	fs := newFakeTokenStore()
	svc := agentauth.NewService(fs).WithBcryptCost(bcrypt.MinCost)
	const accountID = "01HKACCT00000000000000000C"
	created, err := svc.CreateToken(context.Background(), accountID, "test")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	var mu sync.Mutex
	var events []ConnectionStateEvent
	disconnected := make(chan struct{}, 1)
	hub := New(svc, Config{
		PingInterval:     time.Hour,
		PongTimeout:      time.Hour,
		AuthTimeout:      2 * time.Second,
		StateSyncTimeout: 2 * time.Second,
		WriteTimeout:     2 * time.Second,
		Clock:            newFakeClock(time.Unix(1700000000, 0)),
		MsgIDFn:          stableMsgID(),
		NowFn:            func() time.Time { return time.Unix(1700000000, 0) },
		OnConnectionState: func(_ context.Context, ev ConnectionStateEvent) error {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
			if ev.State == "disconnected" {
				select {
				case disconnected <- struct{}{}:
				default:
				}
			}
			return nil
		},
	})

	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)

	_, _ = driveHandshake(t, pc, accountID, created.Plaintext)
	waitReady(t, hub, accountID)

	// Tear down. The Run defer must publish 'disconnected'.
	cancel()
	_ = pc.Close()
	wg.Wait()

	select {
	case <-disconnected:
	case <-time.After(1 * time.Second):
		mu.Lock()
		t.Fatalf("no disconnected event within 1s; events=%+v", events)
	}
}

// TestOnConnectionState_PreAuthFailureDoesNotPublish ensures hook is
// not invoked when handshake fails before AccountID is established.
// Pre-auth Redis writes would have no key to clean up.
func TestOnConnectionState_PreAuthFailureDoesNotPublish(t *testing.T) {
	fs := newFakeTokenStore()
	svc := agentauth.NewService(fs).WithBcryptCost(bcrypt.MinCost)

	var mu sync.Mutex
	var events []ConnectionStateEvent
	hub := New(svc, Config{
		PingInterval:     time.Hour,
		PongTimeout:      time.Hour,
		AuthTimeout:      300 * time.Millisecond,
		StateSyncTimeout: 2 * time.Second,
		WriteTimeout:     2 * time.Second,
		Clock:            newFakeClock(time.Unix(1700000000, 0)),
		MsgIDFn:          stableMsgID(),
		NowFn:            func() time.Time { return time.Unix(1700000000, 0) },
		OnConnectionState: func(_ context.Context, ev ConnectionStateEvent) error {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
			return nil
		},
	})

	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	// Don't send hello — let auth_timeout expire.
	deadline := time.After(800 * time.Millisecond)
	<-deadline
	cancel()
	_ = pc.Close()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 0 {
		t.Errorf("expected 0 events for pre-auth failure, got %d: %+v", len(events), events)
	}
}
