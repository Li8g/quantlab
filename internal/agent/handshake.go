package agent

import (
	"context"
	"errors"
	"fmt"

	"quantlab/internal/wire"
	"quantlab/internal/wsconn"
)

// doHandshake runs the Agent side of §4.2:
//
//	send hello → read auth_required → send auth → read auth_ok|fail →
//	read state_sync_request → send state_sync_response
//
// Returns nil on success; the runReadLoop then takes over.
func (c *Client) doHandshake(ctx context.Context, conn wsconn.Conn) error {
	// 1. send hello
	if err := c.sendTyped(ctx, conn, wire.TypeHello, wire.Hello{
		AgentVersion:  AgentVersion,
		AccountID:     c.cfg.AccountID,
		SchemaVersion: wire.SchemaVersion,
		Exchange:      c.cfg.Exchange.Name,
	}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	// 2. read auth_required
	env, err := readOneEnvelope(ctx, conn)
	if err != nil {
		return fmt.Errorf("read auth_required: %w", err)
	}
	if env.Type != wire.TypeAuthRequired {
		return fmt.Errorf("expected auth_required, got %q", env.Type)
	}

	// 3. send auth
	if err := c.sendTyped(ctx, conn, wire.TypeAuth, wire.Auth{
		Token: c.cfg.SaaSToken,
	}); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}

	// 4. read auth_ok|auth_fail
	env, err = readOneEnvelope(ctx, conn)
	if err != nil {
		return fmt.Errorf("read auth_ok: %w", err)
	}
	switch env.Type {
	case wire.TypeAuthFail:
		fail, _ := wire.DecodePayload[wire.AuthFail](env)
		err := fmt.Errorf("auth_fail: %s (%s)", fail.Code, fail.Reason)
		if isFatalAuthCode(fail.Code) {
			return fmt.Errorf("%w: %w", errFatalAuth, err)
		}
		return err
	case wire.TypeAuthOK:
		// ok
	default:
		return fmt.Errorf("expected auth_ok or auth_fail, got %q", env.Type)
	}

	// 5. read state_sync_request
	env, err = readOneEnvelope(ctx, conn)
	if err != nil {
		return fmt.Errorf("read state_sync_request: %w", err)
	}
	if env.Type != wire.TypeStateSyncRequest {
		return fmt.Errorf("expected state_sync_request, got %q", env.Type)
	}

	// 6. send state_sync_response
	if err := c.sendStateSyncResponse(ctx, conn); err != nil {
		return fmt.Errorf("send state_sync_response: %w", err)
	}
	return nil
}

// sendStateSyncResponse builds and sends a state_sync_response from the
// current exchange snapshot. Per §5.7, this includes positions; v1 does
// not retain open_orders or since_last_fills locally (the MockExchange
// settles every order immediately and idempotency-store fills are
// already mirrored to SaaS via OrderUpdate).
// positionsToWire converts the exchange's []agent.Position snapshot to
// the wire shape. Shared by state_sync_response (handshake) and the
// delta_report sender (§5.11), so both report positions identically.
func positionsToWire(positions []Position) []wire.Position {
	out := make([]wire.Position, 0, len(positions))
	for _, p := range positions {
		out = append(out, wire.Position{
			Symbol:        p.Symbol,
			FreeDecimal:   formatDecimal(p.Free),
			LockedDecimal: formatDecimal(p.Locked),
		})
	}
	return out
}

func (c *Client) sendStateSyncResponse(ctx context.Context, conn wsconn.Conn) error {
	positions, err := c.exchange.Positions(ctx)
	if err != nil {
		return fmt.Errorf("exchange.Positions: %w", err)
	}
	return c.sendTyped(ctx, conn, wire.TypeStateSyncResponse, wire.StateSyncResponse{
		ReportedAtMs:   c.nowMs(),
		Positions:      positionsToWire(positions),
		OpenOrders:     []wire.OpenOrder{}, // v1: mock has no open orders
		SinceLastFills: []wire.Fill{},      // v1: see comment above
	})
}

// readOneEnvelope reads exactly one envelope. Helper used by the
// handshake state machine.
func readOneEnvelope(ctx context.Context, conn wsconn.Conn) (wire.Envelope, error) {
	frame, err := conn.ReadFrame(ctx)
	if err != nil {
		return wire.Envelope{}, err
	}
	env, err := wire.DecodeEnvelope(frame)
	if err != nil {
		return wire.Envelope{}, err
	}
	return env, nil
}

// AgentVersion is reported in Hello.agent_version. Bump in sync with
// any wire-format-affecting change.
const AgentVersion = "0.1.0"

// errFatalAuth marks an auth failure the reconnect loop can never recover
// from by retrying with the same config. Run checks for it via errors.Is
// and exits instead of backing off. Every auth_fail code is fatal: each
// requires an operator fix — a new/un-revoked token, a matching account_id,
// or a redeployed binary — so silently backing off forever would just be
// noise.
var errFatalAuth = errors.New("fatal auth failure")

// isFatalAuthCode classifies an auth_fail code as unrecoverable. Driven by
// the typed wire.AuthFailCode rather than substring-matching the rendered
// error string, so a free-form AuthFail.Reason can never flip the
// classification. A future code defaults to recoverable (the switch's
// default) until explicitly classified here.
func isFatalAuthCode(code wire.AuthFailCode) bool {
	switch code {
	case wire.AuthFailInvalidToken, wire.AuthFailRevoked,
		wire.AuthFailSchemaMismatch, wire.AuthFailAccountMismatch:
		return true
	default:
		return false
	}
}
