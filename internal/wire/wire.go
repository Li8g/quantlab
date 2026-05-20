// Package wire defines the JSON wire types exchanged over the SaaS ↔ Agent
// WebSocket connection. It is the single shared dependency between cmd/saas
// and cmd/agent; both sides import this package to encode and decode frames.
//
// Source of truth: docs/saas-ws-protocol-v1.md (frozen 2026-05-20).
//
// Design rules:
//   - Pure data types and codec helpers. No IO, no time.Now(), no ULID
//     generation. Callers pass timestamps and msg_ids in.
//   - Every WS frame is a JSON object matching Envelope. The Type field
//     dispatches into one of the 16 payload structs in this package.
//   - Cross-boundary numeric precision: amounts/quantities/prices are
//     transported as decimal strings (fields named *_decimal). Basis points
//     and *_ms timestamps stay numeric. See §2.2 / §2.3 of the protocol doc.
//   - SchemaVersion in every envelope must equal resultpkg.SchemaVersionV533;
//     mismatches are rejected at the codec layer.
//
// Layout:
//   - wire.go        — Envelope, MessageType constants, schema_version pin
//   - codec.go       — Encode/Decode helpers + payload-typed decoders
//   - handshake.go   — Hello, AuthRequired, Auth, AuthOK, AuthFail
//   - statesync.go   — StateSyncRequest, StateSyncResponse + Position/OpenOrder
//   - tradecommand.go — TradeCommand
//   - ack.go         — Ack (+ AckStatus enum)
//   - orderupdate.go — OrderUpdate, Fill (shared by DeltaReport)
//   - deltareport.go — DeltaReport, AgentError
//   - heartbeat.go   — Ping, Pong
//   - control.go     — KillSwitch, GracefulShutdown
//   - errormsg.go    — Error
package wire

import (
	"encoding/json"

	"quantlab/internal/resultpkg"
)

// SchemaVersion is the protocol version pin. Every Envelope.SchemaVersion
// must equal this constant; the codec rejects mismatches. Bumping requires
// a coordinated SaaS + Agent release (no minor-version compatibility in v1).
const SchemaVersion = resultpkg.SchemaVersionV533

// MessageType is the type-string carried in Envelope.Type. The constants
// below are the only legal values; codec.DecodeEnvelope returns
// ErrUnknownType for anything else.
type MessageType string

const (
	TypeHello             MessageType = "hello"
	TypeAuthRequired      MessageType = "auth_required"
	TypeAuth              MessageType = "auth"
	TypeAuthOK            MessageType = "auth_ok"
	TypeAuthFail          MessageType = "auth_fail"
	TypeStateSyncRequest  MessageType = "state_sync_request"
	TypeStateSyncResponse MessageType = "state_sync_response"
	TypeTradeCommand      MessageType = "trade_command"
	TypeAck               MessageType = "ack"
	TypeOrderUpdate       MessageType = "order_update"
	TypeDeltaReport       MessageType = "delta_report"
	TypePing              MessageType = "ping"
	TypePong              MessageType = "pong"
	TypeKillSwitch        MessageType = "kill_switch"
	TypeGracefulShutdown  MessageType = "graceful_shutdown"
	TypeError             MessageType = "error"
)

// IsKnown reports whether t is one of the 16 frozen message types.
func (t MessageType) IsKnown() bool {
	switch t {
	case TypeHello, TypeAuthRequired, TypeAuth, TypeAuthOK, TypeAuthFail,
		TypeStateSyncRequest, TypeStateSyncResponse,
		TypeTradeCommand, TypeAck, TypeOrderUpdate, TypeDeltaReport,
		TypePing, TypePong,
		TypeKillSwitch, TypeGracefulShutdown,
		TypeError:
		return true
	}
	return false
}

// Envelope is the outer JSON object wrapping every WS frame. Payload stays
// as json.RawMessage so the codec can route by Type before paying the cost
// of decoding the inner struct.
//
// Field constraints (§2.4 of the protocol doc):
//   - MsgID:         ULID (26 chars). Sender-assigned; receiver must echo
//                    via ref_msg_id / echo_msg_id when responding.
//   - Type:          one of the MessageType constants.
//   - SchemaVersion: must equal SchemaVersion ("v5.3.3"); codec rejects.
//   - TimestampMs:   sender wall clock at send.
//   - AccountID:     ULID; required after auth_ok; may be empty during
//                    handshake (hello / auth_required / auth).
//   - Payload:       JSON object (never null; empty payloads use {}).
type Envelope struct {
	MsgID         string          `json:"msg_id"`
	Type          MessageType     `json:"type"`
	SchemaVersion string          `json:"schema_version"`
	TimestampMs   int64           `json:"timestamp_ms"`
	AccountID     string          `json:"account_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}
