package wire

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Codec errors. The caller (Hub or Agent) maps these to the application-level
// `error` message (§5.15 of the protocol doc) with codes:
//
//	ErrSchemaMismatch  → code="schema_mismatch"
//	ErrUnknownType     → code="unknown_type"
//	ErrInvalidEnvelope → code="invalid_envelope"
//	ErrDecodeFailed    → code="decode_failed"
var (
	ErrSchemaMismatch  = errors.New("wire: schema_version mismatch")
	ErrUnknownType     = errors.New("wire: unknown message type")
	ErrInvalidEnvelope = errors.New("wire: invalid envelope")
	ErrDecodeFailed    = errors.New("wire: payload decode failed")
)

// EncodeEnvelope marshals an Envelope into a single JSON text frame. The
// caller is responsible for filling MsgID / TimestampMs / AccountID; this
// function only fills SchemaVersion (always to the wire package's pinned
// version) if it is empty.
//
// Callers typically use EncodeMessage instead, which constructs the envelope
// from a typed payload.
func EncodeEnvelope(e Envelope) ([]byte, error) {
	if e.SchemaVersion == "" {
		e.SchemaVersion = SchemaVersion
	}
	if !e.Type.IsKnown() {
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, string(e.Type))
	}
	if e.Payload == nil {
		e.Payload = json.RawMessage("{}")
	}
	return json.Marshal(e)
}

// EncodeMessage marshals a typed payload into a complete WS frame. msgID is
// the ULID assigned by the sender; nowMs is the sender's wall clock; accountID
// may be empty during handshake (hello / auth_*).
//
// Payload type must match t; mismatches surface as JSON marshal errors, not
// as a typed wire error (programmer mistake).
func EncodeMessage(t MessageType, msgID string, nowMs int64, accountID string, payload any) ([]byte, error) {
	if !t.IsKnown() {
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, string(t))
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("wire: marshal payload: %w", err)
	}
	return EncodeEnvelope(Envelope{
		MsgID:         msgID,
		Type:          t,
		SchemaVersion: SchemaVersion,
		TimestampMs:   nowMs,
		AccountID:     accountID,
		Payload:       raw,
	})
}

// DecodeEnvelope parses a JSON text frame into an Envelope and validates the
// envelope-level fields (schema_version, type, msg_id non-empty, payload
// non-nil). Payload contents are NOT decoded; callers route by env.Type and
// call DecodePayload.
func DecodeEnvelope(raw []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Envelope{}, fmt.Errorf("%w: %v", ErrInvalidEnvelope, err)
	}
	if env.MsgID == "" {
		return Envelope{}, fmt.Errorf("%w: msg_id empty", ErrInvalidEnvelope)
	}
	if env.SchemaVersion != SchemaVersion {
		return Envelope{}, fmt.Errorf("%w: got %q want %q", ErrSchemaMismatch, env.SchemaVersion, SchemaVersion)
	}
	if !env.Type.IsKnown() {
		return Envelope{}, fmt.Errorf("%w: %q", ErrUnknownType, string(env.Type))
	}
	if len(env.Payload) == 0 {
		return Envelope{}, fmt.Errorf("%w: payload nil/missing", ErrInvalidEnvelope)
	}
	return env, nil
}

// DecodePayload unmarshals env.Payload into *T. The caller is responsible
// for picking the correct T for env.Type — there is no runtime type check
// because the wire package's payload structs are not enumerated centrally
// (each lives next to its message type).
func DecodePayload[T any](env Envelope) (*T, error) {
	var out T
	if err := json.Unmarshal(env.Payload, &out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecodeFailed, err)
	}
	return &out, nil
}
