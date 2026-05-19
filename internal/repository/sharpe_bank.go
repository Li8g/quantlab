// SharpeBank persistence for the DSR pipeline. Source-of-truth:
// docs/Coding-plan-dev-phases-prompts_v3_2_2.md Phase 5.5 + Phase 5B
// step 5 + §I-4.2 ("SharpeBank 累积样本数").
//
// One row per (strategy_id, pair_id, challenger_id) tuple. The Phase
// 5D Epoch service calls Add after each promoted-best Challenger,
// then immediately calls Stats to decide whether DSR can be computed
// (N ≥ MinTrialsForDSR per §I-4.2).
package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
	"quantlab/internal/verification"
)

// SharpeBankEntry is the input side of SharpeBankRepo.Add. The
// challengerID disambiguates rows under the (strategy, pair) index;
// SpawnMode + FingerprintDistanceToParent are audit fields the §I-4.2
// formula doesn't use but the store.SharpeBank GORM model carries.
type SharpeBankEntry struct {
	ChallengerID                string
	SpawnMode                   resultpkg.SpawnMode
	Stats                       verification.SharpeStats
	FingerprintDistanceToParent *float64
}

// SharpeBankStats summarises an accumulated SharpeBank slice for a
// (strategy_id, pair_id) key. N is the row count; SharpeVariance is
// the population (biased, n-divisor) variance of ObservedSharpe.
// Together they feed ComputeDSR.
//
// Note: Variance uses the biased estimator to match the source code
// in §I-4.2 Eq. 14 of the Bailey-Prado paper, which treats Var(SR)
// as a population variance over the cumulative trial pool.
type SharpeBankStats struct {
	N              int
	SharpeMean     float64
	SharpeVariance float64
}

// SharpeBankRepo persists SharpeBankEntry rows and computes
// aggregated stats. Wraps the gorm.DB the same way ChallengerRepo
// does; both repos can share one DB pointer per request.
type SharpeBankRepo struct {
	db *gorm.DB
}

// NewSharpeBankRepo wraps a *gorm.DB. AutoMigrate (in store.NewDB)
// is expected to have already created the sharpe_banks table.
func NewSharpeBankRepo(db *gorm.DB) *SharpeBankRepo {
	return &SharpeBankRepo{db: db}
}

// Add inserts one SharpeBank row. Returns the underlying gorm error
// verbatim so callers can detect duplicate-key violations on
// challenger_id (the column carries a non-unique index in v1; we
// rely on the upstream caller to dedupe).
func (r *SharpeBankRepo) Add(ctx context.Context, strategyID, pairID string, entry SharpeBankEntry) error {
	if strategyID == "" || pairID == "" {
		return errors.New("repository.SharpeBankRepo.Add: empty strategyID or pairID")
	}
	if entry.ChallengerID == "" {
		return errors.New("repository.SharpeBankRepo.Add: empty ChallengerID")
	}
	row := store.SharpeBank{
		StrategyID:                  strategyID,
		PairID:                      pairID,
		ChallengerID:                entry.ChallengerID,
		ObservedSharpe:              entry.Stats.ObservedSharpe,
		BacktestHorizonT:            entry.Stats.HorizonT,
		Skew:                        entry.Stats.Skew,
		Kurtosis:                    entry.Stats.ExcessKurt,
		SpawnMode:                   entry.SpawnMode,
		FingerprintDistanceToParent: entry.FingerprintDistanceToParent,
	}
	return r.db.WithContext(ctx).Create(&row).Error
}

// Stats aggregates all rows under the (strategyID, pairID) key:
// returns the count and the population variance of ObservedSharpe.
// Callers feed those directly into ComputeDSR alongside the new
// challenger's stats. N may be < MinTrialsForDSR; in that case the
// caller skips DSR computation and leaves DSRSummary empty.
func (r *SharpeBankRepo) Stats(ctx context.Context, strategyID, pairID string) (SharpeBankStats, error) {
	if strategyID == "" || pairID == "" {
		return SharpeBankStats{}, errors.New("repository.SharpeBankRepo.Stats: empty strategyID or pairID")
	}

	var sharpes []float64
	if err := r.db.WithContext(ctx).
		Model(&store.SharpeBank{}).
		Where("strategy_id = ? AND pair_id = ?", strategyID, pairID).
		Pluck("observed_sharpe", &sharpes).Error; err != nil {
		return SharpeBankStats{}, fmt.Errorf("pluck observed_sharpe: %w", err)
	}

	return computeBankStats(sharpes), nil
}

// computeBankStats is the pure population-variance reducer used by
// Stats. Extracted so the test suite can pin its arithmetic without
// a DB.
func computeBankStats(sharpes []float64) SharpeBankStats {
	n := len(sharpes)
	if n == 0 {
		return SharpeBankStats{}
	}
	var sum float64
	for _, s := range sharpes {
		sum += s
	}
	mean := sum / float64(n)
	var ssq float64
	for _, s := range sharpes {
		d := s - mean
		ssq += d * d
	}
	variance := ssq / float64(n) // population (biased) variance
	return SharpeBankStats{
		N:              n,
		SharpeMean:     mean,
		SharpeVariance: variance,
	}
}
