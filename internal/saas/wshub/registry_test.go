package wshub

import (
	"errors"
	"sync"
	"testing"
)

func TestRegistry_RegisterGetUnregister(t *testing.T) {
	r := NewRegistry()
	c1 := &Connection{AccountID: "acct-1", outbox: make(chan []byte, 1), closeCh: make(chan struct{})}

	if prev := r.Register(c1); prev != nil {
		t.Errorf("first Register returned non-nil prev: %v", prev)
	}
	if r.Len() != 1 {
		t.Errorf("Len = %d, want 1", r.Len())
	}
	got, err := r.Get("acct-1")
	if err != nil {
		t.Fatalf("Get acct-1: %v", err)
	}
	if got != c1 {
		t.Errorf("Get returned different *Connection")
	}

	_, err = r.Get("acct-missing")
	if !errors.Is(err, ErrAccountNotConnected) {
		t.Errorf("Get missing: got %v, want ErrAccountNotConnected", err)
	}

	r.Unregister(c1)
	if r.Len() != 0 {
		t.Errorf("Len after unregister = %d", r.Len())
	}
}

func TestRegistry_ReregisterReplacesAndClosesPrev(t *testing.T) {
	r := NewRegistry()
	c1 := &Connection{AccountID: "acct-1", outbox: make(chan []byte, 1), closeCh: make(chan struct{})}
	c2 := &Connection{AccountID: "acct-1", outbox: make(chan []byte, 1), closeCh: make(chan struct{})}
	// Stub conn so c1.Close()'s underlying conn.Close() doesn't panic.
	c1.conn = newPipeConn()
	c2.conn = newPipeConn()

	r.Register(c1)
	prev := r.Register(c2)
	if prev != c1 {
		t.Errorf("Register(c2) returned prev=%v, want c1", prev)
	}
	// c1 should be closed.
	select {
	case <-c1.closeCh:
		// ok
	default:
		t.Errorf("c1 not closed after replacement")
	}

	got, _ := r.Get("acct-1")
	if got != c2 {
		t.Errorf("Get returned %v, want c2", got)
	}
}

func TestRegistry_UnregisterIgnoresStale(t *testing.T) {
	r := NewRegistry()
	c1 := &Connection{AccountID: "acct-1", outbox: make(chan []byte, 1), closeCh: make(chan struct{})}
	c1.conn = newPipeConn()
	c2 := &Connection{AccountID: "acct-1", outbox: make(chan []byte, 1), closeCh: make(chan struct{})}
	c2.conn = newPipeConn()

	r.Register(c1)
	r.Register(c2) // c1 evicted

	// Unregister(c1) should NOT remove c2 (different pointer).
	r.Unregister(c1)
	if r.Len() != 1 {
		t.Errorf("Len after stale Unregister = %d, want 1", r.Len())
	}
	got, _ := r.Get("acct-1")
	if got != c2 {
		t.Errorf("Get returned non-c2 after stale Unregister")
	}
}

func TestRegistry_SnapshotIsCopy(t *testing.T) {
	r := NewRegistry()
	c1 := &Connection{AccountID: "acct-1", outbox: make(chan []byte, 1), closeCh: make(chan struct{})}
	c2 := &Connection{AccountID: "acct-2", outbox: make(chan []byte, 1), closeCh: make(chan struct{})}
	r.Register(c1)
	r.Register(c2)

	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("Snapshot len = %d", len(snap))
	}

	// Mutate registry; snap should be unaffected.
	c3 := &Connection{AccountID: "acct-3", outbox: make(chan []byte, 1), closeCh: make(chan struct{})}
	r.Register(c3)
	if len(snap) != 2 {
		t.Errorf("snap mutated externally")
	}
}

// TestRegistry_ConcurrentAccess is a smoke test under -race: many
// goroutines simultaneously Register/Get/Unregister different accounts.
func TestRegistry_ConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			c := &Connection{
				AccountID: "acct",
				outbox:    make(chan []byte, 1),
				closeCh:   make(chan struct{}),
			}
			c.conn = newPipeConn()
			r.Register(c)
			_, _ = r.Get("acct")
			_ = r.Len()
			_ = r.Snapshot()
			r.Unregister(c)
			_ = id
		}(i)
	}
	wg.Wait()
}
