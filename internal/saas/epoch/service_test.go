package epoch

import (
	"sync"
	"testing"
)

// lockFor and the (strategy, pair) mutex are the only Service knobs
// safely testable without a live DB. CreateAndRunTask happy-path
// coverage is deferred to a //go:build integration test (DB required).

func TestService_lockFor_SameTupleReturnsSameMutex(t *testing.T) {
	s := &Service{locks: map[string]*sync.Mutex{}}
	a := s.lockFor("sigmoid_v1", "BTCUSDT")
	b := s.lockFor("sigmoid_v1", "BTCUSDT")
	if a != b {
		t.Errorf("lockFor(same) returned different mutexes: %p vs %p", a, b)
	}
}

func TestService_lockFor_DifferentTupleReturnsDifferentMutex(t *testing.T) {
	s := &Service{locks: map[string]*sync.Mutex{}}
	a := s.lockFor("sigmoid_v1", "BTCUSDT")
	b := s.lockFor("sigmoid_v1", "ETHUSDT")
	c := s.lockFor("toy", "BTCUSDT")
	if a == b || a == c || b == c {
		t.Errorf("different tuples must yield different mutexes; a=%p b=%p c=%p", a, b, c)
	}
}

func TestService_lockFor_TryLockBehaviour(t *testing.T) {
	s := &Service{locks: map[string]*sync.Mutex{}}
	mu := s.lockFor("sigmoid_v1", "BTCUSDT")
	if !mu.TryLock() {
		t.Fatal("first TryLock should succeed on a fresh mutex")
	}
	if mu.TryLock() {
		t.Error("second TryLock on a held mutex should fail")
	}
	mu.Unlock()
	if !mu.TryLock() {
		t.Error("TryLock after Unlock should succeed")
	}
	mu.Unlock()
}
