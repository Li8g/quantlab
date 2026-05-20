package agentauth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"quantlab/internal/saas/store"
)

// Errors returned by Verify. The Hub maps these to AuthFail codes per
// docs/saas-ws-protocol-v1.md §5.5:
//
//	ErrInvalidFormat → AuthFailInvalidToken
//	ErrUnknownAgent  → AuthFailInvalidToken (don't leak existence)
//	ErrRevoked       → AuthFailRevoked
//	ErrBadSecret     → AuthFailInvalidToken
var (
	ErrUnknownAgent = errors.New("agentauth: unknown agent_id")
	ErrRevoked      = errors.New("agentauth: token revoked")
	ErrBadSecret    = errors.New("agentauth: secret mismatch")
)

// DefaultBcryptCost matches docs/saas-tier2-schema-v1.md A4 (also used for
// user passwords). cost=12 → ~250ms on a 2024-era CPU; tolerable for the
// rare Auth event (once per connection), prohibitive for an attacker.
const DefaultBcryptCost = 12

// Verified is the result of a successful Verify call. Hub uses AgentID
// and AccountID to bind the connection and update Redis online state.
type Verified struct {
	AgentID   string
	AccountID string
}

// TokenStore is the persistence interface Service depends on. The GORM
// implementation (GormTokenStore) is the production binding; tests use a
// fake. Methods are intentionally narrow — Service owns the policy, the
// store owns row-level CRUD.
type TokenStore interface {
	// GetByAgentID returns the row by agent_id. Returns (nil, nil) when
	// no row exists; that maps to ErrUnknownAgent in Service.Verify.
	GetByAgentID(ctx context.Context, agentID string) (*store.AgentToken, error)

	// Create inserts a row. The caller has already filled all fields,
	// including bcrypt(secret).
	Create(ctx context.Context, row *store.AgentToken) error

	// Revoke sets revoked_at on the row. Idempotent: a second Revoke on
	// an already-revoked row returns nil without error.
	Revoke(ctx context.Context, agentID string, now time.Time) error

	// TouchLastSeen updates last_seen_at. Best-effort; Service ignores
	// errors so a transient DB hiccup does not fail an otherwise-valid
	// auth_ok.
	TouchLastSeen(ctx context.Context, agentID string, now time.Time) error
}

// Service is the agent-auth orchestrator. Thread-safe (no mutable state
// outside the store).
type Service struct {
	store      TokenStore
	bcryptCost int
	ulid       func() string // injectable so tests can fix the AgentID
}

// NewService wires a TokenStore (and uses store.NewULID + DefaultBcryptCost).
func NewService(s TokenStore) *Service {
	return &Service{
		store:      s,
		bcryptCost: DefaultBcryptCost,
		ulid:       store.NewULID,
	}
}

// WithBcryptCost is a test/diagnostic override. Cost ∈ [4, 31]; bcrypt's
// MinCost (4) for fast tests, MaxCost (31) for paranoia. Returns the
// receiver for fluent setup.
func (s *Service) WithBcryptCost(cost int) *Service {
	s.bcryptCost = cost
	return s
}

// WithULID overrides the AgentID generator. Used by tests to produce
// deterministic tokens.
func (s *Service) WithULID(fn func() string) *Service {
	s.ulid = fn
	return s
}

// Created is the result of CreateToken. Plaintext is the value the admin
// gives to the Agent operator (shown once, never persisted plain).
type Created struct {
	AgentID   string
	Plaintext string
}

// CreateToken provisions a fresh AgentToken row for accountID. Returns
// the plaintext token (display-once secret) and the AgentID embedded in
// it. The plaintext is also the exact value the Agent puts in
// config.agent.yaml's saas_token field.
func (s *Service) CreateToken(ctx context.Context, accountID, label string) (Created, error) {
	if accountID == "" {
		return Created{}, errors.New("agentauth: accountID empty")
	}
	agentID := s.ulid()
	secret, err := NewSecret()
	if err != nil {
		return Created{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), s.bcryptCost)
	if err != nil {
		return Created{}, fmt.Errorf("agentauth: bcrypt: %w", err)
	}
	row := &store.AgentToken{
		AgentID:   agentID,
		AccountID: accountID,
		TokenHash: string(hash),
		Label:     label,
	}
	if err := s.store.Create(ctx, row); err != nil {
		return Created{}, fmt.Errorf("agentauth: store.Create: %w", err)
	}
	return Created{
		AgentID:   agentID,
		Plaintext: FormatToken(agentID, secret),
	}, nil
}

// Verify validates a token presented by an Agent in the `auth` frame.
// On success it returns AgentID + AccountID and best-effort updates the
// LastSeenAt timestamp.
func (s *Service) Verify(ctx context.Context, token string, now time.Time) (*Verified, error) {
	parsed, err := ParseToken(token)
	if err != nil {
		return nil, err
	}
	row, err := s.store.GetByAgentID(ctx, parsed.AgentID)
	if err != nil {
		return nil, fmt.Errorf("agentauth: store.GetByAgentID: %w", err)
	}
	if row == nil {
		return nil, ErrUnknownAgent
	}
	if row.RevokedAt != nil {
		return nil, ErrRevoked
	}
	if err := bcrypt.CompareHashAndPassword([]byte(row.TokenHash), []byte(parsed.Secret)); err != nil {
		// bcrypt.ErrMismatchedHashAndPassword is the canonical wrong-secret
		// signal; other errors (malformed hash) also map to ErrBadSecret
		// — we don't want to leak DB corruption to the wire.
		return nil, ErrBadSecret
	}
	// Best-effort touch — caller decides whether to honor a slow row update.
	_ = s.store.TouchLastSeen(ctx, parsed.AgentID, now)
	return &Verified{AgentID: row.AgentID, AccountID: row.AccountID}, nil
}

// Revoke marks the token revoked. Idempotent (see TokenStore.Revoke).
func (s *Service) Revoke(ctx context.Context, agentID string, now time.Time) error {
	return s.store.Revoke(ctx, agentID, now)
}
