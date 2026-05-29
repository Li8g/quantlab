// Live-monitor read surface (frontend 场景② F2). Two endpoints, both
// pull-only — there is NO browser-facing push channel; /ws/agent is
// SaaS↔Agent. The browser polls these:
//
//	GET /api/v1/instances              — live instance list (owner-scoped)
//	GET /api/v1/instances/:id/live     — one instance's aggregate snapshot
//
// Data-source map (why each field is pull-able):
//   - instance row        → DB (StrategyInstance)
//   - portfolio / equity  → DB hypertable portfolio_states (PortfolioRepo.Latest)
//   - recent trades       → DB (TradeRecord, persisted by cmd/saas/agentmsg.go)
//   - connection health   → Hub in-memory Registry — the ONLY piece with
//                           process affinity. Optional: when Presence is
//                           nil (stateless replica that doesn't hold the
//                           Hub) the `connection` block is simply omitted.
//
// Both endpoints are registered only when their collaborator is non-nil,
// matching the Phase 9 nil-skip convention so existing handler tests
// don't have to wire new fakes.
package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"quantlab/internal/saas/store"
)

// liveSnapshotTrades is the default trade count folded into the /live
// aggregate — smaller than the dedicated /trades list (100) because the
// snapshot is polled every few seconds and only needs the recent tail.
const liveSnapshotTrades = 20

// InstanceLister lists currently-live instances. Backed by
// InstanceRepo.ListLive (status = 'live'). Owner-scoping is applied in
// the handler, not the repo, so admins can see every live instance.
type InstanceLister interface {
	ListLive(ctx context.Context) ([]store.StrategyInstance, error)
}

// PortfolioReader reads the latest persisted portfolio state for an
// instance. Backed by PortfolioRepo over the portfolio_states
// hypertable. Returns (nil, nil) when the instance has not ticked yet.
type PortfolioReader interface {
	Latest(ctx context.Context, instanceID string) (*store.PortfolioState, error)
}

// AgentPresence answers "is this account's Agent connected and past
// handshake". Backed by the wshub Registry, so it is only meaningful in
// the process that holds the Hub (cmd/saas). A stateless API replica
// leaves it nil and /live omits the connection block.
type AgentPresence interface {
	IsConnected(accountID string) bool
}

// ExecutionLister fetches the fill rows for a set of orders in one
// query. Backed by TradeRepo.ListExecutionsForOrders. Nil-skippable:
// when nil, /live still returns the trade tail but without per-order
// fill detail (the `fills` key is simply absent).
type ExecutionLister interface {
	ListExecutionsForOrders(ctx context.Context, clientOrderIDs []string) ([]store.SpotExecution, error)
}

// ListInstances: GET /api/v1/instances. Returns every live instance the
// caller may view (own instances for viewer/operator; all for admin).
func (h *Handlers) ListInstances(c *gin.Context) {
	rows, err := h.InstanceList.ListLive(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	items := make([]InstanceResponse, 0, len(rows))
	for i := range rows {
		if !canViewInstance(c, &rows[i]) {
			continue
		}
		items = append(items, toInstanceResponse(&rows[i]))
	}
	c.JSON(http.StatusOK, InstanceListResponse{Items: items, Count: len(items)})
}

// GetInstanceLive: GET /api/v1/instances/:instance_id/live. Aggregates
// the instance row, latest portfolio, optional connection health, and
// the recent trade tail into one pull-able snapshot.
func (h *Handlers) GetInstanceLive(c *gin.Context) {
	id := c.Param("instance_id")
	if id == "" {
		writeError(c, http.StatusBadRequest, errors.New("instance_id is required"))
		return
	}
	inst, err := h.Instances.Get(c.Request.Context(), id)
	if err != nil {
		writeError(c, mapReadErr(err), err)
		return
	}
	if !canViewInstance(c, inst) {
		writeError(c, http.StatusForbidden,
			errors.New("instance owned by another user"))
		return
	}

	resp := InstanceLiveResponse{
		Instance:     toInstanceResponse(inst),
		RecentTrades: []TradeRecordSummary{},
	}

	// Portfolio — DB pull. (nil, nil) means "no tick yet": leave omitted.
	if h.Portfolios != nil {
		ps, err := h.Portfolios.Latest(c.Request.Context(), id)
		if err != nil {
			writeError(c, http.StatusInternalServerError, err)
			return
		}
		if ps != nil {
			resp.Portfolio = &PortfolioSnapshotView{
				DeadBTC:              ps.DeadBTC,
				FloatBTC:             ps.FloatBTC,
				ColdSealedBTC:        ps.ColdSealedBTC,
				USDT:                 ps.USDT,
				NowMs:                ps.NowMs,
				LastProcessedBarTime: ps.LastProcessedBarTime,
			}
		}
	}

	// Connection health — Hub in-memory, optional (process-affine).
	if h.Presence != nil {
		resp.Connection = &ConnectionHealth{
			Connected: h.Presence.IsConnected(inst.AccountID),
		}
	}

	// Recent trade tail — DB pull, reuses the same store as /trades.
	if h.Trades != nil {
		rows, err := h.Trades.ListByInstance(c.Request.Context(), id, liveSnapshotTrades)
		if err != nil {
			writeError(c, http.StatusInternalServerError, err)
			return
		}
		for _, r := range rows {
			resp.RecentTrades = append(resp.RecentTrades, toTradeSummary(r))
		}
		// Fold in fill detail (optional collaborator). One IN query
		// over the bounded order set, grouped back onto each summary.
		if h.Executions != nil && len(resp.RecentTrades) > 0 {
			if err := h.attachFills(c.Request.Context(), resp.RecentTrades); err != nil {
				writeError(c, http.StatusInternalServerError, err)
				return
			}
		}
	}

	c.JSON(http.StatusOK, resp)
}

// attachFills loads every fill for the given summaries' orders in one
// query and groups them back onto each summary's Fills field. Mutates
// the slice in place (TradeRecordSummary elements are addressable).
func (h *Handlers) attachFills(ctx context.Context, summaries []TradeRecordSummary) error {
	ids := make([]string, len(summaries))
	for i := range summaries {
		ids[i] = summaries[i].ClientOrderID
	}
	fills, err := h.Executions.ListExecutionsForOrders(ctx, ids)
	if err != nil {
		return err
	}
	byOrder := make(map[string][]SpotExecutionSummary, len(summaries))
	for _, f := range fills {
		byOrder[f.ClientOrderID] = append(byOrder[f.ClientOrderID], SpotExecutionSummary{
			ExchangeOrderID:    f.ExchangeOrderID,
			FillQuantity:       f.FillQuantity,
			FillPrice:          f.FillPrice,
			FillFeeAsset:       f.FillFeeAsset,
			FillFeeAmount:      f.FillFeeAmount,
			FilledAtExchangeMs: f.FilledAtExchangeMs,
			ActualSlippageBPS:  f.ActualSlippageBPS,
		})
	}
	for i := range summaries {
		summaries[i].Fills = byOrder[summaries[i].ClientOrderID]
	}
	return nil
}

// toTradeSummary maps a TradeRecord row to its wire summary. Extracted
// so /live and /trades produce identical trade shapes. Fills are folded
// in separately by attachFills (only the /live path populates them).
func toTradeSummary(r store.TradeRecord) TradeRecordSummary {
	return TradeRecordSummary{
		ClientOrderID: r.ClientOrderID,
		Symbol:        r.Symbol,
		Side:          r.Side,
		OrderType:     r.OrderType,
		QuantityUSD:   r.QuantityUSD,
		LimitPrice:    r.LimitPrice,
		NowMsAtSaaS:   r.NowMsAtSaaS,
		ValidUntilMs:  r.ValidUntilMs,
		Status:        string(r.Status),
		CreatedAtMs:   r.CreatedAt.UnixMilli(),
	}
}

// ---- response bodies ----

// InstanceListResponse is the body of GET /api/v1/instances.
type InstanceListResponse struct {
	Items []InstanceResponse `json:"items"`
	Count int                `json:"count"`
}

// PortfolioSnapshotView is the latest persisted portfolio_states row.
type PortfolioSnapshotView struct {
	DeadBTC              float64 `json:"dead_btc"`
	FloatBTC             float64 `json:"float_btc"`
	ColdSealedBTC        float64 `json:"cold_sealed_btc"`
	USDT                 float64 `json:"usdt"`
	NowMs                int64   `json:"now_ms"`
	LastProcessedBarTime int64   `json:"last_processed_bar_time"`
}

// ConnectionHealth is the Agent's live WS presence as seen by the Hub.
type ConnectionHealth struct {
	Connected bool `json:"connected"`
}

// SpotExecutionSummary is one exchange-side fill, folded onto its
// parent TradeRecordSummary in the /live snapshot.
type SpotExecutionSummary struct {
	ExchangeOrderID    string  `json:"exchange_order_id"`
	FillQuantity       float64 `json:"fill_quantity"`
	FillPrice          float64 `json:"fill_price"`
	FillFeeAsset       string  `json:"fill_fee_asset"`
	FillFeeAmount      float64 `json:"fill_fee_amount"`
	FilledAtExchangeMs int64   `json:"filled_at_exchange_ms"`
	ActualSlippageBPS  float64 `json:"actual_slippage_bps"`
}

// InstanceLiveResponse is the body of GET /api/v1/instances/:id/live.
// Portfolio and Connection are omitted when unavailable (no tick yet /
// no Hub in this process); RecentTrades is always present (possibly []).
type InstanceLiveResponse struct {
	Instance     InstanceResponse       `json:"instance"`
	Portfolio    *PortfolioSnapshotView `json:"portfolio,omitempty"`
	Connection   *ConnectionHealth      `json:"connection,omitempty"`
	RecentTrades []TradeRecordSummary   `json:"recent_trades"`
}
