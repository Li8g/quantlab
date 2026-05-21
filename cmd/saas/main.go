// cmd/saas is the SaaS process entrypoint. It loads config.yaml, opens
// the Postgres connection (store.NewDB also enables TimescaleDB +
// AutoMigrates all models + creates the klines hypertable), constructs
// the four repositories + epoch.Service + api.Handlers, and runs Gin
// on cfg.Server.HTTPListen with graceful shutdown.
//
// One process, one DB, in-process per-(strategy, pair) mutex. Scaling
// to multi-instance is out of scope for the prototype (would need a
// Redis or DB-row advisory lock — see project notes).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/redis/go-redis/v9"

	"quantlab/internal/api"
	"quantlab/internal/api/middleware"
	"quantlab/internal/repository"
	"quantlab/internal/saas/agentauth"
	"quantlab/internal/saas/agentstatus"
	"quantlab/internal/saas/auth"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/cron"
	"quantlab/internal/saas/epoch"
	"quantlab/internal/saas/instance"
	"quantlab/internal/saas/store"
	"quantlab/internal/saas/wshub"
)

// Build-time identifiers stamped onto every ChallengerResultPackage's
// ReproducibilityMetadata. Override at link time with -ldflags
// "-X main.buildID=<sha> -X main.engineVersion=<v>".
var (
	buildID         = "dev"
	dataVersion     = "v5.3.3"
	engineVersion   = "v0.1.0-proto"
	strategyVersion = "v0.1.0-proto"
)

// ulidIssuer satisfies api.IDIssuer using the package-shared ULID
// generator (store.NewULID, MonotonicEntropy mode).
type ulidIssuer struct{}

func (ulidIssuer) NewID() string { return store.NewULID() }

func main() {
	configPath := flag.String("config", "", "path to config.yaml (default: $CONFIG_PATH or ./config.yaml)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("saas: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.NewDB(ctx, cfg)
	if err != nil {
		log.Fatalf("saas: open db: %v", err)
	}

	taskRepo := repository.NewEvolutionTaskRepo(db)
	challengerRepo := repository.NewChallengerRepo(db)
	championRepo := repository.NewChampionRepo(db)
	sharpeRepo := repository.NewSharpeBankRepo(db)
	instanceRepo := repository.NewInstanceRepo(db)
	portfolioRepo := repository.NewPortfolioRepo(db)
	runtimeRepo := repository.NewRuntimeRepo(db)
	tradeRepo := repository.NewTradeRepo(db)

	registry := epoch.DefaultRegistry()
	svc := epoch.New(
		db,
		taskRepo,
		challengerRepo,
		sharpeRepo,
		registry,
		epoch.BuildMeta{
			DataVersion:       dataVersion,
			EngineVersion:     engineVersion,
			StrategyVersion:   strategyVersion,
			HardwareSignature: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
			GoVersion:         runtime.Version(),
			BuildID:           buildID,
		},
		epoch.DefaultDefaults(),
	)

	// Phase 6.3: auth + instance lifecycle + scheduler.
	authSvc, err := auth.NewService(cfg.JWT)
	if err != nil {
		log.Fatalf("saas: auth service: %v", err)
	}

	// Phase 7 WS Hub. Agent tokens live in agent_tokens; hub uses the
	// gorm-backed store so revocation + last_seen are persistent across
	// restarts. OnAgentMessage routes Ack / OrderUpdate envelopes to
	// TradeRepo so SaaS persists the order lifecycle. OnConnectionState
	// fans out to Redis (when configured) so other processes can query
	// `agent:{accountID}:status` per protocol §7.2.
	agentAuthSvc := agentauth.NewService(agentauth.NewGormTokenStore(db))
	agentMsgs := newAgentMessageHandler(tradeRepo, nil)

	var statusReporter agentstatus.Reporter = agentstatus.NopReporter{}
	if cfg.Redis.Addr == "" {
		log.Printf("saas: redis.addr empty — Agent online status will not be published")
	} else {
		rdb := redis.NewClient(&redis.Options{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		})
		if err := rdb.Ping(ctx).Err(); err != nil {
			log.Fatalf("saas: redis ping %s: %v", cfg.Redis.Addr, err)
		}
		defer rdb.Close()
		statusReporter = agentstatus.NewRedisReporter(rdb, agentstatus.DefaultTTL)
	}

	hub := wshub.New(agentAuthSvc, wshub.Config{
		OnAgentMessage:    agentMsgs.Hook,
		OnConnectionState: makeConnectionStateHook(statusReporter),
	})

	tickManager := instance.New(
		instanceRepo, portfolioRepo, runtimeRepo,
		&instance.DefaultBarLoader{DB: db},
		&instance.DefaultStrategyResolver{Registry: registry},
		&instance.DefaultChampionGeneLoader{Challengers: challengerRepo},
		newRecordingDispatcher(hub, tradeRepo, nil), // TradeCommandDispatcher with pre-insert
		nil, // logger: slog.Default
	)
	scheduler := cron.New(instanceRepo, tickManager, cron.Config{})

	h := &api.Handlers{
		Epoch:       svc,
		Tasks:       taskRepo,
		Challengers: challengerRepo,
		Champions:   championRepo,
		Instances:   instanceRepo,
		IDIssuer:    ulidIssuer{},
		AuthRequired: middleware.AuthRequired(authSvc),
		RequireOperator: middleware.RequireRole(
			store.UserRoleOperator, store.UserRoleAdmin,
		),
	}

	if cfg.AppRole != config.AppRoleDev {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())
	h.Register(r)

	srv := &http.Server{
		Addr:              cfg.Server.HTTPListen,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Dedicated WS mux on cfg.Server.WSListen (port 8081, frozen in
	// docs/saas-ws-protocol-v1.md §7.1). Path /api/v1/ws/agent is the
	// only route exposed; Agent connections are long-lived so no read
	// timeout — ReadHeaderTimeout still guards the initial HTTP upgrade.
	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/api/v1/ws/agent", hub.ServeWS)
	wsSrv := &http.Server{
		Addr:              cfg.Server.WSListen,
		Handler:           wsMux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("saas: listening on %s (app_role=%s, strategies=%v)",
			cfg.Server.HTTPListen, cfg.AppRole, registry.IDs())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("saas: http server: %v", err)
		}
	}()
	go func() {
		log.Printf("saas: ws listening on %s", cfg.Server.WSListen)
		if err := wsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("saas: ws server: %v", err)
		}
	}()

	// Cron Tick scheduler (Phase 6.2). Runs in-process; stops cleanly
	// when ctx is cancelled. In-flight Ticks complete on their own
	// (per-instance mutex in Manager).
	go scheduler.Run(ctx)

	<-ctx.Done()
	log.Printf("saas: shutdown signal received, draining within %s", cfg.Server.ShutdownTimeout)

	// Notify connected Agents before tearing the WS listener down so
	// they reconnect with backoff instead of error-spinning.
	hub.BroadcastGracefulShutdown(5 * time.Second)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("saas: shutdown: %v", err)
	}
	if err := wsSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("saas: ws shutdown: %v", err)
	}
}
