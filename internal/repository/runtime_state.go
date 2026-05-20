// runtime_state.go — RuntimeRepo reads + UPSERTs the strategy-private
// state blob, one row per instance (§5.2). Opaque jsonb; engine does
// not interpret.
package repository

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"quantlab/internal/saas/store"
)

type RuntimeRepo struct {
	db *gorm.DB
}

func NewRuntimeRepo(db *gorm.DB) *RuntimeRepo {
	return &RuntimeRepo{db: db}
}

// Get returns the RuntimeState for an instance. Returns (nil, nil)
// when no row exists — caller treats as cold start (Step() receives
// empty RuntimeState, strategy initialises its own blob).
func (r *RuntimeRepo) Get(ctx context.Context, instanceID string) (*store.RuntimeState, error) {
	var rs store.RuntimeState
	err := r.db.WithContext(ctx).Where("instance_id = ?", instanceID).First(&rs).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rs, nil
}

// Upsert writes the RuntimeState for an instance, replacing any
// existing row. nowMs and stateJSON come from the Tick that just
// finished Step() — both reflect the latest Step's view of the world.
func (r *RuntimeRepo) Upsert(ctx context.Context, instanceID string, nowMs int64, stateJSON json.RawMessage) error {
	if instanceID == "" {
		return errors.New("repository.RuntimeRepo.Upsert: empty instanceID")
	}
	rec := store.RuntimeState{
		InstanceID: instanceID,
		NowMs:      nowMs,
		StateJSON:  stateJSON,
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "instance_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"now_ms", "state_json", "updated_at"}),
	}).Create(&rec).Error
}
