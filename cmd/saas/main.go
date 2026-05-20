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

	"quantlab/internal/api"
	"quantlab/internal/api/middleware"
	"quantlab/internal/repository"
	"quantlab/internal/saas/auth"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/cron"
	"quantlab/internal/saas/epoch"
	"quantlab/internal/saas/instance"
	"quantlab/internal/saas/store"
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
	tickManager := instance.New(
		instanceRepo, portfolioRepo, runtimeRepo,
		&instance.DefaultBarLoader{DB: db},
		&instance.DefaultStrategyResolver{Registry: registry},
		&instance.DefaultChampionGeneLoader{Challengers: challengerRepo},
		nil, // dispatcher: LogDispatcher until Phase 8 WS Hub
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

	go func() {
		log.Printf("saas: listening on %s (app_role=%s, strategies=%v)",
			cfg.Server.HTTPListen, cfg.AppRole, registry.IDs())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("saas: http server: %v", err)
		}
	}()

	// Cron Tick scheduler (Phase 6.2). Runs in-process; stops cleanly
	// when ctx is cancelled. In-flight Ticks complete on their own
	// (per-instance mutex in Manager).
	go scheduler.Run(ctx)

	<-ctx.Done()
	log.Printf("saas: shutdown signal received, draining within %s", cfg.Server.ShutdownTimeout)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("saas: shutdown: %v", err)
	}
}
