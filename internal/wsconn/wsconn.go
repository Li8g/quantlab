// Package wsconn is the WebSocket-frame I/O layer shared between
// cmd/saas (wshub) and cmd/agent. Both directions speak the same
// JSON-text frame protocol per docs/saas-ws-protocol-v1.md §4.1.
//
// The Conn interface is the testable abstraction; gorilla-backed impls
// are constructed via NewGorillaConn and NewGorillaConnFromDial. Test
// code (wshub.pipeConn, internal/agent's pipe fake) implements Conn
// directly with a channel pair.
package wsconn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Conn abstracts a single WebSocket connection so the SaaS Hub and the
// Agent client can share the same state-machine code. ReadFrame /
// WriteFrame transport one JSON text frame each.
//
// Concurrent ReadFrame and WriteFrame on the same Conn are safe;
// concurrent ReadFrame from two goroutines is NOT.
type Conn interface {
	ReadFrame(ctx context.Context) ([]byte, error)
	WriteFrame(ctx context.Context, frame []byte) error
	Close() error
}

// ErrConnClosed is returned by Read/WriteFrame after Close. Wraps to
// io.EOF-equivalent semantics so callers don't depend on net.ErrClosed
// or gorilla's CloseError sentinels directly.
var ErrConnClosed = errors.New("wsconn: conn closed")

// gorillaConn adapts *websocket.Conn to the Conn interface. Concurrent
// WriteFrames serialize through writeMu (gorilla forbids concurrent
// writes); reads are single-goroutine by Connection contract.
type gorillaConn struct {
	c       *websocket.Conn
	writeMu sync.Mutex

	// writeFallbackTimeout caps a write when no ctx deadline is set.
	// Slow writes mean kernel buffer pressure or a misbehaving peer —
	// either way, drop the connection.
	writeFallbackTimeout time.Duration
}

// NewGorillaConn wraps an already-upgraded *websocket.Conn (typical
// server side after Upgrader.Upgrade).
func NewGorillaConn(c *websocket.Conn) Conn {
	return &gorillaConn{c: c, writeFallbackTimeout: 10 * time.Second}
}

// ReadFrame returns one text frame. Binary frames are rejected
// (protocol §4.1: text only). On context cancellation, the underlying
// socket is closed to interrupt the blocking ReadMessage.
func (g *gorillaConn) ReadFrame(ctx context.Context) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		mt, data, err := g.c.ReadMessage()
		if err != nil {
			ch <- result{nil, mapGorillaErr(err)}
			return
		}
		if mt != websocket.TextMessage {
			ch <- result{nil, fmt.Errorf("wsconn: non-text frame (mt=%d)", mt)}
			return
		}
		ch <- result{data, nil}
	}()
	select {
	case r := <-ch:
		return r.data, r.err
	case <-ctx.Done():
		_ = g.c.Close()
		return nil, ctx.Err()
	}
}

// WriteFrame writes one text frame, honoring ctx via SetWriteDeadline
// or falling back to writeFallbackTimeout.
func (g *gorillaConn) WriteFrame(ctx context.Context, frame []byte) error {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		_ = g.c.SetWriteDeadline(dl)
	} else {
		_ = g.c.SetWriteDeadline(time.Now().Add(g.writeFallbackTimeout))
	}
	err := g.c.WriteMessage(websocket.TextMessage, frame)
	_ = g.c.SetWriteDeadline(time.Time{})
	if err != nil {
		return mapGorillaErr(err)
	}
	return nil
}

// Close shuts the socket. Idempotent — gorilla's Close returns an error
// for an already-closed conn, which we collapse to nil.
func (g *gorillaConn) Close() error {
	_ = g.c.Close()
	return nil
}

func mapGorillaErr(err error) error {
	if err == nil {
		return nil
	}
	if websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseNoStatusReceived,
		websocket.CloseAbnormalClosure) {
		return ErrConnClosed
	}
	return err
}

// ServerUpgrader is the gorilla upgrader the SaaS Hub uses to upgrade
// incoming HTTP requests. Exposed so callers don't have to know about
// gorilla constants. 64KB buffers fit the largest expected payload
// (state_sync_response with many open orders).
var ServerUpgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 64 * 1024,
	// v1: no origin check — Agent does not run in browser. Production
	// deployments rely on TLS + bearer token; CSRF doesn't apply.
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// Dial connects to a SaaS-side WebSocket endpoint and returns a Conn.
// Headers are forwarded verbatim; callers typically include nothing
// (auth happens via the bearer-token flow inside the WS session, not
// via HTTP headers).
//
// ctx applies to the entire handshake; once the WS handshake completes,
// the returned Conn's I/O is bound by its own per-call contexts.
func Dial(ctx context.Context, url string, header http.Header) (Conn, error) {
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		return nil, fmt.Errorf("wsconn.Dial: %w", err)
	}
	return NewGorillaConn(conn), nil
}
