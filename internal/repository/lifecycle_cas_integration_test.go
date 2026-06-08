//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"quantlab/internal/api"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

func openLifecycleCASDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg, err := config.Load(*configPath)
	if err != nil {
		t.Fatalf("load config %s: %v", *configPath, err)
	}
	db, err := store.NewDB(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestChampionRepo_RetireCASRejectsStaleHistory(t *testing.T) {
	db := openLifecycleCASDB(t)
	ctx := context.Background()
	repo := NewChampionRepo(db)

	challengerID := "cas-retire-" + store.NewULID()
	cleanup := func() {
		_ = db.Unscoped().
			Where("challenger_id = ?", challengerID).
			Delete(&store.ChampionHistory{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := db.Create(&store.ChampionHistory{
		StrategyID:   "sigmoid_v1",
		Pair:         "CAS" + store.NewULID(),
		ChallengerID: challengerID,
		PromotedAt:   time.Now().UTC().Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("create champion_history: %v", err)
	}

	var stale store.ChampionHistory
	if err := db.Where("challenger_id = ?", challengerID).First(&stale).Error; err != nil {
		t.Fatalf("read stale champion_history: %v", err)
	}

	firstNote := "first retire wins"
	if err := repo.Retire(ctx, challengerID, api.RetireChampionRequest{
		ReviewedBy:   "first-reviewer",
		DecisionNote: &firstNote,
	}); err != nil {
		t.Fatalf("first Retire: %v", err)
	}

	secondNote := "stale retire must not rewrite"
	err := retireHistory(db.WithContext(ctx), stale, api.RetireChampionRequest{
		ReviewedBy:   "second-reviewer",
		DecisionNote: &secondNote,
	}, time.Now().UTC())
	if !errors.Is(err, api.ErrAlreadyRetired) {
		t.Fatalf("stale retire err = %v, want ErrAlreadyRetired", err)
	}

	var got store.ChampionHistory
	if err := db.Where("challenger_id = ?", challengerID).First(&got).Error; err != nil {
		t.Fatalf("read retired champion_history: %v", err)
	}
	if got.RetiredBy == nil || *got.RetiredBy != "first-reviewer" {
		t.Fatalf("RetiredBy = %v, want first-reviewer", got.RetiredBy)
	}
	if got.RetireNote == nil || *got.RetireNote != firstNote {
		t.Fatalf("RetireNote = %v, want first note", got.RetireNote)
	}
}

func TestInstanceRepo_UpdateStatusCASRejectsStaleState(t *testing.T) {
	db := openLifecycleCASDB(t)
	ctx := context.Background()
	repo := NewInstanceRepo(db)

	instanceID := store.NewULID()
	cleanup := func() {
		_ = db.Where("instance_id = ?", instanceID).Delete(&store.StrategyInstance{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := repo.Create(ctx, &store.StrategyInstance{
		InstanceID:  instanceID,
		StrategyID:  "sigmoid_v1",
		Pair:        "CAS" + store.NewULID(),
		AccountID:   "cas-account",
		OwnerUserID: 1,
		Status:      store.InstanceStatusIdle,
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}
	if err := db.Model(&store.StrategyInstance{}).
		Where("instance_id = ?", instanceID).
		Update("status", store.InstanceStatusRetired).Error; err != nil {
		t.Fatalf("simulate concurrent retire: %v", err)
	}

	err := repo.UpdateStatus(ctx, instanceID, store.InstanceStatusIdle, store.InstanceStatusLive)
	if !errors.Is(err, api.ErrInstanceTransitionRefused) {
		t.Fatalf("UpdateStatus err = %v, want ErrInstanceTransitionRefused", err)
	}
	got, err := repo.Get(ctx, instanceID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != store.InstanceStatusRetired {
		t.Fatalf("Status = %q, want retired", got.Status)
	}
}

func TestInstanceRepo_SetActiveChampionRequiresMatchingActiveChampionAndLiveInstance(t *testing.T) {
	db := openLifecycleCASDB(t)
	ctx := context.Background()
	repo := NewInstanceRepo(db)

	pairA := "CAS" + store.NewULID()
	pairB := "CAS" + store.NewULID()
	pairC := "CAS" + store.NewULID()
	instanceID := store.NewULID()
	retiredInstanceID := store.NewULID()
	activeChallenger := "cas-active-" + store.NewULID()
	wrongPairChallenger := "cas-wrong-" + store.NewULID()
	retiredChallenger := "cas-retired-" + store.NewULID()
	retiredInstanceChallenger := "cas-inst-" + store.NewULID()
	challengers := []string{
		activeChallenger,
		wrongPairChallenger,
		retiredChallenger,
		retiredInstanceChallenger,
	}
	instances := []string{instanceID, retiredInstanceID}

	cleanup := func() {
		_ = db.Unscoped().
			Where("challenger_id IN ?", challengers).
			Delete(&store.ChampionHistory{}).Error
		_ = db.Where("instance_id IN ?", instances).
			Delete(&store.StrategyInstance{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	if err := repo.Create(ctx, &store.StrategyInstance{
		InstanceID:  instanceID,
		StrategyID:  "sigmoid_v1",
		Pair:        pairA,
		AccountID:   "cas-account-a",
		OwnerUserID: 1,
		Status:      store.InstanceStatusIdle,
	}); err != nil {
		t.Fatalf("create live instance: %v", err)
	}
	if err := repo.Create(ctx, &store.StrategyInstance{
		InstanceID:  retiredInstanceID,
		StrategyID:  "sigmoid_v1",
		Pair:        pairC,
		AccountID:   "cas-account-b",
		OwnerUserID: 1,
		Status:      store.InstanceStatusRetired,
	}); err != nil {
		t.Fatalf("create retired instance: %v", err)
	}

	retiredAt := time.Now().UTC()
	rows := []store.ChampionHistory{
		{StrategyID: "sigmoid_v1", Pair: pairA, ChallengerID: activeChallenger, PromotedAt: time.Now().UTC()},
		{StrategyID: "sigmoid_v1", Pair: pairB, ChallengerID: wrongPairChallenger, PromotedAt: time.Now().UTC()},
		{StrategyID: "sigmoid_v1", Pair: pairA, ChallengerID: retiredChallenger, PromotedAt: time.Now().UTC(), RetiredAt: &retiredAt},
		{StrategyID: "sigmoid_v1", Pair: pairC, ChallengerID: retiredInstanceChallenger, PromotedAt: time.Now().UTC()},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("create champion rows: %v", err)
	}

	if err := repo.SetActiveChampion(ctx, instanceID, activeChallenger); err != nil {
		t.Fatalf("SetActiveChampion active/matching: %v", err)
	}
	assertDeployRefused := func(name, instID, challengerID string) {
		t.Helper()
		err := repo.SetActiveChampion(ctx, instID, challengerID)
		if !errors.Is(err, api.ErrDeployChampionRefused) {
			t.Fatalf("%s err = %v, want ErrDeployChampionRefused", name, err)
		}
	}
	assertDeployRefused("wrong pair", instanceID, wrongPairChallenger)
	assertDeployRefused("retired champion", instanceID, retiredChallenger)
	assertDeployRefused("retired instance", retiredInstanceID, retiredInstanceChallenger)

	got, err := repo.Get(ctx, instanceID)
	if err != nil {
		t.Fatalf("Get live instance: %v", err)
	}
	if got.ActiveChampID == nil || *got.ActiveChampID != activeChallenger {
		t.Fatalf("ActiveChampID = %v, want %s", got.ActiveChampID, activeChallenger)
	}
}
