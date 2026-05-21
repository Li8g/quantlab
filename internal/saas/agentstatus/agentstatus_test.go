package agentstatus

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestReporter(t *testing.T, ttl time.Duration) (*RedisReporter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	cli := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = cli.Close() })
	return NewRedisReporter(cli, ttl), mr
}

func TestRedisReporter_SetThenGet(t *testing.T) {
	r, _ := newTestReporter(t, 0) // 0 → DefaultTTL
	ctx := context.Background()

	want := Status{
		AgentID:         "01HKAGENT00000000000000000",
		ConnectionState: StateReady,
		LastSeenMs:      1714000000123,
		LastMsgID:       "01HKMSG0000000000000000001",
	}
	if err := r.Set(ctx, "01HKACCT00000000000000000A", want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, err := r.Get(ctx, "01HKACCT00000000000000000A")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get ok=false after Set")
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestRedisReporter_GetMissReturnsFalse(t *testing.T) {
	r, _ := newTestReporter(t, 0)
	_, ok, err := r.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("ok = true on missing key")
	}
}

func TestRedisReporter_KeyShape(t *testing.T) {
	r, mr := newTestReporter(t, 0)
	if err := r.Set(context.Background(), "ACCT-1", Status{ConnectionState: StateReady}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Verify the literal key shape from the spec: agent:{accountID}:status
	if !mr.Exists("agent:ACCT-1:status") {
		t.Errorf("key agent:ACCT-1:status not present; keys=%v", mr.Keys())
	}
}

func TestRedisReporter_TTLApplied(t *testing.T) {
	r, mr := newTestReporter(t, 60*time.Second)
	if err := r.Set(context.Background(), "ACCT", Status{ConnectionState: StateReady}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ttl := mr.TTL("agent:ACCT:status")
	if ttl <= 0 || ttl > 60*time.Second {
		t.Errorf("TTL = %v, want (0, 60s]", ttl)
	}
}

func TestRedisReporter_DefaultTTLWhenZero(t *testing.T) {
	r, mr := newTestReporter(t, 0)
	if err := r.Set(context.Background(), "ACCT", Status{ConnectionState: StateReady}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ttl := mr.TTL("agent:ACCT:status")
	// DefaultTTL = 60s; allow a small skew because miniredis ticks
	// integer seconds.
	if ttl < 55*time.Second || ttl > DefaultTTL {
		t.Errorf("TTL = %v, want ~60s (DefaultTTL)", ttl)
	}
}

func TestRedisReporter_ResetExtendsTTL(t *testing.T) {
	r, mr := newTestReporter(t, 60*time.Second)
	ctx := context.Background()
	st := Status{ConnectionState: StateReady}
	if err := r.Set(ctx, "ACCT", st); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Fast-forward past half the TTL, then Set again — the new write
	// should reset the timer (acts as the spec's "refresh on pong").
	mr.FastForward(50 * time.Second)
	if err := r.Set(ctx, "ACCT", st); err != nil {
		t.Fatalf("Set #2: %v", err)
	}
	ttl := mr.TTL("agent:ACCT:status")
	if ttl < 55*time.Second {
		t.Errorf("TTL after refresh = %v, want close to 60s", ttl)
	}
}

func TestRedisReporter_Delete(t *testing.T) {
	r, mr := newTestReporter(t, 0)
	ctx := context.Background()
	if err := r.Set(ctx, "ACCT", Status{ConnectionState: StateReady}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := r.Delete(ctx, "ACCT"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if mr.Exists("agent:ACCT:status") {
		t.Error("key still present after Delete")
	}
	_, ok, _ := r.Get(ctx, "ACCT")
	if ok {
		t.Error("Get ok=true after Delete")
	}
}

func TestRedisReporter_EmptyAccountIDErrors(t *testing.T) {
	r, _ := newTestReporter(t, 0)
	if err := r.Set(context.Background(), "", Status{}); err == nil {
		t.Error("Set with empty accountID: want err")
	}
	if _, _, err := r.Get(context.Background(), ""); err == nil {
		t.Error("Get with empty accountID: want err")
	}
	if err := r.Delete(context.Background(), ""); err == nil {
		t.Error("Delete with empty accountID: want err")
	}
}

func TestNopReporter_AlwaysSuccessAlwaysMiss(t *testing.T) {
	var r Reporter = NopReporter{}
	ctx := context.Background()
	if err := r.Set(ctx, "x", Status{ConnectionState: StateReady}); err != nil {
		t.Errorf("NopReporter.Set: %v", err)
	}
	s, ok, err := r.Get(ctx, "x")
	if err != nil {
		t.Errorf("NopReporter.Get: %v", err)
	}
	if ok {
		t.Errorf("NopReporter.Get returned ok=true with status=%+v", s)
	}
	if err := r.Delete(ctx, "x"); err != nil {
		t.Errorf("NopReporter.Delete: %v", err)
	}
}
