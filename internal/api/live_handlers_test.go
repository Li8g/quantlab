package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gorm.io/gorm"

	"quantlab/internal/saas/auth"
	"quantlab/internal/saas/store"
)

// ===== fakes (the three live-monitor collaborators) =====

type fakeInstanceLister struct {
	rows []store.StrategyInstance
	err  error
}

func (f *fakeInstanceLister) ListLive(_ context.Context) ([]store.StrategyInstance, error) {
	return f.rows, f.err
}

type fakePortfolio struct {
	ps  *store.PortfolioState
	err error
}

func (f *fakePortfolio) Latest(_ context.Context, _ string) (*store.PortfolioState, error) {
	return f.ps, f.err
}

type fakePresence struct{ connected bool }

func (f *fakePresence) IsConnected(_ string) bool { return f.connected }

type fakeExecutionLister struct {
	rows   []store.SpotExecution
	err    error
	gotIDs []string
}

func (f *fakeExecutionLister) ListExecutionsForOrders(_ context.Context, ids []string) ([]store.SpotExecution, error) {
	f.gotIDs = ids
	return f.rows, f.err
}

type fakePriceReader struct {
	k   *store.KLine
	err error
}

func (f *fakePriceReader) LatestClose(_ context.Context, _, _ string) (*store.KLine, error) {
	return f.k, f.err
}

func liveJSON(t *testing.T, body []byte) InstanceLiveResponse {
	t.Helper()
	var out InstanceLiveResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode InstanceLiveResponse: %v", err)
	}
	return out
}

// ===== GET /api/v1/instances (list, owner-scoped) =====

func TestListInstances(t *testing.T) {
	// Three live instances across two owners; admin sees all, a
	// viewer sees only its own.
	rows := []store.StrategyInstance{
		{InstanceID: "i-a", OwnerUserID: 7, Status: store.InstanceStatusLive},
		{InstanceID: "i-b", OwnerUserID: 9, Status: store.InstanceStatusLive},
		{InstanceID: "i-c", OwnerUserID: 7, Status: store.InstanceStatusLive},
	}

	cases := []struct {
		name     string
		role     store.UserRole
		userID   uint
		listErr  error
		wantCode int
		wantIDs  []string // expected instance_ids in response, order-preserved
	}{
		{
			name: "admin sees all owners", role: store.UserRoleAdmin, userID: 1,
			wantCode: http.StatusOK, wantIDs: []string{"i-a", "i-b", "i-c"},
		},
		{
			name: "viewer sees only own", role: store.UserRoleViewer, userID: 7,
			wantCode: http.StatusOK, wantIDs: []string{"i-a", "i-c"},
		},
		{
			name: "operator with no instances sees none", role: store.UserRoleOperator, userID: 99,
			wantCode: http.StatusOK, wantIDs: []string{},
		},
		{
			name: "repo error → 500", role: store.UserRoleAdmin, userID: 1,
			listErr: gorm.ErrInvalidDB, wantCode: http.StatusInternalServerError,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &Handlers{InstanceList: &fakeInstanceLister{rows: rows, err: c.listErr}}
			r := withClaimsHandlers(h, &auth.Claims{UserID: c.userID, Role: string(c.role)})

			rec := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/instances", nil)
			r.ServeHTTP(rec, req)

			if rec.Code != c.wantCode {
				t.Fatalf("Code = %d, want %d; body=%s", rec.Code, c.wantCode, rec.Body.String())
			}
			if c.wantCode != http.StatusOK {
				return
			}
			var resp InstanceListResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Count != len(c.wantIDs) {
				t.Errorf("Count = %d, want %d", resp.Count, len(c.wantIDs))
			}
			got := make([]string, len(resp.Items))
			for i, it := range resp.Items {
				got[i] = it.InstanceID
			}
			if !equalStrings(got, c.wantIDs) {
				t.Errorf("instance_ids = %v, want %v", got, c.wantIDs)
			}
		})
	}
}

// ===== GET /api/v1/instances/:id/live (aggregate snapshot) =====

func TestGetInstanceLive(t *testing.T) {
	const owner = 7
	samplePortfolio := &store.PortfolioState{
		InstanceID: "i-1", NowMs: 1_700_000_000_000,
		FloatBTC: 0.5, USDT: 1234.5, LastProcessedBarTime: 1_699_999_000_000,
	}
	sampleTrades := []store.TradeRecord{
		{ClientOrderID: "co-1", Symbol: "BTCUSDT", Side: "buy", OrderType: "market", Status: store.TradeStatusFilled},
		{ClientOrderID: "co-2", Symbol: "BTCUSDT", Side: "sell", OrderType: "limit", Status: store.TradeStatusPending},
	}

	cases := []struct {
		name      string
		role      store.UserRole
		userID    uint
		seed      bool // seed the instance into the store
		instErr   error
		portfolio *store.PortfolioState
		portErr   error
		presence   *fakePresence        // nil → collaborator not wired
		trades     []store.TradeRecord
		tradesErr  error
		executions *fakeExecutionLister // nil → collaborator not wired
		prices     *fakePriceReader     // nil → collaborator not wired
		wantCode   int
		check      func(t *testing.T, resp InstanceLiveResponse, trades *fakeTradeLister)
	}{
		{
			name: "happy: all blocks present", role: store.UserRoleAdmin, userID: 1,
			seed: true, portfolio: samplePortfolio, presence: &fakePresence{connected: true},
			trades: sampleTrades, wantCode: http.StatusOK,
			check: func(t *testing.T, resp InstanceLiveResponse, tr *fakeTradeLister) {
				if resp.Portfolio == nil || resp.Portfolio.USDT != 1234.5 {
					t.Errorf("portfolio = %+v, want USDT 1234.5", resp.Portfolio)
				}
				if resp.Connection == nil || !resp.Connection.Connected {
					t.Errorf("connection = %+v, want connected=true", resp.Connection)
				}
				if len(resp.RecentTrades) != 2 {
					t.Errorf("RecentTrades len = %d, want 2", len(resp.RecentTrades))
				}
				if tr.got.limit != liveSnapshotTrades {
					t.Errorf("trade limit = %d, want %d", tr.got.limit, liveSnapshotTrades)
				}
			},
		},
		{
			name: "no tick yet: portfolio omitted", role: store.UserRoleAdmin, userID: 1,
			seed: true, portfolio: nil, presence: &fakePresence{connected: true},
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp InstanceLiveResponse, _ *fakeTradeLister) {
				if resp.Portfolio != nil {
					t.Errorf("portfolio = %+v, want omitted (nil)", resp.Portfolio)
				}
			},
		},
		{
			name: "no Hub in process: connection omitted", role: store.UserRoleAdmin, userID: 1,
			seed: true, portfolio: samplePortfolio, presence: nil,
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp InstanceLiveResponse, _ *fakeTradeLister) {
				if resp.Connection != nil {
					t.Errorf("connection = %+v, want omitted (Presence nil)", resp.Connection)
				}
			},
		},
		{
			name: "agent disconnected: connected=false", role: store.UserRoleAdmin, userID: 1,
			seed: true, portfolio: samplePortfolio, presence: &fakePresence{connected: false},
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp InstanceLiveResponse, _ *fakeTradeLister) {
				if resp.Connection == nil || resp.Connection.Connected {
					t.Errorf("connection = %+v, want connected=false", resp.Connection)
				}
			},
		},
		{
			name: "owner viewer allowed", role: store.UserRoleViewer, userID: owner,
			seed: true, portfolio: samplePortfolio, presence: &fakePresence{connected: true},
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp InstanceLiveResponse, _ *fakeTradeLister) {
				if resp.Instance.InstanceID != "i-1" {
					t.Errorf("instance_id = %q, want i-1", resp.Instance.InstanceID)
				}
			},
		},
		{
			name: "cross-owner viewer → 403", role: store.UserRoleViewer, userID: 999,
			seed: true, portfolio: samplePortfolio, wantCode: http.StatusForbidden,
		},
		{
			name: "missing instance → 404", role: store.UserRoleAdmin, userID: 1,
			seed: false, instErr: gorm.ErrRecordNotFound, wantCode: http.StatusNotFound,
		},
		{
			name: "portfolio read error → 500", role: store.UserRoleAdmin, userID: 1,
			seed: true, portErr: gorm.ErrInvalidDB, wantCode: http.StatusInternalServerError,
		},
		{
			name: "trade read error → 500", role: store.UserRoleAdmin, userID: 1,
			seed: true, portfolio: samplePortfolio, tradesErr: gorm.ErrInvalidDB,
			wantCode: http.StatusInternalServerError,
		},
		{
			name: "fills folded onto matching orders", role: store.UserRoleAdmin, userID: 1,
			seed: true, trades: sampleTrades,
			executions: &fakeExecutionLister{rows: []store.SpotExecution{
				{ClientOrderID: "co-1", ExchangeOrderID: "ex-1a", FillQuantity: 0.3, FillPrice: 60000},
				{ClientOrderID: "co-1", ExchangeOrderID: "ex-1b", FillQuantity: 0.2, FillPrice: 60010},
				{ClientOrderID: "co-2", ExchangeOrderID: "ex-2a", FillQuantity: 0.1, FillPrice: 61000},
			}},
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp InstanceLiveResponse, _ *fakeTradeLister) {
				byOrder := map[string]int{}
				for _, tr := range resp.RecentTrades {
					byOrder[tr.ClientOrderID] = len(tr.Fills)
				}
				if byOrder["co-1"] != 2 {
					t.Errorf("co-1 fills = %d, want 2", byOrder["co-1"])
				}
				if byOrder["co-2"] != 1 {
					t.Errorf("co-2 fills = %d, want 1", byOrder["co-2"])
				}
			},
		},
		{
			name: "no ExecutionLister: fills omitted", role: store.UserRoleAdmin, userID: 1,
			seed: true, trades: sampleTrades, executions: nil,
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp InstanceLiveResponse, _ *fakeTradeLister) {
				for _, tr := range resp.RecentTrades {
					if tr.Fills != nil {
						t.Errorf("order %s fills = %v, want nil (collaborator unwired)", tr.ClientOrderID, tr.Fills)
					}
				}
			},
		},
		{
			name: "execution read error → 500", role: store.UserRoleAdmin, userID: 1,
			seed: true, trades: sampleTrades,
			executions: &fakeExecutionLister{err: gorm.ErrInvalidDB},
			wantCode:   http.StatusInternalServerError,
		},
		{
			name: "equity marked to market (Cold excluded)", role: store.UserRoleAdmin, userID: 1,
			seed: true,
			portfolio: &store.PortfolioState{
				DeadBTC: 0.3, FloatBTC: 0.2, ColdSealedBTC: 1.0, USDT: 100, NowMs: 5,
			},
			prices:   &fakePriceReader{k: &store.KLine{Close: 60000, OpenTime: 999}},
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp InstanceLiveResponse, _ *fakeTradeLister) {
				// (0.3+0.2)*60000 + 100 = 30100; the 1.0 ColdSealedBTC
				// must NOT be counted (would push it to 90100).
				if resp.Portfolio == nil || resp.Portfolio.Equity == nil {
					t.Fatalf("equity missing: %+v", resp.Portfolio)
				}
				if *resp.Portfolio.Equity != 30100 {
					t.Errorf("equity = %v, want 30100 (Cold excluded)", *resp.Portfolio.Equity)
				}
				if resp.Portfolio.MarkPrice == nil || *resp.Portfolio.MarkPrice != 60000 {
					t.Errorf("mark_price = %v, want 60000", resp.Portfolio.MarkPrice)
				}
				if resp.Portfolio.MarkPriceMs != 999 {
					t.Errorf("mark_price_ms = %d, want 999", resp.Portfolio.MarkPriceMs)
				}
			},
		},
		{
			name: "no bar: equity omitted", role: store.UserRoleAdmin, userID: 1,
			seed: true, portfolio: samplePortfolio,
			prices:   &fakePriceReader{k: nil},
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp InstanceLiveResponse, _ *fakeTradeLister) {
				if resp.Portfolio == nil || resp.Portfolio.Equity != nil {
					t.Errorf("equity = %v, want nil (no bar)", resp.Portfolio.Equity)
				}
			},
		},
		{
			name: "no PriceReader: equity omitted", role: store.UserRoleAdmin, userID: 1,
			seed: true, portfolio: samplePortfolio, prices: nil,
			wantCode: http.StatusOK,
			check: func(t *testing.T, resp InstanceLiveResponse, _ *fakeTradeLister) {
				if resp.Portfolio == nil || resp.Portfolio.Equity != nil {
					t.Errorf("equity = %v, want nil (collaborator unwired)", resp.Portfolio.Equity)
				}
			},
		},
		{
			name: "price read error → 500", role: store.UserRoleAdmin, userID: 1,
			seed: true, portfolio: samplePortfolio,
			prices:   &fakePriceReader{err: gorm.ErrInvalidDB},
			wantCode: http.StatusInternalServerError,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			insts := newFakeInstances()
			if c.seed {
				insts.byID["i-1"] = &store.StrategyInstance{
					InstanceID: "i-1", OwnerUserID: owner, AccountID: "acct-1",
					Status: store.InstanceStatusLive,
				}
			}
			insts.getErr = c.instErr
			trades := &fakeTradeLister{rows: c.trades, err: c.tradesErr}
			h := &Handlers{
				Instances:  insts,
				Portfolios: &fakePortfolio{ps: c.portfolio, err: c.portErr},
				Trades:     trades,
			}
			if c.presence != nil {
				h.Presence = c.presence
			}
			if c.executions != nil {
				h.Executions = c.executions
			}
			if c.prices != nil {
				h.Prices = c.prices
			}
			r := withClaimsHandlers(h, &auth.Claims{UserID: c.userID, Role: string(c.role)})

			rec := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/instances/i-1/live", nil)
			r.ServeHTTP(rec, req)

			if rec.Code != c.wantCode {
				t.Fatalf("Code = %d, want %d; body=%s", rec.Code, c.wantCode, rec.Body.String())
			}
			if c.wantCode != http.StatusOK || c.check == nil {
				return
			}
			c.check(t, liveJSON(t, rec.Body.Bytes()), trades)
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
