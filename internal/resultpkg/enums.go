package resultpkg

// TaskStatus is the lifecycle state of an evolution task.
type TaskStatus string

// DecisionStatus is the human-review state of a Challenger.
//
// v3 P02: approved → promoted (state-machine terminal alignment).
// Champion retirement (retired) is owned by champion_history, NOT here.
type DecisionStatus string

// VerificationStatus is the result of an offline verification run
// (OOS, ReviewBacktest, DSR, Stress).
type VerificationStatus string

// DecisionColor is the verification-result severity hint.
type DecisionColor string

// WindowName is the frozen four-window crucible enum.
type WindowName string

// SkippedBy indicates which earlier window's Fatal caused a cascade skip.
type SkippedBy string

// SpawnMode is the SpawnPoint-injection scheme for an Epoch.
// Frozen as a three-state enum (v5.3.2+); no longer a free string.
type SpawnMode string

const (
	TaskStatusQueued    TaskStatus = "queued"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusSucceeded TaskStatus = "succeeded"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCancelled TaskStatus = "cancelled"
)

const (
	// DecisionStatusPending: awaiting human review.
	DecisionStatusPending DecisionStatus = "pending"

	// DecisionStatusPromoted: approved by human; promoted to Champion.
	// Triggers a champion_history.promoted_at write.
	// (v3 P02: renamed from "approved".)
	DecisionStatusPromoted DecisionStatus = "promoted"

	// DecisionStatusRejected: human declined to promote.
	DecisionStatusRejected DecisionStatus = "rejected"
)

const (
	VerificationStatusNotRun           VerificationStatus = "not_run"
	VerificationStatusOK               VerificationStatus = "ok"
	VerificationStatusFailed           VerificationStatus = "failed"
	VerificationStatusInsufficientData VerificationStatus = "insufficient_data"
	// VerificationStatusMismatch: a reproducibility replay (RunReview)
	// found the re-evaluated ScoreTotal / fingerprint, or a rebuilt
	// plan/bars hash, disagreeing with the persisted package. Signals an
	// audit-integrity failure, not a strategy-performance verdict.
	VerificationStatusMismatch VerificationStatus = "mismatch"
)

const (
	DecisionColorGreen  DecisionColor = "green"
	DecisionColorYellow DecisionColor = "yellow"
	DecisionColorRed    DecisionColor = "red"
	DecisionColorGray   DecisionColor = "gray"
)

const (
	Window6M  WindowName = "6m"
	Window2Y  WindowName = "2y"
	Window5Y  WindowName = "5y"
	Window10Y WindowName = "10y"

	// WindowOOS labels the Anchored Holdout window. NOT part of cascade
	// evaluation — held separately on EvaluablePlan.OosWindow and never
	// returned by AllWindowsInEvalOrder().
	WindowOOS WindowName = "oos"
)

// AllWindowsInEvalOrder returns the four IS crucible windows in the FIXED
// cascade evaluation order (6m → 2y → 5y → 10y). Any code that iterates
// IS windows must use this helper; declaring an alternate order silently
// breaks the SkippedBy cascade enum semantics. WindowOOS is intentionally
// excluded.
func AllWindowsInEvalOrder() []WindowName {
	return []WindowName{Window6M, Window2Y, Window5Y, Window10Y}
}

const (
	SkippedByCascadeFrom6M SkippedBy = "cascaded_from_6m"
	SkippedByCascadeFrom2Y SkippedBy = "cascaded_from_2y"
	SkippedByCascadeFrom5Y SkippedBy = "cascaded_from_5y"
)

const (
	SpawnModeInherit    SpawnMode = "inherit"     // continue current champion's SpawnPoint
	SpawnModeRandomOnce SpawnMode = "random_once" // randomized once at task-queue time
	SpawnModeManual     SpawnMode = "manual"      // taken from request body's spawn_point
)

// GeneEncodingJSON is the only legal Encoding value for ChampionGenePayload
// during the prototype phase. Other encodings (array/base64) require a
// FingerprintVersion bump.
const GeneEncodingJSON = "json"

// IsValid reports whether s is one of the three legal SpawnMode values.
func (s SpawnMode) IsValid() bool {
	switch s {
	case SpawnModeInherit, SpawnModeRandomOnce, SpawnModeManual:
		return true
	}
	return false
}

// IsValid reports whether s is one of the three legal DecisionStatus values.
// "retired" is intentionally absent — see champion_history.
func (s DecisionStatus) IsValid() bool {
	switch s {
	case DecisionStatusPending, DecisionStatusPromoted, DecisionStatusRejected:
		return true
	}
	return false
}

// IsValid reports whether w is one of the four IS crucible windows or
// the OOS Anchored Holdout window.
func (w WindowName) IsValid() bool {
	switch w {
	case Window6M, Window2Y, Window5Y, Window10Y, WindowOOS:
		return true
	}
	return false
}

// IsValid reports whether s is one of the four verification statuses.
func (s VerificationStatus) IsValid() bool {
	switch s {
	case VerificationStatusNotRun, VerificationStatusOK,
		VerificationStatusFailed, VerificationStatusInsufficientData,
		VerificationStatusMismatch:
		return true
	}
	return false
}

// IsValid reports whether s is one of the three cascade-skip causes.
func (s SkippedBy) IsValid() bool {
	switch s {
	case SkippedByCascadeFrom6M, SkippedByCascadeFrom2Y, SkippedByCascadeFrom5Y:
		return true
	}
	return false
}
