// instance.go — InstanceRepo persists StrategyInstance rows and
// answers the queries the Phase 6 Cron Tick needs. Per the design
// doc (docs/saas-tier2-schema-v1.md §4.2), InstanceID is a ULID
// allocated by the caller (HTTP create-instance handler), not by
// the repo.
package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"quantlab/internal/api"
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

// Create inserts a new StrategyInstance. The caller is responsible for
// allocating InstanceID (ULID) and setting initial Status (idle).
// Returns api.ErrAccountActiveInstanceExists when the per-account partial
// unique (uq_inst_one_per_account) fires — handler maps to 409 Conflict.
// Other unique violations are returned as-is for the handler to classify.
func (r *InstanceRepo) Create(ctx context.Context, inst *store.StrategyInstance) error {
	if inst == nil {
		return errors.New("repository.InstanceRepo.Create: nil instance")
	}
	if err := r.db.WithContext(ctx).Create(inst).Error; err != nil {
		if strings.Contains(err.Error(), "uq_inst_one_per_account") {
			return api.ErrAccountActiveInstanceExists
		}
		return err
	}
	return nil
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

// UpdateStatus transitions an instance from an expected current Status to a
// new Status. The handler owns the state machine; the DB predicate owns the
// compare-and-set so stale reads cannot overwrite a concurrent terminal write.
func (r *InstanceRepo) UpdateStatus(ctx context.Context, instanceID string, from, to store.InstanceStatus) error {
	res := r.db.WithContext(ctx).
		Model(&store.StrategyInstance{}).
		Where("instance_id = ? AND status = ?", instanceID, from).
		Update("status", to)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return r.classifyInstanceNoRows(ctx, instanceID, api.ErrInstanceTransitionRefused)
	}
	return nil
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

// MarkFunded claims the genesis funding slot via a NULL guard.
// Returns (true, nil) when this call wrote the stamp (the caller won the
// race and must write the seed portfolio). Returns (false, nil) when
// funded_at_ms was already set (another goroutine already claimed it — skip
// the seed write). See agentmsg.fundInstance for the claim-first protocol.
func (r *InstanceRepo) MarkFunded(ctx context.Context, instanceID string, ms int64) (bool, error) {
	res := r.db.WithContext(ctx).
		Model(&store.StrategyInstance{}).
		Where("instance_id = ? AND funded_at_ms IS NULL", instanceID).
		Update("funded_at_ms", ms)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// SetActiveChampion attaches the active, unretired Champion (ChallengerID) to
// a non-retired instance. The correlated EXISTS ties the requested challenger
// to the instance's (strategy_id, pair) in the same write predicate, so deploy
// cannot race on a handler-side read or attach a wrong/retired champion.
func (r *InstanceRepo) SetActiveChampion(ctx context.Context, instanceID string, challengerID string) error {
	res := r.db.WithContext(ctx).
		Model(&store.StrategyInstance{}).
		Where("instance_id = ? AND status <> ?", instanceID, store.InstanceStatusRetired).
		Where(`EXISTS (
			SELECT 1
			FROM champion_histories
			WHERE champion_histories.challenger_id = ?
				AND champion_histories.strategy_id = strategy_instances.strategy_id
				AND champion_histories.pair = strategy_instances.pair
				AND champion_histories.retired_at IS NULL
				AND champion_histories.deleted_at IS NULL
		)`, challengerID).
		Update("active_champ_id", challengerID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return r.classifyInstanceNoRows(ctx, instanceID, api.ErrDeployChampionRefused)
	}
	return nil
}

// RetireInstance transitions an instance to the terminal "retired" status from
// any non-retired state (idle / paused / live). Returns
// api.ErrInstanceAlreadyRetired when already retired, gorm.ErrRecordNotFound
// when the instance does not exist.
func (r *InstanceRepo) RetireInstance(ctx context.Context, instanceID string) error {
	res := r.db.WithContext(ctx).
		Model(&store.StrategyInstance{}).
		Where("instance_id = ? AND status <> ?", instanceID, store.InstanceStatusRetired).
		Update("status", store.InstanceStatusRetired)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return r.classifyInstanceNoRows(ctx, instanceID, api.ErrInstanceAlreadyRetired)
	}
	return nil
}

// BlockingInstanceForChampion returns the instance_id of the first non-retired
// instance whose active_champ_id = championID, or "" when none exists. Used by
// RetireChampion to enforce the deployed-champion safety gate.
func (r *InstanceRepo) BlockingInstanceForChampion(ctx context.Context, championID string) (string, error) {
	var inst store.StrategyInstance
	err := r.db.WithContext(ctx).
		Select("instance_id").
		Where("active_champ_id = ? AND status <> ?", championID, store.InstanceStatusRetired).
		First(&inst).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return inst.InstanceID, nil
}

func (r *InstanceRepo) classifyInstanceNoRows(ctx context.Context, instanceID string, transitionErr error) error {
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&store.StrategyInstance{}).
		Where("instance_id = ?", instanceID).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return transitionErr
}
