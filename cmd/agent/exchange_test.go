package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quantlab/internal/agent"
	"quantlab/internal/agent/binance"
)

func TestBuildExchange_MockNoCloser(t *testing.T) {
	cfg := &agent.Config{Exchange: agent.ExchangeConfig{Name: "mock"}}
	ex, closeFn, err := buildExchange(context.Background(), cfg, slog.Default())
	if err != nil {
		t.Fatalf("buildExchange: %v", err)
	}
	if ex == nil {
		t.Fatal("exchange nil")
	}
	if closeFn != nil {
		t.Error("mock exchange should not require a closer")
	}
	if _, ok := ex.(*agent.MockExchange); !ok {
		t.Errorf("type = %T, want *agent.MockExchange", ex)
	}
}

func TestBuildExchange_BinanceSpot_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &agent.Config{Exchange: agent.ExchangeConfig{
		Name:      "binance_spot",
		APIKey:    "PUBKEY",
		APISecret: "SECRET",
		BaseURL:   srv.URL,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ex, closeFn, err := buildExchange(ctx, cfg, slog.Default())
	if err != nil {
		t.Fatalf("buildExchange: %v", err)
	}
	if closeFn == nil {
		t.Fatal("binance exchange must return a closer (ping loop owns a goroutine)")
	}
	defer closeFn()

	if _, ok := ex.(*binance.Exchange); !ok {
		t.Errorf("type = %T, want *binance.Exchange", ex)
	}
}

func TestBuildExchange_BinanceSpot_MissingAPIKey(t *testing.T) {
	cfg := &agent.Config{Exchange: agent.ExchangeConfig{
		Name:      "binance_spot",
		APIKey:    "",
		APISecret: "SECRET",
	}}
	_, _, err := buildExchange(context.Background(), cfg, slog.Default())
	if err == nil {
		t.Fatal("want error on empty api_key")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("err = %v, want mention of api_key", err)
	}
}

func TestBuildExchange_BinanceSpot_MissingAPISecret(t *testing.T) {
	cfg := &agent.Config{Exchange: agent.ExchangeConfig{
		Name:      "binance_spot",
		APIKey:    "PUBKEY",
		APISecret: "",
	}}
	_, _, err := buildExchange(context.Background(), cfg, slog.Default())
	if err == nil {
		t.Fatal("want error on empty api_secret")
	}
	if !strings.Contains(err.Error(), "api_secret") {
		t.Errorf("err = %v, want mention of api_secret", err)
	}
}

func TestBuildExchange_UnknownNameFatal(t *testing.T) {
	cfg := &agent.Config{Exchange: agent.ExchangeConfig{Name: "ftx"}}
	_, _, err := buildExchange(context.Background(), cfg, slog.Default())
	if err == nil {
		t.Fatal("want error on unsupported exchange")
	}
	if !strings.Contains(err.Error(), `"ftx"`) {
		t.Errorf("err = %v, want quoted unknown name", err)
	}
	if !strings.Contains(err.Error(), "mock") || !strings.Contains(err.Error(), "binance_spot") {
		t.Errorf("err = %v, want list of supported names", err)
	}
}
