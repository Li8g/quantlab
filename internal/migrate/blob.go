// Package migrate runs one-shot rewrites on gene_records.full_package_json
// to reconcile stored ChallengerResultPackage blobs with the canonical
// gene_records columns.
//
// Rationale: the blob is the audit-time replay payload, but several
// fields (PromoteLayer reviewer info, DSR after SharpeBank crosses
// threshold, OOS post-Phase-5D) are written or recomputed *after* the
// blob is first serialized. Code that mutates state going forward
// re-marshals the blob inline; this package handles the historical
// catch-up.
//
// Each migration plugs a Filter (GORM WHERE narrowing) and a Transform
// (pure GeneRecord → []byte) into the shared harness. The harness owns
// the transaction, the dry-run gate, the slog audit trail, and the
// idempotency contract (Transform returns nil to skip).
package migrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"quantlab/internal/saas/store"
)

// BlobTransform converts a GeneRecord into a new full_package_json
// payload. Returning (nil, nil) skips the row — the contract for
// "this row is already in sync, leave it alone." Errors halt the
// whole migration so a single corrupt row doesn't silently bypass
// the harness's audit invariant.
type BlobTransform func(rec store.GeneRecord) ([]byte, error)

// BlobMigrationOptions wires one migration's identity and behaviour.
// Filter narrows the scan; if nil, every gene_records row is scanned
// (rare — most migrations want a narrowing predicate). Transform is
// required.
type BlobMigrationOptions struct {
	Name      string
	Filter    func(*gorm.DB) *gorm.DB
	Transform BlobTransform
	DryRun    bool
}

// BlobMigrationResult counts what the run touched. Scanned = rows
// matched by Filter; Touched = rows whose blob the harness rewrote
// (or would have rewritten under DryRun); Skipped = rows where
// Transform returned nil (already in sync).
type BlobMigrationResult struct {
	Scanned int
	Touched int
	Skipped int
}

// ErrMissingTransform signals a misconfigured migration. Returned
// before any DB I/O so callers can fail at flag-parse time.
var ErrMissingTransform = errors.New("migrate: Transform is required")

// RunBlobMigration scans gene_records under Filter, applies Transform
// to each row, and rewrites full_package_json for the ones that
// changed. All writes run in one transaction — a single transform
// error rolls back the whole batch.
//
// Emits a slog.Info line per touched row with migration name +
// challenger_id + dry_run flag, so an operator can grep the audit
// trail post-run.
func RunBlobMigration(
	ctx context.Context,
	db *gorm.DB,
	opts BlobMigrationOptions,
) (BlobMigrationResult, error) {
	if opts.Transform == nil {
		return BlobMigrationResult{}, ErrMissingTransform
	}
	if opts.Name == "" {
		opts.Name = "unnamed"
	}
	if opts.Filter == nil {
		opts.Filter = func(q *gorm.DB) *gorm.DB { return q }
	}

	var result BlobMigrationResult
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []store.GeneRecord
		if err := opts.Filter(tx.Model(&store.GeneRecord{})).Find(&rows).Error; err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		result.Scanned = len(rows)
		for _, rec := range rows {
			newBlob, err := opts.Transform(rec)
			if err != nil {
				return fmt.Errorf("transform challenger_id=%s: %w", rec.ChallengerID, err)
			}
			if newBlob == nil {
				result.Skipped++
				continue
			}
			slog.Info("blob_migration_touch",
				"migration", opts.Name,
				"challenger_id", rec.ChallengerID,
				"dry_run", opts.DryRun,
			)
			if opts.DryRun {
				result.Touched++
				continue
			}
			if err := tx.Model(&store.GeneRecord{}).
				Where("challenger_id = ?", rec.ChallengerID).
				Update("full_package_json", newBlob).Error; err != nil {
				return fmt.Errorf("update challenger_id=%s: %w", rec.ChallengerID, err)
			}
			result.Touched++
		}
		return nil
	})
	return result, err
}
