// cmd/agent is the LocalAgent entrypoint per docs/saas-ws-protocol-v1.md
// §8. Single-process, one exchange account, persistent WebSocket client
// to the SaaS Hub.
//
// API keys live in config.agent.yaml; they never leave this process
// (iron rule 3).
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shopspring/decimal"

	"quantlab/internal/agent"
)

func main() {
	configPath := flag.String("config", "config.agent.yaml", "path to config.agent.yaml")
	flag.Parse()

	cfg, err := agent.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("agent: load config: %v", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: parseLevel(cfg.Log.Level),
	}))
	slog.SetDefault(logger)

	// v1 hardcodes the mock exchange. Replacing this with a real
	// exchange impl (binance) is a Phase 7/8 polish item — the
	// Exchange interface keeps the swap surface-area minimal.
	exchange := agent.NewMockExchange(map[string]decimal.Decimal{
		// Seed reasonable prices so dev runs don't crash on first order.
		// Real-exchange impls discover prices via REST/WS tickers.
		"BTCUSDT": decimal.NewFromInt(65000),
		"ETHUSDT": decimal.NewFromInt(3500),
	})

	// idempotency.db_path empty (e.g. test config) falls back to the
	// in-memory store; lifecycle data is lost on restart but that is
	// acceptable for dev / smoke runs.
	var idem agent.IdempotencyStore
	if cfg.Idempotency.DBPath == "" {
		logger.Warn("idempotency_in_memory_only",
			"reason", "idempotency.db_path empty in config — Agent restart will lose dedupe state")
		idem = agent.NewMemoryStore()
	} else {
		s, err := agent.NewSqliteStore(cfg.Idempotency.DBPath)
		if err != nil {
			log.Fatalf("agent: open idempotency sqlite: %v", err)
		}
		defer s.Close()
		// Startup purge: drop rows older than retention_days. Returns
		// row count for ops visibility.
		retention := time.Duration(cfg.Idempotency.RetentionDays) * 24 * time.Hour
		cutoff := time.Now().Add(-retention).UnixMilli()
		if n, err := s.Purge(cutoff); err != nil {
			logger.Warn("idempotency_purge_failed", "err", err)
		} else if n > 0 {
			logger.Info("idempotency_purged", "rows", n, "retention_days", cfg.Idempotency.RetentionDays)
		}
		idem = s
	}

	client := agent.NewClient(*cfg, exchange, idem, agent.Options{
		Logger: logger,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("agent_starting",
		"agent_id", cfg.AgentID,
		"account_id", cfg.AccountID,
		"saas_url", cfg.SaaSURL,
		"exchange", cfg.Exchange.Name)

	if err := client.Run(ctx); err != nil && err != context.Canceled {
		logger.Error("agent_exited", "err", err)
		os.Exit(1)
	}
	logger.Info("agent_stopped")
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
