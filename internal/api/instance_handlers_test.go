package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"quantlab/internal/api/middleware"
	"quantlab/internal/saas/auth"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

func testAuthService(t *testing.T) *auth.Service {
	t.Helper()
	svc, err := auth.NewService(config.JWTConfig{
		Secret: "instance-handler-test-secret-32-bytes!!",
		TTL:    time.Hour,
	})
	if err != nil {
		t.Fatalf("auth.NewService: %v", err)
	}
	return svc
}

// ===== fakes =====

type fakeInstances struct {
	mu        sync.Mutex
	byID      map[string]*store.StrategyInstance
	createErr error
	getErr    error
	upErr     error
	deployErr error
}

func newFakeInstances() *fakeInstances {
	return &fakeInstances{byID: map[string]*store.StrategyInstance{}}
}

func (f *fakeInstances) Create(_ context.Context, inst *store.StrategyInstance) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[inst.InstanceID] = inst
	return nil
}

func (f *fakeInstances) Get(_ context.Context, id string) (*store.StrategyInstance, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.byID[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return inst, nil
}

func (f *fakeInstances) UpdateStatus(_ context.Context, id string, status store.InstanceStatus) error {
	if f.upErr != nil {
		return f.upErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if inst, ok := f.byID[id]; ok {
		inst.Status = status
	}
	return nil
}

func (f *fakeInstances) SetActiveChampion(_ context.Context, id, chID string) error {
	if f.deployErr != nil {
		return f.deployErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if inst, ok := f.byID[id]; ok {
		inst.ActiveChampID = &chID
	}
	return nil
}

type fakeIssuer struct{ next string }

func (f *fakeIssuer) NewID() string { return f.next }

// withAuth returns a gin engine that pre-injects auth claims so
// handlers can call ownerFromContext without a real middleware chain.
func withClaimsHandlers(h *Handlers, claims *auth.Claims) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("quantlab.auth.claims", claims)
		c.Next()
	})
	h.Register(r)
	return r
}

func instJSON(t *testing.T, body []byte) InstanceResponse {
	t.Helper()
	var out InstanceResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode InstanceResponse: %v", err)
	}
	return out
}

// ===== tests =====

func TestCreateInstance_HappyPath(t *testing.T) {
	insts := newFakeInstances()
	h := &Handlers{Instances: insts, IDIssuer: &fakeIssuer{next: "inst-fake-1"}}
	claims := &auth.Claims{UserID: 7, Role: "operator"}
	r := withClaimsHandlers(h, claims)

	body := []byte(`{"strategy_id":"sigmoid_v1","pair":"BTCUSDT","account_id":"main"}`)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("Code = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	resp := instJSON(t, rec.Body.Bytes())
	if resp.InstanceID != "inst-fake-1" {
		t.Errorf("InstanceID = %q, want inst-fake-1", resp.InstanceID)
	}
	if resp.OwnerUserID != 7 {
		t.Errorf("OwnerUserID = %d, want 7 (from JWT claims)", resp.OwnerUserID)
	}
	if resp.Status != "idle" {
		t.Errorf("Status = %q, want idle (initial)", resp.Status)
	}
}

func TestCreateInstance_InvalidBodyReturns400(t *testing.T) {
	h := &Handlers{Instances: newFakeInstances(), IDIssuer: &fakeIssuer{next: "x"}}
	r := withClaimsHandlers(h, &auth.Claims{UserID: 1, Role: "operator"})

	body := []byte(`{"strategy_id":"","pair":"","account_id":""}`)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Code = %d, want 400", rec.Code)
	}
}

func TestGetInstance_NotFoundReturns404(t *testing.T) {
	insts := newFakeInstances()
	h := &Handlers{Instances: insts, IDIssuer: &fakeIssuer{next: "x"}}
	r := withClaimsHandlers(h, &auth.Claims{UserID: 1, Role: "viewer"})

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/instances/missing", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404", rec.Code)
	}
}

func TestStartInstance_TransitionsIdleToLive(t *testing.T) {
	insts := newFakeInstances()
	insts.byID["inst-1"] = &store.StrategyInstance{
		InstanceID: "inst-1", Status: store.InstanceStatusIdle,
	}
	h := &Handlers{Instances: insts, IDIssuer: &fakeIssuer{next: "x"}}
	r := withClaimsHandlers(h, &auth.Claims{UserID: 1, Role: "operator"})

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances/inst-1/start", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if insts.byID["inst-1"].Status != store.InstanceStatusLive {
		t.Errorf("Status = %q, want live", insts.byID["inst-1"].Status)
	}
}

func TestStartInstance_FromRetiredReturns422(t *testing.T) {
	insts := newFakeInstances()
	insts.byID["inst-1"] = &store.StrategyInstance{
		InstanceID: "inst-1", Status: store.InstanceStatusRetired,
	}
	h := &Handlers{Instances: insts, IDIssuer: &fakeIssuer{next: "x"}}
	r := withClaimsHandlers(h, &auth.Claims{UserID: 1, Role: "operator"})

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances/inst-1/start", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("Code = %d, want 422 (retired is terminal)", rec.Code)
	}
}

func TestStopInstance_TransitionsLiveToPaused(t *testing.T) {
	insts := newFakeInstances()
	insts.byID["inst-1"] = &store.StrategyInstance{
		InstanceID: "inst-1", Status: store.InstanceStatusLive,
	}
	h := &Handlers{Instances: insts, IDIssuer: &fakeIssuer{next: "x"}}
	r := withClaimsHandlers(h, &auth.Claims{UserID: 1, Role: "operator"})

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances/inst-1/stop", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if insts.byID["inst-1"].Status != store.InstanceStatusPaused {
		t.Errorf("Status = %q, want paused", insts.byID["inst-1"].Status)
	}
}

func TestDeployChampion_SetsActiveChampID(t *testing.T) {
	insts := newFakeInstances()
	insts.byID["inst-1"] = &store.StrategyInstance{
		InstanceID: "inst-1", Status: store.InstanceStatusIdle,
	}
	h := &Handlers{Instances: insts, IDIssuer: &fakeIssuer{next: "x"}}
	r := withClaimsHandlers(h, &auth.Claims{UserID: 1, Role: "operator"})

	body := []byte(`{"challenger_id":"ch-001"}`)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances/inst-1/deploy-champion", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	inst := insts.byID["inst-1"]
	if inst.ActiveChampID == nil || *inst.ActiveChampID != "ch-001" {
		t.Errorf("ActiveChampID = %v, want ch-001", inst.ActiveChampID)
	}
}

// TestPromoteRetire_AdminGated covers the RequireAdmin gate added in
// Phase 5D. Per docs/saas-tier2-schema-v1.md §3.2 only admin role can
// Promote a Challenger or Retire a Champion — operator + viewer must
// both 403. Mirrors TestInstanceWriteEndpoints_RoleGated.
func TestPromoteRetire_AdminGated(t *testing.T) {
	authSvc := testAuthService(t)
	h := &Handlers{
		Champions:    &fakeChampions{},
		AuthRequired: middleware.AuthRequired(authSvc),
		RequireAdmin: middleware.RequireRole(store.UserRoleAdmin),
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.Register(r)

	promoteBody := []byte(`{"reviewed_by":"alice"}`)
	retireBody := []byte(`{"reviewed_by":"bob"}`)

	cases := []struct {
		name     string
		role     store.UserRole
		userID   uint
		method   string
		path     string
		body     []byte
		wantCode int
	}{
		{"viewer promote → 403", store.UserRoleViewer, 1, http.MethodPost, "/api/v1/challengers/ch-x/promote", promoteBody, http.StatusForbidden},
		{"operator promote → 403", store.UserRoleOperator, 2, http.MethodPost, "/api/v1/challengers/ch-x/promote", promoteBody, http.StatusForbidden},
		{"admin promote → 200", store.UserRoleAdmin, 3, http.MethodPost, "/api/v1/challengers/ch-x/promote", promoteBody, http.StatusOK},
		{"viewer retire → 403", store.UserRoleViewer, 1, http.MethodPost, "/api/v1/champions/ch-x/retire", retireBody, http.StatusForbidden},
		{"operator retire → 403", store.UserRoleOperator, 2, http.MethodPost, "/api/v1/champions/ch-x/retire", retireBody, http.StatusForbidden},
		{"admin retire → 200", store.UserRoleAdmin, 3, http.MethodPost, "/api/v1/champions/ch-x/retire", retireBody, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			token, _ := authSvc.SignToken(c.userID, string(c.role))
			rec := httptest.NewRecorder()
			req, _ := http.NewRequest(c.method, c.path, bytesReader(c.body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(rec, req)
			if rec.Code != c.wantCode {
				t.Errorf("code = %d, want %d; body=%s", rec.Code, c.wantCode, rec.Body.String())
			}
		})
	}
}

// TestInstanceWriteEndpoints_RoleGated verifies the operator+ gate
// when a real RequireOperator middleware is wired. Viewer is rejected
// with 403; operator passes.
func TestInstanceWriteEndpoints_RoleGated(t *testing.T) {
	insts := newFakeInstances()
	authSvc := testAuthService(t)

	h := &Handlers{
		Instances:    insts,
		IDIssuer:     &fakeIssuer{next: "inst-gated-1"},
		AuthRequired: middleware.AuthRequired(authSvc),
		RequireOperator: middleware.RequireRole(
			store.UserRoleOperator, store.UserRoleAdmin,
		),
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.Register(r)

	body := []byte(`{"strategy_id":"sigmoid_v1","pair":"BTCUSDT","account_id":"main"}`)

	// Viewer → 403
	viewerToken, _ := authSvc.SignToken(1, string(store.UserRoleViewer))
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances", bytesReader(body))
	req.Header.Set("Authorization", "Bearer "+viewerToken)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer creating instance: code = %d, want 403", rec.Code)
	}

	// Operator → 201
	operatorToken, _ := authSvc.SignToken(2, string(store.UserRoleOperator))
	rec2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/api/v1/instances", bytesReader(body))
	req2.Header.Set("Authorization", "Bearer "+operatorToken)
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Errorf("operator creating instance: code = %d, want 201; body=%s",
			rec2.Code, rec2.Body.String())
	}
}
