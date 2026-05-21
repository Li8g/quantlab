// trade.go — TradeRepo persists TradeRecord (order intent + lifecycle
// status) and SpotExecution (per-fill rows) per docs/saas-tier2-schema-v1.md
// §6.2/§6.3. Inserted at Dispatch time (status=pending) and updated via
// Agent Ack / OrderUpdate frames received on the WS Hub.
package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"quantlab/internal/saas/store"
)

type TradeRepo struct {
	db *gorm.DB
}

func NewTradeRepo(db *gorm.DB) *TradeRepo {
	return &TradeRepo{db: db}
}

// InsertTradeRecord creates the TradeRecord row. client_order_id has a
// unique index — duplicate inserts (e.g. retried dispatch on a flapped
// connection) surface as gorm.ErrDuplicatedKey via the underlying
// driver; callers should treat that as "already recorded" and proceed.
func (r *TradeRepo) InsertTradeRecord(ctx context.Context, tr *store.TradeRecord) error {
	if tr == nil {
		return errors.New("repository.TradeRepo.InsertTradeRecord: nil record")
	}
	if tr.ClientOrderID == "" {
		return errors.New("repository.TradeRepo.InsertTradeRecord: empty client_order_id")
	}
	return r.db.WithContext(ctx).Create(tr).Error
}

// UpdateTradeStatus updates Status for the row keyed by client_order_id.
// Returns gorm.ErrRecordNotFound when no row matches — callers (the
// Ack/OrderUpdate hook) treat that as a protocol error (Agent acked a
// command SaaS never recorded). Idempotent on identical updates.
func (r *TradeRepo) UpdateTradeStatus(ctx context.Context, clientOrderID string, status store.TradeStatus) error {
	if clientOrderID == "" {
		return errors.New("repository.TradeRepo.UpdateTradeStatus: empty client_order_id")
	}
	res := r.db.WithContext(ctx).Model(&store.TradeRecord{}).
		Where("client_order_id = ?", clientOrderID).
		Update("status", status)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// InsertSpotExecution appends one fill row. SpotExecution has no unique
// index on (client_order_id, filled_at_exchange_ms) — duplicate Fills
// on retried OrderUpdate frames are deduplicated at the caller via
// msg_id replay protection (envelope-level).
func (r *TradeRepo) InsertSpotExecution(ctx context.Context, ex *store.SpotExecution) error {
	if ex == nil {
		return errors.New("repository.TradeRepo.InsertSpotExecution: nil execution")
	}
	if ex.ClientOrderID == "" {
		return errors.New("repository.TradeRepo.InsertSpotExecution: empty client_order_id")
	}
	return r.db.WithContext(ctx).Create(ex).Error
}
