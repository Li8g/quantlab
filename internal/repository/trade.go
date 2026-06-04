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
// index on (client_order_id, filled_at_exchange_ms): both fill channels
// (order_update and delta_report) dedup at the caller via ExecutionExists
// before inserting, since the exchange event stream is at-least-once and a
// redelivered fill rides a fresh envelope msg_id that envelope-level replay
// protection can't catch (see agentmsg.insertFillIfNew).
func (r *TradeRepo) InsertSpotExecution(ctx context.Context, ex *store.SpotExecution) error {
	if ex == nil {
		return errors.New("repository.TradeRepo.InsertSpotExecution: nil execution")
	}
	if ex.ClientOrderID == "" {
		return errors.New("repository.TradeRepo.InsertSpotExecution: empty client_order_id")
	}
	return r.db.WithContext(ctx).Create(ex).Error
}

// ExecutionExistsByTrade reports whether a fill with this (client_order_id,
// trade_id) is already persisted. This is the canonical dedup key when the
// exchange surfaces a per-trade id: it distinguishes the genuine multi-level
// fills of one market sweep (which share filled_at_exchange_ms) while still
// catching the same trade replayed across the order_update + delta_report
// channels or by the at-least-once event stream. Callers use ExecutionExists
// (the ms key) only as a fallback when trade_id is absent (MockExchange).
func (r *TradeRepo) ExecutionExistsByTrade(ctx context.Context, clientOrderID string, tradeID int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&store.SpotExecution{}).
		Where("client_order_id = ? AND trade_id = ?", clientOrderID, tradeID).
		Limit(1).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ExecutionExists reports whether a fill with this (client_order_id,
// filled_at_exchange_ms) is already persisted. Both fill channels dedup
// against it before inserting: order_update (hot path) because the exchange
// event stream is at-least-once and replays rides a fresh envelope msg_id,
// and delta_report (§5.11 fallback) because it is redundant with
// order_update. Cheap SELECT; the hot path adds one per fill.
func (r *TradeRepo) ExecutionExists(ctx context.Context, clientOrderID string, filledAtExchangeMs int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&store.SpotExecution{}).
		Where("client_order_id = ? AND filled_at_exchange_ms = ?", clientOrderID, filledAtExchangeMs).
		Limit(1).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// NewExecutionsForInstance returns the instance's confirmed fills with
// SpotExecution.ID > sinceID, oldest first. It joins spot_executions to
// trade_records (on client_order_id) to recover the order Side and scope
// the rows to one instance. The monotonic, collision-free ID watermark lets
// the Tick fold each fill into the portfolio ledger exactly once (③).
func (r *TradeRepo) NewExecutionsForInstance(ctx context.Context, instanceID string, sinceID uint) ([]store.InstanceFill, error) {
	if instanceID == "" {
		return nil, errors.New("repository.TradeRepo.NewExecutionsForInstance: empty instance_id")
	}
	var rows []store.InstanceFill
	err := r.db.WithContext(ctx).
		Model(&store.SpotExecution{}).
		Select("spot_executions.id AS id, "+
			"trade_records.side AS side, "+
			"spot_executions.fill_quantity AS fill_quantity, "+
			"spot_executions.fill_price AS fill_price, "+
			"spot_executions.fill_fee_asset AS fill_fee_asset, "+
			"spot_executions.fill_fee_amount AS fill_fee_amount").
		Joins("JOIN trade_records ON trade_records.client_order_id = spot_executions.client_order_id").
		Where("trade_records.instance_id = ? AND spot_executions.id > ?", instanceID, sinceID).
		Order("spot_executions.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// MaxExecutionIDForInstance returns the highest SpotExecution.ID among the
// instance's orders, or 0 if it has none. Used to set a freshly funded
// instance's ledger watermark: the genesis balance already reflects every
// pre-funding fill, so those must not be re-applied by the first Tick.
func (r *TradeRepo) MaxExecutionIDForInstance(ctx context.Context, instanceID string) (uint, error) {
	if instanceID == "" {
		return 0, errors.New("repository.TradeRepo.MaxExecutionIDForInstance: empty instance_id")
	}
	var maxID *uint
	err := r.db.WithContext(ctx).
		Model(&store.SpotExecution{}).
		Joins("JOIN trade_records ON trade_records.client_order_id = spot_executions.client_order_id").
		Where("trade_records.instance_id = ?", instanceID).
		Select("MAX(spot_executions.id)").
		Scan(&maxID).Error
	if err != nil {
		return 0, err
	}
	if maxID == nil {
		return 0, nil
	}
	return *maxID, nil
}

// MarkPartialIfPending advances a TradeRecord from pending → partial_filled,
// and only then. Used by the delta_report recovery path (①): a recovered
// fill proves the order executed at least partially, so a still-pending row
// must not stay stuck. The authoritative terminal status (filled/cancelled)
// remains the order_update channel's job; the WHERE guard ensures this never
// downgrades a row order_update already moved. RowsAffected==0 (already
// non-pending) is not an error.
func (r *TradeRepo) MarkPartialIfPending(ctx context.Context, clientOrderID string) error {
	if clientOrderID == "" {
		return errors.New("repository.TradeRepo.MarkPartialIfPending: empty client_order_id")
	}
	return r.db.WithContext(ctx).Model(&store.TradeRecord{}).
		Where("client_order_id = ? AND status = ?", clientOrderID, store.TradeStatusPending).
		Update("status", store.TradeStatusPartialFilled).Error
}

// SweepOrphanPending cancels orphaned pending TradeRecords: rows that
// recordingDispatcher pre-inserted (status=pending) but whose dispatch then
// failed (agent offline / latestClose=0), so no Ack/OrderUpdate ever advanced
// them. A row is an orphan only when its GTT window has lapsed
// (valid_until_ms < nowMs, so it can never fill) AND it never executed (no
// SpotExecution) — the latter guard keeps a real-but-status-stuck fill (the
// order_update gap) from being wrongly cancelled. Marks matches
// TradeStatusCancelled and returns the count. Startup-only backstop,
// mirroring ImportJobRepo/EvolutionTaskRepo SweepOrphans. nowMs is injected
// (not time.Now) for deterministic tests; callers pass time.Now().UnixMilli().
func (r *TradeRepo) SweepOrphanPending(ctx context.Context, nowMs int64) (int64, error) {
	noFill := r.db.Model(&store.SpotExecution{}).
		Select("1").
		Where("spot_executions.client_order_id = trade_records.client_order_id")
	res := r.db.WithContext(ctx).Model(&store.TradeRecord{}).
		Where("status = ?", store.TradeStatusPending).
		Where("valid_until_ms < ?", nowMs).
		Where("NOT EXISTS (?)", noFill).
		Update("status", store.TradeStatusCancelled)
	return res.RowsAffected, res.Error
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
