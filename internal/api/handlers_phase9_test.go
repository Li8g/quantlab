package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"quantlab/internal/api/middleware"
	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/auth"
	"quantlab/internal/saas/store"
)

// ===== fakes =====

type fakeTaskLister struct {
	rows []store.EvolutionTask
	err  error
	got  int
}

func (f *fakeTaskLister) List(_ context.Context, limit int) ([]store.EvolutionTask, error) {
	f.got = limit
	return f.rows, f.err
}

type fakeChampionHistory struct {
	listRows  []store.ChampionHistory
	listErr   error
	gotStrat  string
	gotPair   string
	gotLimit  int
	activeRow *store.ChampionHistory
	activeErr error
	byIDRow   *store.ChampionHistory
	byIDErr   error
	gotByID   string
}

func (f *fakeChampionHistory) List(_ context.Context, strategyID, pair string, limit int) ([]store.ChampionHistory, error) {
	f.gotStrat = strategyID
	f.gotPair = pair
	f.gotLimit = limit
	return f.listRows, f.listErr
}

func (f *fakeChampionHistory) GetActive(_ context.Context, strategyID, pair string) (*store.ChampionHistory, error) {
	f.gotStrat = strategyID
	f.gotPair = pair
	return f.activeRow, f.activeErr
}

func (f *fakeChampionHistory) GetByChallengerID(_ context.Context, challengerID string) (*store.ChampionHistory, error) {
	f.gotByID = challengerID
	return f.byIDRow, f.byIDErr
}

type fakeGaps struct {
	rows []store.KLineGap
	err  error
	got  struct {
		symbol, interval string
		limit            int
	}
}

func (f *fakeGaps) List(_ context.Context, symbol, interval string, limit int) ([]store.KLineGap, error) {
	f.got.symbol = symbol
	f.got.interval = interval
	f.got.limit = limit
	return f.rows, f.err
}

type fakeInstanceStore struct {
	inst *store.StrategyInstance
	err  error
}

func (f *fakeInstanceStore) Create(_ context.Context, _ *store.StrategyInstance) error {
	return errors.New("not used in phase 9 tests")
}
func (f *fakeInstanceStore) Get(_ context.Context, _ string) (*store.StrategyInstance, error) {
	return f.inst, f.err
}
func (f *fakeInstanceStore) UpdateStatus(_ context.Context, _ string, _ store.InstanceStatus) error {
	return errors.New("not used")
}
func (f *fakeInstanceStore) SetActiveChampion(_ context.Context, _ string, _ string) error {
	return errors.New("not used")
}

type fakeSharpeBank struct {
	snap SharpeBankStatsSnapshot
	err  error
	got  struct {
		strategyID, pair string
	}
}

func (f *fakeSharpeBank) Stats(_ context.Context, strategyID, pair string) (SharpeBankStatsSnapshot, error) {
	f.got.strategyID = strategyID
	f.got.pair = pair
	return f.snap, f.err
}

type fakeTradeLister struct {
	rows []store.TradeRecord
	err  error
	got  struct {
		instanceID string
		limit      int
	}
}

func (f *fakeTradeLister) ListByInstance(_ context.Context, instanceID string, limit int) ([]store.TradeRecord, error) {
	f.got.instanceID = instanceID
	f.got.limit = limit
	return f.rows, f.err
}

// ===== /evolution/tasks (list) =====

func TestListTasks_HappyPath(t *testing.T) {
	created := time.UnixMilli(1700000000000)
	f := &fakeTaskLister{rows: []store.EvolutionTask{
		{
			Model: gorm.Model{CreatedAt: created},
			TaskID: "t-1", StrategyID: "sigmoid_v1", Pair: "BTCUSDT",
			Interval: "1h", Status: resultpkg.TaskStatusSucceeded,
		},
	}}
	h := &Handlers{TaskLister: f}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/evolution/tasks", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, body=%s", w.Code, w.Body.String())
	}
	var resp ListTasksResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || len(resp.Items) != 1 {
		t.Fatalf("count=%d items=%d", resp.Count, len(resp.Items))
	}
	if resp.Items[0].TaskID != "t-1" {
		t.Errorf("TaskID = %q", resp.Items[0].TaskID)
	}
	if resp.Items[0].CreatedAt != created.UnixMilli() {
		t.Errorf("CreatedAt = %d, want %d", resp.Items[0].CreatedAt, created.UnixMilli())
	}
	if f.got != defaultTaskListLimit {
		t.Errorf("limit = %d, want default %d", f.got, defaultTaskListLimit)
	}
}

func TestListTasks_LimitClampedToMax(t *testing.T) {
	f := &fakeTaskLister{}
	h := &Handlers{TaskLister: f}
	doJSON(newRouter(h), http.MethodGet, "/api/v1/evolution/tasks?limit=9999", nil)
	if f.got != maxTaskListLimit {
		t.Errorf("limit = %d, want %d (clamped)", f.got, maxTaskListLimit)
	}
}

func TestListTasks_GarbageLimitFallsBackToDefault(t *testing.T) {
	f := &fakeTaskLister{}
	h := &Handlers{TaskLister: f}
	doJSON(newRouter(h), http.MethodGet, "/api/v1/evolution/tasks?limit=abc", nil)
	if f.got != defaultTaskListLimit {
		t.Errorf("limit = %d, want default", f.got)
	}
}

func TestListTasks_RepoError500(t *testing.T) {
	h := &Handlers{TaskLister: &fakeTaskLister{err: errors.New("boom")}}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/evolution/tasks", nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("Code = %d, want 500", w.Code)
	}
}

func TestListTasks_RouteNotRegisteredWhenListerNil(t *testing.T) {
	// Confirm Phase 9 routes silently fall away when the collaborator
	// is nil — protects callers that build a Handlers struct for
	// older endpoints without bothering with the new fields.
	h := &Handlers{Epoch: &fakeEpoch{}} // no TaskLister
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/evolution/tasks", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404 (route should not be registered)", w.Code)
	}
}

// ===== /champions/history =====

func TestListChampionHistory_HappyPath(t *testing.T) {
	promoted := time.UnixMilli(1700000000000)
	retired := promoted.Add(24 * time.Hour)
	retiredBy := "admin@example"
	f := &fakeChampionHistory{listRows: []store.ChampionHistory{
		{
			Model: gorm.Model{ID: 1},
			StrategyID: "sigmoid_v1", Pair: "BTCUSDT",
			ChallengerID: "c-001", PromotedAt: promoted,
			RetiredAt: &retired, RetiredBy: &retiredBy,
		},
		{
			Model: gorm.Model{ID: 2},
			StrategyID: "sigmoid_v1", Pair: "BTCUSDT",
			ChallengerID: "c-002", PromotedAt: retired,
		},
	}}
	h := &Handlers{ChampionHistory: f}
	w := doJSON(newRouter(h), http.MethodGet,
		"/api/v1/champions/history?strategy_id=sigmoid_v1&pair=BTCUSDT", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, body=%s", w.Code, w.Body.String())
	}
	var resp ListChampionHistoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 2 || len(resp.Items) != 2 {
		t.Fatalf("count=%d items=%d", resp.Count, len(resp.Items))
	}
	if resp.Items[0].RetiredAtMs == nil {
		t.Error("first item RetiredAtMs nil; want set")
	}
	if resp.Items[1].RetiredAtMs != nil {
		t.Error("second item RetiredAtMs set; want nil (active champion)")
	}
	if f.gotStrat != "sigmoid_v1" || f.gotPair != "BTCUSDT" {
		t.Errorf("filter args = %q/%q", f.gotStrat, f.gotPair)
	}
}

func TestListChampionHistory_NoFilters(t *testing.T) {
	f := &fakeChampionHistory{}
	h := &Handlers{ChampionHistory: f}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/champions/history", nil)
	if w.Code != http.StatusOK {
		t.Errorf("Code = %d, body=%s", w.Code, w.Body.String())
	}
	if f.gotStrat != "" || f.gotPair != "" {
		t.Errorf("filters should be empty; got %q/%q", f.gotStrat, f.gotPair)
	}
}

// ===== /genome/champion =====

func TestGetChampionGenome_HappyPath(t *testing.T) {
	promoted := time.UnixMilli(1700000000000)
	score := 2.5
	gene := &store.GeneRecord{
		ChallengerID: "c-001", StrategyID: "sigmoid_v1", Pair: "BTCUSDT",
		ScoreTotal: &score,
		PlanHash:   "ph-abc", BarsHash: "bh-xyz",
	}
	champFake := &fakeChampionHistory{activeRow: &store.ChampionHistory{
		StrategyID: "sigmoid_v1", Pair: "BTCUSDT",
		ChallengerID: "c-001", PromotedAt: promoted,
	}}
	h := &Handlers{
		ChampionHistory: champFake,
		Challengers:     &fakeChallengers{rec: gene},
	}
	w := doJSON(newRouter(h), http.MethodGet,
		"/api/v1/genome/champion?strategy_id=sigmoid_v1&pair=BTCUSDT", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, body=%s", w.Code, w.Body.String())
	}
	var resp ChampionGenomeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ChallengerID != "c-001" {
		t.Errorf("ChallengerID = %q", resp.ChallengerID)
	}
	if resp.ScoreTotal == nil || *resp.ScoreTotal != 2.5 {
		t.Errorf("ScoreTotal = %v, want 2.5", resp.ScoreTotal)
	}
	if resp.PlanHash != "ph-abc" {
		t.Errorf("PlanHash = %q", resp.PlanHash)
	}
}

func TestGetChampionGenome_MissingQueryParamsReturns400(t *testing.T) {
	h := &Handlers{ChampionHistory: &fakeChampionHistory{}}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/genome/champion", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Code = %d, want 400", w.Code)
	}
}

func TestGetChampionGenome_NoActiveChampReturns404(t *testing.T) {
	h := &Handlers{ChampionHistory: &fakeChampionHistory{activeErr: gorm.ErrRecordNotFound}}
	w := doJSON(newRouter(h), http.MethodGet,
		"/api/v1/genome/champion?strategy_id=sigmoid_v1&pair=BTCUSDT", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404", w.Code)
	}
}

func TestGetChampionGenome_ScoreEnrichmentBestEffort(t *testing.T) {
	// Missing GeneRecord should not 500 — fall through with score nil.
	promoted := time.UnixMilli(1700000000000)
	champFake := &fakeChampionHistory{activeRow: &store.ChampionHistory{
		StrategyID: "sigmoid_v1", Pair: "BTCUSDT",
		ChallengerID: "c-orphan", PromotedAt: promoted,
	}}
	h := &Handlers{
		ChampionHistory: champFake,
		Challengers:     &fakeChallengers{err: gorm.ErrRecordNotFound},
	}
	w := doJSON(newRouter(h), http.MethodGet,
		"/api/v1/genome/champion?strategy_id=sigmoid_v1&pair=BTCUSDT", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200 (score enrichment is best-effort); body=%s",
			w.Code, w.Body.String())
	}
	var resp ChampionGenomeResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ChallengerID != "c-orphan" {
		t.Errorf("ChallengerID = %q", resp.ChallengerID)
	}
	if resp.ScoreTotal != nil {
		t.Errorf("ScoreTotal = %v, want nil (gene missing)", resp.ScoreTotal)
	}
}

// ===== /data/gaps =====

func TestListGaps_HappyPath(t *testing.T) {
	detected := time.UnixMilli(1700000000000)
	f := &fakeGaps{rows: []store.KLineGap{
		{Symbol: "BTCUSDT", Interval: "1h", GapStartMs: 1, GapEndMs: 11, DetectedAt: detected},
	}}
	h := &Handlers{Gaps: f}
	w := doJSON(newRouter(h), http.MethodGet,
		"/api/v1/data/gaps?symbol=BTCUSDT&interval=1h", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, body=%s", w.Code, w.Body.String())
	}
	var resp ListGapsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 {
		t.Fatalf("count=%d", resp.Count)
	}
	if resp.Items[0].DurationMs != 10 {
		t.Errorf("DurationMs = %d, want 10 (server-computed)", resp.Items[0].DurationMs)
	}
	if f.got.symbol != "BTCUSDT" || f.got.interval != "1h" {
		t.Errorf("repo args = %+v", f.got)
	}
	if f.got.limit != defaultGapsLimit {
		t.Errorf("limit = %d, want default", f.got.limit)
	}
}

func TestListGaps_MissingQueryParamsReturns400(t *testing.T) {
	h := &Handlers{Gaps: &fakeGaps{}}
	// Both missing.
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/data/gaps", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Code = %d, want 400", w.Code)
	}
	// Symbol only.
	w = doJSON(newRouter(h), http.MethodGet, "/api/v1/data/gaps?symbol=BTCUSDT", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Code = %d, want 400 (interval missing)", w.Code)
	}
}

func TestListGaps_LimitClamped(t *testing.T) {
	f := &fakeGaps{}
	h := &Handlers{Gaps: f}
	doJSON(newRouter(h), http.MethodGet,
		"/api/v1/data/gaps?symbol=BTCUSDT&interval=1h&limit=9999", nil)
	if f.got.limit != maxGapsLimit {
		t.Errorf("limit = %d, want %d", f.got.limit, maxGapsLimit)
	}
}

// ===== /data/coverage =====

type fakeCoverage struct {
	rows []DataCoverageRow
	err  error
	got  struct {
		symbol, interval string
	}
}

func (f *fakeCoverage) Coverage(_ context.Context, symbol, interval string) ([]DataCoverageRow, error) {
	f.got.symbol = symbol
	f.got.interval = interval
	return f.rows, f.err
}

func TestGetCoverage_SinglePair(t *testing.T) {
	f := &fakeCoverage{rows: []DataCoverageRow{
		{Symbol: "BTCUSDT", Interval: "1h", MinOpenMs: 100, MaxOpenMs: 900, BarCount: 9},
	}}
	h := &Handlers{Klines: f}
	w := doJSON(newRouter(h), http.MethodGet,
		"/api/v1/data/coverage?symbol=BTCUSDT&interval=1h", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, body=%s", w.Code, w.Body.String())
	}
	var resp ListCoverageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || resp.Items[0].BarCount != 9 {
		t.Fatalf("resp = %+v", resp)
	}
	if f.got.symbol != "BTCUSDT" || f.got.interval != "1h" {
		t.Errorf("repo args = %+v, both should pass through as filter", f.got)
	}
}

func TestGetCoverage_ListAll_NoParams(t *testing.T) {
	f := &fakeCoverage{rows: []DataCoverageRow{
		{Symbol: "BTCUSDT", Interval: "1h", MinOpenMs: 1, MaxOpenMs: 2, BarCount: 2},
		{Symbol: "ETHUSDT", Interval: "1d", MinOpenMs: 3, MaxOpenMs: 4, BarCount: 5},
	}}
	h := &Handlers{Klines: f}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/data/coverage", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, body=%s", w.Code, w.Body.String())
	}
	var resp ListCoverageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 2 {
		t.Fatalf("count=%d, want 2 (inventory listing)", resp.Count)
	}
	if f.got.symbol != "" || f.got.interval != "" {
		t.Errorf("repo args = %+v, want empty (list-all path)", f.got)
	}
}

func TestGetCoverage_LoneParamReturns400(t *testing.T) {
	h := &Handlers{Klines: &fakeCoverage{}}
	// symbol without interval
	w := doJSON(newRouter(h), http.MethodGet,
		"/api/v1/data/coverage?symbol=BTCUSDT", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Code = %d, want 400 (lone symbol)", w.Code)
	}
	// interval without symbol
	w = doJSON(newRouter(h), http.MethodGet,
		"/api/v1/data/coverage?interval=1h", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Code = %d, want 400 (lone interval)", w.Code)
	}
}

// ===== /instances/:id/trades =====

func TestListInstanceTrades_HappyPath_NoAuth(t *testing.T) {
	// Without AuthRequired in the chain, canViewInstance returns true
	// — exercise the happy path with that simplification.
	inst := &store.StrategyInstance{InstanceID: "inst-1", OwnerUserID: 42}
	created := time.UnixMilli(1700000000000)
	limit := 50000.0
	trades := []store.TradeRecord{
		{
			ID: 1, ClientOrderID: "co-1", InstanceID: "inst-1",
			Symbol: "BTCUSDT", Side: "buy", OrderType: "limit",
			QuantityUSD: 100, LimitPrice: &limit,
			NowMsAtSaaS: 1, ValidUntilMs: 2,
			Status: store.TradeStatusFilled, CreatedAt: created,
		},
	}
	h := &Handlers{
		Instances: &fakeInstanceStore{inst: inst},
		Trades:    &fakeTradeLister{rows: trades},
	}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/instances/inst-1/trades", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, body=%s", w.Code, w.Body.String())
	}
	var resp ListInstanceTradesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || resp.Items[0].LimitPrice == nil || *resp.Items[0].LimitPrice != 50000.0 {
		t.Errorf("response = %+v", resp)
	}
}

func TestListInstanceTrades_InstanceNotFound404(t *testing.T) {
	h := &Handlers{
		Instances: &fakeInstanceStore{err: gorm.ErrRecordNotFound},
		Trades:    &fakeTradeLister{},
	}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/instances/missing/trades", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404", w.Code)
	}
}

func TestListInstanceTrades_OperatorOnlySeesOwnInstance403(t *testing.T) {
	// Operator claims (UserID=99) reading an instance owned by UserID=42 → 403.
	inst := &store.StrategyInstance{InstanceID: "inst-1", OwnerUserID: 42}
	h := &Handlers{
		Instances: &fakeInstanceStore{inst: inst},
		Trades:    &fakeTradeLister{},
	}
	r := gin.New()
	// Mid-stream installs Claims so the ownership check sees them.
	r.Use(func(c *gin.Context) {
		c.Set("quantlab.auth.claims", &auth.Claims{UserID: 99, Role: string(store.UserRoleOperator)})
	})
	h.Register(r)
	w := doRequest(r, http.MethodGet, "/api/v1/instances/inst-1/trades", nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("Code = %d, want 403", w.Code)
	}
}

func TestListInstanceTrades_AdminSeesAnyInstance(t *testing.T) {
	inst := &store.StrategyInstance{InstanceID: "inst-1", OwnerUserID: 42}
	h := &Handlers{
		Instances: &fakeInstanceStore{inst: inst},
		Trades:    &fakeTradeLister{},
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("quantlab.auth.claims", &auth.Claims{UserID: 99, Role: string(store.UserRoleAdmin)})
	})
	h.Register(r)
	w := doRequest(r, http.MethodGet, "/api/v1/instances/inst-1/trades", nil)
	if w.Code != http.StatusOK {
		t.Errorf("Code = %d, want 200 (admin must read any instance); body=%s",
			w.Code, w.Body.String())
	}
}

func TestListInstanceTrades_OwnerSeesOwnInstance(t *testing.T) {
	inst := &store.StrategyInstance{InstanceID: "inst-1", OwnerUserID: 42}
	h := &Handlers{
		Instances: &fakeInstanceStore{inst: inst},
		Trades:    &fakeTradeLister{},
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("quantlab.auth.claims", &auth.Claims{UserID: 42, Role: string(store.UserRoleOperator)})
	})
	h.Register(r)
	w := doRequest(r, http.MethodGet, "/api/v1/instances/inst-1/trades", nil)
	if w.Code != http.StatusOK {
		t.Errorf("Code = %d, want 200 (owner)", w.Code)
	}
}

// ===== /ga/sharpebank/stats =====

func TestGetSharpeBankStats_HappyPath_DSREligible(t *testing.T) {
	f := &fakeSharpeBank{snap: SharpeBankStatsSnapshot{
		N: 7, SharpeMean: 1.2, SharpeVariance: 0.05,
	}}
	h := &Handlers{SharpeBank: f}
	w := doJSON(newRouter(h), http.MethodGet,
		"/api/v1/ga/sharpebank/stats?strategy_id=sigmoid_v1&pair=BTCUSDT", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, body=%s", w.Code, w.Body.String())
	}
	var resp SharpeBankStatsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.N != 7 || resp.SharpeMean != 1.2 || resp.SharpeVariance != 0.05 {
		t.Errorf("resp = %+v", resp)
	}
	if resp.MinTrialsForDSR != 5 {
		t.Errorf("MinTrialsForDSR = %d, want 5", resp.MinTrialsForDSR)
	}
	if !resp.DSREligible {
		t.Error("DSREligible = false; N=7 ≥ 5 → must be eligible")
	}
}

func TestGetSharpeBankStats_BelowMinTrialsIneligible(t *testing.T) {
	f := &fakeSharpeBank{snap: SharpeBankStatsSnapshot{N: 3}}
	h := &Handlers{SharpeBank: f}
	w := doJSON(newRouter(h), http.MethodGet,
		"/api/v1/ga/sharpebank/stats?strategy_id=sigmoid_v1&pair=BTCUSDT", nil)
	var resp SharpeBankStatsResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.DSREligible {
		t.Errorf("DSREligible = true at N=%d; want false (gate is %d)",
			resp.N, resp.MinTrialsForDSR)
	}
}

func TestGetSharpeBankStats_MissingQueryParamsReturns400(t *testing.T) {
	h := &Handlers{SharpeBank: &fakeSharpeBank{}}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/ga/sharpebank/stats", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Code = %d, want 400", w.Code)
	}
}

func TestGetSharpeBankStats_RepoError500(t *testing.T) {
	h := &Handlers{SharpeBank: &fakeSharpeBank{err: errors.New("db down")}}
	w := doJSON(newRouter(h), http.MethodGet,
		"/api/v1/ga/sharpebank/stats?strategy_id=s&pair=p", nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("Code = %d, want 500", w.Code)
	}
}

func TestGetSharpeBankStats_RouteNotRegisteredWhenStatterNil(t *testing.T) {
	h := &Handlers{} // no SharpeBank
	w := doJSON(newRouter(h), http.MethodGet,
		"/api/v1/ga/sharpebank/stats?strategy_id=s&pair=p", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404", w.Code)
	}
}

// ===== shared helpers =====

// doRequest is doJSON's sibling for tests that pre-build a router
// (e.g. with custom Claims middleware) — it doesn't marshal a body
// nor call h.Register itself.
func doRequest(r *gin.Engine, method, path string, _ []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// silence unused-import: middleware is used indirectly via the
// claims-key constant. ClaimsFrom remains the public read API.
var _ = middleware.ClaimsFrom