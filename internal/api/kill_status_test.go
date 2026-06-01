package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quantlab/internal/saas/auth"
	"quantlab/internal/saas/store"
)

type fakeKillReader struct {
	row *store.AuditLog
	err error
}

func (f *fakeKillReader) LatestKill(_ context.Context, _ string) (*store.AuditLog, error) {
	return f.row, f.err
}

// TestGetInstanceLive_KillStatus covers the frozen banner surface (Option
// 3 step 4): a killed account's /live snapshot carries kill_status (reason
// + actor + trigger from the audit event); a never-killed one omits it.
func TestGetInstanceLive_KillStatus(t *testing.T) {
	const owner uint = 7
	seed := func() *fakeInstances {
		insts := newFakeInstances()
		insts.byID["i-1"] = &store.StrategyInstance{
			InstanceID: "i-1", OwnerUserID: owner, AccountID: "acct-1",
			Status: store.InstanceStatusLive,
		}
		return insts
	}
	get := func(t *testing.T, h *Handlers) InstanceLiveResponse {
		t.Helper()
		r := withClaimsHandlers(h, &auth.Claims{UserID: owner, Role: string(store.UserRoleAdmin)})
		rec := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/api/v1/instances/i-1/live", nil)
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("Code = %d; body=%s", rec.Code, rec.Body.String())
		}
		return liveJSON(t, rec.Body.Bytes())
	}

	t.Run("killed account surfaces kill_status", func(t *testing.T) {
		h := &Handlers{
			Instances: seed(),
			Kills: &fakeKillReader{row: &store.AuditLog{
				CreatedAt: time.UnixMilli(1700000000000),
				Actor:     "system",
				Action:    store.AuditActionInstanceKill,
				Subject:   "account:acct-1",
				DataJSON:  []byte(`{"reason":"discrepancy_detected","trigger":"auto","operator_user_id":"system"}`),
			}},
		}
		ks := get(t, h).KillStatus
		if ks == nil {
			t.Fatal("kill_status nil, want populated")
		}
		if ks.Reason != "discrepancy_detected" || ks.Trigger != "auto" || ks.Actor != "system" {
			t.Errorf("kill_status = %+v", *ks)
		}
		if ks.KilledAtMs != 1700000000000 {
			t.Errorf("killed_at_ms = %d, want 1700000000000", ks.KilledAtMs)
		}
	})

	t.Run("never-killed account omits kill_status", func(t *testing.T) {
		h := &Handlers{Instances: seed(), Kills: &fakeKillReader{row: nil}}
		if ks := get(t, h).KillStatus; ks != nil {
			t.Errorf("kill_status = %+v, want nil (never killed)", *ks)
		}
	})
}
