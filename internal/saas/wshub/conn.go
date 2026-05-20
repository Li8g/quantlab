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
// Conn-level types (the WebSocket frame abstraction) live in
// internal/wsconn so cmd/agent can share them.
package wshub

import (
	"quantlab/internal/wsconn"
)

// Conn is re-exported from internal/wsconn for the wshub call sites
// that historically used wshub.Conn. New code can import wsconn directly.
type Conn = wsconn.Conn

// ErrConnClosed is re-exported from internal/wsconn.
var ErrConnClosed = wsconn.ErrConnClosed
