package agent

import (
	"path/filepath"
	"testing"

	"github.com/shopspring/decimal"
)

// newTestSqliteStore opens a fresh sqlite file under t.TempDir(). The
// file is cleaned by the testing framework when the test ends.
func newTestSqliteStore(t *testing.T) *SqliteStore {
	t.Helper()
	s, err := NewSqliteStore(filepath.Join(t.TempDir(), "idem.sqlite"))
	if err != nil {
		t.Fatalf("NewSqliteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSqliteStore_GetMissReturnsFalse(t *testing.T) {
	s := newTestSqliteStore(t)
	rec, ok, err := s.Get("missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Errorf("ok = true; want false on missing row, rec=%+v", rec)
	}
}

func TestSqliteStore_PutThenGetRoundTrip(t *testing.T) {
	s := newTestSqliteStore(t)
	in := IdempotencyRecord{
		ClientOrderID:   "01HKCOID000000000000000001",
		ExchangeOrderID: "EX-100",
		Status:          IdempotencyStatusAccepted,
		MarketRef:       decimal.RequireFromString("50000.25"),
		SubmittedAtMs:   1714000000000,
		LastUpdatedMs:   1714000000123,
	}
	if err := s.Put(in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := s.Get(in.ClientOrderID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("Get ok=false after Put")
	}
	if got.ClientOrderID != in.ClientOrderID ||
		got.ExchangeOrderID != in.ExchangeOrderID ||
		got.Status != in.Status ||
		got.SubmittedAtMs != in.SubmittedAtMs ||
		got.LastUpdatedMs != in.LastUpdatedMs {
		t.Errorf("round-trip mismatch:\n got=%+v\nwant=%+v", got, in)
	}
	if !got.MarketRef.Equal(in.MarketRef) {
		t.Errorf("MarketRef = %s, want %s", got.MarketRef, in.MarketRef)
	}
}

func TestSqliteStore_PutIsUpsert(t *testing.T) {
	s := newTestSqliteStore(t)
	rec := IdempotencyRecord{
		ClientOrderID: "01HKCOID000000000000000002",
		Status:        IdempotencyStatusPending,
		SubmittedAtMs: 100,
		LastUpdatedMs: 100,
	}
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put1: %v", err)
	}
	rec.Status = IdempotencyStatusFilled
	rec.ExchangeOrderID = "EX-200"
	rec.LastUpdatedMs = 200
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put2: %v", err)
	}
	got, _, _ := s.Get(rec.ClientOrderID)
	if got.Status != IdempotencyStatusFilled || got.ExchangeOrderID != "EX-200" {
		t.Errorf("upsert did not overwrite: %+v", got)
	}
}

func TestSqliteStore_UpdateStatus(t *testing.T) {
	s := newTestSqliteStore(t)
	rec := IdempotencyRecord{
		ClientOrderID: "01HKCOID000000000000000003",
		Status:        IdempotencyStatusPending,
		SubmittedAtMs: 300,
		LastUpdatedMs: 300,
	}
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.UpdateStatus(rec.ClientOrderID, IdempotencyStatusAccepted, "EX-300", 400); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, _, _ := s.Get(rec.ClientOrderID)
	if got.Status != IdempotencyStatusAccepted {
		t.Errorf("Status = %q, want accepted", got.Status)
	}
	if got.ExchangeOrderID != "EX-300" {
		t.Errorf("ExchangeOrderID = %q, want EX-300", got.ExchangeOrderID)
	}
	if got.LastUpdatedMs != 400 {
		t.Errorf("LastUpdatedMs = %d, want 400", got.LastUpdatedMs)
	}
}

func TestSqliteStore_UpdateStatus_PreservesExchangeIDWhenBlank(t *testing.T) {
	s := newTestSqliteStore(t)
	rec := IdempotencyRecord{
		ClientOrderID:   "01HKCOID000000000000000004",
		ExchangeOrderID: "EX-400",
		Status:          IdempotencyStatusAccepted,
		SubmittedAtMs:   500,
		LastUpdatedMs:   500,
	}
	if err := s.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Subsequent UpdateStatus with empty exchangeOrderID (typical for
	// order_update events that don't repeat the field) must leave the
	// existing value intact — the protocol cares about this.
	if err := s.UpdateStatus(rec.ClientOrderID, IdempotencyStatusFilled, "", 600); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, _, _ := s.Get(rec.ClientOrderID)
	if got.ExchangeOrderID != "EX-400" {
		t.Errorf("ExchangeOrderID = %q, want EX-400 to be preserved", got.ExchangeOrderID)
	}
	if got.Status != IdempotencyStatusFilled {
		t.Errorf("Status = %q, want filled", got.Status)
	}
}

func TestSqliteStore_UpdateStatus_MissingRowIsNoop(t *testing.T) {
	s := newTestSqliteStore(t)
	// Matches MemoryStore semantics: UPDATE WHERE not-found affects
	// zero rows and returns nil.
	if err := s.UpdateStatus("nope", IdempotencyStatusFilled, "EX-1", 700); err != nil {
		t.Errorf("UpdateStatus missing row: err=%v, want nil", err)
	}
}

func TestSqliteStore_Purge(t *testing.T) {
	s := newTestSqliteStore(t)
	// Three rows with distinct last_updated_ms.
	for i, ts := range []int64{100, 200, 300} {
		rec := IdempotencyRecord{
			ClientOrderID: "01HKCOID00000000000000000" + string(rune('A'+i)),
			Status:        IdempotencyStatusFilled,
			SubmittedAtMs: ts,
			LastUpdatedMs: ts,
		}
		if err := s.Put(rec); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	// Purge anything strictly older than 250 → 100, 200 go; 300 stays.
	n, err := s.Purge(250)
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if n != 2 {
		t.Errorf("Purge n = %d, want 2", n)
	}
	if _, ok, _ := s.Get("01HKCOID00000000000000000A"); ok {
		t.Error("row at ts=100 not purged")
	}
	if _, ok, _ := s.Get("01HKCOID00000000000000000B"); ok {
		t.Error("row at ts=200 not purged")
	}
	if _, ok, _ := s.Get("01HKCOID00000000000000000C"); !ok {
		t.Error("row at ts=300 unexpectedly purged")
	}
}

func TestSqliteStore_DurableAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idem.sqlite")
	s1, err := NewSqliteStore(path)
	if err != nil {
		t.Fatalf("NewSqliteStore #1: %v", err)
	}
	rec := IdempotencyRecord{
		ClientOrderID: "01HKCOID000000000000000099",
		Status:        IdempotencyStatusAccepted,
		MarketRef:     decimal.RequireFromString("123.45"),
		SubmittedAtMs: 900,
		LastUpdatedMs: 900,
	}
	if err := s1.Put(rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := NewSqliteStore(path)
	if err != nil {
		t.Fatalf("NewSqliteStore #2: %v", err)
	}
	defer s2.Close()
	got, ok, err := s2.Get(rec.ClientOrderID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !ok {
		t.Fatalf("row not durable across reopen")
	}
	if !got.MarketRef.Equal(rec.MarketRef) {
		t.Errorf("MarketRef = %s, want %s", got.MarketRef, rec.MarketRef)
	}
}

func TestNewSqliteStore_EmptyPathError(t *testing.T) {
	if _, err := NewSqliteStore(""); err == nil {
		t.Error("want error on empty path")
	}
}
