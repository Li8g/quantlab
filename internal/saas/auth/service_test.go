package auth

import (
	"strings"
	"testing"
	"time"

	"quantlab/internal/saas/config"
)

func TestJWT_RoundTrip(t *testing.T) {
	svc, err := NewService(config.JWTConfig{Secret: "test-secret-32-bytes-long-enough", TTL: time.Hour})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	tok, err := svc.SignToken(42, "admin")
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	if !strings.HasPrefix(tok, "eyJ") {
		t.Fatalf("expected JWT-shaped string, got %q", tok)
	}
	c, err := svc.ParseToken(tok)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if c.UserID != 42 || c.Role != "admin" {
		t.Errorf("claims mismatch: got %+v", c)
	}
}

func TestJWT_RejectAlgNone(t *testing.T) {
	svc, err := NewService(config.JWTConfig{Secret: "secret", TTL: time.Hour})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// A header asserting alg=none. The body and "signature" don't matter
	// because ParseToken should reject the algorithm before checking the
	// signature.
	const algNoneToken = "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." +
		"eyJ1c2VyX2lkIjo0Mn0."
	if _, err := svc.ParseToken(algNoneToken); err == nil {
		t.Error("ParseToken should reject alg=none tokens")
	}
}

func TestJWT_RejectMissingSecret(t *testing.T) {
	if _, err := NewService(config.JWTConfig{}); err == nil {
		t.Error("NewService with empty secret should fail")
	}
}
