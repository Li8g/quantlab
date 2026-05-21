// agentstatus_hook.go — translates wshub ConnectionStateEvents into
// agentstatus.Reporter writes. The hook is best-effort (errors are
// logged inside wshub when returned), so we map all error paths to
// nil here too.
package main

import (
	"context"

	"quantlab/internal/saas/agentstatus"
	"quantlab/internal/saas/wshub"
)

// makeConnectionStateHook returns a closure suitable for
// wshub.Config.OnConnectionState. Translates the event's state string
// into the protocol enum and writes (or deletes, on 'disconnected')
// the corresponding Redis row.
func makeConnectionStateHook(r agentstatus.Reporter) func(context.Context, wshub.ConnectionStateEvent) error {
	return func(ctx context.Context, ev wshub.ConnectionStateEvent) error {
		if ev.State == "disconnected" {
			// Explicit delete is informational only; the 60s TTL would
			// expire the key anyway. Failure is non-fatal — the next
			// pong from a fresh connection re-writes the slot.
			return r.Delete(ctx, ev.AccountID)
		}
		return r.Set(ctx, ev.AccountID, agentstatus.Status{
			AgentID:         ev.AgentID,
			ConnectionState: agentstatus.ConnectionState(ev.State),
			LastSeenMs:      ev.NowMs,
			LastMsgID:       ev.LastMsgID,
		})
	}
}
