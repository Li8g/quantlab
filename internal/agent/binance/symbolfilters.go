// symbolfilters.go — pre-submit order compliance against Binance's
// per-symbol order filters. Binance rejects an order whose quantity is
// not a multiple of the LOT_SIZE stepSize, whose price is off the
// PRICE_FILTER tick grid, or whose notional is below the NOTIONAL floor
// with HTTP 400 code=-1013 ("Filter failure"). The SaaS dispatcher sizes
// orders in USD and renders quantity at a fixed 8 decimals
// (wshub.buildTradeCommand), which for most symbols is finer than the
// stepSize grid — so without correction every order round-trips only to
// be rejected.
//
// The agent owns this concern because the exchange owns the filters: we
// fetch exchangeInfo, cache it (filters are static per pair), snap the
// quantity/price onto the grid, and reject locally — saving a round-trip
// — anything that still can't satisfy minQty/minNotional after snapping.
package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/shopspring/decimal"

	"quantlab/internal/agent"
)

// symbolFilter holds the order-validity filters for one trading pair.
// A zero field means the exchange did not advertise that filter, so the
// corresponding check/snap is skipped.
type symbolFilter struct {
	StepSize    decimal.Decimal // LOT_SIZE.stepSize    — quantity grid
	MinQty      decimal.Decimal // LOT_SIZE.minQty      — minimum quantity
	TickSize    decimal.Decimal // PRICE_FILTER.tickSize — price grid (limit)
	MinNotional decimal.Decimal // NOTIONAL.minNotional — minimum qty×price
}

// rawExchangeInfo mirrors the subset of GET /api/v3/exchangeInfo we read.
type rawExchangeInfo struct {
	Symbols []struct {
		Symbol  string `json:"symbol"`
		Filters []struct {
			FilterType  string `json:"filterType"`
			StepSize    string `json:"stepSize"`
			MinQty      string `json:"minQty"`
			TickSize    string `json:"tickSize"`
			MinNotional string `json:"minNotional"`
		} `json:"filters"`
	} `json:"symbols"`
}

// parseSymbolFilter extracts the LOT_SIZE / PRICE_FILTER / NOTIONAL
// filters for the first symbol in an exchangeInfo response. Pure (no IO)
// so the filter-extraction logic is unit-testable without a server.
func parseSymbolFilter(body []byte) (*symbolFilter, error) {
	var raw rawExchangeInfo
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("exchangeInfo decode: %w", err)
	}
	if len(raw.Symbols) == 0 {
		return nil, errors.New("exchangeInfo: response carried no symbols")
	}
	dec := func(field, s string) (decimal.Decimal, error) {
		if s == "" {
			return decimal.Zero, nil
		}
		d, err := decimal.NewFromString(s)
		if err != nil {
			return decimal.Zero, fmt.Errorf("exchangeInfo %s=%q: %w", field, s, err)
		}
		return d, nil
	}
	f := &symbolFilter{}
	for _, fl := range raw.Symbols[0].Filters {
		switch fl.FilterType {
		case "LOT_SIZE":
			step, err := dec("stepSize", fl.StepSize)
			if err != nil {
				return nil, err
			}
			minQty, err := dec("minQty", fl.MinQty)
			if err != nil {
				return nil, err
			}
			f.StepSize, f.MinQty = step, minQty
		case "PRICE_FILTER":
			tick, err := dec("tickSize", fl.TickSize)
			if err != nil {
				return nil, err
			}
			f.TickSize = tick
		case "NOTIONAL", "MIN_NOTIONAL":
			// NOTIONAL (current) and MIN_NOTIONAL (legacy) both expose the
			// floor under the same JSON key; either populates it.
			mn, err := dec("minNotional", fl.MinNotional)
			if err != nil {
				return nil, err
			}
			if mn.IsPositive() {
				f.MinNotional = mn
			}
		}
	}
	return f, nil
}

// symbolFilterFor returns the cached order filters for symbol, fetching
// from exchangeInfo on first use. Concurrent first-uses may each fetch
// (a benign duplicate GET); the last writer wins and both see an
// equivalent result.
func (c *Client) symbolFilterFor(ctx context.Context, symbol string) (*symbolFilter, error) {
	c.filterMu.RLock()
	f, ok := c.filterCache[symbol]
	c.filterMu.RUnlock()
	if ok {
		return f, nil
	}

	params := url.Values{}
	params.Set("symbol", symbol)
	body, err := c.unsigned(ctx, http.MethodGet, "/api/v3/exchangeInfo", params)
	if err != nil {
		return nil, fmt.Errorf("binance.exchangeInfo(%s): %w", symbol, err)
	}
	f, err = parseSymbolFilter(body)
	if err != nil {
		return nil, fmt.Errorf("binance.exchangeInfo(%s): %w", symbol, err)
	}

	c.filterMu.Lock()
	c.filterCache[symbol] = f
	c.filterMu.Unlock()
	return f, nil
}

// compliantQuantity floors qty onto the LOT_SIZE step grid, then checks
// the result against minQty and, when refPrice is known, the order
// notional (qty×refPrice) against minNotional. refPrice is the limit
// price for limit orders or the captured market reference for market
// orders. An order that cannot satisfy the floors is rejected locally
// with agent.ErrExchangeRejected (Binance would return -1013 anyway), so
// the reason reaches the SaaS Ack without a wasted round-trip.
func compliantQuantity(qty, refPrice decimal.Decimal, f *symbolFilter) (decimal.Decimal, error) {
	out := qty
	if f.StepSize.IsPositive() {
		// Floor onto the grid: q - (q mod step). Floor (not round) so we
		// never size UP past what the strategy asked for. Leave an
		// already-on-grid quantity untouched so its string representation
		// (and a compliant order's behaviour) is unchanged.
		steps := out.Div(f.StepSize)
		if floored := steps.Floor(); !floored.Equal(steps) {
			out = floored.Mul(f.StepSize)
		}
	}
	if !out.IsPositive() {
		return decimal.Zero, fmt.Errorf("%w: quantity %s floored to zero by step_size=%s",
			agent.ErrExchangeRejected, qty, f.StepSize)
	}
	if f.MinQty.IsPositive() && out.LessThan(f.MinQty) {
		return decimal.Zero, fmt.Errorf("%w: quantity %s below min_qty=%s",
			agent.ErrExchangeRejected, out, f.MinQty)
	}
	if f.MinNotional.IsPositive() && refPrice.IsPositive() {
		if notional := out.Mul(refPrice); notional.LessThan(f.MinNotional) {
			return decimal.Zero, fmt.Errorf("%w: notional %s (qty %s × ref %s) below min_notional=%s",
				agent.ErrExchangeRejected, notional, out, refPrice, f.MinNotional)
		}
	}
	return out, nil
}

// compliantPrice snaps a limit price to the PRICE_FILTER tick grid
// (nearest tick). A zero tickSize leaves the price unchanged.
func compliantPrice(price decimal.Decimal, f *symbolFilter) decimal.Decimal {
	if !f.TickSize.IsPositive() {
		return price
	}
	// Leave an already-on-grid price untouched (preserve representation);
	// otherwise snap to the nearest tick.
	ticks := price.Div(f.TickSize)
	if ticks.Equal(ticks.Round(0)) {
		return price
	}
	return ticks.Round(0).Mul(f.TickSize)
}
