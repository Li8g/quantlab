// Asset-accounting simulator for sigmoid_v1. Source-of-truth:
// docs/strategies/sigmoid_v1.md §9 (per-bar bookkeeping rules) +
// docs/策略数学引擎.md §1.1 (three-state asset model) + §1.2
// (legal three-state transitions).
//
// Pure functions that take (PortfolioSnapshot, intent, price, friction)
// and return the post-application snapshot. Step() emits THEORETICAL
// orders (§5.8 upstream); this layer applies the actual fee +
// slippage + cash-availability rules. Adapter.Evaluate (Phase 4d-loop
// + 4d-adapter) drives this layer bar-by-bar.
//
// Conservation invariants every helper guarantees:
//
//	- USDT ≥ 0   (insufficient cash → skip the buy entirely)
//	- DeadBTC, FloatBTC ≥ 0  (insufficient holdings → skip the sell)
//	- applyRelease preserves DeadBTC + FloatBTC exactly
//	- Friction (fee + slippage) is consumed from USDT and FloatBTC
//	  only; it never reaches DeadBTC (the DeadBTC quantity is the
//	  post-fee asset received at macro buy time)
//
// Location: this file is strategy-private for the prototype phase.
// Upstream §3.4 says "具体记账由 internal/adapters/backtest/" — when
// the second strategy lands, this should be promoted to a shared
// package. The signatures here intentionally take only inputs that
// don't depend on sigmoid_v1 internals to ease that lift.
package sigmoid_v1

import (
	"quantlab/internal/domain"
	"quantlab/internal/quant"
	"quantlab/internal/strategy"
)

// applyMacroBuy moves `order.QuantityUSD` from USDT into DeadBTC at
// the close-of-bar price under taker fee + slippage. Returns the
// updated portfolio and applied=false when the order was skipped
// (USDT insufficient — §3.3 sigmoid_v1.md "直接跳过这次注入").
//
// Macro buy never touches FloatBTC — §3.4 upstream pins the
// destination wallet.
func applyMacroBuy(p strategy.PortfolioSnapshot, order strategy.OrderIntent, price float64, fp domain.FrictionParams) (strategy.PortfolioSnapshot, bool) {
	if order.Kind != strategy.OrderKindMacro || order.Side != strategy.OrderSideBuy {
		return p, false
	}
	if order.QuantityUSD <= 0 || price <= 0 {
		return p, false
	}
	if order.QuantityUSD > p.USDT {
		return p, false
	}
	filledQty, _, _ := quant.ApplyBuyFriction(order.QuantityUSD, price, fp)
	if filledQty <= 0 {
		return p, false
	}
	p.USDT -= order.QuantityUSD
	p.DeadBTC += filledQty
	return p, true
}

// applyMicroBuy moves `order.QuantityUSD` from USDT into FloatBTC at
// the close-of-bar price under taker fee + slippage. Skip when USDT
// is insufficient — Step() emitted a theoretical amount and didn't
// see the live cash state.
func applyMicroBuy(p strategy.PortfolioSnapshot, order strategy.OrderIntent, price float64, fp domain.FrictionParams) (strategy.PortfolioSnapshot, bool) {
	if order.Kind != strategy.OrderKindMicro || order.Side != strategy.OrderSideBuy {
		return p, false
	}
	if order.QuantityUSD <= 0 || price <= 0 {
		return p, false
	}
	if order.QuantityUSD > p.USDT {
		return p, false
	}
	filledQty, _, _ := quant.ApplyBuyFriction(order.QuantityUSD, price, fp)
	if filledQty <= 0 {
		return p, false
	}
	p.USDT -= order.QuantityUSD
	p.FloatBTC += filledQty
	return p, true
}

// applyMicroSell converts FloatBTC into USDT. `order.QuantityUSD` is
// the THEORETICAL notional Step() wants to sell; we map it to a BTC
// quantity via the close-of-bar price, then cap at FloatBTC available.
// Skip when FloatBTC is zero. [INVENTED v1] capping vs skipping:
// partial fill (cap) is more graceful for a strategy that targets a
// fraction of equity.
func applyMicroSell(p strategy.PortfolioSnapshot, order strategy.OrderIntent, price float64, fp domain.FrictionParams) (strategy.PortfolioSnapshot, bool) {
	if order.Kind != strategy.OrderKindMicro || order.Side != strategy.OrderSideSell {
		return p, false
	}
	if order.QuantityUSD <= 0 || price <= 0 {
		return p, false
	}
	if p.FloatBTC <= 0 {
		return p, false
	}
	qty := order.QuantityUSD / price
	if qty > p.FloatBTC {
		qty = p.FloatBTC
	}
	filledQuoteUSD, _, _ := quant.ApplySellFriction(qty, price, fp)
	if filledQuoteUSD <= 0 {
		return p, false
	}
	p.FloatBTC -= qty
	p.USDT += filledQuoteUSD
	return p, true
}

// applyRelease transfers `intent.Quantity` BTC from DeadBTC to
// FloatBTC. No price, no friction — §5 upstream calls this a "账本
// 翻账" (book transfer) inside the SaaS ledger; it never reaches the
// exchange. Conservation: DeadBTC + FloatBTC is exactly preserved.
//
// Skip on a zero/negative quantity or insufficient DeadBTC. evaluate-
// Release already caps Quantity at DeadBTC × 10% so insufficient
// shouldn't happen on Step()-generated intents, but the simulator is
// the load-bearing safety net.
func applyRelease(p strategy.PortfolioSnapshot, intent strategy.ReleaseIntent) (strategy.PortfolioSnapshot, bool) {
	if intent.Quantity <= 0 {
		return p, false
	}
	if intent.Quantity > p.DeadBTC {
		return p, false
	}
	p.DeadBTC -= intent.Quantity
	p.FloatBTC += intent.Quantity
	return p, true
}

// applyStrategyOutput is the per-bar wrapper Adapter.Evaluate will
// call. Processes intents in the order:
//
//	1. Releases (cheap, free, no friction) — front-load to make
//	   freshly-floated BTC available for any same-bar micro orders.
//	   The order is documented; tests pin it.
//	2. Macro orders
//	3. Micro orders
//
// All intents are best-effort; a skip returns the unchanged
// portfolio for that intent and the wrapper continues. Returns the
// final post-bar portfolio.
//
// `price` is the close-of-bar reference price. The §5.8 upstream
// contract says Step() emits at the close; we honour that exact
// instant.
func applyStrategyOutput(p strategy.PortfolioSnapshot, out strategy.StrategyOutput, price float64, fp domain.FrictionParams) strategy.PortfolioSnapshot {
	for _, r := range out.ReleaseIntents {
		p, _ = applyRelease(p, r)
	}
	for _, m := range out.MacroOrders {
		p, _ = applyMacroBuy(p, m, price, fp)
	}
	for _, m := range out.MicroOrders {
		switch m.Side {
		case strategy.OrderSideBuy:
			p, _ = applyMicroBuy(p, m, price, fp)
		case strategy.OrderSideSell:
			p, _ = applyMicroSell(p, m, price, fp)
		}
	}
	return p
}
