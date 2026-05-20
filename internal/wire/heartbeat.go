package wire

// Ping is the SaaS-side heartbeat (§4.4: 30s cadence). ServerNowMs lets the
// Agent log clock drift; it is informational only.
type Ping struct {
	ServerNowMs int64 `json:"server_now_ms"`
}

// Pong is the Agent's heartbeat response. EchoMsgID MUST equal the
// triggering Ping's MsgID — this protects against cross-cycle confusion
// where a delayed Pong from cycle N arrives during cycle N+1.
//
// ExchangeReachable is a soft signal: false logs an audit event but does
// NOT pause TradeCommand dispatch (§5.12). Operator intervention is needed
// to flip a Kill Switch.
type Pong struct {
	EchoMsgID         string `json:"echo_msg_id"`
	AgentNowMs        int64  `json:"agent_now_ms"`
	ExchangeReachable bool   `json:"exchange_reachable"`
}
