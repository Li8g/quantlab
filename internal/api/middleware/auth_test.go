package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"quantlab/internal/saas/auth"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

func newTestAuth(t *testing.T) *auth.Service {
	t.Helper()
	svc, err := auth.NewService(config.JWTConfig{
		Secret: "test-secret-at-least-32-bytes-long!!",
		TTL:    1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("auth.NewService: %v", err)
	}
	return svc
}

func ginTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	return r
}

// TestAuthRequired_NoHeaderReturns401 verifies missing Authorization
// header is rejected with 401.
func TestAuthRequired_NoHeaderReturns401(t *testing.T) {
	svc := newTestAuth(t)
	r := ginTestEngine()
	r.GET("/protected", AuthRequired(svc), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Code = %d, want 401", rec.Code)
	}
}

// TestAuthRequired_NotBearerReturns401 verifies "Basic ..." header
// (or any non-Bearer scheme) is rejected.
func TestAuthRequired_NotBearerReturns401(t *testing.T) {
	svc := newTestAuth(t)
	r := ginTestEngine()
	r.GET("/p", AuthRequired(svc), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Basic foo")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Code = %d, want 401", rec.Code)
	}
}

// TestAuthRequired_ValidTokenInjectsClaims verifies a valid Bearer
// token surfaces Claims via ClaimsFrom inside the handler.
func TestAuthRequired_ValidTokenInjectsClaims(t *testing.T) {
	svc := newTestAuth(t)
	token, err := svc.SignToken(42, "operator")
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}

	var observed *auth.Claims
	r := ginTestEngine()
	r.GET("/p", AuthRequired(svc), func(c *gin.Context) {
		cl, _ := ClaimsFrom(c)
		observed = cl
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if observed == nil {
		t.Fatal("Claims not injected")
	}
	if observed.UserID != 42 || observed.Role != "operator" {
		t.Errorf("observed claims = %+v; want UserID=42 Role=operator", observed)
	}
}

// TestAuthRequired_ExpiredTokenReturns401 verifies the exp claim is
// checked.
func TestAuthRequired_ExpiredTokenReturns401(t *testing.T) {
	svc := newTestAuth(t)
	// Hand-roll an expired token with the same secret.
	claims := auth.Claims{
		UserID: 1, Role: "operator",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
	}
	tk := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tk.SignedString([]byte("test-secret-at-least-32-bytes-long!!"))
	if err != nil {
		t.Fatalf("sign expired: %v", err)
	}

	r := ginTestEngine()
	r.GET("/p", AuthRequired(svc), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("Code = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "auth: parse") {
		t.Errorf("body = %s; want parse error", rec.Body.String())
	}
}

// TestRequireRole_AllowsListedRoles + RejectsOthers verify the role
// gate.
func TestRequireRole_AllowsAdmin(t *testing.T) {
	svc := newTestAuth(t)
	token, _ := svc.SignToken(1, string(store.UserRoleAdmin))

	r := ginTestEngine()
	r.GET("/p",
		AuthRequired(svc),
		RequireRole(store.UserRoleAdmin),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("admin must pass admin-only gate, got %d", rec.Code)
	}
}

func TestRequireRole_RejectsViewerOnOperatorGate(t *testing.T) {
	svc := newTestAuth(t)
	token, _ := svc.SignToken(1, string(store.UserRoleViewer))

	r := ginTestEngine()
	r.GET("/p",
		AuthRequired(svc),
		RequireRole(store.UserRoleOperator, store.UserRoleAdmin),
		func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer must hit 403 on operator+ gate, got %d", rec.Code)
	}
}
