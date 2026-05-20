package instance

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"sync"
	"testing"
	"time"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
	"quantlab/internal/strategy"
)

// ===== fakes =====

type fakeInstanceStore struct {
	lastTickAt map[string]time.Time
	err        error
	mu         sync.Mutex
}

func (f *fakeInstanceStore) SetLastTickWallTime(_ context.Context, id string, t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastTickAt == nil {
		f.lastTickAt = map[string]time.Time{}
	}
	f.lastTickAt[id] = t
	return f.err
}

type fakePortfolioStore struct {
	latest   *store.PortfolioState
	appended []*store.PortfolioState
	latestErr error
	appendErr error
	mu       sync.Mutex
}

func (f *fakePortfolioStore) Latest(_ context.Context, _ string) (*store.PortfolioState, error) {
	return f.latest, f.latestErr
}

func (f *fakePortfolioStore) Append(_ context.Context, ps *store.PortfolioState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appendErr != nil {
		return f.appendErr
	}
	f.appended = append(f.appended, ps)
	return nil
}

type fakeRuntimeStore struct {
	current *store.RuntimeState
	upserts []upsertCall
	getErr  error
	upErr   error
	mu      sync.Mutex
}

type upsertCall struct {
	instanceID string
	nowMs      int64
	state      json.RawMessage
}

func (f *fakeRuntimeStore) Get(_ context.Context, _ string) (*store.RuntimeState, error) {
	return f.current, f.getErr
}

func (f *fakeRuntimeStore) Upsert(_ context.Context, id string, nowMs int64, state json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upErr != nil {
		return f.upErr
	}
	f.upserts = append(f.upserts, upsertCall{id, nowMs, state})
	return nil
}

type fakeBarLoader struct {
	bars []domain.Bar
	err  error
}

func (f *fakeBarLoader) LoadTrailing(_ context.Context, _ string, _ int, _ int64) ([]domain.Bar, error) {
	return f.bars, f.err
}

type fakeStrategy struct {
	id      string
	output  strategy.StrategyOutput
	stepErr error
	inputs  []strategy.StrategyInput
	mu      sync.Mutex
}

func (s *fakeStrategy) Step(in strategy.StrategyInput) (strategy.StrategyOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, in)
	return s.output, s.stepErr
}

// satisfy EvolvableStrategy. Tick only calls MinEvalBars + DecodeElite;
// the others panic if exercised so misuse fails loudly.
func (s *fakeStrategy) StrategyID() string { return s.id }
func (s *fakeStrategy) MinEvalBars() int   { return 5 }
func (s *fakeStrategy) DecodeElite(_ resultpkg.ChampionGenePayload) (domain.Gene, error) {
	return domain.Gene{0.1, 0.2}, nil
}
func (s *fakeStrategy) Segments() []domain.SegmentInfo { panic("fakeStrategy.Segments not used in Tick tests") }
func (s *fakeStrategy) Sample(_ *rand.Rand) domain.Gene { panic("fakeStrategy.Sample") }
func (s *fakeStrategy) Clamp(g domain.Gene) domain.Gene { return g }
func (s *fakeStrategy) Validate(_ domain.Gene) error    { return nil }
func (s *fakeStrategy) Crossover(p1, _ domain.Gene, _ *rand.Rand) domain.Gene {
	return p1
}
func (s *fakeStrategy) Mutate(g domain.Gene, _, _ float64, _ *rand.Rand) domain.Gene { return g }
func (s *fakeStrategy) Fingerprint(_ domain.Gene) string                             { return "fake-fp" }
func (s *fakeStrategy) Evaluate(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.RawEvaluateResult, error) {
	panic("fakeStrategy.Evaluate not used in Tick tests")
}
func (s *fakeStrategy) ReviewBacktest(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.ReviewSummary, error) {
	return nil, nil
}
func (s *fakeStrategy) EncodeResult(_ domain.Gene, _ resultpkg.SpawnPointPayload, _ resultpkg.ReproducibilityMetadata, _ resultpkg.GAConfigSnapshot, _ *resultpkg.EvaluationLayer, _ *resultpkg.VerificationLayer, _ *resultpkg.DiagnosticsLayer) (resultpkg.ChallengerResultPackage, error) {
	panic("fakeStrategy.EncodeResult not used in Tick tests")
}
func (s *fakeStrategy) NewAdapter(_ *domain.EvaluablePlan) (strategy.Adapter, error) {
	panic("fakeStrategy.NewAdapter not used in Tick tests")
}

type fakeResolver struct {
	strat   *fakeStrategy
	estrat  strategy.EvolvableStrategy
	rstrat  strategy.RuntimeStrategy
	err     error
}

func (r *fakeResolver) Resolve(_ string) (strategy.EvolvableStrategy, strategy.RuntimeStrategy, error) {
	if r.err != nil {
		return nil, nil, r.err
	}
	if r.estrat != nil || r.rstrat != nil {
		return r.estrat, r.rstrat, nil
	}
	return nil, r.strat, nil // common case: only rstrat used by Tick paths the tests exercise
}

type fakeGenes struct {
	gene  domain.Gene
	spawn resultpkg.SpawnPointPayload
	err   error
}

func (f *fakeGenes) Load(_ context.Context, _ string, _ strategy.EvolvableStrategy) (domain.Gene, resultpkg.SpawnPointPayload, error) {
	return f.gene, f.spawn, f.err
}

type recordingDispatcher struct {
	calls []dispatchCall
	err   error
	mu    sync.Mutex
}

type dispatchCall struct {
	instanceID  string
	accountID   string
	symbol      string
	latestClose float64
	orders      []strategy.OrderIntent
}

func (d *recordingDispatcher) Dispatch(_ context.Context, instID, acctID, symbol string, latestClose float64, orders []strategy.OrderIntent) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return d.err
	}
	d.calls = append(d.calls, dispatchCall{instID, acctID, symbol, latestClose, append([]strategy.OrderIntent(nil), orders...)})
	return nil
}

// ===== helpers =====

func newTickRig() (*Manager, *fakePortfolioStore, *fakeRuntimeStore, *recordingDispatcher, *fakeStrategy) {
	ps := &fakePortfolioStore{}
	rs := &fakeRuntimeStore{}
	disp := &recordingDispatcher{}
	strat := &fakeStrategy{id: "fake"}

	// Resolver returns the fake as both EvolvableStrategy and RuntimeStrategy.
	res := &fakeResolver{strat: strat, estrat: strat, rstrat: strat}

	m := New(
		&fakeInstanceStore{},
		ps,
		rs,
		&fakeBarLoader{bars: []domain.Bar{
			{OpenTime: 1_000_000, Close: 100, IsGap: false},
			{OpenTime: 1_000_060_000, Close: 101, IsGap: false},
			{OpenTime: 1_000_120_000, Close: 102, IsGap: false},
		}},
		res,
		&fakeGenes{gene: domain.Gene{0.5}, spawn: resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeRandomOnce}},
		disp,
		nil,
	)
	return m, ps, rs, disp, strat
}

func liveInstance(champID string) store.StrategyInstance {
	cid := champID
	return store.StrategyInstance{
		InstanceID:    "inst-001",
		StrategyID:    "fake",
		Pair:          "BTCUSDT",
		AccountID:     "acct-1",
		OwnerUserID:   1,
		Status:        store.InstanceStatusLive,
		ActiveChampID: &cid,
	}
}

// ===== tests =====

// TestTick_ColdStartHappyPath verifies the first Tick on an instance
// with no prior state writes a fresh PortfolioState, UPSERTs
// RuntimeState, and does not dispatch (strategy emits no orders).
func TestTick_ColdStartHappyPath(t *testing.T) {
	m, ps, rs, disp, strat := newTickRig()
	strat.output = strategy.StrategyOutput{
		RuntimeState: json.RawMessage(`{"cold":true}`),
	}

	if err := m.Tick(context.Background(), liveInstance("ch-001")); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if len(ps.appended) != 1 {
		t.Fatalf("PortfolioState appends = %d, want 1", len(ps.appended))
	}
	got := ps.appended[0]
	if got.InstanceID != "inst-001" {
		t.Errorf("appended InstanceID = %q, want inst-001", got.InstanceID)
	}
	if got.LastProcessedBarTime != 1_000_120_000 {
		t.Errorf("LastProcessedBarTime = %d, want last bar OpenTime 1_000_120_000", got.LastProcessedBarTime)
	}
	if got.DeadBTC != 0 || got.FloatBTC != 0 || got.USDT != 0 {
		t.Errorf("cold-start portfolio must be zeros, got %+v", got)
	}

	if len(rs.upserts) != 1 {
		t.Fatalf("RuntimeState upserts = %d, want 1", len(rs.upserts))
	}
	if string(rs.upserts[0].state) != `{"cold":true}` {
		t.Errorf("RuntimeState upsert state = %s, want strategy output", string(rs.upserts[0].state))
	}

	if len(disp.calls) != 0 {
		t.Errorf("dispatcher should NOT be called when no orders, got %d calls", len(disp.calls))
	}
}

// TestTick_WarmStartCarriesPortfolio verifies the second Tick reads the
// prior PortfolioState and threads it into StrategyInput.Portfolio.
func TestTick_WarmStartCarriesPortfolio(t *testing.T) {
	m, ps, _, _, strat := newTickRig()
	ps.latest = &store.PortfolioState{
		InstanceID:           "inst-001",
		NowMs:                999_999,
		DeadBTC:              0.5,
		FloatBTC:             0.25,
		USDT:                 1_000,
		LastProcessedBarTime: 1_000_060_000,
	}
	strat.output = strategy.StrategyOutput{}

	if err := m.Tick(context.Background(), liveInstance("ch-001")); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if len(strat.inputs) != 1 {
		t.Fatalf("strategy Step called %d times, want 1", len(strat.inputs))
	}
	in := strat.inputs[0]
	if in.Portfolio.DeadBTC != 0.5 || in.Portfolio.FloatBTC != 0.25 || in.Portfolio.USDT != 1_000 {
		t.Errorf("Portfolio not threaded into StrategyInput, got %+v", in.Portfolio)
	}
	if in.LastProcessedBarTime != 1_000_060_000 {
		t.Errorf("LastProcessedBarTime not threaded, got %d", in.LastProcessedBarTime)
	}
}

// TestTick_ReleaseIntentsFlipDeadToFloat verifies Step 8: ReleaseIntent
// reduces DeadBTC and increases FloatBTC by Quantity; no dispatch.
func TestTick_ReleaseIntentsFlipDeadToFloat(t *testing.T) {
	m, ps, _, disp, strat := newTickRig()
	ps.latest = &store.PortfolioState{
		InstanceID: "inst-001",
		DeadBTC:    1.0,
		FloatBTC:   0.0,
	}
	strat.output = strategy.StrategyOutput{
		ReleaseIntents: []strategy.ReleaseIntent{
			{NowMs: 1, Quantity: 0.3, Reason: "macro_release"},
		},
	}

	if err := m.Tick(context.Background(), liveInstance("ch-001")); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := ps.appended[0]
	if got.DeadBTC != 0.7 {
		t.Errorf("DeadBTC = %v, want 0.7 (1.0 - 0.3)", got.DeadBTC)
	}
	if got.FloatBTC != 0.3 {
		t.Errorf("FloatBTC = %v, want 0.3 (0.0 + 0.3)", got.FloatBTC)
	}
	if len(disp.calls) != 0 {
		t.Errorf("ReleaseIntent must NOT dispatch to Agent, got %d calls", len(disp.calls))
	}
}

// TestTick_MacroAndMicroOrdersDispatched verifies Step 9: both macro
// and micro orders combine into one Dispatch call carrying both kinds.
func TestTick_MacroAndMicroOrdersDispatched(t *testing.T) {
	m, _, _, disp, strat := newTickRig()
	strat.output = strategy.StrategyOutput{
		MacroOrders: []strategy.OrderIntent{
			{Kind: strategy.OrderKindMacro, Side: strategy.OrderSideBuy, OrderType: strategy.OrderTypeMarket, QuantityUSD: 500, ClientOrderID: "m-1"},
		},
		MicroOrders: []strategy.OrderIntent{
			{Kind: strategy.OrderKindMicro, Side: strategy.OrderSideSell, OrderType: strategy.OrderTypeLimit, QuantityUSD: 100, LimitPrice: 50_000, ClientOrderID: "u-1"},
		},
	}

	if err := m.Tick(context.Background(), liveInstance("ch-001")); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if len(disp.calls) != 1 {
		t.Fatalf("dispatcher calls = %d, want 1", len(disp.calls))
	}
	c := disp.calls[0]
	if c.instanceID != "inst-001" || c.accountID != "acct-1" {
		t.Errorf("dispatch context mismatch: inst=%q acct=%q", c.instanceID, c.accountID)
	}
	if len(c.orders) != 2 {
		t.Fatalf("dispatched orders = %d, want 2 (macro + micro)", len(c.orders))
	}
	if c.orders[0].Kind != strategy.OrderKindMacro || c.orders[1].Kind != strategy.OrderKindMicro {
		t.Errorf("order order: want [macro, micro], got [%v, %v]", c.orders[0].Kind, c.orders[1].Kind)
	}
}

// TestTick_NoChampionReturnsSentinel verifies an instance with no
// ActiveChampID returns ErrInstanceNoChampion without writing state.
func TestTick_NoChampionReturnsSentinel(t *testing.T) {
	m, ps, rs, _, _ := newTickRig()
	inst := liveInstance("ch-001")
	inst.ActiveChampID = nil

	err := m.Tick(context.Background(), inst)
	if !errors.Is(err, ErrInstanceNoChampion) {
		t.Errorf("err = %v, want ErrInstanceNoChampion", err)
	}
	if len(ps.appended) != 0 {
		t.Errorf("PortfolioState should NOT be written without champion, got %d", len(ps.appended))
	}
	if len(rs.upserts) != 0 {
		t.Errorf("RuntimeState should NOT be written without champion, got %d", len(rs.upserts))
	}
}

// TestTick_ConcurrentInflightReturnsSentinel verifies per-instance
// mutex: the second goroutine sees ErrTickInFlight while the first
// runs.
func TestTick_ConcurrentInflightReturnsSentinel(t *testing.T) {
	m, _, _, _, strat := newTickRig()

	// Force Step to block on a channel so we can race a second Tick.
	gate := make(chan struct{})
	strat.output = strategy.StrategyOutput{}
	// Wrap Step to block via interception: redefine output read by
	// chaining through a Step-call hook. Simplest: spin a goroutine
	// that calls Tick, then immediately attempt second Tick.
	//
	// Easier route: just hold the per-instance mutex manually before
	// invoking Tick. Mimics in-flight without strategy plumbing.
	mu := m.lockFor("inst-001")
	mu.Lock()
	defer mu.Unlock()
	close(gate) // unused gate; kept to mark the pattern

	err := m.Tick(context.Background(), liveInstance("ch-001"))
	if !errors.Is(err, ErrTickInFlight) {
		t.Errorf("err = %v, want ErrTickInFlight", err)
	}
}

// TestTick_StepErrorPropagates verifies a strategy.Step error halts
// the Tick before persistence, surfacing the error wrapped.
func TestTick_StepErrorPropagates(t *testing.T) {
	m, ps, rs, _, strat := newTickRig()
	strat.stepErr = errors.New("synthetic step failure")

	err := m.Tick(context.Background(), liveInstance("ch-001"))
	if err == nil {
		t.Fatal("Tick: want error, got nil")
	}
	if len(ps.appended) != 0 {
		t.Errorf("Step error must NOT persist portfolio, got %d", len(ps.appended))
	}
	if len(rs.upserts) != 0 {
		t.Errorf("Step error must NOT persist runtime state, got %d", len(rs.upserts))
	}
}
