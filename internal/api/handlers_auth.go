// POST /auth/login. Sudo-style step-up: the request body carries an
// optional `role` that defaults to "viewer"; the issued JWT is signed
// with the requested role capped at the user's DB role (admin > operator
// > viewer). Admin tokens auto-expire on the short AdminTTL so a misused
// elevated session has a small fat-finger window — see
// auth.Service.IssueToken.
//
// Errors are uniformly 401 ("invalid credentials") for any auth failure
// — wrong email, wrong password, inactive user. Distinguishing them
// would leak account existence to an attacker probing email addresses.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"quantlab/internal/saas/store"
)

// UserAuthenticator is the persistence surface the Login handler needs.
// repository.UserRepo satisfies this; tests inject a fake.
type UserAuthenticator interface {
	GetByEmail(ctx context.Context, email string) (*store.User, error)
	UpdateLastLoginAt(ctx context.Context, userID uint, at time.Time) error
}

// TokenIssuer signs a JWT for (userID, role) and returns the token plus
// its expiry. auth.Service satisfies this via IssueToken.
type TokenIssuer interface {
	IssueToken(userID uint, role string, adminCapable bool) (string, time.Time, error)
}

// LoginRequest is the POST /auth/login body. Email + Password are
// required; Role defaults to "viewer" when omitted, capped at the
// user's DB role on the server side.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role,omitempty"`
}

// LoginResponse carries the JWT plus its expiry so clients can prompt
// for re-login before the token dies (admin tokens are short-lived).
type LoginResponse struct {
	Token     string `json:"token"`
	Role      string `json:"role"`
	ExpiresAt int64  `json:"expires_at"` // unix milliseconds
}

// Login: POST /api/v1/auth/login. 200 + LoginResponse on success;
// 401 {"error":"invalid credentials"} on any auth failure;
// 400 on malformed body or role > user's DB role.
func (h *Handlers) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(c, http.StatusBadRequest, errors.New("email and password are required"))
		return
	}

	// Default role: viewer. Sudo-style: most sessions don't need
	// elevation; you opt in by passing "operator" or "admin".
	requestedRole := store.UserRoleViewer
	if req.Role != "" {
		r, ok := store.ParseUserRole(req.Role)
		if !ok {
			writeError(c, http.StatusBadRequest, errors.New("invalid role"))
			return
		}
		requestedRole = r
	}

	u, err := h.Users.GetByEmail(c.Request.Context(), req.Email)
	if err != nil {
		// Same 401 whether the row was missing or the DB hiccuped;
		// don't leak existence.
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			slog.Warn("auth_login_repo_error", "err", err, "email", req.Email)
		}
		writeError(c, http.StatusUnauthorized, errors.New("invalid credentials"))
		return
	}

	if !u.Active {
		writeError(c, http.StatusUnauthorized, errors.New("invalid credentials"))
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)); err != nil {
		writeError(c, http.StatusUnauthorized, errors.New("invalid credentials"))
		return
	}

	// Cap the requested role at the user's DB role. A viewer asking for
	// admin gets 400 — that's a client bug, surface it so the UI shows
	// "you don't have permission" instead of silently downgrading.
	if !store.RoleAtLeast(u.Role, requestedRole) {
		writeError(c, http.StatusBadRequest, errors.New("requested role exceeds user role"))
		return
	}

	// AdminCapable tracks the DB role, not the issued role: an admin who
	// logs in with the default viewer role still gets a 24h session that
	// can read admin-scoped views (e.g. the full instance fleet), while
	// privileged writes still require an admin-role step-up token.
	adminCapable := u.Role == store.UserRoleAdmin
	token, expiresAt, err := h.Tokens.IssueToken(u.ID, string(requestedRole), adminCapable)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}

	// Audit elevated sessions — admin role has the smallest fat-finger
	// budget so it gets its own log line. Non-admin logins are not
	// audited at this layer (gin's default access log suffices).
	if requestedRole == store.UserRoleAdmin {
		slog.Info("auth_admin_session_issued",
			"user_id", u.ID,
			"email", u.Email,
			"client_ip", c.ClientIP(),
			"expires_at", expiresAt.UnixMilli())
	}

	// Best-effort last_login_at touch. Failure here is logged but does
	// not fail the otherwise-valid login.
	if err := h.Users.UpdateLastLoginAt(c.Request.Context(), u.ID, time.Now()); err != nil {
		slog.Warn("auth_login_touch_last_login_failed", "err", err, "user_id", u.ID)
	}

	c.JSON(http.StatusOK, LoginResponse{
		Token:     token,
		Role:      string(requestedRole),
		ExpiresAt: expiresAt.UnixMilli(),
	})
}
