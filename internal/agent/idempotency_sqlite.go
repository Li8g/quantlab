// idempotency_sqlite.go — SqliteStore is the production IdempotencyStore
// per docs/saas-ws-protocol-v1.md §8.3. Pure-Go driver (modernc.org/sqlite),
// no CGO, so cmd/agent builds on any platform Go targets.
//
// Schema is the table + index defined verbatim in §8.3. Reads run inside
// a process-wide sync.Mutex because modernc.org/sqlite serialises
// connections by default and the Agent is single-process anyway; the
// mutex makes ordering deterministic across goroutines that race on the
// same client_order_id.
package agent

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/shopspring/decimal"

	_ "modernc.org/sqlite"
)

// SqliteStore satisfies IdempotencyStore. Construct via NewSqliteStore.
type SqliteStore struct {
	db *sql.DB
	mu sync.Mutex
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS idempotency (
  client_order_id     TEXT PRIMARY KEY,
  exchange_order_id   TEXT,
  status              TEXT NOT NULL,
  market_ref_decimal  TEXT,
  submitted_at_ms     INTEGER NOT NULL,
  last_updated_ms     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_idem_last_updated ON idempotency(last_updated_ms);
`

// NewSqliteStore opens (or creates) the sqlite file at path and applies
// the §8.3 schema. WAL mode is enabled for crash safety + concurrent
// readers; busy_timeout=5s avoids SQLITE_BUSY on the rare contention.
func NewSqliteStore(path string) (*SqliteStore, error) {
	if path == "" {
		return nil, errors.New("agent.NewSqliteStore: empty path")
	}
	// _pragma URL params apply on every connection; _txlock=immediate
	// keeps lock acquisition fast under modernc's default serial mode.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("agent.NewSqliteStore: open %s: %w", path, err)
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agent.NewSqliteStore: schema: %w", err)
	}
	return &SqliteStore{db: db}, nil
}

func (s *SqliteStore) Close() error { return s.db.Close() }

// Get returns the row keyed by clientOrderID. Second return is false
// when the row does not exist. Errors propagate as transient I/O.
func (s *SqliteStore) Get(clientOrderID string) (IdempotencyRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(`
        SELECT client_order_id, COALESCE(exchange_order_id, ''),
               status, COALESCE(market_ref_decimal, ''),
               submitted_at_ms, last_updated_ms
        FROM idempotency WHERE client_order_id = ?`, clientOrderID)
	var rec IdempotencyRecord
	var marketRef string
	if err := row.Scan(&rec.ClientOrderID, &rec.ExchangeOrderID,
		&rec.Status, &marketRef,
		&rec.SubmittedAtMs, &rec.LastUpdatedMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IdempotencyRecord{}, false, nil
		}
		return IdempotencyRecord{}, false, fmt.Errorf("agent.SqliteStore.Get: %w", err)
	}
	if marketRef != "" {
		d, err := decimal.NewFromString(marketRef)
		if err != nil {
			return IdempotencyRecord{}, false, fmt.Errorf("agent.SqliteStore.Get: market_ref %q: %w", marketRef, err)
		}
		rec.MarketRef = d
	}
	return rec, true, nil
}

// Put upserts the row. Used at trade_command intake to record the
// pending intent before exchange submission.
func (s *SqliteStore) Put(rec IdempotencyRecord) error {
	if rec.ClientOrderID == "" {
		return errors.New("agent.SqliteStore.Put: empty client_order_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var marketRef any
	if !rec.MarketRef.IsZero() {
		marketRef = rec.MarketRef.String()
	}
	var exchangeID any
	if rec.ExchangeOrderID != "" {
		exchangeID = rec.ExchangeOrderID
	}
	_, err := s.db.Exec(`
        INSERT INTO idempotency (
            client_order_id, exchange_order_id, status,
            market_ref_decimal, submitted_at_ms, last_updated_ms
        ) VALUES (?, ?, ?, ?, ?, ?)
        ON CONFLICT(client_order_id) DO UPDATE SET
            exchange_order_id  = excluded.exchange_order_id,
            status             = excluded.status,
            market_ref_decimal = excluded.market_ref_decimal,
            submitted_at_ms    = excluded.submitted_at_ms,
            last_updated_ms    = excluded.last_updated_ms`,
		rec.ClientOrderID, exchangeID, string(rec.Status),
		marketRef, rec.SubmittedAtMs, rec.LastUpdatedMs)
	if err != nil {
		return fmt.Errorf("agent.SqliteStore.Put: %w", err)
	}
	return nil
}

// UpdateStatus advances the lifecycle. Missing row is a no-op (matches
// MemoryStore semantics) — an Agent could legitimately see an
// order_update for a row it lost across a corrupt-DB reset.
func (s *SqliteStore) UpdateStatus(clientOrderID string, status IdempotencyStatus, exchangeOrderID string, nowMs int64) error {
	if clientOrderID == "" {
		return errors.New("agent.SqliteStore.UpdateStatus: empty client_order_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if exchangeOrderID != "" {
		_, err := s.db.Exec(`
            UPDATE idempotency
            SET status = ?, exchange_order_id = ?, last_updated_ms = ?
            WHERE client_order_id = ?`,
			string(status), exchangeOrderID, nowMs, clientOrderID)
		if err != nil {
			return fmt.Errorf("agent.SqliteStore.UpdateStatus: %w", err)
		}
		return nil
	}
	_, err := s.db.Exec(`
        UPDATE idempotency
        SET status = ?, last_updated_ms = ?
        WHERE client_order_id = ?`,
		string(status), nowMs, clientOrderID)
	if err != nil {
		return fmt.Errorf("agent.SqliteStore.UpdateStatus: %w", err)
	}
	return nil
}

// Purge removes rows older than retentionMs. Called once at Agent
// startup per §8.3 "每次 Agent 启动 DELETE WHERE last_updated_ms < now - 7d".
// Returns the deleted-row count for logging.
func (s *SqliteStore) Purge(beforeMs int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM idempotency WHERE last_updated_ms < ?`, beforeMs)
	if err != nil {
		return 0, fmt.Errorf("agent.SqliteStore.Purge: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
