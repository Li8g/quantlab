package agentauth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"quantlab/internal/saas/store"
)

// GormTokenStore is the production TokenStore backed by *gorm.DB. The
// agent_tokens table is provisioned by store.NewDB's AutoMigrate
// (AgentToken is registered in store.AllModels).
type GormTokenStore struct {
	db *gorm.DB
}

// NewGormTokenStore wraps a *gorm.DB.
func NewGormTokenStore(db *gorm.DB) *GormTokenStore {
	return &GormTokenStore{db: db}
}

// GetByAgentID returns the row by agent_id, or (nil, nil) when no row
// exists. GORM's gorm.ErrRecordNotFound is mapped to (nil, nil) so
// callers don't need to depend on the GORM error sentinel.
func (s *GormTokenStore) GetByAgentID(ctx context.Context, agentID string) (*store.AgentToken, error) {
	var row store.AgentToken
	err := s.db.WithContext(ctx).Where("agent_id = ?", agentID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gormstore.GetByAgentID: %w", err)
	}
	return &row, nil
}

// Create inserts a new AgentToken row. The caller has already set
// AgentID / AccountID / TokenHash / Label. CreatedAt/UpdatedAt are
// auto-managed by GORM.
func (s *GormTokenStore) Create(ctx context.Context, row *store.AgentToken) error {
	if row == nil {
		return errors.New("gormstore.Create: row nil")
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("gormstore.Create: %w", err)
	}
	return nil
}

// Revoke sets revoked_at on the row. Idempotent — re-revoking is a no-op
// (UPDATE matches 0 rows when the WHERE filter excludes already-revoked).
// To honor idempotency we use an unconditional UPDATE that only matches
// rows where revoked_at IS NULL, then treat 0 rows affected as success.
func (s *GormTokenStore) Revoke(ctx context.Context, agentID string, now time.Time) error {
	tx := s.db.WithContext(ctx).
		Model(&store.AgentToken{}).
		Where("agent_id = ? AND revoked_at IS NULL", agentID).
		Update("revoked_at", now)
	if tx.Error != nil {
		return fmt.Errorf("gormstore.Revoke: %w", tx.Error)
	}
	return nil
}

// TouchLastSeen updates last_seen_at on the row. Best-effort: Service
// calls this and discards any error (an unauthenticated agent's auth_ok
// must not be blocked by a transient UPDATE failure).
func (s *GormTokenStore) TouchLastSeen(ctx context.Context, agentID string, now time.Time) error {
	tx := s.db.WithContext(ctx).
		Model(&store.AgentToken{}).
		Where("agent_id = ?", agentID).
		Update("last_seen_at", now)
	if tx.Error != nil {
		return fmt.Errorf("gormstore.TouchLastSeen: %w", tx.Error)
	}
	return nil
}
