package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	gotFrom   store.InstanceStatus
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

func (f *fakeInstances) UpdateStatus(_ context.Context, id string, from, status store.InstanceStatus) error {
	if f.upErr != nil {
		return f.upErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotFrom = from
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

func (f *fakeInstances) RetireInstance(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.byID[id]
	if !ok {
		return gorm.ErrRecordNotFound
	}
	if inst.Status == store.InstanceStatusRetired {
		return ErrInstanceAlreadyRetired
	}
	inst.Status = store.InstanceStatusRetired
	return nil
}

func (f *fakeInstances) BlockingInstanceForChampion(_ context.Context, championID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, inst := range f.byID {
		if inst.Status != store.InstanceStatusRetired &&
			inst.ActiveChampID != nil && *inst.ActiveChampID == championID {
			return inst.InstanceID, nil
		}
	}
	return "", nil
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

func TestCreateInstance_AccountActiveInstanceReturns409(t *testing.T) {
	insts := newFakeInstances()
	insts.createErr = ErrAccountActiveInstanceExists
	h := &Handlers{Instances: insts, IDIssuer: &fakeIssuer{next: "x"}}
	r := withClaimsHandlers(h, &auth.Claims{UserID: 1, Role: "operator"})

	body := []byte(`{"strategy_id":"sigmoid_v1","pair":"BTCUSDT","account_id":"main"}`)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("Code = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "active instance") {
		t.Errorf("body %q does not mention active instance", rec.Body.String())
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
	if insts.gotFrom != store.InstanceStatusIdle {
		t.Errorf("UpdateStatus from = %q, want idle", insts.gotFrom)
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
	if insts.gotFrom != store.InstanceStatusLive {
		t.Errorf("UpdateStatus from = %q, want live", insts.gotFrom)
	}
}

func TestStartInstance_CASRaceReturns422(t *testing.T) {
	insts := newFakeInstances()
	insts.byID["inst-1"] = &store.StrategyInstance{
		InstanceID: "inst-1", Status: store.InstanceStatusIdle,
	}
	insts.upErr = ErrInstanceTransitionRefused
	h := &Handlers{Instances: insts, IDIssuer: &fakeIssuer{next: "x"}}
	r := withClaimsHandlers(h, &auth.Claims{UserID: 1, Role: "operator"})

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances/inst-1/start", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Code = %d, want 422; body=%s", rec.Code, rec.Body.String())
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

// fakeAudit captures AuditLog rows for assertion.
type fakeAudit struct {
	rows []*store.AuditLog
	err  error
}

func (f *fakeAudit) Insert(_ context.Context, e *store.AuditLog) error {
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, e)
	return nil
}

// TestDeployChampion_RejectsCapBelowSlippage: B2 invariant 1 — a cap tighter
// than the champion's effective slippage_bps would reject the backtest's own
// normal fill price live, so deploy is refused (422) and ActiveChampID stays.
func TestDeployChampion_RejectsCapBelowSlippage(t *testing.T) {
	insts := newFakeInstances()
	insts.byID["inst-1"] = &store.StrategyInstance{
		InstanceID: "inst-1", Status: store.InstanceStatusIdle,
	}
	h := &Handlers{
		Instances:   insts,
		IDIssuer:    &fakeIssuer{next: "x"},
		Challengers: &fakeChallengers{rec: &store.GeneRecord{ChallengerID: "ch-001", SlippageBPS: 80}},
		PriceCapBps: 50, // 50 < 80 → violates cap ≥ slippage
	}
	r := withClaimsHandlers(h, &auth.Claims{UserID: 1, Role: "operator"})

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances/inst-1/deploy-champion",
		bytesReader([]byte(`{"challenger_id":"ch-001"}`)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Code = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if insts.byID["inst-1"].ActiveChampID != nil {
		t.Errorf("ActiveChampID = %v, want nil (deploy must not mutate on reject)", insts.byID["inst-1"].ActiveChampID)
	}
}

// TestDeployChampion_CapAtOrAboveSlippage_RecordsAudit: cap ≥ slippage passes
// and writes the deploy provenance row carrying the cap in effect.
func TestDeployChampion_CapAtOrAboveSlippage_RecordsAudit(t *testing.T) {
	insts := newFakeInstances()
	insts.byID["inst-1"] = &store.StrategyInstance{
		InstanceID: "inst-1", Status: store.InstanceStatusIdle,
	}
	audit := &fakeAudit{}
	h := &Handlers{
		Instances:   insts,
		IDIssuer:    &fakeIssuer{next: "x"},
		Challengers: &fakeChallengers{rec: &store.GeneRecord{ChallengerID: "ch-001", SlippageBPS: 20}},
		PriceCapBps: 50, // 50 ≥ 20 → OK
		Audit:       audit,
	}
	r := withClaimsHandlers(h, &auth.Claims{UserID: 7, Role: "operator"})

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances/inst-1/deploy-champion",
		bytesReader([]byte(`{"challenger_id":"ch-001"}`)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ac := insts.byID["inst-1"].ActiveChampID; ac == nil || *ac != "ch-001" {
		t.Errorf("ActiveChampID = %v, want ch-001", ac)
	}
	if len(audit.rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(audit.rows))
	}
	row := audit.rows[0]
	if row.Action != store.AuditActionInstanceDeployChampion {
		t.Errorf("audit action = %q, want %q", row.Action, store.AuditActionInstanceDeployChampion)
	}
	if !strings.Contains(string(row.DataJSON), `"price_cap_bps":50`) {
		t.Errorf("audit DataJSON = %s, want price_cap_bps=50", row.DataJSON)
	}
}

func TestDeployChampion_RefusedReturns422(t *testing.T) {
	insts := newFakeInstances()
	insts.byID["inst-1"] = &store.StrategyInstance{
		InstanceID: "inst-1", Status: store.InstanceStatusIdle,
	}
	insts.deployErr = ErrDeployChampionRefused
	h := &Handlers{Instances: insts, IDIssuer: &fakeIssuer{next: "x"}}
	r := withClaimsHandlers(h, &auth.Claims{UserID: 1, Role: "operator"})

	body := []byte(`{"challenger_id":"ch-001"}`)
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/instances/inst-1/deploy-champion", bytesReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Code = %d, want 422; body=%s", rec.Code, rec.Body.String())
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
		Instances:    newFakeInstances(),
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
