package migrate

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// NewPromoteBlobMigration returns options that backfill
// full_package_json.promote.* from the canonical gene_records columns
// (DecisionStatus, ReviewedAtTS, ReviewedBy, DecisionNote). It is the
// one-shot complement of the Promote-time re-marshal added in commit
// 17d87c7 — rows promoted before that commit have stale "pending" in
// the blob's PromoteLayer despite the column saying "promoted".
//
// Idempotent: a second run scans the same Filter set but Transform
// returns nil (already in sync) for every row.
func NewPromoteBlobMigration(dryRun bool) BlobMigrationOptions {
	return BlobMigrationOptions{
		Name:   "promote_blob",
		DryRun: dryRun,
		Filter: func(q *gorm.DB) *gorm.DB {
			return q.Where("decision_status <> ?", resultpkg.DecisionStatusPending)
		},
		Transform: transformPromoteBlob,
	}
}

// transformPromoteBlob rewrites the PromoteLayer to mirror the
// gene_records columns. Returns nil when the blob is already
// consistent — the harness uses that as the "skip" signal.
func transformPromoteBlob(rec store.GeneRecord) ([]byte, error) {
	if len(rec.FullPackageJSON) == 0 {
		return nil, nil
	}
	var pkg resultpkg.ChallengerResultPackage
	if err := json.Unmarshal(rec.FullPackageJSON, &pkg); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if pkg.Promote.DecisionStatus == rec.DecisionStatus {
		return nil, nil
	}
	pkg.Promote.DecisionStatus = rec.DecisionStatus
	pkg.Promote.ReviewedAtTS = rec.ReviewedAtTS
	pkg.Promote.ReviewedBy = rec.ReviewedBy
	pkg.Promote.DecisionNote = rec.DecisionNote
	return json.Marshal(&pkg)
}
