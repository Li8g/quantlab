// Package agentstatus publishes per-Agent connection state to Redis
// for cross-process queries ("is this account's Agent online?").
//
// Spec: docs/saas-ws-protocol-v1.md §7.2.
//
//	key:   agent:{accountID}:status
//	value: JSON { agent_id, connection_state, last_seen_ms, last_msg_id }
//	TTL:   60s (refreshed on pong or any Agent → SaaS message)
//
// TTL is intentionally shorter than the §4.4 90s STALE threshold:
// Redis TTL governs the "agent is online" view that other services
// query; STALE is the local Hub's decision to tear the connection
// down. The two can disagree briefly while a stale-but-still-pending
// connection is being closed.
//
// Iron rule 6 (fallback): on Redis miss, callers may fall back to
// agent_tokens.last_seen_at (a slower but persistent signal).
package agentstatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultTTL is the per-key expiration. Set on every write; reads do
// not refresh — the connection is responsible for re-writing.
const DefaultTTL = 60 * time.Second

// ConnectionState is the published lifecycle label. Frozen enum
// matching protocol §7.2.
type ConnectionState string

const (
	StateConnecting   ConnectionState = "connecting"
	StateAuthed       ConnectionState = "authed"
	StateReady        ConnectionState = "ready"
	StateStale        ConnectionState = "stale"
	StateDisconnected ConnectionState = "disconnected"
)

// Status is the JSON object serialised to Redis.
type Status struct {
	AgentID         string          `json:"agent_id"`
	ConnectionState ConnectionState `json:"connection_state"`
	LastSeenMs      int64           `json:"last_seen_ms"`
	LastMsgID       string          `json:"last_msg_id,omitempty"`
}

// Reporter writes Agent status with TTL. The interface keeps wshub
// agnostic of the backend so tests / dev builds can use NopReporter.
type Reporter interface {
	Set(ctx context.Context, accountID string, status Status) error
	Get(ctx context.Context, accountID string) (Status, bool, error)
	Delete(ctx context.Context, accountID string) error
}

// NopReporter discards all writes. Returned when Redis is not
// configured; callers see all reads as miss.
type NopReporter struct{}

func (NopReporter) Set(_ context.Context, _ string, _ Status) error { return nil }
func (NopReporter) Get(_ context.Context, _ string) (Status, bool, error) {
	return Status{}, false, nil
}
func (NopReporter) Delete(_ context.Context, _ string) error { return nil }

// RedisReporter is the production Reporter backed by go-redis. Key
// format is fixed (agent:{accountID}:status); TTL is from the
// constructor.
type RedisReporter struct {
	client redis.UniversalClient
	ttl    time.Duration
}

// NewRedisReporter constructs a RedisReporter. ttl≤0 falls back to
// DefaultTTL so callers can pass time.Duration(0) when the protocol
// default is fine.
func NewRedisReporter(client redis.UniversalClient, ttl time.Duration) *RedisReporter {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &RedisReporter{client: client, ttl: ttl}
}

// keyFor builds the Redis key. accountID empty is a programmer error
// (the Reporter is keyed by account) — return a sentinel that produces
// a clearly invalid key so the resulting Redis error is loud.
func keyFor(accountID string) string {
	return fmt.Sprintf("agent:%s:status", accountID)
}

func (r *RedisReporter) Set(ctx context.Context, accountID string, status Status) error {
	if accountID == "" {
		return errors.New("agentstatus.Set: empty accountID")
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("agentstatus.Set: marshal: %w", err)
	}
	if err := r.client.Set(ctx, keyFor(accountID), raw, r.ttl).Err(); err != nil {
		return fmt.Errorf("agentstatus.Set: redis: %w", err)
	}
	return nil
}

func (r *RedisReporter) Get(ctx context.Context, accountID string) (Status, bool, error) {
	if accountID == "" {
		return Status{}, false, errors.New("agentstatus.Get: empty accountID")
	}
	raw, err := r.client.Get(ctx, keyFor(accountID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, fmt.Errorf("agentstatus.Get: redis: %w", err)
	}
	var s Status
	if err := json.Unmarshal(raw, &s); err != nil {
		return Status{}, false, fmt.Errorf("agentstatus.Get: unmarshal: %w", err)
	}
	return s, true, nil
}

func (r *RedisReporter) Delete(ctx context.Context, accountID string) error {
	if accountID == "" {
		return errors.New("agentstatus.Delete: empty accountID")
	}
	if err := r.client.Del(ctx, keyFor(accountID)).Err(); err != nil {
		return fmt.Errorf("agentstatus.Delete: redis: %w", err)
	}
	return nil
}
