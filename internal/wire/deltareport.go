package wire

// AgentError is one entry in DeltaReport.SinceLastReport.Errors. These are
// exchange-layer errors the Agent collected since its last report (rate
// limits, partial outages); they are informational, not the application-
// level wire `error` message (§5.15).
type AgentError struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	OccurredAtMs int64  `json:"occurred_at_ms"`
}

// DeltaReportSince groups the events that happened since the previous
// DeltaReport. Fills here are the same shape as OrderUpdate.Fills; SaaS
// dedupes by (client_order_id, filled_at_exchange_ms).
type DeltaReportSince struct {
	Fills  []Fill        `json:"fills"`
	Errors []AgentError  `json:"errors"`
}

// DeltaReport is the low-frequency (~60s) account-level snapshot used as
// a fallback when OrderUpdate frames are lost (§5.11). Hot-path fills go
// through OrderUpdate; DeltaReport is the reconciliation channel.
type DeltaReport struct {
	ReportedAtMs    int64            `json:"reported_at_ms"`
	Positions       []Position       `json:"positions"`
	SinceLastReport DeltaReportSince `json:"since_last_report"`
}
