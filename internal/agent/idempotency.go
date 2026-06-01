package agent

import (
	"sync"

	"github.com/shopspring/decimal"
)

// IdempotencyStatus is the lifecycle state recorded against a
// client_order_id. Matches docs/saas-ws-protocol-v1.md §8.3 column
// values.
type IdempotencyStatus string

const (
	IdempotencyStatusPending   IdempotencyStatus = "pending"
	IdempotencyStatusAccepted  IdempotencyStatus = "accepted"
	IdempotencyStatusFilled    IdempotencyStatus = "filled"
	IdempotencyStatusCancelled IdempotencyStatus = "cancelled"
	IdempotencyStatusRejected  IdempotencyStatus = "rejected"
)

// IdempotencyRecord is one entry keyed by ClientOrderID. MarketRef is
// the best bid/ask captured at submit so subsequent fills can compute
// ActualSlippageBps without re-querying the exchange.
type IdempotencyRecord struct {
	ClientOrderID   string
	ExchangeOrderID string
	Status          IdempotencyStatus
	MarketRef       decimal.Decimal
	SubmittedAtMs   int64
	LastUpdatedMs   int64
}

// IsTerminal reports whether the lifecycle has settled.
func (r IdempotencyRecord) IsTerminal() bool {
	switch r.Status {
	case IdempotencyStatusFilled, IdempotencyStatusCancelled, IdempotencyStatusRejected:
		return true
	}
	return false
}

// IdempotencyStore is the persistence interface for client_order_id →
// IdempotencyRecord. The protocol doc §8.3 specifies a sqlite-backed
// production implementation; v1 ships an in-memory store (MemoryStore)
// and defers sqlite to Phase 7/8 polish.
//
// Methods are synchronous and safe for concurrent use.
type IdempotencyStore interface {
	Get(clientOrderID string) (IdempotencyRecord, bool, error)
	Put(rec IdempotencyRecord) error
	UpdateStatus(clientOrderID string, status IdempotencyStatus, exchangeOrderID string, nowMs int64) error
}

// MemoryStore is the in-process IdempotencyStore. Goroutine-safe.
type MemoryStore struct {
	mu   sync.Mutex
	rows map[string]IdempotencyRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: make(map[string]IdempotencyRecord)}
}

func (m *MemoryStore) Get(clientOrderID string) (IdempotencyRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.rows[clientOrderID]
	return rec, ok, nil
}

func (m *MemoryStore) Put(rec IdempotencyRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[rec.ClientOrderID] = rec
	return nil
}

func (m *MemoryStore) UpdateStatus(clientOrderID string, status IdempotencyStatus, exchangeOrderID string, nowMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.rows[clientOrderID]
	if !ok {
		return nil // silently ignore — Update on missing row is OK
	}
	rec.Status = status
	if exchangeOrderID != "" {
		rec.ExchangeOrderID = exchangeOrderID
	}
	rec.LastUpdatedMs = nowMs
	m.rows[clientOrderID] = rec
	return nil
}

// Snapshot returns all current records. Test helper; not part of the
// interface contract.
func (m *MemoryStore) Snapshot() []IdempotencyRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]IdempotencyRecord, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, r)
	}
	return out
}
