// Package auth issues and verifies JWTs for QuantLab's HTTP/WebSocket
// surface. HS256 with a config-injected secret; payload carries user ID
// and role. No refresh tokens in the prototype.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"quantlab/internal/saas/config"
)

// Service signs and verifies tokens.
type Service struct {
	secret   []byte
	ttl      time.Duration
	adminTTL time.Duration
}

// Claims is QuantLab's JWT body.
type Claims struct {
	UserID uint   `json:"user_id"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// NewService builds a Service from a config.JWTConfig.
func NewService(cfg config.JWTConfig) (*Service, error) {
	if cfg.Secret == "" {
		return nil, errors.New("auth: jwt secret is empty")
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	adminTTL := cfg.AdminTTL
	if adminTTL <= 0 {
		adminTTL = 10 * time.Minute
	}
	return &Service{
		secret:   []byte(cfg.Secret),
		ttl:      ttl,
		adminTTL: adminTTL,
	}, nil
}

// IssueToken signs a JWT and returns both the token and its expiry.
// Admin-role tokens use AdminTTL (default 10min, sudo-style); other
// roles use TTL (default 24h). Login handlers surface ExpiresAt to the
// client so the UI can prompt for re-login before the token dies.
//
// Note: time.Now() is allowed here — this is HTTP-handler-adjacent code,
// not 策略 Step() / engine Evaluate. See 铁律 2 exception list in
// docs/系统总体拓扑结构.md §8.3.
func (s *Service) IssueToken(userID uint, role string) (string, time.Time, error) {
	ttl := s.ttl
	if role == "admin" {
		ttl = s.adminTTL
	}
	now := time.Now()
	expiresAt := now.Add(ttl)
	claims := Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign: %w", err)
	}
	return signed, expiresAt, nil
}

// SignToken is the original signature kept for existing test callers
// that only need the token string.
func (s *Service) SignToken(userID uint, role string) (string, error) {
	tok, _, err := s.IssueToken(userID, role)
	return tok, err
}

// ParseToken verifies a JWT and returns its claims.
// Rejects tokens with a non-HS256 alg header (alg=none attack guard).
func (s *Service) ParseToken(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method %v", t.Header["alg"])
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: parse: %w", err)
	}
	if !tok.Valid {
		return nil, errors.New("auth: token invalid")
	}
	return claims, nil
}
