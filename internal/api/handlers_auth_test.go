package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"quantlab/internal/saas/auth"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

// ===== fakes =====

type fakeUserAuth struct {
	byEmail  map[string]*store.User
	getErr   error
	touched  uint
	touchErr error
}

func (f *fakeUserAuth) GetByEmail(_ context.Context, email string) (*store.User, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	u, ok := f.byEmail[email]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return u, nil
}

func (f *fakeUserAuth) UpdateLastLoginAt(_ context.Context, userID uint, _ time.Time) error {
	f.touched = userID
	return f.touchErr
}

// ===== test harness =====

func newLoginTestServer(t *testing.T, users *fakeUserAuth) (*httptest.Server, *auth.Service) {
	t.Helper()
	svc, err := auth.NewService(config.JWTConfig{
		Secret:   "test-secret-32-bytes-long-enough",
		TTL:      24 * time.Hour,
		AdminTTL: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("auth.NewService: %v", err)
	}
	h := &Handlers{Users: users, Tokens: svc}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.Register(r)
	return httptest.NewServer(r), svc
}

func postLogin(t *testing.T, srv *httptest.Server, body any) (*http.Response, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func mustBcrypt(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return string(h)
}

func seedTestUser(t *testing.T, email, pw string, role store.UserRole) (*store.User, *fakeUserAuth) {
	t.Helper()
	u := &store.User{
		ID:           42,
		UserID:       "01HX...",
		Email:        email,
		PasswordHash: mustBcrypt(t, pw),
		Role:         role,
		Active:       true,
	}
	return u, &fakeUserAuth{byEmail: map[string]*store.User{email: u}}
}

// ===== happy paths =====

func TestLogin_DefaultRoleIsViewer(t *testing.T) {
	_, fake := seedTestUser(t, "me@example.com", "hunter2", store.UserRoleAdmin)
	srv, _ := newLoginTestServer(t, fake)
	defer srv.Close()

	resp, body := postLogin(t, srv, LoginRequest{Email: "me@example.com", Password: "hunter2"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var got LoginResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Role != "viewer" {
		t.Errorf("Role = %q, want viewer (default)", got.Role)
	}
	if got.Token == "" {
		t.Error("Token empty")
	}
	if got.ExpiresAt <= time.Now().UnixMilli() {
		t.Errorf("ExpiresAt %d is in the past", got.ExpiresAt)
	}
	if fake.touched != 42 {
		t.Errorf("UpdateLastLoginAt not called (touched=%d)", fake.touched)
	}
}

func TestLogin_ExplicitOperatorRole(t *testing.T) {
	_, fake := seedTestUser(t, "me@example.com", "hunter2", store.UserRoleAdmin)
	srv, _ := newLoginTestServer(t, fake)
	defer srv.Close()

	resp, body := postLogin(t, srv, LoginRequest{
		Email: "me@example.com", Password: "hunter2", Role: "operator",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var got LoginResponse
	_ = json.Unmarshal(body, &got)
	if got.Role != "operator" {
		t.Errorf("Role = %q, want operator", got.Role)
	}
}

func TestLogin_AdminRoleUsesShortTTL(t *testing.T) {
	_, fake := seedTestUser(t, "me@example.com", "hunter2", store.UserRoleAdmin)
	srv, svc := newLoginTestServer(t, fake)
	defer srv.Close()

	resp, body := postLogin(t, srv, LoginRequest{
		Email: "me@example.com", Password: "hunter2", Role: "admin",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var got LoginResponse
	_ = json.Unmarshal(body, &got)

	// Admin TTL is 10min; expires_at should be within 11 minutes of now,
	// and well below the 24h viewer TTL.
	deltaMs := got.ExpiresAt - time.Now().UnixMilli()
	if deltaMs > 11*60*1000 {
		t.Errorf("admin token expires_at delta = %dms, want <= 11min (short TTL)", deltaMs)
	}
	if deltaMs < 1*60*1000 {
		t.Errorf("admin token expires_at delta = %dms, want >= 1min", deltaMs)
	}

	// Parse the token to confirm the claim is admin.
	claims, err := svc.ParseToken(got.Token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.Role != "admin" {
		t.Errorf("claims.Role = %q, want admin", claims.Role)
	}
}

// ===== auth failures (all 401, no leak) =====

func TestLogin_WrongPasswordReturns401(t *testing.T) {
	_, fake := seedTestUser(t, "me@example.com", "hunter2", store.UserRoleAdmin)
	srv, _ := newLoginTestServer(t, fake)
	defer srv.Close()

	resp, _ := postLogin(t, srv, LoginRequest{Email: "me@example.com", Password: "wrong"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestLogin_UnknownEmailReturns401(t *testing.T) {
	_, fake := seedTestUser(t, "me@example.com", "hunter2", store.UserRoleAdmin)
	srv, _ := newLoginTestServer(t, fake)
	defer srv.Close()

	resp, _ := postLogin(t, srv, LoginRequest{Email: "other@example.com", Password: "hunter2"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestLogin_InactiveUserReturns401(t *testing.T) {
	u, fake := seedTestUser(t, "me@example.com", "hunter2", store.UserRoleAdmin)
	u.Active = false
	srv, _ := newLoginTestServer(t, fake)
	defer srv.Close()

	resp, _ := postLogin(t, srv, LoginRequest{Email: "me@example.com", Password: "hunter2"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for inactive user", resp.StatusCode)
	}
}

func TestLogin_RepoErrorReturns401NotLeaked(t *testing.T) {
	_, fake := seedTestUser(t, "me@example.com", "hunter2", store.UserRoleAdmin)
	fake.getErr = errors.New("db down")
	srv, _ := newLoginTestServer(t, fake)
	defer srv.Close()

	resp, body := postLogin(t, srv, LoginRequest{Email: "me@example.com", Password: "hunter2"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (no DB-error leak)", resp.StatusCode)
	}
	if bytes.Contains(body, []byte("db down")) {
		t.Errorf("body leaked the underlying error: %s", body)
	}
}

// ===== request validation =====

func TestLogin_MissingEmailOrPasswordReturns400(t *testing.T) {
	_, fake := seedTestUser(t, "me@example.com", "hunter2", store.UserRoleAdmin)
	srv, _ := newLoginTestServer(t, fake)
	defer srv.Close()

	for _, tc := range []LoginRequest{
		{Email: "", Password: "x"},
		{Email: "x", Password: ""},
	} {
		resp, _ := postLogin(t, srv, tc)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body=%+v: status = %d, want 400", tc, resp.StatusCode)
		}
	}
}

func TestLogin_InvalidRoleReturns400(t *testing.T) {
	_, fake := seedTestUser(t, "me@example.com", "hunter2", store.UserRoleAdmin)
	srv, _ := newLoginTestServer(t, fake)
	defer srv.Close()

	resp, _ := postLogin(t, srv, LoginRequest{
		Email: "me@example.com", Password: "hunter2", Role: "superuser",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid role", resp.StatusCode)
	}
}

// ===== role hierarchy gate =====

func TestLogin_ViewerCannotRequestAdmin(t *testing.T) {
	_, fake := seedTestUser(t, "me@example.com", "hunter2", store.UserRoleViewer)
	srv, _ := newLoginTestServer(t, fake)
	defer srv.Close()

	resp, body := postLogin(t, srv, LoginRequest{
		Email: "me@example.com", Password: "hunter2", Role: "admin",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (viewer asked for admin); body=%s", resp.StatusCode, body)
	}
}

func TestLogin_OperatorCannotRequestAdmin(t *testing.T) {
	_, fake := seedTestUser(t, "me@example.com", "hunter2", store.UserRoleOperator)
	srv, _ := newLoginTestServer(t, fake)
	defer srv.Close()

	resp, _ := postLogin(t, srv, LoginRequest{
		Email: "me@example.com", Password: "hunter2", Role: "admin",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (operator asked for admin)", resp.StatusCode)
	}
}

func TestLogin_OperatorCanRequestViewer(t *testing.T) {
	// Downgrade should always work; same operator user requesting
	// viewer token is fine.
	_, fake := seedTestUser(t, "me@example.com", "hunter2", store.UserRoleOperator)
	srv, _ := newLoginTestServer(t, fake)
	defer srv.Close()

	resp, _ := postLogin(t, srv, LoginRequest{
		Email: "me@example.com", Password: "hunter2", Role: "viewer",
	})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (downgrade allowed)", resp.StatusCode)
	}
}

// ===== route registration =====

func TestLogin_NilCollaboratorsSkipRoute(t *testing.T) {
	h := &Handlers{} // no Users, no Tokens
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.Register(r)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, _ := postLogin(t, srv, LoginRequest{Email: "x", Password: "y"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (route should not exist)", resp.StatusCode)
	}
}
