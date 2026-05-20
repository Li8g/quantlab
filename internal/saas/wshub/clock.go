package wshub

import "time"

// Clock abstracts time.Ticker for the heartbeat goroutine so tests can
// fire ticks deterministically. Production uses realClock; tests use
// fakeClock (in clock_test_helpers.go).
type Clock interface {
	NewTicker(d time.Duration) Ticker
	Now() time.Time
}

// Ticker mirrors time.Ticker's surface area, but lets tests substitute
// a channel they control.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type realClock struct{}

func (realClock) NewTicker(d time.Duration) Ticker {
	return &realTicker{t: time.NewTicker(d)}
}

func (realClock) Now() time.Time { return time.Now() }

type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time { return r.t.C }
func (r *realTicker) Stop()               { r.t.Stop() }
