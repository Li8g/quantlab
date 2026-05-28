package migrate

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// oosBackfillNote is stamped into the rewritten Notes field so an
// operator (or a curious frontend dev) can tell at a glance that this
// is a pre-5D row whose blob was patched to satisfy the
// VerificationStatus enum, NOT a real verification run.
const oosBackfillNote = "backfilled: pre-Phase-5D challenger; oos_result was not produced when this row ran. " +
	"Re-run with oos_days set on a new task if you need actual alpha numbers."

// NewOOSBlobMigration returns options that backfill
// full_package_json.verification.oos_result.status on pre-Phase-5D
// rows. Those rows shipped with an empty Status string (or no
// oos_result object at all) because RunOOS / the default-NotRun stamp
// in engine.BuildChallengerPackage were not yet wired (see commit
// 945f5c2 / Phase 5D in commit 453100a).
//
// Why "not_run" and not a re-run: GAConfigSnapshot does NOT persist
// the GhostDCAConfig the GA used, so an offline re-run would have to
// guess at InitialCapital / MonthlyInject and produce alpha numbers
// that don't match the original task's economics. The minimal honest
// backfill is to stamp the enum-valid status and note the reason; a
// caller who wants real numbers should issue a fresh task.
//
// Idempotent: rows whose blob already carries a non-empty
// VerificationStatus get Transform returning nil (skip). Filter is
// blob-level (json operator) because the gene_records columns don't
// mirror oos_result.
//
// Filter uses .Unscoped() — soft-deleted rows are in scope. Decision
// 2026-05-28: migrations are about data consistency, soft-delete is a
// UX concern. Status-validity must hold regardless of deleted_at so
// that a future un-delete (forensics, history backfill) doesn't
// resurface the enum violation.
func NewOOSBlobMigration(dryRun bool) BlobMigrationOptions {
	return BlobMigrationOptions{
		Name:   "oos_blob",
		DryRun: dryRun,
		Filter: func(q *gorm.DB) *gorm.DB {
			return q.Unscoped().Where(
				"COALESCE(full_package_json::jsonb->'verification'->'oos_result'->>'status', '') = ''",
			)
		},
		Transform: transformOOSBlob,
	}
}

// transformOOSBlob rewrites Verification.OOSResult.Status from empty
// to NotRun + an explanatory Notes. Returns nil when the blob already
// carries a non-empty status (idempotency: a second run must not
// re-mark recently-stamped rows).
func transformOOSBlob(rec store.GeneRecord) ([]byte, error) {
	if len(rec.FullPackageJSON) == 0 {
		return nil, nil
	}
	var pkg resultpkg.ChallengerResultPackage
	if err := json.Unmarshal(rec.FullPackageJSON, &pkg); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	if pkg.Verification.OOSResult.Status != "" {
		return nil, nil
	}
	pkg.Verification.OOSResult.Status = resultpkg.VerificationStatusNotRun
	note := oosBackfillNote
	pkg.Verification.OOSResult.Notes = &note
	return json.Marshal(&pkg)
}
