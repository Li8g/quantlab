package wshub

import (
	"errors"
	"sync"
)

// ErrAccountNotConnected is returned by Registry.Get / Hub.Dispatch when
// the target AccountID has no active READY connection. Hub maps this to
// a logged warning + AuditLog entry — TradeCommand is dropped (we do
// NOT queue locally; the Agent is responsible for state recovery via
// state_sync on next reconnect).
var ErrAccountNotConnected = errors.New("wshub: account not connected")

// Registry is the AccountID → *Connection map. One *Connection per
// AccountID at most (Agent v1 is 1:1 with an account; protocol §1).
// Concurrent re-registration (e.g. Agent reconnect while old conn is
// still being torn down) closes the prior conn and replaces.
type Registry struct {
	mu    sync.RWMutex
	conns map[string]*Connection
}

func NewRegistry() *Registry {
	return &Registry{conns: make(map[string]*Connection)}
}

// Register installs c under its AccountID. If a previous connection
// exists for the same AccountID, it is closed and replaced. The old
// connection's read/write goroutines exit on the next read/write error.
//
// Returns the previously-registered connection (if any) so the caller
// can record metrics; nil if this is a fresh binding.
func (r *Registry) Register(c *Connection) *Connection {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.conns[c.AccountID]
	if prev != nil {
		_ = prev.Close()
	}
	r.conns[c.AccountID] = c
	return prev
}

// Unregister removes c. No-op if c is not the currently-registered
// connection (e.g. it was already replaced by a reconnect).
func (r *Registry) Unregister(c *Connection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conns[c.AccountID] == c {
		delete(r.conns, c.AccountID)
	}
}

// Get returns the Connection for accountID, or ErrAccountNotConnected
// when none is registered. The returned *Connection is safe to use
// concurrently with future Register/Unregister calls; if the conn is
// being torn down its WriteFrame will return ErrConnClosed.
func (r *Registry) Get(accountID string) (*Connection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.conns[accountID]
	if !ok {
		return nil, ErrAccountNotConnected
	}
	return c, nil
}

// Snapshot returns every currently-registered Connection. Used for
// broadcast (graceful_shutdown) and for ops introspection. The returned
// slice is a copy; iterating it is safe even if Register/Unregister
// races.
func (r *Registry) Snapshot() []*Connection {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Connection, 0, len(r.conns))
	for _, c := range r.conns {
		out = append(out, c)
	}
	return out
}

// Len reports the number of currently-registered Connections.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.conns)
}
