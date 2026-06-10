package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"quantlab/internal/saas/store"
	"quantlab/internal/wire"
)

// auditSink is the narrow AuditLog writer (satisfied by
// *repository.AuditRepo). Kept as an interface so the kill paths stay
// unit-testable with a fake.
type auditSink interface {
	Insert(ctx context.Context, e *store.AuditLog) error
}

// recordKillAudit appends one instance.kill AuditLog event — the
// action trail behind every kill_switch (Option 3 step 5), manual or auto.
//
// actor is "user:<id>" (manual) or "system" (auto). Subject is the
// account (kill is account-scoped); extra folds trigger/drift detail into
// DataJSON alongside the reason/scope/operator.
//
// B1: this row is also the durable freeze latch (AuditRepo.IsAccountFrozen
// reads it back to re-arm a reconnecting agent), so callers that need the
// latch to persist MUST treat the returned error as load-bearing — a failed
// insert means the kill did NOT durably latch. The error is returned, not
// swallowed; the caller decides (the HTTP path fails the request, the
// auto-freeze path stays armed to retry).
func recordKillAudit(
	ctx context.Context, sink auditSink, logger *slog.Logger,
	actor, accountID string, ks wire.KillSwitch, extra map[string]any,
) error {
	return recordKillSwitchAudit(ctx, sink, logger, store.AuditActionInstanceKill, actor, accountID, ks, extra)
}

// recordResumeAudit appends one instance.resume AuditLog event — the
// inverse trail of a kill (§5.13 v2). Same shape as recordKillAudit; the
// distinct action lets the /live banner reader (and IsAccountFrozen) tell
// "frozen now" from "was frozen, since resumed". The returned error is
// load-bearing for the same B1 reason: a failed resume insert means the
// latch did NOT durably clear.
func recordResumeAudit(
	ctx context.Context, sink auditSink, logger *slog.Logger,
	actor, accountID string, ks wire.KillSwitch, extra map[string]any,
) error {
	return recordKillSwitchAudit(ctx, sink, logger, store.AuditActionInstanceResume, actor, accountID, ks, extra)
}

// recordKillSwitchAudit is the shared writer for kill/resume audit rows.
// Returns the sink error (also logged) so callers can treat the latch write
// as load-bearing (B1). A nil sink is a no-op returning nil — auditing is
// optional in tests, but production always wires one.
func recordKillSwitchAudit(
	ctx context.Context, sink auditSink, logger *slog.Logger,
	action store.AuditAction, actor, accountID string, ks wire.KillSwitch, extra map[string]any,
) error {
	if sink == nil {
		return nil
	}
	data := map[string]any{
		"reason":           string(ks.Reason),
		"scope":            string(ks.Scope),
		"operator_user_id": ks.OperatorUserID,
	}
	for k, v := range extra {
		data[k] = v
	}
	blob, _ := json.Marshal(data)
	e := &store.AuditLog{
		Actor:    actor,
		Action:   action,
		Subject:  fmt.Sprintf("account:%s", accountID),
		DataJSON: blob,
	}
	if err := sink.Insert(ctx, e); err != nil {
		if logger != nil {
			logger.Error("kill_audit_insert_failed",
				"account_id", accountID, "actor", actor, "action", string(action), "err", err)
		}
		return err
	}
	return nil
}
