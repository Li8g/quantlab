package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// ExchangeOrder is the input to Exchange.Submit. Built by the Agent
// after decoding a wire.TradeCommand. Quantities are decimal.Decimal
// here (Agent-internal precision); the WS boundary uses string.
type ExchangeOrder struct {
	ClientOrderID string
	Symbol        string
	Side          string // "buy" | "sell"
	OrderType     string // "market" | "limit"
	Quantity      decimal.Decimal
	LimitPrice    decimal.Decimal // zero for market
	// TimeInForce is honored for limit orders only. Empty ⇒ GTC (pre-B2
	// default). The dispatcher stamps IOC on B2 marketable limits.
	TimeInForce string
}

// ExchangeSubmitResult is the outcome of one Submit call.
type ExchangeSubmitResult struct {
	ExchangeOrderID string
	AcceptedAtMs    int64

	// MarketRef is the best bid/ask the exchange returned at submit
	// time, used downstream to compute ActualSlippageBps. Zero for
	// limit orders (caller uses LimitPrice as ref instead).
	MarketRef decimal.Decimal

	// Immediate fills (rare for limit, common for market). One entry
	// per partial fill on the same submit.
	Fills []ExchangeFill
}

// ExchangeFill is one settled execution. FilledAtExchangeMs is the
// exchange's clock, not the Agent's.
type ExchangeFill struct {
	FillQuantity       decimal.Decimal
	FillPrice          decimal.Decimal
	FillFeeAsset       string
	FillFeeAmount      decimal.Decimal
	FilledAtExchangeMs int64
	// TradeID is the exchange's globally-unique trade id, carried to SaaS
	// as the canonical fill-dedup key (see wire.Fill.TradeID). 0 when the
	// backend has no per-trade id (MockExchange).
	TradeID int64
}

// Position is one row of the Exchange.Positions() snapshot — input to
// wire.StateSyncResponse.Positions.
type Position struct {
	Symbol string
	Free   decimal.Decimal
	Locked decimal.Decimal
}

// Exchange is the v1 abstraction the Agent uses to talk to a real or
// mock exchange. Implementations:
//
//   - mock.Exchange (this package) for tests and dev
//   - binance.Exchange (future) for production
//
// All methods are synchronous; the Agent calls them from its single
// receive goroutine. Implementations must be safe for concurrent reads
// (Positions) but Submit is called serially.
type Exchange interface {
	// Submit places an order. Errors are exchange-level (rate limit,
	// rejection); the Agent maps them to Ack{status=rejected}.
	Submit(ctx context.Context, order ExchangeOrder) (*ExchangeSubmitResult, error)

	// Positions returns the latest snapshot. Used for state_sync_response.
	Positions(ctx context.Context) ([]Position, error)

	// Reachable returns true if the exchange API has been contacted
	// successfully within the last ping interval. Surfaces in Pong.
	Reachable() bool
}

// ErrExchangeRejected is the canonical "exchange said no" error.
// Wrap with %w to include the reason.
var ErrExchangeRejected = errors.New("agent: exchange rejected")

// MockExchange is an in-process Exchange. Every Submit completes
// instantly with one full fill at MockExchange.MarketPrice (with a
// configurable +/-N bps deviation to exercise the slippage path).
// Positions are an in-memory ledger.
type MockExchange struct {
	mu          sync.Mutex
	nextOrderID int
	prices      map[string]decimal.Decimal // symbol → mid
	positions   map[string]Position
	slippageBps float64 // applied to market orders; sign per side
	reachable   bool
	nowFn       func() time.Time
}

// NewMockExchange constructs a MockExchange with one starting price.
// Tests usually call SetPrice / SetSlippageBps / SetPosition after.
func NewMockExchange(startPrices map[string]decimal.Decimal) *MockExchange {
	if startPrices == nil {
		startPrices = map[string]decimal.Decimal{}
	}
	return &MockExchange{
		nextOrderID: 1,
		prices:      startPrices,
		positions:   map[string]Position{},
		reachable:   true,
		nowFn:       time.Now,
	}
}

// SetPrice overrides the mid price for a symbol.
func (m *MockExchange) SetPrice(symbol string, p decimal.Decimal) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prices[symbol] = p
}

// SetSlippageBps overrides the slippage applied to market orders. For
// buy: fill = mid * (1 + bps/10000); for sell: fill = mid * (1 - bps/10000).
func (m *MockExchange) SetSlippageBps(bps float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.slippageBps = bps
}

// SetPosition seeds the ledger for state_sync_response.
func (m *MockExchange) SetPosition(p Position) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.positions[p.Symbol] = p
}

// SetReachable controls the Reachable() return — tests can simulate
// exchange downtime to verify Pong.exchange_reachable propagation.
func (m *MockExchange) SetReachable(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reachable = v
}

// SetClock overrides time source (tests use a fixed clock).
func (m *MockExchange) SetClock(fn func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nowFn = fn
}

func (m *MockExchange) Submit(_ context.Context, order ExchangeOrder) (*ExchangeSubmitResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mid, ok := m.prices[order.Symbol]
	if !ok {
		return nil, errors.New("mock: unknown symbol")
	}

	var fillPrice decimal.Decimal
	var marketRef decimal.Decimal
	switch order.OrderType {
	case "market":
		marketRef = mid
		// fill = mid * (1 ± slippage/10000)
		bpsAdj := decimal.NewFromFloat(m.slippageBps / 10000.0)
		switch order.Side {
		case "buy":
			fillPrice = mid.Mul(decimal.NewFromInt(1).Add(bpsAdj))
		case "sell":
			fillPrice = mid.Mul(decimal.NewFromInt(1).Sub(bpsAdj))
		default:
			return nil, errors.New("mock: invalid side")
		}
	case "limit":
		// Assume the limit fills at limit price in v1 (no partial
		// fills, no rejection). The slippage path uses limit_price as
		// reference downstream.
		fillPrice = order.LimitPrice
		marketRef = mid
	default:
		return nil, errors.New("mock: invalid order_type")
	}

	now := m.nowFn().UnixMilli()
	m.nextOrderID++
	xoid := decFormat(m.nextOrderID)

	return &ExchangeSubmitResult{
		ExchangeOrderID: xoid,
		AcceptedAtMs:    now,
		MarketRef:       marketRef,
		Fills: []ExchangeFill{
			{
				FillQuantity:       order.Quantity,
				FillPrice:          fillPrice,
				FillFeeAsset:       "USDT",
				FillFeeAmount:      decimal.Zero,
				FilledAtExchangeMs: now,
			},
		},
	}, nil
}

func (m *MockExchange) Positions(_ context.Context) ([]Position, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Position, 0, len(m.positions))
	for _, p := range m.positions {
		out = append(out, p)
	}
	return out, nil
}

func (m *MockExchange) Reachable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reachable
}

// decFormat renders an int as a decimal string with no separators —
// used as the synthetic exchange_order_id for the mock.
func decFormat(n int) string {
	// Inline to avoid pulling strconv into the exchange package only
	// for this. Two-digit prefix lets us sort lexicographically when
	// debugging logs ordered by exchange_order_id.
	return "mock-" + decimal.NewFromInt(int64(n)).String()
}
