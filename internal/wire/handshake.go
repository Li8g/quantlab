package wire

// Hello is the first frame on a new connection, sent by the Agent to the
// SaaS Hub before authenticating (§5.1).
//
// AccountID is also carried in the envelope; the duplication is intentional
// — envelope is for routing, payload is for audit and account_mismatch
// detection (token bound to account X but hello claims account Y).
type Hello struct {
	AgentVersion  string `json:"agent_version"`
	AccountID     string `json:"account_id"`
	SchemaVersion string `json:"schema_version"`
	Platform      string `json:"platform,omitempty"`
	Exchange      string `json:"exchange,omitempty"`
	// Environment is the trading environment the Agent's exchange is
	// pointed at: one of the Environment* constants. The SaaS Hub asserts
	// it against its expected environment at handshake (backlog ⑥
	// consistency assertion) to catch a misconfigured agent before it
	// trades. Additive/optional: empty → the assertion is skipped, so
	// pre-⑥ agents stay compatible.
	Environment string `json:"environment,omitempty"`
}

// Environment values for Hello.Environment (backlog ⑥).
const (
	EnvironmentMainnet = "mainnet"
	EnvironmentTestnet = "testnet"
	EnvironmentMock    = "mock"
)

// AuthRequired is the SaaS response to Hello, signaling the Agent to send
// an Auth frame within 10s (§4.2 timeout). Payload is empty by design.
type AuthRequired struct{}

// Auth carries the long-lived bearer token from config.agent.yaml. Token
// format: `agt_<ULID>_<base64-secret>` — agent_id is embedded in the prefix
// so the SaaS side can look up the bcrypt hash without a full-table scan
// (§4.3).
type Auth struct {
	Token string `json:"token"`
}

// AuthOK is the SaaS response when Auth succeeds. ServerNowMs lets the
// Agent log its local clock drift (informational only — never used in
// decision logic).
type AuthOK struct {
	ServerNowMs int64  `json:"server_now_ms"`
	AgentID     string `json:"agent_id"`
}

// AuthFailCode enumerates the reasons SaaS rejects an Auth frame. The Agent
// treats every code as fatal (no reconnect backoff — each needs an operator
// fix: new/un-revoked token, matching account_id, or redeployed binary, so
// retrying with the same config is pointless and noisy). See the Agent's
// isFatalAuthCode.
type AuthFailCode string

const (
	AuthFailInvalidToken        AuthFailCode = "invalid_token"
	AuthFailRevoked             AuthFailCode = "revoked"
	AuthFailSchemaMismatch      AuthFailCode = "schema_mismatch"
	AuthFailAccountMismatch     AuthFailCode = "account_mismatch"
	AuthFailEnvironmentMismatch AuthFailCode = "environment_mismatch"
)

// AuthFail is sent immediately before SaaS closes the connection (§5.5).
type AuthFail struct {
	Code   AuthFailCode `json:"code"`
	Reason string       `json:"reason"`
}
