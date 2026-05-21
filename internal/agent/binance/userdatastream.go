// userdatastream.go — Binance Spot User Data Stream (UDS) listenKey
// lifecycle. The three REST endpoints are USER_STREAM tier: only the
// X-MBX-APIKEY header is required (no HMAC signing).
//
// Lifecycle per Binance docs:
//
//	POST   /api/v3/userDataStream                    → {"listenKey":"…"}
//	PUT    /api/v3/userDataStream?listenKey=…        → {} (keepalive)
//	DELETE /api/v3/userDataStream?listenKey=…        → {} (close)
//
// A listenKey is valid for 60 minutes from creation or last
// keepalive. The convention is to PUT every 30 minutes so a single
// dropped keepalive (e.g. transient 5xx) doesn't expire the key.
package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// CreateListenKey opens a new User Data Stream and returns its
// listenKey. The key is opaque to callers; pass it verbatim to the
// keepalive/close endpoints and to the stream WebSocket URL.
func (c *Client) CreateListenKey(ctx context.Context) (string, error) {
	body, err := c.withAPIKey(ctx, http.MethodPost, "/api/v3/userDataStream", nil)
	if err != nil {
		return "", fmt.Errorf("binance.CreateListenKey: %w", err)
	}
	var raw struct {
		ListenKey string `json:"listenKey"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", fmt.Errorf("binance.CreateListenKey: decode: %w", err)
	}
	if raw.ListenKey == "" {
		return "", fmt.Errorf("binance.CreateListenKey: empty listenKey in response: %s", string(body))
	}
	return raw.ListenKey, nil
}

// KeepaliveListenKey refreshes the 60-minute TTL on an existing
// listenKey. Binance returns an empty JSON object on success. A 404
// (or APIError code -2014/-1125) means the key has already expired
// and the caller must Create a new one.
func (c *Client) KeepaliveListenKey(ctx context.Context, listenKey string) error {
	if listenKey == "" {
		return errors.New("binance.KeepaliveListenKey: empty listenKey")
	}
	params := url.Values{}
	params.Set("listenKey", listenKey)
	if _, err := c.withAPIKey(ctx, http.MethodPut, "/api/v3/userDataStream", params); err != nil {
		return fmt.Errorf("binance.KeepaliveListenKey: %w", err)
	}
	return nil
}

// CloseListenKey explicitly invalidates a listenKey. Called on
// graceful shutdown so the next session doesn't accumulate dangling
// streams against the same account. A failure here is non-fatal —
// the key will expire on its own within 60 minutes.
func (c *Client) CloseListenKey(ctx context.Context, listenKey string) error {
	if listenKey == "" {
		return errors.New("binance.CloseListenKey: empty listenKey")
	}
	params := url.Values{}
	params.Set("listenKey", listenKey)
	if _, err := c.withAPIKey(ctx, http.MethodDelete, "/api/v3/userDataStream", params); err != nil {
		return fmt.Errorf("binance.CloseListenKey: %w", err)
	}
	return nil
}
