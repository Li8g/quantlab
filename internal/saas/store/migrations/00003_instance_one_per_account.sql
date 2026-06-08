-- 00003_instance_one_per_account.sql — Enforce one non-retired instance
-- per exchange account.
--
-- v1 assumption: whole-balance anchor, one instance per exchange account.
-- Two non-retired instances on the same (owner_user_id, account_id) double-
-- count the account's expected holdings on the next delta_report, guaranteed
-- to breach the auto-freeze threshold. This partial unique index makes that
-- configuration structurally impossible at the DB level.
--
-- Existing idx_inst_unique_active (owner_user_id, strategy_id, pair, account_id)
-- is now strictly subsumed by this constraint and can never fire independently,
-- but is kept for schema continuity (no DROP in this migration).

-- +goose Up

CREATE UNIQUE INDEX IF NOT EXISTS uq_inst_one_per_account
    ON strategy_instances (owner_user_id, account_id)
    WHERE status != 'retired';

-- +goose Down

DROP INDEX IF EXISTS uq_inst_one_per_account;
