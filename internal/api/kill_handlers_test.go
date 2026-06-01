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

type fakeInstanceKiller struct {
	err           error
	gotInstanceID string
	gotOperator   uint
}

func (f *fakeInstanceKiller) Kill(_ context.Context, instanceID string, operatorUserID uint) error {
	f.gotInstanceID = instanceID
	f.gotOperator = operatorUserID
	return f.err
}

// TestKillInstance covers the manual kill route (Option 3 step 3b): the
// admin gate (operator+viewer 403), the success path (200 + operator id
// threaded from the JWT), and the sentinel→status mapping (404/409).
func TestKillInstance(t *testing.T) {
	authSvc := testAuthService(t)
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name       string
		role       store.UserRole
		killerErr  error
		wantCode   int
		wantKilled bool // killer reached (gate passed)
	}{
		{"viewer → 403", store.UserRoleViewer, nil, http.StatusForbidden, false},
		{"operator → 403", store.UserRoleOperator, nil, http.StatusForbidden, false},
		{"admin success → 200", store.UserRoleAdmin, nil, http.StatusOK, true},
		{"admin instance-not-found → 404", store.UserRoleAdmin, ErrKillInstanceNotFound, http.StatusNotFound, true},
		{"admin agent-offline → 409", store.UserRoleAdmin, ErrKillAgentOffline, http.StatusConflict, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fk := &fakeInstanceKiller{err: c.killerErr}
			h := &Handlers{
				AuthRequired: middleware.AuthRequired(authSvc),
				RequireAdmin: middleware.RequireRole(store.UserRoleAdmin),
				Killer:       fk,
			}
			r := gin.New()
			h.Register(r)

			const operatorID uint = 7
			token, _ := authSvc.SignToken(operatorID, string(c.role))
			rec := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances/inst-x/kill", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			r.ServeHTTP(rec, req)

			if rec.Code != c.wantCode {
				t.Errorf("code = %d, want %d; body=%s", rec.Code, c.wantCode, rec.Body.String())
			}
			if c.wantKilled {
				if fk.gotInstanceID != "inst-x" {
					t.Errorf("killer instanceID = %q, want inst-x", fk.gotInstanceID)
				}
				if fk.gotOperator != operatorID {
					t.Errorf("killer operator = %d, want %d (from JWT)", fk.gotOperator, operatorID)
				}
			} else if fk.gotInstanceID != "" {
				t.Errorf("killer was called despite role gate (instanceID=%q)", fk.gotInstanceID)
			}
		})
	}
}
