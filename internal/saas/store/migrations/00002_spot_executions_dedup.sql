-- 00002_spot_executions_dedup.sql — DB-level backstop for fill dedup.
--
-- insertFillIfNew uses check-then-insert (non-atomic). Two concurrent
-- deliveries of the same fill on order_update and delta_report can both pass
-- the EXISTS check and both attempt INSERT. These partial unique indexes are
-- the backstop: the second INSERT hits a unique violation, which
-- InsertSpotExecution translates into a no-op so no duplicate row is persisted.
--
-- Two keys because of the trade_id semantics:
--   trade_id <> 0: Binance per-trade id is the canonical key; multiple
--     same-ms fills of a single market sweep are distinguishable by trade_id.
--   trade_id = 0: MockExchange and legacy rows have no per-trade id, so the
--     ms-based key is used as a fallback.

-- +goose Up

CREATE UNIQUE INDEX IF NOT EXISTS uq_spot_exec_by_trade
    ON spot_executions (client_order_id, trade_id)
    WHERE trade_id <> 0;

CREATE UNIQUE INDEX IF NOT EXISTS uq_spot_exec_by_ms
    ON spot_executions (client_order_id, filled_at_exchange_ms)
    WHERE trade_id = 0;

-- +goose Down

DROP INDEX IF EXISTS uq_spot_exec_by_trade;
DROP INDEX IF EXISTS uq_spot_exec_by_ms;
