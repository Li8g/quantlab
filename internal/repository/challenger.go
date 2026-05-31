// Package repository persists evolution outputs to Postgres. Phase 5B
// scope: ChallengerRepo writes one ChallengerResultPackage per
// best-of-Epoch winner into the gene_records table.
//
// Boundary: this package is the only part of the engine ↔ DB bridge.
// Engine code returns resultpkg.ChallengerResultPackage values;
// repository turns those into store.GeneRecord rows. The strategy
// layer never reaches into either side.
//
// Decomposition strategy: the full package is stored verbatim in
// GeneRecord.FullPackageJSON (jsonb) as the source-of-truth for
// replay. Hot fields (score, plan_hash, decision_status, friction,
// etc.) are additionally lifted to top-level columns so the UI can
// filter / sort without parsing the blob. The pure-function
// buildGeneRecord captures that decomposition so unit tests don't
// need a database.
package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// ChallengerRepo persists ChallengerResultPackage instances to the
// gene_records table.
type ChallengerRepo struct {
	db *gorm.DB
}

// NewChallengerRepo wraps a *gorm.DB. The DB must already have the
// gene_records table — provisioning is store.NewDB's responsibility
// via AutoMigrate.
func NewChallengerRepo(db *gorm.DB) *ChallengerRepo {
	return &ChallengerRepo{db: db}
}

// Save inserts a new GeneRecord row built from pkg. challengerID
// becomes the row's ChallengerID and must be unique (the column
// carries a UNIQUE index per store/models.go). Returns the underlying
// gorm error verbatim so callers can detect duplicate-key races.
func (r *ChallengerRepo) Save(ctx context.Context, challengerID string, pkg resultpkg.ChallengerResultPackage) error {
	record, err := buildGeneRecord(challengerID, pkg)
	if err != nil {
		return fmt.Errorf("repository: build GeneRecord: %w", err)
	}
	return r.db.WithContext(ctx).Create(&record).Error
}

// Get fetches the gene_records row by challenger_id. Returns
// gorm.ErrRecordNotFound when no matching row exists.
func (r *ChallengerRepo) Get(ctx context.Context, challengerID string) (*store.GeneRecord, error) {
	if challengerID == "" {
		return nil, fmt.Errorf("repository.ChallengerRepo.Get: empty challengerID")
	}
	var rec store.GeneRecord
	if err := r.db.WithContext(ctx).Where("challenger_id = ?", challengerID).First(&rec).Error; err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetPackageBlob returns just the FullPackageJSON column for the given
// challenger_id. Cheaper than Get when the caller only wants to stream
// the raw blob back to the HTTP client (the package endpoint). Returns
// gorm.ErrRecordNotFound when the row is absent.
func (r *ChallengerRepo) GetPackageBlob(ctx context.Context, challengerID string) ([]byte, error) {
	if challengerID == "" {
		return nil, fmt.Errorf("repository.ChallengerRepo.GetPackageBlob: empty challengerID")
	}
	var rec store.GeneRecord
	if err := r.db.WithContext(ctx).
		Select("full_package_json").
		Where("challenger_id = ?", challengerID).
		First(&rec).Error; err != nil {
		return nil, err
	}
	return rec.FullPackageJSON, nil
}

// buildGeneRecord is the pure-function decomposition of a
// ChallengerResultPackage into a GeneRecord ready for INSERT. Returns
// an error if the package fails JSON encoding (only possible on
// programmer-error inputs like cyclic structs that resultpkg
// definitions can never produce — kept as an error return for
// defensive symmetry).
//
// Field-by-field rationale:
//   - Identity (ChallengerID, StrategyID, Pair) comes from the caller
//   - pkg.Core.
//   - Score fields lift from pkg.Evaluation.ScoreTotal; Fatal scores
//     leave ScoreTotal.Value nil and the corresponding *float64
//     columns nil too.
//   - WindowScoresJSON marshals the per-window CrucibleResult slice.
//     Window alpha-monthly/weekly remain nil until phase 5.5 lights
//     up alpha breakdowns.
//   - OosAlphaMonthly/Weekly lift from pkg.Verification.OOSResult
//     (currently zero-valued; populated in 5D when OOS runs).
//   - DSR fields lift from VerificationLayer.DSRSummary (which is
//     json.RawMessage today; the prototype leaves them nil).
//   - Reproducibility, version, friction, and decision-status fields
//     mirror the package verbatim — these are the columns the UI
//     queries for filtering/sort.
//   - FullPackageJSON is the canonical replay payload.
func buildGeneRecord(challengerID string, pkg resultpkg.ChallengerResultPackage) (store.GeneRecord, error) {
	blob, err := json.Marshal(pkg)
	if err != nil {
		return store.GeneRecord{}, fmt.Errorf("marshal package: %w", err)
	}

	windowScoresJSON, err := json.Marshal(pkg.Evaluation.WindowScores)
	if err != nil {
		return store.GeneRecord{}, fmt.Errorf("marshal window scores: %w", err)
	}

	repro := pkg.Core.ReproducibilityMetadata
	ga := pkg.Core.GAConfig
	st := pkg.Evaluation.ScoreTotal
	oos := pkg.Verification.OOSResult

	record := store.GeneRecord{
		ChallengerID: challengerID,
		StrategyID:   pkg.Core.StrategyID,
		Pair:         ga.Pair,

		ScoreTotal:         st.Value,
		ScoreRaw:           st.ScoreRaw,
		ConsistencyPenalty: st.ConsistencyPenalty,

		WindowScoresJSON: windowScoresJSON,
		// WindowAlphaMonthlyJSON / WindowAlphaWeeklyJSON: denormalized
		// from pkg.Evaluation.AlphaBreakdown after this literal (A3).

		OosAlphaMonthly: oos.OOSAlphaMonthly,
		OosAlphaWeekly:  oos.OOSAlphaWeekly,
		// DSR / DSRTrialsN / DSRTrialsVar: VerificationLayer.DSRSummary
		// is json.RawMessage in v1; structured-DSR fields go in
		// Phase 5B-sharpe.

		EpochSeed:       repro.EpochSeed,
		DataVersion:     repro.DataVersion,
		EngineVersion:   repro.EngineVersion,
		StrategyVersion: repro.StrategyVersion,

		SchemaVersion:      repro.SchemaVersion,
		FitnessVersion:     repro.FitnessVersion,
		FingerprintVersion: repro.FingerprintVersion,

		HardwareSignature: repro.HardwareSignature,
		GoVersion:         repro.GoVersion,
		BuildID:           repro.BuildID,

		PlanHash: repro.PlanHash,
		BarsHash: repro.BarsHash,

		TakerFeeBPS: ga.TakerFeeBPS,
		SlippageBPS: ga.SlippageBPS,
		TestMode:    ga.TestMode,

		DecisionStatus: pkg.Promote.DecisionStatus,
		DecisionNote:   pkg.Promote.DecisionNote,
		ReviewedAtTS:   pkg.Promote.ReviewedAtTS,
		ReviewedBy:     pkg.Promote.ReviewedBy,

		FullPackageJSON: blob,
	}

	// Denormalize the IS alpha breakdown (A3) into the two queryable
	// columns: one compact [{window, alpha_ann}] array per DCA baseline.
	// Best-effort and diagnostic — a missing/malformed breakdown just
	// leaves both columns nil (older challengers predate A3).
	if len(pkg.Evaluation.AlphaBreakdown) > 0 {
		var ab resultpkg.ISAlphaBreakdown
		if err := json.Unmarshal(pkg.Evaluation.AlphaBreakdown, &ab); err == nil && len(ab.Windows) > 0 {
			type windowAlphaCol struct {
				Window string  `json:"window"`
				Alpha  float64 `json:"alpha_ann"`
			}
			monthly := make([]windowAlphaCol, len(ab.Windows))
			weekly := make([]windowAlphaCol, len(ab.Windows))
			for i, w := range ab.Windows {
				monthly[i] = windowAlphaCol{Window: string(w.Window), Alpha: w.AlphaMonthlyAnn}
				weekly[i] = windowAlphaCol{Window: string(w.Window), Alpha: w.AlphaWeeklyAnn}
			}
			if b, err := json.Marshal(monthly); err == nil {
				record.WindowAlphaMonthlyJSON = b
			}
			if b, err := json.Marshal(weekly); err == nil {
				record.WindowAlphaWeeklyJSON = b
			}
		}
	}

	return record, nil
}
