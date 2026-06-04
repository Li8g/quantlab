// instance.go — InstanceRepo persists StrategyInstance rows and
// answers the queries the Phase 6 Cron Tick needs. Per the design
// doc (docs/saas-tier2-schema-v1.md §4.2), InstanceID is a ULID
// allocated by the caller (HTTP create-instance handler), not by
// the repo.
package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"quantlab/internal/saas/store"
)

// InstanceRepo wraps a *gorm.DB for StrategyInstance access. Methods
// are scoped narrowly to what Phase 6 Tick needs; CRUD-style
// admin endpoints (Phase 6.3 + Phase 9) will extend.
type InstanceRepo struct {
	db *gorm.DB
}

func NewInstanceRepo(db *gorm.DB) *InstanceRepo {
	return &InstanceRepo{db: db}
}

// Create inserts a new StrategyInstance. The caller is responsible
// for allocating InstanceID (ULID) and setting initial Status (idle).
// Returns the partial-unique violation as-is — handler maps to 409/422.
func (r *InstanceRepo) Create(ctx context.Context, inst *store.StrategyInstance) error {
	if inst == nil {
		return errors.New("repository.InstanceRepo.Create: nil instance")
	}
	return r.db.WithContext(ctx).Create(inst).Error
}

// Get fetches an instance by its public InstanceID (ULID).
func (r *InstanceRepo) Get(ctx context.Context, instanceID string) (*store.StrategyInstance, error) {
	var inst store.StrategyInstance
	if err := r.db.WithContext(ctx).Where("instance_id = ?", instanceID).First(&inst).Error; err != nil {
		return nil, err
	}
	return &inst, nil
}

// ListLive returns every instance with Status='live'. Cron scanner
// iterates the result to fire Tick goroutines.
//
// No pagination — prototype assumes O(100) live instances max. Phase
// 9 scale work can add cursor/limit when it becomes meaningful.
func (r *InstanceRepo) ListLive(ctx context.Context) ([]store.StrategyInstance, error) {
	var rows []store.StrategyInstance
	if err := r.db.WithContext(ctx).
		Where("status = ?", store.InstanceStatusLive).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListByAccount returns the account's non-retired instances. delta_report
// reconciliation (Phase 8) uses it to resolve the account-level position
// snapshot back to the SaaS-side portfolio(s): 1 row → attribute the
// discrepancy to that instance; many → account-level. Retired instances are
// excluded: a retired instance is terminal (its positions were handed off),
// so summing its stale ledger into the account's expected holdings would
// fabricate drift against the real exchange snapshot and auto-freeze the
// account. idle/paused instances still hold positions and stay in scope.
func (r *InstanceRepo) ListByAccount(ctx context.Context, accountID string) ([]store.StrategyInstance, error) {
	var rows []store.StrategyInstance
	if err := r.db.WithContext(ctx).
		Where("account_id = ? AND status <> ?", accountID, store.InstanceStatusRetired).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// UpdateStatus transitions an instance to a new Status. The state
// machine (§4.2 transition graph) is enforced by the caller — this
// repo is a thin writer.
func (r *InstanceRepo) UpdateStatus(ctx context.Context, instanceID string, status store.InstanceStatus) error {
	return r.db.WithContext(ctx).
		Model(&store.StrategyInstance{}).
		Where("instance_id = ?", instanceID).
		Update("status", status).Error
}

// SetLastTickWallTime stamps the wall-clock time of the last Tick
// invocation. Updated AFTER the Tick body completes (success or
// failure) so ops dashboards can spot stale instances.
func (r *InstanceRepo) SetLastTickWallTime(ctx context.Context, instanceID string, t time.Time) error {
	return r.db.WithContext(ctx).
		Model(&store.StrategyInstance{}).
		Where("instance_id = ?", instanceID).
		Update("last_tick_wall_time", t).Error
}

// MarkFunded stamps the genesis funding time, but only while it is still
// NULL (idempotent claim) so a fresh exchange snapshot anchors the ledger
// exactly once even if two delta_reports race. See agentmsg.fundInstance.
func (r *InstanceRepo) MarkFunded(ctx context.Context, instanceID string, ms int64) error {
	return r.db.WithContext(ctx).
		Model(&store.StrategyInstance{}).
		Where("instance_id = ? AND funded_at_ms IS NULL", instanceID).
		Update("funded_at_ms", ms).Error
}

// SetActiveChampion attaches a Champion (ChallengerID) to an instance.
// Used by the Promote → Deploy split (B2 frozen): Promote alone does
// not touch instances; an explicit deploy call comes through here.
func (r *InstanceRepo) SetActiveChampion(ctx context.Context, instanceID string, challengerID string) error {
	return r.db.WithContext(ctx).
		Model(&store.StrategyInstance{}).
		Where("instance_id = ?", instanceID).
		Update("active_champ_id", challengerID).Error
}
