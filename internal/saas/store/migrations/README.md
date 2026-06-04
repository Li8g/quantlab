# Schema migrations (goose)

Versioned SQL migrations for the **production** schema (`app_role=saas`). Applied
by `store.RunMigrations` (embedded via `//go:embed`, see `../migrate.go`); for
dev/lab `store.NewDB` still uses GORM `AutoMigrate`. Decision record:
`docs/saas-schema-migration-draft.md`.

## The two source-of-truth rule

- **DDL authority** = these migration files.
- **ORM-mapping authority** = the GORM structs in `../models.go`.
- They are pinned together by `../migrate_drift_test.go` (`//go:build integration`):
  it builds one DB via AutoMigrate and one via these migrations and asserts a
  byte-identical `pg_dump --schema-only`. **A struct change with no matching
  migration (or vice-versa) fails that test.** Run it before merging any schema
  change:

  ```
  go test -tags=integration ./internal/saas/store/ \
      -run TestMigrationsMatchAutoMigrate -args -config=/abs/config.yaml
  ```

## `00001_baseline.sql`

The frozen baseline: the complete 20-table schema as AutoMigrate produced it at
the time of cut-over, generated from `pg_dump --schema-only` + three edits
(preamble/owner noise stripped, the two `create_hypertable`-generated default
indexes removed, two `create_hypertable` calls injected). Its fidelity is proven
by the drift test, not by Squawk — so it is **excluded** from Squawk
(`excluded_paths` in `/.squawk.toml`).

> **Why there is no `00002` for the ledger columns.** The draft sketched a
> `00002_add_ledger_columns.sql` for `strategy_instances.funded_at_ms`,
> `portfolio_states.last_applied_exec_id`, and `spot_executions.trade_id`. Those
> three columns were already in dev's AutoMigrate output when the baseline was
> captured, so they are **already in `00001`** — there is no delta to migrate.
> The next real schema change becomes `00002`.

## Adding a migration (`00002_*.sql` onward)

1. Make the GORM struct change in `../models.go` (+ `AllModels()` if a new table).
2. Add `NNNNN_short_name.sql` here with `-- +goose Up` / `-- +goose Down`
   sections. Files apply in lexical order; keep the zero-padded numeric prefix.
3. Run the drift test (above). Iterate until the diff is empty.
4. Squawk lints it automatically (CI + locally:
   `squawk -c .squawk.toml internal/saas/store/migrations/NNNNN_*.sql`).

### Review checklist

- [ ] Drift test passes (`TestMigrationsMatchAutoMigrate`).
- [ ] `ADD COLUMN ... NOT NULL` carries a `DEFAULT` (or is split expand/contract);
      a bare `NOT NULL` add rewrites/locks the table.
- [ ] New columns match the struct's nullability. GORM maps a non-pointer scalar
      with **no** `not null` tag to a **nullable** column with no default (this is
      why the ledger columns are `bigint` nullable, not `NOT NULL DEFAULT 0`).
- [ ] `DROP` / rename of a live column goes through expand–contract across two
      releases, never a single destructive migration. A `DROP` needs a ticket.
- [ ] New indexes on populated tables use `CREATE INDEX CONCURRENTLY`
      — which means the migration must declare `-- +goose NO TRANSACTION`
      (CONCURRENTLY cannot run inside the txn goose wraps by default).
- [ ] `lock_timeout` is handled by `RunMigrations` (`SET lock_timeout='3s'`); a
      legitimately long migration sets its own `SET LOCAL statement_timeout`
      inside the file — there is no tight global `statement_timeout`.

### Squawk blind spots (human review must cover)

Squawk reasons about generic Postgres DDL; it does **not** understand this repo's
TimescaleDB / partial-index specifics:

- **Hypertable chunk broadcast.** `ALTER TABLE` on `klines` / `portfolio_states`
  fans out to every chunk; Squawk sees one plain table. Weigh chunk count.
- **`create_hypertable` semantics, chunk intervals, compression** — invisible to
  Squawk and to `pg_dump --schema-only` (so also invisible to the drift test;
  verify via `timescaledb_information.dimensions` when a hypertable changes).
- **Partial unique indexes** (`WHERE status != 'retired'`, etc.) — match the
  source spelling to the form already in the schema; Postgres canonicalizes
  `IN (...)` and `ANY(ARRAY[...])` differently, which the drift test will catch.
- **Expand–contract / rename** sequencing across releases — a process concern,
  not a single-file lint.
