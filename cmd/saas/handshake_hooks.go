package main

import (
	"context"
	"log/slog"
	"time"

	"quantlab/internal/repository"
	"quantlab/internal/saas/store"
)

// makeHandshakeRejectHook returns a wshub.Config.OnHandshakeReject handler
// that records the rejection as an AgentError so it surfaces in /live's
// recent_errors panel (backlog ⑥ observability).
//
// The hook looks up the account's live instance to attach an instanceID;
// if none is found the error is written with an empty instanceID and is
// visible only at the account level. Both repos are called with a fresh
// background context — the connection context may already be cancelled.
func makeHandshakeRejectHook(
	instances *repository.InstanceRepo,
	recon *repository.ReconRepo,
) func(ctx context.Context, accountID, code, msg string) error {
	return func(_ context.Context, accountID, code, msg string) error {
		ctx := context.Background()
		instanceID := ""
		rows, err := instances.ListByAccount(ctx, accountID)
		if err != nil {
			slog.Warn("handshake_reject_hook: ListByAccount failed",
				"account_id", accountID, "err", err)
		} else if len(rows) > 0 {
			instanceID = rows[0].InstanceID
		}
		nowMs := time.Now().UnixMilli()
		return recon.InsertAgentError(ctx, &store.AgentError{
			AccountID:    accountID,
			InstanceID:   instanceID,
			Code:         code,
			Message:      msg,
			OccurredAtMs: nowMs,
			ReportedAtMs: nowMs,
		})
	}
}
