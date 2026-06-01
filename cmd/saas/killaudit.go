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
// DataJSON alongside the reason/scope/operator. Best-effort: a sink error
// is logged but never blocks the kill — the freeze has already been sent.
func recordKillAudit(
	ctx context.Context, sink auditSink, logger *slog.Logger,
	actor, accountID string, ks wire.KillSwitch, extra map[string]any,
) {
	if sink == nil {
		return
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
		Action:   store.AuditActionInstanceKill,
		Subject:  fmt.Sprintf("account:%s", accountID),
		DataJSON: blob,
	}
	if err := sink.Insert(ctx, e); err != nil && logger != nil {
		logger.Error("kill_audit_insert_failed",
			"account_id", accountID, "actor", actor, "err", err)
	}
}
