// Package instance is the SaaS live-trading lifecycle. The Manager
// owns per-strategy-instance state mutations driven by the Cron Tick
// (Phase 6). One Tick = one decision cycle = one StrategyInput / Step /
// StrategyOutput / state-persist / dispatch.
//
// Source of truth for the Tick pipeline:
//   - docs/Coding-plan-dev-phases-prompts_v3_2_2.md Phase 6 (10-step list)
//   - docs/系统总体拓扑结构.md §6.2 (Cron Tick body)
//
// 铁律 2 boundary: time.Now() is read exactly once per Tick at step 4,
// then threaded into StrategyInput.NowMs. Step() implementations must
// never call time.Now() themselves.
package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
	"quantlab/internal/strategy"
)

// Narrow store interfaces — Manager depends on these so unit tests
// can inject fakes without standing up Postgres+TimescaleDB. The
// concrete repository.{InstanceRepo, PortfolioRepo, RuntimeRepo}
// types satisfy them by method-set match (no explicit declaration
// needed). Same pattern as internal/api/handlers.go.

type InstanceStore interface {
	SetLastTickWallTime(ctx context.Context, instanceID string, t time.Time) error
}

type PortfolioStore interface {
	Latest(ctx context.Context, instanceID string) (*store.PortfolioState, error)
	Append(ctx context.Context, ps *store.PortfolioState) error
}

type RuntimeStore interface {
	Get(ctx context.Context, instanceID string) (*store.RuntimeState, error)
	Upsert(ctx context.Context, instanceID string, nowMs int64, stateJSON json.RawMessage) error
}

// StrategyResolver maps a StrategyID to its strategy implementation,
// returning both the EvolvableStrategy (for DecodeElite / MinEvalBars)
// and the RuntimeStrategy (for Step). Most concrete strategies satisfy
// both; the resolver enforces the contract at lookup time.
type StrategyResolver interface {
	Resolve(strategyID string) (strategy.EvolvableStrategy, strategy.RuntimeStrategy, error)
}

// BarLoader fetches the trailing `count` bars for (pair, "1m") ending
// at or before `nowMs`. Returned slice must be ascending by OpenTime.
// Phase 6 currently hardcodes "1m" K-lines per docs/系统总体拓扑结构.md
// §6.2 step b; multi-interval support is Phase 9+ work.
type BarLoader interface {
	LoadTrailing(ctx context.Context, pair string, count int, nowMs int64) ([]domain.Bar, error)
}

// ChampionGeneLoader pulls a Champion's ChallengerResultPackage from
// the database and unwraps it into the domain.Gene the strategy needs.
// SpawnPoint is returned alongside because Step() consumes both.
type ChampionGeneLoader interface {
	Load(ctx context.Context, challengerID string, strat strategy.EvolvableStrategy) (domain.Gene, resultpkg.SpawnPointPayload, error)
}

// TradeCommandDispatcher delivers macro/micro OrderIntents to the
// downstream channel that ultimately reaches the LocalAgent. Phase 6.1
// ships an interface so Phase 8 (WS Hub) can plug a real impl without
// touching Manager.
type TradeCommandDispatcher interface {
	Dispatch(ctx context.Context, instanceID, accountID string, orders []strategy.OrderIntent) error
}

// LogDispatcher is the zero-config TradeCommandDispatcher: it slog.Info's
// each command. Useful while Phase 8 is unbuilt and for tests.
type LogDispatcher struct {
	Logger *slog.Logger
}

func (d *LogDispatcher) Dispatch(_ context.Context, instanceID, accountID string, orders []strategy.OrderIntent) error {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	for _, o := range orders {
		log.Info("trade_command_dispatch",
			"instance_id", instanceID,
			"account_id", accountID,
			"kind", string(o.Kind),
			"side", string(o.Side),
			"type", string(o.OrderType),
			"qty_usd", o.QuantityUSD,
			"client_order_id", o.ClientOrderID,
		)
	}
	return nil
}

// Manager owns the per-instance Tick pipeline. One Manager per SaaS
// process; the Cron scheduler (Phase 6.2) iterates ListLive() output
// and goroutine-fires Tick(ctx, instance) for each.
type Manager struct {
	instances  InstanceStore
	portfolios PortfolioStore
	runtimes   RuntimeStore
	bars       BarLoader
	resolver   StrategyResolver
	genes      ChampionGeneLoader
	dispatcher TradeCommandDispatcher
	logger     *slog.Logger

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
}

// New constructs a Manager. All dependencies are required; nil
// dispatcher falls back to LogDispatcher so callers can stand up the
// Manager before Phase 8 WS Hub exists.
func New(
	instances InstanceStore,
	portfolios PortfolioStore,
	runtimes RuntimeStore,
	bars BarLoader,
	resolver StrategyResolver,
	genes ChampionGeneLoader,
	dispatcher TradeCommandDispatcher,
	logger *slog.Logger,
) *Manager {
	if dispatcher == nil {
		dispatcher = &LogDispatcher{Logger: logger}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		instances:  instances,
		portfolios: portfolios,
		runtimes:   runtimes,
		bars:       bars,
		resolver:   resolver,
		genes:      genes,
		dispatcher: dispatcher,
		logger:     logger,
		locks:      map[string]*sync.Mutex{},
	}
}

// lockFor returns the per-instance mutex. Identical pattern to
// epoch.Service.lockFor — TryLock at the Tick entry implements the
// "step 1 idempotency bucket dedup" — if a previous Tick for this
// instance is still running, the new one bails out without error.
func (m *Manager) lockFor(instanceID string) *sync.Mutex {
	m.locksMu.Lock()
	defer m.locksMu.Unlock()
	mu, ok := m.locks[instanceID]
	if !ok {
		mu = &sync.Mutex{}
		m.locks[instanceID] = mu
	}
	return mu
}

// ErrInstanceNoChampion signals the Tick was skipped because no
// Champion has been deployed to the instance yet (Promote → Deploy
// split per B2). Caller (cron scheduler) treats as non-fatal.
var ErrInstanceNoChampion = errors.New("instance has no ActiveChampID; deploy a champion first")

// ErrTickInFlight signals another Tick goroutine already holds the
// per-instance lock. Returned synchronously when TryLock fails —
// caller (cron scheduler) silently skips, since a Tick is by
// definition already underway.
var ErrTickInFlight = errors.New("tick already in flight for this instance")

// Tick runs one decision cycle for instance `inst`. The 10 steps map
// to the Phase 6 prompt:
//
//	1.  dedup (per-instance TryLock)
//	2.  read PortfolioState + RuntimeState
//	3.  load champion gene (via DecodeElite)
//	4.  read NowMs (only allowed time.Now() in Tick)
//	5.  build StrategyInput
//	6.  Step()
//	7.  upsert RuntimeState
//	8.  apply ReleaseIntents (DeadBTC → FloatBTC, internal ledger)
//	9.  dispatch OrderIntents (TradeCommandDispatcher)
//	10. write new PortfolioState row carrying updated portfolio +
//	    LastProcessedBarTime
//
// Step 8 and 10 are bundled into the single PortfolioState INSERT
// since they both write portfolio state. Order of writes:
// PortfolioState first (carries portfolio + bar-time advance), then
// RuntimeState (strategy-private blob), then dispatch (side effect).
// This minimises the window where a crash mid-Tick leaves the
// strategy's view of the world ahead of persisted state.
//
// LastTickWallTime is always stamped via defer, success or failure,
// so ops dashboards can spot stale instances.
func (m *Manager) Tick(ctx context.Context, inst store.StrategyInstance) error {
	// Step 1: dedup. TryLock returns false if a prior Tick is still
	// running; the cron scheduler treats this as a benign skip.
	mu := m.lockFor(inst.InstanceID)
	if !mu.TryLock() {
		return ErrTickInFlight
	}
	defer mu.Unlock()

	defer func() {
		// Best-effort stamp; ignore error (logged separately).
		if err := m.instances.SetLastTickWallTime(ctx, inst.InstanceID, time.Now()); err != nil {
			m.logger.Warn("tick_set_last_tick_failed",
				"instance_id", inst.InstanceID, "err", err)
		}
	}()

	// Step 4: NowMs — ONLY allowed time.Now() in Tick (铁律 2).
	nowMs := time.Now().UnixMilli()

	// Step 2: load state.
	ps, err := m.portfolios.Latest(ctx, inst.InstanceID)
	if err != nil {
		return fmt.Errorf("tick: load portfolio: %w", err)
	}
	rs, err := m.runtimes.Get(ctx, inst.InstanceID)
	if err != nil {
		return fmt.Errorf("tick: load runtime state: %w", err)
	}

	// Resolve strategy implementations.
	estrat, rstrat, err := m.resolver.Resolve(inst.StrategyID)
	if err != nil {
		return fmt.Errorf("tick: resolve strategy %q: %w", inst.StrategyID, err)
	}

	// Step 3: champion gene. No champion → bail (non-fatal).
	if inst.ActiveChampID == nil || *inst.ActiveChampID == "" {
		return ErrInstanceNoChampion
	}
	gene, spawn, err := m.genes.Load(ctx, *inst.ActiveChampID, estrat)
	if err != nil {
		return fmt.Errorf("tick: load champion gene %q: %w", *inst.ActiveChampID, err)
	}

	// Step 5: build StrategyInput.
	barRows, err := m.bars.LoadTrailing(ctx, inst.Pair, estrat.MinEvalBars(), nowMs)
	if err != nil {
		return fmt.Errorf("tick: load bars: %w", err)
	}
	closes, timestamps := splitBars(barRows)

	portfolio := strategy.PortfolioSnapshot{}
	lastBarTime := int64(0)
	if ps != nil {
		portfolio = strategy.PortfolioSnapshot{
			DeadBTC:       ps.DeadBTC,
			FloatBTC:      ps.FloatBTC,
			ColdSealedBTC: ps.ColdSealedBTC,
			USDT:          ps.USDT,
		}
		lastBarTime = ps.LastProcessedBarTime
	}

	var stateBlob json.RawMessage
	if rs != nil {
		stateBlob = rs.StateJSON
	}

	input := strategy.StrategyInput{
		NowMs:                nowMs,
		Closes:               closes,
		Timestamps:           timestamps,
		Portfolio:            portfolio,
		Chromosome:           gene,
		Spawn:                spawn,
		LastProcessedBarTime: lastBarTime,
		RuntimeState:         stateBlob,
	}

	// Step 6: Step().
	output, err := rstrat.Step(input)
	if err != nil {
		return fmt.Errorf("tick: strategy step: %w", err)
	}

	// Step 8: apply ReleaseIntents to the local portfolio view
	// (DeadBTC → FloatBTC). These never reach the exchange — the
	// Agent has no concept of DeadBTC; it's a SaaS-internal ledger
	// state that locks a long-held lot off the active trading float.
	nextPortfolio := portfolio
	for _, ri := range output.ReleaseIntents {
		nextPortfolio.DeadBTC -= ri.Quantity
		nextPortfolio.FloatBTC += ri.Quantity
	}

	// Step 10 (folded into the new PortfolioState row): the last bar
	// consumed by Step is the rightmost timestamp we loaded.
	nextLastBarTime := lastBarTime
	if n := len(timestamps); n > 0 {
		nextLastBarTime = timestamps[n-1]
	}

	// Persist portfolio first.
	nextPS := &store.PortfolioState{
		InstanceID:           inst.InstanceID,
		NowMs:                nowMs,
		DeadBTC:              nextPortfolio.DeadBTC,
		FloatBTC:             nextPortfolio.FloatBTC,
		ColdSealedBTC:        nextPortfolio.ColdSealedBTC,
		USDT:                 nextPortfolio.USDT,
		LastProcessedBarTime: nextLastBarTime,
	}
	if err := m.portfolios.Append(ctx, nextPS); err != nil {
		return fmt.Errorf("tick: append portfolio: %w", err)
	}

	// Step 7: persist RuntimeState (opaque blob owned by strategy).
	if err := m.runtimes.Upsert(ctx, inst.InstanceID, nowMs, output.RuntimeState); err != nil {
		return fmt.Errorf("tick: upsert runtime state: %w", err)
	}

	// Step 9: dispatch OrderIntents. Macro + micro share the same wire
	// type; the consumer (Agent) reads Kind to route. Empty slices skip
	// the dispatcher entirely.
	orders := make([]strategy.OrderIntent, 0, len(output.MacroOrders)+len(output.MicroOrders))
	orders = append(orders, output.MacroOrders...)
	orders = append(orders, output.MicroOrders...)
	if len(orders) > 0 {
		if err := m.dispatcher.Dispatch(ctx, inst.InstanceID, inst.AccountID, orders); err != nil {
			return fmt.Errorf("tick: dispatch orders: %w", err)
		}
	}

	return nil
}

func splitBars(bars []domain.Bar) (closes []float64, ts []int64) {
	closes = make([]float64, len(bars))
	ts = make([]int64, len(bars))
	for i, b := range bars {
		closes[i] = b.Close
		ts[i] = b.OpenTime
	}
	return closes, ts
}
