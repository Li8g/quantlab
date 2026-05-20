package wshub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"quantlab/internal/saas/agentauth"
	"quantlab/internal/wire"
)

// connPhase is the per-Connection state machine. Transitions are linear
// up to phaseReady and final; STALE / errors jump straight to phaseClosed.
type connPhase int32

const (
	phaseConnecting   connPhase = iota // upgraded, awaiting hello
	phaseAwaitingAuth                  // hello received, awaiting auth
	phaseAuthed                        // auth_ok sent, awaiting state_sync_response
	phaseReady                         // state_sync_response received, business traffic flows
	phaseClosed                        // terminal
)

// Connection is one upgraded Agent socket plus the goroutines driving
// its read pump, write pump, and heartbeat ticker. AccountID is empty
// until phaseAuthed.
//
// Lifecycle: NewConnection → handshake → Run (blocks until close).
// Caller (Hub.ServeWS) is one goroutine; Connection spawns the write
// pump and heartbeat ticker.
type Connection struct {
	hub  *Hub
	conn Conn
	log  *slog.Logger

	// AccountID is set in phaseAuthed and never changes thereafter.
	AccountID string
	AgentID   string

	phase atomic.Int32 // connPhase

	// outbox carries already-encoded frames to the write pump. Capacity
	// chosen so a slow Agent does not block the read pump for one tick;
	// overflow signals a stuck consumer and the connection is closed.
	outbox chan []byte

	// pong tracking. ping_msg_id → 1; cleared when matching pong arrives.
	// Heartbeat goroutine bumps misses on each tick where the most-recent
	// ping is still unanswered.
	pongMu        sync.Mutex
	pendingPingID string

	closeOnce sync.Once
	closeCh   chan struct{} // Close() closes this to begin shutdown
	doneCh    chan struct{} // writePump closes this when fully drained
}

// NewConnection wraps a freshly-upgraded Conn under hub's policy. It
// does NOT start any goroutines; call Run to start the pumps.
func NewConnection(hub *Hub, conn Conn, log *slog.Logger) *Connection {
	if log == nil {
		log = slog.Default()
	}
	return &Connection{
		hub:     hub,
		conn:    conn,
		log:     log,
		outbox:  make(chan []byte, 32),
		closeCh: make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

// Run drives the connection through handshake and then the steady-state
// read pump. It returns when the connection is closed (clean or error).
// Blocks the caller's goroutine — Hub.ServeWS uses this directly.
func (c *Connection) Run(ctx context.Context) {
	defer c.Close()

	// Write pump runs concurrently from start so handshake replies can
	// be queued without a read-write race.
	go c.writePump(ctx)

	if err := c.doHandshake(ctx); err != nil {
		c.log.Info("ws_handshake_failed",
			"account_id", c.AccountID, "err", err)
		return
	}

	// Heartbeat ticker starts after handshake — pings only make sense
	// once the connection is Ready.
	go c.heartbeatLoop(ctx)

	c.readPump(ctx)
}

// Close transitions the connection to phaseClosed and signals the write
// pump (which owns the final conn.Close()) to drain and exit. Idempotent.
//
// Close returns immediately; writePump continues to drain in its own
// goroutine. Callers that need to wait for full teardown can call
// Wait() afterward. The Agent sees buffered handshake auth_fail /
// wire.Error replies because writePump finishes draining the outbox
// before tearing down the socket.
func (c *Connection) Close() error {
	c.closeOnce.Do(func() {
		c.phase.Store(int32(phaseClosed))
		close(c.closeCh)
	})
	return nil
}

// Wait blocks until writePump has finished draining and closed the
// underlying socket. Only safe to call after Run; calling on a raw
// Connection that never started its goroutines will deadlock.
func (c *Connection) Wait() {
	<-c.doneCh
}

// IsReady reports whether the connection is in phaseReady. Hub.Dispatch
// gates on this — TradeCommand sent before phaseReady would arrive
// before state_sync completed.
func (c *Connection) IsReady() bool {
	return connPhase(c.phase.Load()) == phaseReady
}

// SendFrame enqueues an already-encoded frame to the write pump.
// Returns ErrConnClosed if the connection is shut down or the outbox
// is full (slow consumer policy: drop + close).
func (c *Connection) SendFrame(frame []byte) error {
	select {
	case <-c.closeCh:
		return ErrConnClosed
	default:
	}
	select {
	case c.outbox <- frame:
		return nil
	case <-c.closeCh:
		return ErrConnClosed
	default:
		// Outbox full — slow consumer. Close the connection so the
		// Agent reconnects and state-syncs fresh.
		c.log.Warn("ws_outbox_full_closing",
			"account_id", c.AccountID)
		_ = c.Close()
		return ErrConnClosed
	}
}

// writePump drains c.outbox into the conn. Owns the final conn.Close()
// + doneCh signal, so Close() can synchronize on full teardown.
//
// On steady state: blocks on (outbox | closeCh | ctx.Done). On shutdown:
// flushes remaining outbox entries up to a 200ms drain budget, then
// closes the conn and doneCh.
func (c *Connection) writePump(ctx context.Context) {
	defer close(c.doneCh)
	defer func() { _ = c.conn.Close() }()
	for {
		select {
		case frame := <-c.outbox:
			wctx, cancel := context.WithTimeout(ctx, c.hub.writeTimeout)
			err := c.conn.WriteFrame(wctx, frame)
			cancel()
			if err != nil {
				if !errors.Is(err, ErrConnClosed) && !errors.Is(err, context.Canceled) {
					c.log.Warn("ws_write_failed",
						"account_id", c.AccountID, "err", err)
				}
				// fall through to drain — no point flushing more on a
				// dead socket, but be consistent and exit cleanly.
				c.drainOutbox(200 * time.Millisecond)
				return
			}
		case <-c.closeCh:
			c.drainOutbox(200 * time.Millisecond)
			return
		case <-ctx.Done():
			c.drainOutbox(200 * time.Millisecond)
			return
		}
	}
}

// drainOutbox flushes any buffered frames within the budget. Best-effort:
// write errors on already-dead sockets are silent.
func (c *Connection) drainOutbox(budget time.Duration) {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		select {
		case frame := <-c.outbox:
			wctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			_ = c.conn.WriteFrame(wctx, frame)
			cancel()
		default:
			return
		}
	}
}

// readPump runs after handshake and dispatches messages by type until
// the connection is closed.
func (c *Connection) readPump(ctx context.Context) {
	for {
		select {
		case <-c.closeCh:
			return
		default:
		}
		env, err := c.readEnvelope(ctx)
		if err != nil {
			if !errors.Is(err, ErrConnClosed) && !errors.Is(err, context.Canceled) {
				c.log.Info("ws_read_failed",
					"account_id", c.AccountID, "err", err)
			}
			return
		}
		c.dispatchInbound(ctx, env)
	}
}

// readEnvelope reads one frame and decodes the envelope. Decoding errors
// are written back as application-level wire.Error frames per §5.15.
func (c *Connection) readEnvelope(ctx context.Context) (wire.Envelope, error) {
	frame, err := c.conn.ReadFrame(ctx)
	if err != nil {
		return wire.Envelope{}, err
	}
	env, err := wire.DecodeEnvelope(frame)
	if err != nil {
		c.replyError(ctx, errorCodeForDecode(err), err.Error(), "")
		return wire.Envelope{}, err
	}
	return env, nil
}

// dispatchInbound is the post-handshake message router. Pre-Ready
// messages other than ping/pong are protocol errors.
func (c *Connection) dispatchInbound(ctx context.Context, env wire.Envelope) {
	switch env.Type {
	case wire.TypePong:
		c.handlePong(env)
	case wire.TypeAck, wire.TypeOrderUpdate, wire.TypeDeltaReport:
		// Always structured-log so dev/test traffic is visible; also
		// fire the OnAgentMessage hook so downstream persistence
		// (Step 4/5) can react without parsing logs.
		c.log.Info("ws_agent_msg",
			"account_id", c.AccountID, "type", env.Type, "msg_id", env.MsgID,
			"payload", string(env.Payload))
		if c.hub.onAgentMessage != nil {
			if err := c.hub.onAgentMessage(ctx, c.AccountID, env); err != nil {
				c.log.Warn("ws_agent_msg_hook_err",
					"account_id", c.AccountID, "type", env.Type, "err", err)
			}
		}
	case wire.TypeError:
		c.log.Warn("ws_agent_error",
			"account_id", c.AccountID, "msg_id", env.MsgID,
			"payload", string(env.Payload))
	default:
		c.replyError(ctx, wire.ErrorCodeUnknownType,
			fmt.Sprintf("unexpected type after handshake: %q", env.Type),
			env.MsgID)
	}
}

// replyError sends a wire.Error frame. Best-effort — failures during
// teardown are silent.
func (c *Connection) replyError(_ context.Context, code wire.ErrorCode, msg, refMsgID string) {
	payload := wire.Error{Code: code, Message: msg, RefMsgID: refMsgID}
	raw, _ := wire.EncodeMessage(wire.TypeError, c.hub.msgIDFn(), c.hub.nowMs(),
		c.AccountID, payload)
	_ = c.SendFrame(raw)
}

func errorCodeForDecode(err error) wire.ErrorCode {
	switch {
	case errors.Is(err, wire.ErrSchemaMismatch):
		return wire.ErrorCodeSchemaMismatch
	case errors.Is(err, wire.ErrUnknownType):
		return wire.ErrorCodeUnknownType
	case errors.Is(err, wire.ErrInvalidEnvelope):
		return wire.ErrorCodeInvalidEnvelope
	default:
		return wire.ErrorCodeDecodeFailed
	}
}

// doHandshake runs §4.2:
//
//	hello → auth_required → auth → auth_ok / auth_fail → state_sync_request → state_sync_response
//
// On error, the relevant auth_fail or wire.Error is sent before close.
func (c *Connection) doHandshake(ctx context.Context) error {
	// Step 1: read hello
	helloCtx, cancel := context.WithTimeout(ctx, c.hub.authTimeout)
	defer cancel()
	env, err := c.readEnvelope(helloCtx)
	if err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	if env.Type != wire.TypeHello {
		c.replyError(ctx, wire.ErrorCodeUnknownType,
			"expected hello as first frame", env.MsgID)
		return fmt.Errorf("expected hello, got %q", env.Type)
	}
	hello, err := wire.DecodePayload[wire.Hello](env)
	if err != nil {
		return fmt.Errorf("decode hello: %w", err)
	}
	if hello.SchemaVersion != wire.SchemaVersion {
		c.sendAuthFail(ctx, wire.AuthFailSchemaMismatch,
			"schema_version mismatch")
		return fmt.Errorf("schema_version mismatch")
	}
	c.phase.Store(int32(phaseAwaitingAuth))

	// Step 2: send auth_required
	if err := c.sendTyped(ctx, wire.TypeAuthRequired, wire.AuthRequired{}); err != nil {
		return fmt.Errorf("send auth_required: %w", err)
	}

	// Step 3: read auth within authTimeout
	authCtx, cancelAuth := context.WithTimeout(ctx, c.hub.authTimeout)
	defer cancelAuth()
	env, err = c.readEnvelope(authCtx)
	if err != nil {
		return fmt.Errorf("read auth: %w", err)
	}
	if env.Type != wire.TypeAuth {
		c.replyError(ctx, wire.ErrorCodeUnknownType,
			"expected auth", env.MsgID)
		return fmt.Errorf("expected auth, got %q", env.Type)
	}
	auth, err := wire.DecodePayload[wire.Auth](env)
	if err != nil {
		return fmt.Errorf("decode auth: %w", err)
	}

	// Step 4: verify token
	verified, verr := c.hub.auth.Verify(ctx, auth.Token, c.hub.now())
	if verr != nil {
		code := mapAuthErr(verr)
		c.sendAuthFail(ctx, code, verr.Error())
		return fmt.Errorf("auth verify: %w", verr)
	}
	// hello.account_id must match the token's bound AccountID.
	if hello.AccountID != verified.AccountID {
		c.sendAuthFail(ctx, wire.AuthFailAccountMismatch,
			"hello.account_id does not match token binding")
		return fmt.Errorf("account_mismatch: hello=%q token=%q",
			hello.AccountID, verified.AccountID)
	}
	c.AgentID = verified.AgentID
	c.AccountID = verified.AccountID

	// Step 5: send auth_ok
	if err := c.sendTyped(ctx, wire.TypeAuthOK, wire.AuthOK{
		ServerNowMs: c.hub.nowMs(),
		AgentID:     verified.AgentID,
	}); err != nil {
		return fmt.Errorf("send auth_ok: %w", err)
	}
	c.phase.Store(int32(phaseAuthed))

	// Register so Dispatch can find us. Replaces any stale conn for
	// the same AccountID.
	c.hub.registry.Register(c)

	// Step 6: state_sync_request → state_sync_response
	if err := c.sendTyped(ctx, wire.TypeStateSyncRequest, wire.StateSyncRequest{}); err != nil {
		return fmt.Errorf("send state_sync_request: %w", err)
	}
	syncCtx, cancelSync := context.WithTimeout(ctx, c.hub.stateSyncTimeout)
	defer cancelSync()
	env, err = c.readEnvelope(syncCtx)
	if err != nil {
		return fmt.Errorf("read state_sync_response: %w", err)
	}
	if env.Type != wire.TypeStateSyncResponse {
		c.replyError(ctx, wire.ErrorCodeUnknownType,
			"expected state_sync_response", env.MsgID)
		return fmt.Errorf("expected state_sync_response, got %q", env.Type)
	}
	// Persisting the snapshot is Step 4/5 work; for v1 we just log.
	if c.hub.onStateSync != nil {
		raw, _ := json.Marshal(env.Payload)
		_ = c.hub.onStateSync(ctx, c.AccountID, raw)
	}

	c.phase.Store(int32(phaseReady))
	c.log.Info("ws_agent_ready",
		"account_id", c.AccountID, "agent_id", c.AgentID)
	return nil
}

func mapAuthErr(err error) wire.AuthFailCode {
	switch {
	case errors.Is(err, agentauth.ErrRevoked):
		return wire.AuthFailRevoked
	case errors.Is(err, agentauth.ErrUnknownAgent),
		errors.Is(err, agentauth.ErrBadSecret),
		errors.Is(err, agentauth.ErrInvalidFormat):
		return wire.AuthFailInvalidToken
	default:
		return wire.AuthFailInvalidToken
	}
}

func (c *Connection) sendAuthFail(ctx context.Context, code wire.AuthFailCode, reason string) {
	_ = c.sendTyped(ctx, wire.TypeAuthFail, wire.AuthFail{Code: code, Reason: reason})
}

// sendTyped encodes and enqueues one typed payload.
func (c *Connection) sendTyped(_ context.Context, t wire.MessageType, payload any) error {
	raw, err := wire.EncodeMessage(t, c.hub.msgIDFn(), c.hub.nowMs(), c.AccountID, payload)
	if err != nil {
		return err
	}
	return c.SendFrame(raw)
}

// heartbeatLoop pings every pingInterval and tracks pong latency. After
// pingMisses unanswered pings, the connection is closed (STALE).
func (c *Connection) heartbeatLoop(ctx context.Context) {
	ticker := c.hub.clock.NewTicker(c.hub.pingInterval)
	defer ticker.Stop()

	misses := 0
	for {
		select {
		case <-c.closeCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C():
		}

		// If a prior ping is still outstanding when the next tick fires,
		// count a miss.
		c.pongMu.Lock()
		outstanding := c.pendingPingID != ""
		c.pongMu.Unlock()
		if outstanding {
			misses++
			if misses >= c.hub.pingMisses {
				c.log.Warn("ws_agent_stale_closing",
					"account_id", c.AccountID, "misses", misses)
				if c.hub.onStale != nil {
					_ = c.hub.onStale(ctx, c.AccountID)
				}
				_ = c.Close()
				return
			}
		} else {
			misses = 0
		}

		// Send next ping.
		msgID := c.hub.msgIDFn()
		c.pongMu.Lock()
		c.pendingPingID = msgID
		c.pongMu.Unlock()

		raw, err := wire.EncodeMessage(wire.TypePing, msgID, c.hub.nowMs(),
			c.AccountID, wire.Ping{ServerNowMs: c.hub.nowMs()})
		if err != nil {
			c.log.Warn("ws_ping_encode_failed",
				"account_id", c.AccountID, "err", err)
			continue
		}
		if err := c.SendFrame(raw); err != nil {
			return
		}
	}
}

// handlePong clears the pending-ping marker when the echoed msg_id
// matches.
func (c *Connection) handlePong(env wire.Envelope) {
	pong, err := wire.DecodePayload[wire.Pong](env)
	if err != nil {
		return
	}
	c.pongMu.Lock()
	defer c.pongMu.Unlock()
	if pong.EchoMsgID == c.pendingPingID {
		c.pendingPingID = ""
	}
}

// closeAfter is a test/diagnostic helper — Run() uses it to bound the
// total connection lifetime in tests where we don't want a goroutine to
// hang forever.
//
//nolint:unused
func (c *Connection) closeAfter(d time.Duration) {
	go func() {
		time.Sleep(d)
		_ = c.Close()
	}()
}
