// Package strategy defines the abstract contract between QuantLab's
// engine layer and any concrete strategy. Engine code only ever touches
// these types; never reaches into internal/strategies/<name>/ internals.
//
// 铁律 1 (Step() isomorphism): the same Step() runs in backtest and live;
// no `if isBacktest` branches.
// 铁律 2 (no wall clock in Step()): Step() must derive "current time" only
// from StrategyInput.NowMs.
//
// The EvolvableStrategy and Adapter interfaces live in evolvable.go
// (defined in Phase 5A); this file holds only the value-typed contract
// shared between strategy and engine.
package strategy

import (
	"encoding/json"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
)

// OrderKind tags whether an order came from the macro or micro engine.
type OrderKind string

const (
	OrderKindMacro OrderKind = "macro"
	OrderKindMicro OrderKind = "micro"
)

// OrderSide is the trade direction.
type OrderSide string

const (
	OrderSideBuy  OrderSide = "buy"
	OrderSideSell OrderSide = "sell"
)

// OrderType is the execution type. Prototype phase only supports market
// and limit orders; stop / OCO / trailing are deferred.
type OrderType string

const (
	OrderTypeMarket OrderType = "market"
	OrderTypeLimit  OrderType = "limit"
)

// OrderIntent is the strategy-side order proposal emitted by Step().
// The SaaS dispatcher converts each OrderIntent into a wire.TradeCommand
// (docs/saas-ws-protocol-v1.md §5.8 + appendix B): QuantityUSD is divided
// by the latest close price to produce quantity_decimal in asset units.
type OrderIntent struct {
	Kind          OrderKind `json:"kind"`
	Side          OrderSide `json:"side"`
	OrderType     OrderType `json:"order_type"`
	QuantityUSD   float64   `json:"quantity_usd"`
	LimitPrice    float64   `json:"limit_price,omitempty"`
	ClientOrderID string    `json:"client_order_id"`
	ValidUntilMs  int64     `json:"valid_until_ms"`
}

// ReleaseIntent transitions DeadBTC → FloatBTC within the SaaS ledger.
// Never dispatched to the agent — the exchange does not see DeadBTC.
type ReleaseIntent struct {
	NowMs    int64   `json:"now_ms"`
	Quantity float64 `json:"quantity"`
	Reason   string  `json:"reason"`
}

// PortfolioSnapshot is the asset three-state view handed to Step().
// Backtest adapter and live-side SaaS both must populate identically;
// any divergence breaks 铁律 1.
type PortfolioSnapshot struct {
	DeadBTC       float64 `json:"dead_btc"`
	FloatBTC      float64 `json:"float_btc"`
	ColdSealedBTC float64 `json:"cold_sealed_btc"`
	USDT          float64 `json:"usdt"`
}

// DebugSnapshot is optional per-tick diagnostic data. Persisted only when
// app_role=lab or an explicit debug flag is set; never persisted on the
// production SaaS path (avoids DB bloat). Populating it must be a pure
// function of the same inputs Step() consumes.
type DebugSnapshot struct {
	Signal       *float64 `json:"signal,omitempty"`
	TargetWeight *float64 `json:"target_weight,omitempty"`
	MarketState  *string  `json:"market_state,omitempty"`
}

// StrategyInput is the immutable per-tick input to Step().
// NowMs is the ONLY legal time source inside Step() (铁律 2).
type StrategyInput struct {
	NowMs                int64                       `json:"now_ms"`
	Closes               []float64                   `json:"closes"`
	Timestamps           []int64                     `json:"timestamps"`
	Portfolio            PortfolioSnapshot           `json:"portfolio"`
	Chromosome           domain.Gene                 `json:"chromosome"`
	Spawn                resultpkg.SpawnPointPayload `json:"spawn"`
	LastProcessedBarTime int64                       `json:"last_processed_bar_time"`

	// RuntimeState is the strategy-private state from the previous tick.
	// Engine treats it as opaque bytes; strategy serializes/deserializes
	// according to its own internal schema.
	RuntimeState json.RawMessage `json:"runtime_state,omitempty"`
}

// StrategyOutput is what Step() returns for this tick.
// All four collections may be empty; nil and empty are equivalent.
type StrategyOutput struct {
	MacroOrders    []OrderIntent   `json:"macro_orders,omitempty"`
	MicroOrders    []OrderIntent   `json:"micro_orders,omitempty"`
	ReleaseIntents []ReleaseIntent `json:"release_intents,omitempty"`
	RuntimeState   json.RawMessage `json:"runtime_state,omitempty"`
	DebugSnapshot  *DebugSnapshot  `json:"debug_snapshot,omitempty"`
}
