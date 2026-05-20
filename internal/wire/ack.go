package wire

// AckStatus enumerates the Agent's response to a TradeCommand (§5.9).
//
// duplicate_pending / duplicate_terminal are the Agent's idempotency replies
// for repeated client_order_id; the Agent does NOT re-submit to the exchange.
type AckStatus string

const (
	AckStatusAccepted          AckStatus = "accepted"
	AckStatusRejected          AckStatus = "rejected"
	AckStatusExpired           AckStatus = "expired"
	AckStatusDuplicatePending  AckStatus = "duplicate_pending"
	AckStatusDuplicateTerminal AckStatus = "duplicate_terminal"
)

// Ack is the Agent's per-command response. ExchangeOrderID is only filled
// for accepted / duplicate_pending; RejectReason only for rejected /
// duplicate_terminal. ExchangeNowMs is the Agent's clock at the time the
// exchange responded — used for end-to-end latency diagnostics.
type Ack struct {
	ClientOrderID   string    `json:"client_order_id"`
	Status          AckStatus `json:"status"`
	ExchangeOrderID string    `json:"exchange_order_id,omitempty"`
	ExchangeNowMs   int64     `json:"exchange_now_ms"`
	RejectReason    string    `json:"reject_reason,omitempty"`
}
