-- preflight_champion_dup_check.sql
--
-- Run BEFORE deploying the build that creates the `uq_champion_active`
-- partial unique index (internal/saas/store/db.go). That index enforces the
-- core invariant "at most one active champion per (strategy_id, pair)".
-- Postgres aborts CREATE UNIQUE INDEX if the table already holds duplicates,
-- so this script lets an operator find and clean them up first.
--
-- "Active" mirrors the index predicate and the application-level query:
-- retired_at IS NULL (not yet retired) AND deleted_at IS NULL (not GORM
-- soft-deleted).
--
-- Usage:
--   psql "$DATABASE_URL" -f scripts/preflight_champion_dup_check.sql
--
-- Expected: zero rows. Any row is a (strategy_id, pair) that must be
-- reconciled (retire the stale champion) before the index can be built.

SELECT
    strategy_id,
    pair,
    count(*)                 AS active_count,
    array_agg(challenger_id) AS challenger_ids,
    array_agg(id)            AS row_ids
FROM champion_histories
WHERE retired_at IS NULL
  AND deleted_at IS NULL
GROUP BY strategy_id, pair
HAVING count(*) > 1
ORDER BY active_count DESC, strategy_id, pair;
