package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"quantlab/internal/api/middleware"
	"quantlab/internal/saas/store"
)

type fakeInstanceResumer struct {
	err           error
	gotInstanceID string
	gotOperator   uint
}

func (f *fakeInstanceResumer) Resume(_ context.Context, instanceID string, operatorUserID uint) error {
	f.gotInstanceID = instanceID
	f.gotOperator = operatorUserID
	return f.err
}

// TestResumeInstance covers the resume route (§5.13 v2): the admin gate
// (operator+viewer 403), the success path (200 + operator id from the
// JWT), and the sentinel→status mapping (404/409). Mirrors TestKillInstance.
func TestResumeInstance(t *testing.T) {
	authSvc := testAuthService(t)
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name        string
		role        store.UserRole
		resumerErr  error
		wantCode    int
		wantResumed bool // resumer reached (gate passed)
	}{
		{"viewer → 403", store.UserRoleViewer, nil, http.StatusForbidden, false},
		{"operator → 403", store.UserRoleOperator, nil, http.StatusForbidden, false},
		{"admin success → 200", store.UserRoleAdmin, nil, http.StatusOK, true},
		{"admin instance-not-found → 404", store.UserRoleAdmin, ErrKillInstanceNotFound, http.StatusNotFound, true},
		{"admin agent-offline → 409", store.UserRoleAdmin, ErrKillAgentOffline, http.StatusConflict, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fr := &fakeInstanceResumer{err: c.resumerErr}
			h := &Handlers{
				AuthRequired: middleware.AuthRequired(authSvc),
				RequireAdmin: middleware.RequireRole(store.UserRoleAdmin),
				Resumer:      fr,
			}
			r := gin.New()
			h.Register(r)

			const operatorID uint = 7
			token, _ := authSvc.SignToken(operatorID, string(c.role))
			rec := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances/inst-x/resume", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			r.ServeHTTP(rec, req)

			if rec.Code != c.wantCode {
				t.Errorf("code = %d, want %d; body=%s", rec.Code, c.wantCode, rec.Body.String())
			}
			if c.wantResumed {
				if fr.gotInstanceID != "inst-x" {
					t.Errorf("resumer instanceID = %q, want inst-x", fr.gotInstanceID)
				}
				if fr.gotOperator != operatorID {
					t.Errorf("resumer operator = %d, want %d (from JWT)", fr.gotOperator, operatorID)
				}
			} else if fr.gotInstanceID != "" {
				t.Errorf("resumer was called despite role gate (instanceID=%q)", fr.gotInstanceID)
			}
		})
	}
}
