// Package cron drives the per-minute scan that fires Manager.Tick for
// every live StrategyInstance (Phase 6.2). One scheduler per SaaS
// process; cmd/saas runs it in a goroutine alongside the HTTP server
// (wiring lands in Phase 6.3 / Phase 10 main updates).
//
// Source-of-truth: Phase 6 prompt + docs/系统总体拓扑结构.md §6.2 (cron
// Tick body). The scheduler itself does no business logic — it
// repeats:
//
//  1. List live instances (DB read)
//  2. For each instance, fire a goroutine that calls Manager.Tick
//  3. Sleep until next interval
//  4. On ctx cancel, return (in-flight Tick goroutines complete on
//     their own; per-instance mutex inside Manager guarantees no
//     overlap).
//
// 铁律 2 note: time.Now() is read INSIDE Manager.Tick (the "tick outer
// loop" allowance), not here. The scheduler uses a Go time.Ticker for
// pacing — that's an internal clock, not a strategy time source.
package cron

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"quantlab/internal/saas/instance"
	"quantlab/internal/saas/store"
)

// InstanceLister is the read-side abstraction the scheduler needs.
// repository.InstanceRepo satisfies it via method-set match.
type InstanceLister interface {
	ListLive(ctx context.Context) ([]store.StrategyInstance, error)
}

// Ticker runs one decision cycle for one instance. instance.Manager
// satisfies this (its Tick method has the exact signature).
type Ticker interface {
	Tick(ctx context.Context, inst store.StrategyInstance) error
}

// Config tunes the scheduler. Zero values get sensible defaults.
type Config struct {
	// Interval between scans. Default 1m (matches the strategy's
	// 1m bar cadence in Phase 6).
	Interval time.Duration

	// TickTimeout is the per-Tick context deadline. A stuck Tick
	// must not block forever and starve other ticks (cron keeps
	// firing — Manager's per-instance mutex skips overlapping work,
	// but a hung Tick still wastes a goroutine slot).
	// Default 30s.
	TickTimeout time.Duration

	// Logger is structured-log sink. Default slog.Default().
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.Interval == 0 {
		c.Interval = time.Minute
	}
	if c.TickTimeout == 0 {
		c.TickTimeout = 30 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// Scheduler orchestrates per-minute Tick firing. Construct with New,
// run with Run(ctx).
type Scheduler struct {
	lister InstanceLister
	ticker Ticker
	cfg    Config
}

func New(lister InstanceLister, ticker Ticker, cfg Config) *Scheduler {
	return &Scheduler{
		lister: lister,
		ticker: ticker,
		cfg:    cfg.withDefaults(),
	}
}

// Run blocks until ctx is cancelled. Fires the first scan immediately
// (don't wait one full Interval at startup), then on every Interval.
//
// In-flight Tick goroutines are NOT awaited on ctx cancel — they hold
// the per-instance mutex inside Manager and complete on their own.
// Phase 5D-cmd shutdown (graceful drain) takes the same approach.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()

	s.cfg.Logger.Info("scheduler_started",
		"interval", s.cfg.Interval.String(),
		"tick_timeout", s.cfg.TickTimeout.String(),
	)
	defer s.cfg.Logger.Info("scheduler_stopped")

	s.scanAndTick(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.scanAndTick(ctx)
		}
	}
}

// scanAndTick performs one ListLive scan + per-instance Tick fan-out.
// Each instance gets its own goroutine; failures are logged but do
// not stop the scheduler.
func (s *Scheduler) scanAndTick(ctx context.Context) {
	rows, err := s.lister.ListLive(ctx)
	if err != nil {
		s.cfg.Logger.Error("scheduler_list_live_failed", "err", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	for _, inst := range rows {
		go s.runTick(ctx, inst)
	}
}

// runTick wraps a single Tick call with a deadline, panic recovery,
// and error-class-aware logging. Per-instance dedup is provided by
// Manager's internal mutex.
func (s *Scheduler) runTick(ctx context.Context, inst store.StrategyInstance) {
	defer func() {
		if r := recover(); r != nil {
			s.cfg.Logger.Error("scheduler_tick_panic",
				"instance_id", inst.InstanceID,
				"panic", r,
			)
		}
	}()

	tickCtx, cancel := context.WithTimeout(ctx, s.cfg.TickTimeout)
	defer cancel()

	err := s.ticker.Tick(tickCtx, inst)
	switch {
	case err == nil:
		s.cfg.Logger.Debug("scheduler_tick_ok", "instance_id", inst.InstanceID)
	case errors.Is(err, instance.ErrTickInFlight):
		s.cfg.Logger.Debug("scheduler_tick_skipped_inflight", "instance_id", inst.InstanceID)
	case errors.Is(err, instance.ErrInstanceNoChampion):
		s.cfg.Logger.Warn("scheduler_tick_skipped_no_champion", "instance_id", inst.InstanceID)
	default:
		s.cfg.Logger.Error("scheduler_tick_failed",
			"instance_id", inst.InstanceID,
			"err", err,
		)
	}
}
