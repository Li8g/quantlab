// Package middleware bundles cross-cutting HTTP middleware. Phase 6.3
// scope is auth: parse the Authorization Bearer token, verify via
// auth.Service.ParseToken, inject Claims into gin.Context for handlers
// downstream. RequireRole gates routes per docs/saas-tier2-schema-v1.md
// §3.2 A2 (admin / operator / viewer enforcement).
//
// The middleware itself does not consult the DB. The 24h JWT TTL
// (A1 decision) means a disabled user retains access until token
// expiry — explicit and accepted prototype limitation.
package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"quantlab/internal/saas/auth"
	"quantlab/internal/saas/store"
)

const (
	// claimsCtxKey is the gin.Context key that holds the parsed
	// *auth.Claims after AuthRequired succeeds. Use ClaimsFrom to
	// extract it in handlers.
	claimsCtxKey = "quantlab.auth.claims"

	bearerPrefix = "Bearer "
)

// AuthRequired returns a gin middleware that:
//   - reads Authorization: Bearer <jwt>
//   - parses + verifies via svc.ParseToken (HS256, exp check)
//   - injects *auth.Claims into c.Set(claimsCtxKey, …)
//   - 401 on missing / malformed / invalid token
//
// Subsequent handlers retrieve claims via ClaimsFrom(c).
func AuthRequired(svc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader("Authorization")
		if raw == "" {
			abort401(c, errors.New("missing Authorization header"))
			return
		}
		if !strings.HasPrefix(raw, bearerPrefix) {
			abort401(c, errors.New("Authorization must be 'Bearer <token>'"))
			return
		}
		token := strings.TrimPrefix(raw, bearerPrefix)
		claims, err := svc.ParseToken(token)
		if err != nil {
			abort401(c, err)
			return
		}
		c.Set(claimsCtxKey, claims)
		c.Next()
	}
}

// RequireRole gates a route to a closed set of allowed roles. Returns
// 403 when AuthRequired succeeded but the caller's role is not in
// the allowed set. Panics if AuthRequired did not run first —
// programmer error, not a runtime user issue.
//
// Typical wiring:
//
//	g := r.Group("/api/v1", middleware.AuthRequired(svc))
//	g.POST("/instances", middleware.RequireRole(store.UserRoleOperator, store.UserRoleAdmin), h.CreateInstance)
func RequireRole(allowed ...store.UserRole) gin.HandlerFunc {
	if len(allowed) == 0 {
		panic("middleware.RequireRole: at least one allowed role required")
	}
	allowedSet := make(map[store.UserRole]struct{}, len(allowed))
	for _, r := range allowed {
		allowedSet[r] = struct{}{}
	}
	return func(c *gin.Context) {
		claims, ok := ClaimsFrom(c)
		if !ok {
			// Programmer error: AuthRequired must precede RequireRole.
			panic("middleware.RequireRole: no claims in context; AuthRequired not invoked")
		}
		if _, allowed := allowedSet[store.UserRole(claims.Role)]; !allowed {
			c.AbortWithStatusJSON(http.StatusForbidden,
				gin.H{"error": "insufficient role"})
			return
		}
		c.Next()
	}
}

// ClaimsFrom extracts the *auth.Claims injected by AuthRequired. The
// second return is false when called outside an AuthRequired'd handler.
func ClaimsFrom(c *gin.Context) (*auth.Claims, bool) {
	v, ok := c.Get(claimsCtxKey)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*auth.Claims)
	return claims, ok
}

func abort401(c *gin.Context, err error) {
	c.AbortWithStatusJSON(http.StatusUnauthorized,
		gin.H{"error": err.Error()})
}
