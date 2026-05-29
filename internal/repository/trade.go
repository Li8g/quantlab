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

// ListByInstance returns TradeRecord rows for one instance, ordered
// by CreatedAt descending (newest first). limit ≤ 0 returns
// everything matching — callers should cap externally to bound the
// payload size on long-running instances.
func (r *TradeRepo) ListByInstance(ctx context.Context, instanceID string, limit int) ([]store.TradeRecord, error) {
	if instanceID == "" {
		return nil, errors.New("repository.TradeRepo.ListByInstance: empty instance_id")
	}
	q := r.db.WithContext(ctx).
		Where("instance_id = ?", instanceID).
		Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []store.TradeRecord
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
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

// ListExecutionsForOrders returns every fill whose client_order_id is in
// the supplied set, oldest fill first. Used by the live-monitor /live
// snapshot to attach fill detail to the recent trade tail in a single
// query (the order set is already bounded by the trade limit). An empty
// input returns (nil, nil) without touching the DB.
func (r *TradeRepo) ListExecutionsForOrders(ctx context.Context, clientOrderIDs []string) ([]store.SpotExecution, error) {
	if len(clientOrderIDs) == 0 {
		return nil, nil
	}
	var rows []store.SpotExecution
	err := r.db.WithContext(ctx).
		Where("client_order_id IN ?", clientOrderIDs).
		Order("filled_at_exchange_ms ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}
