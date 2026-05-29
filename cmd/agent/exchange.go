// exchange.go — selects the agent.Exchange implementation per
// config.agent.yaml. Switches on cfg.Exchange.Name:
//
//	"mock"          — in-process MockExchange seeded with reasonable
//	                  prices; used for dev / smoke tests.
//	"binance_spot"  — binance.Exchange against the URL from
//	                  cfg.Exchange.BaseURL (defaults to mainnet inside
//	                  the binance package; set base_url:
//	                  https://testnet.binance.vision for sandbox).
//
// Returned cleanup is non-nil only when the chosen exchange owns
// background state (e.g. binance's ping goroutine); main defers it.
package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/shopspring/decimal"

	"quantlab/internal/agent"
	"quantlab/internal/agent/binance"
)

// buildExchange returns the configured exchange, an optional close
// function (nil when there is no background state to tear down), and
// an error on misconfiguration.
//
// Iron rule 3: api_key / api_secret stay inside this process — they
// flow only into binance.NewExchange and never reach the WS Hub.
func buildExchange(ctx context.Context, cfg *agent.Config, logger *slog.Logger) (agent.Exchange, func(), error) {
	switch cfg.Exchange.Name {
	case "mock":
		return agent.NewMockExchange(map[string]decimal.Decimal{
			// Seed prices so dev runs don't crash on first market order.
			// MockExchange.Submit fills these prices ±slippageBps.
			"BTCUSDT": decimal.NewFromInt(65000),
			"ETHUSDT": decimal.NewFromInt(3500),
		}), nil, nil

	case "binance_spot":
		if cfg.Exchange.APIKey == "" || cfg.Exchange.APISecret == "" {
			return nil, nil, fmt.Errorf(
				"binance_spot requires exchange.api_key and exchange.api_secret in config")
		}
		ex := binance.NewExchange(cfg.Exchange.APIKey, cfg.Exchange.APISecret,
			binance.ExchangeOptions{
				BaseURL: cfg.Exchange.BaseURL,
				Logger:  logger,
			})
		ex.Start(ctx)
		logger.Info("binance_exchange_started",
			"base_url", ex.Client().BaseURL())
		return ex, func() { _ = ex.Close() }, nil
	}
	return nil, nil, fmt.Errorf("unsupported exchange.name %q (expected mock or binance_spot)",
		cfg.Exchange.Name)
}
