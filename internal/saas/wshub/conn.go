// Package wshub is the SaaS-side WebSocket server for Agent connections,
// per docs/saas-ws-protocol-v1.md §4 + §7.3.
//
// One Hub per SaaS process. The Hub:
//   - upgrades incoming HTTP requests at /api/v1/ws/agent to WebSocket
//   - drives the handshake state machine (Hello → Auth → AuthOK → StateSync)
//   - maintains a per-Connection heartbeat (30s ping / 5s pong / 3 misses)
//   - exposes Dispatch(accountID, orders) for the instance.Manager to call
//     when emitting trade commands (implements TradeCommandDispatcher)
//
// Production wiring uses gorilla/websocket via gorillaConn. Tests use
// pipeConn (a channel-pair in-memory Conn).
package wshub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Conn abstracts a single WebSocket connection so the Hub state machine
// can be unit-tested without a real socket. Both gorillaConn and the
// test-only pipeConn satisfy it.
//
// ReadFrame / WriteFrame transport one JSON text frame each. Context is
// honored only for cancellation; gorilla's deadline support is mapped
// onto context.Deadline by the impl. Concurrent ReadFrame and WriteFrame
// on the same Conn are safe; concurrent ReadFrame from two goroutines
// is NOT (caller serializes — Connection has one read goroutine).
type Conn interface {
	ReadFrame(ctx context.Context) ([]byte, error)
	WriteFrame(ctx context.Context, frame []byte) error
	Close() error
}

// ErrConnClosed is returned by Read/WriteFrame after Close. Wraps to
// io.EOF-equivalent semantics so callers don't depend on net.ErrClosed
// or gorilla's CloseError directly.
var ErrConnClosed = errors.New("wshub: conn closed")

// gorillaConn adapts *websocket.Conn to the Conn interface.
//
// Write serializes through writeMu because gorilla forbids concurrent
// writes; reads run from the single Connection.readLoop goroutine so no
// readMu is needed.
type gorillaConn struct {
	c       *websocket.Conn
	writeMu sync.Mutex
}

// newGorillaConn wraps an upgraded *websocket.Conn.
func newGorillaConn(c *websocket.Conn) *gorillaConn {
	return &gorillaConn{c: c}
}

// ReadFrame returns one text frame. Binary frames are rejected
// (protocol §4.1: text only). On cancellation, the underlying socket is
// closed to interrupt the blocked Read — gorilla offers no native ctx
// integration.
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
			ch <- result{nil, fmt.Errorf("wshub: non-text frame (mt=%d)", mt)}
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

// WriteFrame writes one text frame, honoring ctx via SetWriteDeadline.
func (g *gorillaConn) WriteFrame(ctx context.Context, frame []byte) error {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		_ = g.c.SetWriteDeadline(dl)
	} else {
		// 10s default ceiling; the Hub never expects a write to take
		// longer than that. Slow writers indicate kernel buffer pressure
		// or a misbehaving Agent — drop the connection in either case.
		_ = g.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
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

// defaultUpgrader is the gorilla upgrader used by Hub.ServeWS. Read/write
// buffer sizes are bumped to 64KB to fit the largest expected frame
// (state_sync_response with many open orders).
var defaultUpgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 64 * 1024,
	// v1: no origin check — Agent does not run in browser. Production
	// deployments rely on TLS + bearer token; CSRF doesn't apply.
	CheckOrigin: func(_ *http.Request) bool { return true },
}
