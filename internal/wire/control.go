package wire

// KillSwitchReason enumerates why SaaS issued a halt order. In v1, Scope is
// always "all" — symbol-level kill is deferred (§5.13).
type KillSwitchReason string

const (
	KillSwitchManualAdminAction   KillSwitchReason = "manual_admin_action"
	KillSwitchDiscrepancyDetected KillSwitchReason = "discrepancy_detected"
	KillSwitchComplianceFreeze    KillSwitchReason = "compliance_freeze"
)

// KillSwitchScope is "all" or "symbol"; v1 only emits "all". The struct
// keeps the field so future symbol-level support is non-breaking.
type KillSwitchScope string

const (
	KillSwitchScopeAll    KillSwitchScope = "all"
	KillSwitchScopeSymbol KillSwitchScope = "symbol"
)

// KillSwitchSymbolResume is the resume sentinel (§5.13 v2): a KillSwitch
// carrying Symbol=="resume" is an UN-freeze — the Agent lifts its frozen
// latch instead of setting it. Resume rides the existing kill_switch
// message rather than a new type, per the frozen protocol.
const KillSwitchSymbolResume = "resume"

// KillSwitch tells the Agent to cancel all open orders and refuse new
// TradeCommands until it is un-frozen (§5.13). Agent acks then enters a
// frozen state. The inverse — Symbol==KillSwitchSymbolResume — lifts the
// latch (v2 resume); v1's only un-freeze was a process restart.
type KillSwitch struct {
	Reason         KillSwitchReason `json:"reason"`
	OperatorUserID string           `json:"operator_user_id"`
	Scope          KillSwitchScope  `json:"scope"`
	Symbol         string           `json:"symbol"`
}

// GracefulShutdownReason distinguishes a planned restart from a maintenance
// window — the Agent uses it for logging only; behavior is identical.
type GracefulShutdownReason string

const (
	GracefulShutdownSaaSRestart     GracefulShutdownReason = "saas_restart"
	GracefulShutdownSaaSMaintenance GracefulShutdownReason = "saas_maintenance"
)

// GracefulShutdown is broadcast to every connected Agent before SaaS exits.
// Agent: finish in-flight acks (≤2s), close, then wait RetryInMs before
// reconnecting (skipping the exponential-backoff first step).
type GracefulShutdown struct {
	Reason    GracefulShutdownReason `json:"reason"`
	RetryInMs int64                  `json:"retry_in_ms"`
}
