package wire

// ErrorCode is the application-level error taxonomy carried by the `error`
// message (§5.15). These map 1:1 to the codec errors in codec.go for
// transport-level issues, plus internal_error for handler-side faults.
type ErrorCode string

const (
	ErrorCodeSchemaMismatch  ErrorCode = "schema_mismatch"
	ErrorCodeUnknownType     ErrorCode = "unknown_type"
	ErrorCodeDecodeFailed    ErrorCode = "decode_failed"
	ErrorCodeInternalError   ErrorCode = "internal_error"
	ErrorCodeInvalidEnvelope ErrorCode = "invalid_envelope"
)

// Error is the bidirectional application-error frame. The receiver logs
// and (optionally) records an audit event — it does NOT auto-retry the
// message that triggered the error.
//
// RefMsgID points at the frame that caused this error; empty when the
// trigger has no msg_id (e.g. malformed envelope that failed to parse).
type Error struct {
	Code     ErrorCode `json:"code"`
	Message  string    `json:"message"`
	RefMsgID string    `json:"ref_msg_id,omitempty"`
}
