// queries.go — read-only Binance Spot REST endpoints used by the
// agent.Exchange implementation. Step 2 of the adapter build:
//
//   Ping        — unsigned /api/v3/ping        (Reachable health probe)
//   BookTicker  — unsigned /api/v3/ticker/bookTicker  (best bid/ask for MarketRef)
//   Account     — signed   /api/v3/account     (balances → []agent.Position)
//
// Order placement (POST /api/v3/order) lives in order.go (Step 3).
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

// Ping calls /api/v3/ping. The endpoint returns an empty JSON object
// with HTTP 200 when reachable; any non-2xx or transport error is
// surfaced verbatim to the caller. Used by the agent.Exchange.Reachable
// poller (Step 4).
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.unsigned(ctx, http.MethodGet, "/api/v3/ping", nil); err != nil {
		return fmt.Errorf("binance.Ping: %w", err)
	}
	return nil
}

// BookTicker is the parsed shape of /api/v3/ticker/bookTicker. The
// raw fields are strings on the wire; we keep decimal.Decimal in-Go
// because downstream slippage math (Step 3) requires exact arithmetic.
type BookTicker struct {
	Symbol   string
	BidPrice decimal.Decimal
	BidQty   decimal.Decimal
	AskPrice decimal.Decimal
	AskQty   decimal.Decimal
}

// rawBookTicker mirrors the on-wire JSON. Keep separate from BookTicker
// so the public type stays string-free.
type rawBookTicker struct {
	Symbol   string `json:"symbol"`
	BidPrice string `json:"bidPrice"`
	BidQty   string `json:"bidQty"`
	AskPrice string `json:"askPrice"`
	AskQty   string `json:"askQty"`
}

// BookTicker fetches the best bid/ask for one symbol from
// /api/v3/ticker/bookTicker. Step 3's SubmitMarket calls this
// immediately before POSTing the order so we have a MarketRef for
// ActualSlippageBps computation (see docs/saas-ws-protocol-v1.md §8.2).
func (c *Client) BookTicker(ctx context.Context, symbol string) (*BookTicker, error) {
	if symbol == "" {
		return nil, errors.New("binance.BookTicker: empty symbol")
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	body, err := c.unsigned(ctx, http.MethodGet, "/api/v3/ticker/bookTicker", params)
	if err != nil {
		return nil, fmt.Errorf("binance.BookTicker: %w", err)
	}
	var raw rawBookTicker
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("binance.BookTicker: decode: %w", err)
	}
	bid, err := decimal.NewFromString(raw.BidPrice)
	if err != nil {
		return nil, fmt.Errorf("binance.BookTicker: bidPrice %q: %w", raw.BidPrice, err)
	}
	bidQty, err := decimal.NewFromString(raw.BidQty)
	if err != nil {
		return nil, fmt.Errorf("binance.BookTicker: bidQty %q: %w", raw.BidQty, err)
	}
	ask, err := decimal.NewFromString(raw.AskPrice)
	if err != nil {
		return nil, fmt.Errorf("binance.BookTicker: askPrice %q: %w", raw.AskPrice, err)
	}
	askQty, err := decimal.NewFromString(raw.AskQty)
	if err != nil {
		return nil, fmt.Errorf("binance.BookTicker: askQty %q: %w", raw.AskQty, err)
	}
	return &BookTicker{
		Symbol:   raw.Symbol,
		BidPrice: bid,
		BidQty:   bidQty,
		AskPrice: ask,
		AskQty:   askQty,
	}, nil
}

// rawAccount captures only the balances slice from /api/v3/account.
// The endpoint returns many more fields (makerCommission, accountType,
// permissions, etc.) — we discard them to keep the conversion focused
// on what state_sync_response needs.
type rawAccount struct {
	Balances []rawBalance `json:"balances"`
}

type rawBalance struct {
	Asset  string `json:"asset"`
	Free   string `json:"free"`
	Locked string `json:"locked"`
}

// Account fetches the signed /api/v3/account snapshot and converts
// non-empty balances to []agent.Position. Zero-balance rows (free=0
// AND locked=0) are filtered — Binance returns one entry per asset
// the account has ever held and the long tail is noise for
// state_sync_response.
func (c *Client) Account(ctx context.Context) ([]agent.Position, error) {
	body, err := c.signed(ctx, http.MethodGet, "/api/v3/account", nil)
	if err != nil {
		return nil, fmt.Errorf("binance.Account: %w", err)
	}
	var raw rawAccount
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("binance.Account: decode: %w", err)
	}
	out := make([]agent.Position, 0, len(raw.Balances))
	for _, b := range raw.Balances {
		free, err := decimal.NewFromString(b.Free)
		if err != nil {
			return nil, fmt.Errorf("binance.Account: free %q (asset %s): %w", b.Free, b.Asset, err)
		}
		locked, err := decimal.NewFromString(b.Locked)
		if err != nil {
			return nil, fmt.Errorf("binance.Account: locked %q (asset %s): %w", b.Locked, b.Asset, err)
		}
		if free.IsZero() && locked.IsZero() {
			continue
		}
		out = append(out, agent.Position{
			Symbol: b.Asset,
			Free:   free,
			Locked: locked,
		})
	}
	return out, nil
}
