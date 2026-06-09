// cmd/saas is the SaaS process entrypoint. It loads config.yaml, opens
// the Postgres connection (store.NewDB also enables TimescaleDB +
// AutoMigrates all models + creates the klines hypertable), constructs
// the four repositories + epoch.Service + api.Handlers, and runs Gin
// on cfg.Server.HTTPListen with graceful shutdown.
//
// One process, one DB, single trading account. Multi-replica is out of
// scope for this system (single-user design).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"quantlab/internal/api"
	"quantlab/internal/api/middleware"
	"quantlab/internal/data"
	"quantlab/internal/migrate"
	"quantlab/internal/repository"
	"quantlab/internal/saas/agentauth"
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

// sharpeBankAdapter bridges repository.SharpeBankRepo to the api
// layer's SharpeBankStatter — translates between the two stats types
// without making repository → api → repository introspect each other.
type sharpeBankAdapter struct{ repo *repository.SharpeBankRepo }

func (a sharpeBankAdapter) Stats(ctx context.Context, strategyID, pair string) (api.SharpeBankStatsSnapshot, error) {
	s, err := a.repo.Stats(ctx, strategyID, pair)
	if err != nil {
		return api.SharpeBankStatsSnapshot{}, err
	}
	return api.SharpeBankStatsSnapshot{
		N:              s.N,
		SharpeMean:     s.SharpeMean,
		SharpeVariance: s.SharpeVariance,
	}, nil
}

// hubPresence bridges the wshub.Registry to the api layer's
// AgentPresence — reports whether an account's Agent is connected and
// past handshake. Lives here because the Registry is process-local to
// the Hub; a stateless API replica would leave Handlers.Presence nil.
type hubPresence struct{ reg *wshub.Registry }

func (p hubPresence) IsConnected(accountID string) bool {
	c, err := p.reg.Get(accountID)
	return err == nil && c.IsReady()
}

// klineCoverageAdapter bridges repository.KLineRepo to the api layer's
// DataCoverageLister — translates repository.CoverageRow to
// api.DataCoverageRow without coupling the two packages.
type klineCoverageAdapter struct{ repo *repository.KLineRepo }

func (a klineCoverageAdapter) Coverage(ctx context.Context, symbol, interval string) ([]api.DataCoverageRow, error) {
	rows, err := a.repo.Coverage(ctx, symbol, interval)
	if err != nil {
		return nil, err
	}
	out := make([]api.DataCoverageRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, api.DataCoverageRow{
			Symbol:    r.Symbol,
			Interval:  r.Interval,
			MinOpenMs: r.MinOpenMs,
			MaxOpenMs: r.MaxOpenMs,
			BarCount:  r.BarCount,
		})
	}
	return out, nil
}

// ulidIssuer satisfies api.IDIssuer using the package-shared ULID
// generator (store.NewULID, MonotonicEntropy mode).
type ulidIssuer struct{}

func (ulidIssuer) NewID() string { return store.NewULID() }

// seedUser creates one User row with role=admin. Returns an error if a
// row with the same email already exists (the unique index surfaces the
// collision); the operator must delete the row manually before
// re-seeding. bcrypt cost matches agentauth.DefaultBcryptCost = 12 so
// brute force costs the same on both surfaces.
func seedUser(ctx context.Context, db *gorm.DB, email, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return fmt.Errorf("bcrypt: %w", err)
	}
	u := &store.User{
		UserID:       store.NewULID(),
		Email:        email,
		PasswordHash: string(hash),
		Role:         store.UserRoleAdmin,
		Active:       true,
	}
	return repository.NewUserRepo(db).Create(ctx, u)
}

func main() {
	configPath := flag.String("config", "", "path to config.yaml (default: $CONFIG_PATH or ./config.yaml)")
	seedEmail := flag.String("seed-user-email", "", "if set, seed one User row with this email and exit (use with --seed-user-password)")
	seedPassword := flag.String("seed-user-password", "", "password for --seed-user-email; bcrypt-hashed at cost 12, then discarded")
	seedAgentAccount := flag.String("seed-agent-token", "", "if set, create one AgentToken for this account_id, print the plaintext token, and exit (the only AgentToken-creation path)")
	backfillPromoteBlob := flag.Bool("backfill-promote-blob", false, "scan gene_records and rewrite full_package_json's PromoteLayer from canonical columns, then exit; pair with --dry-run for preview")
	backfillOOSBlob := flag.Bool("backfill-oos-blob", false, "scan gene_records and stamp full_package_json.verification.oos_result.status=not_run on pre-Phase-5D rows whose status is empty, then exit; pair with --dry-run for preview")
	dryRun := flag.Bool("dry-run", false, "preview migrations without writing (no effect outside migration flags)")
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

	// Seed mode: create one admin User row, then exit. Single-account
	// design — manual seeding via this flag is the only User-creation
	// path in v1; there is no signup endpoint.
	if *seedEmail != "" || *seedPassword != "" {
		if *seedEmail == "" || *seedPassword == "" {
			log.Fatalf("saas: --seed-user-email and --seed-user-password must both be provided")
		}
		if err := seedUser(ctx, db, *seedEmail, *seedPassword); err != nil {
			log.Fatalf("saas: seed user: %v", err)
		}
		log.Printf("saas: seeded user email=%s role=admin; exiting", *seedEmail)
		return
	}

	// Seed mode: provision one agent token for an account, print the
	// plaintext (the ONLY time it's recoverable — only bcrypt(secret) is
	// stored), then exit. This is the only AgentToken-creation path; the
	// Agent authenticates its WS handshake with this token.
	if *seedAgentAccount != "" {
		svc := agentauth.NewService(agentauth.NewGormTokenStore(db))
		created, err := svc.CreateToken(ctx, *seedAgentAccount, "cli-seed")
		if err != nil {
			log.Fatalf("saas: seed agent token: %v", err)
		}
		fmt.Printf("agent_token for account_id=%s (put this in config.agent.yaml saas_token):\n%s\n",
			*seedAgentAccount, created.Plaintext)
		return
	}

	// Migration mode: rewrite stored package blobs, then exit. The
	// harness owns the transaction + audit log; each --backfill-* flag
	// plugs a transform. Pair with --dry-run for a preview that
	// reports counts without writing.
	if *backfillPromoteBlob {
		res, err := migrate.RunBlobMigration(ctx, db, migrate.NewPromoteBlobMigration(*dryRun))
		if err != nil {
			log.Fatalf("saas: backfill-promote-blob: %v", err)
		}
		log.Printf("saas: backfill-promote-blob done dry_run=%v scanned=%d touched=%d skipped=%d",
			*dryRun, res.Scanned, res.Touched, res.Skipped)
		return
	}

	if *backfillOOSBlob {
		res, err := migrate.RunBlobMigration(ctx, db, migrate.NewOOSBlobMigration(*dryRun))
		if err != nil {
			log.Fatalf("saas: backfill-oos-blob: %v", err)
		}
		log.Printf("saas: backfill-oos-blob done dry_run=%v scanned=%d touched=%d skipped=%d",
			*dryRun, res.Scanned, res.Touched, res.Skipped)
		return
	}

	taskRepo := repository.NewEvolutionTaskRepo(db)
	challengerRepo := repository.NewChallengerRepo(db)
	championRepo := repository.NewChampionRepo(db)
	sharpeRepo := repository.NewSharpeBankRepo(db)
	traceRepo := repository.NewEvaluationTraceRepo(db)
	instanceRepo := repository.NewInstanceRepo(db)
	portfolioRepo := repository.NewPortfolioRepo(db)
	runtimeRepo := repository.NewRuntimeRepo(db)
	tradeRepo := repository.NewTradeRepo(db)
	reconRepo := repository.NewReconRepo(db)
	importJobRepo := repository.NewImportJobRepo(db)

	// Sweep orphaned pending trades left by a previous run: recordingDispatcher
	// pre-inserts a pending TradeRecord, but if the dispatch then failed (agent
	// offline / latestClose=0) it never got an Ack/OrderUpdate and is stuck
	// pending. Cancel any whose GTT window has lapsed and that never executed.
	// Startup-only backstop (cf. import/task SweepOrphans).
	if n, err := tradeRepo.SweepOrphanPending(ctx, time.Now().UnixMilli()); err != nil {
		log.Printf("saas: sweep orphan pending trades: %v", err)
	} else if n > 0 {
		log.Printf("saas: swept %d orphan pending trade(s) → cancelled", n)
	}

	registry := epoch.DefaultRegistry()
	svc := epoch.New(
		db,
		taskRepo,
		challengerRepo,
		sharpeRepo,
		traceRepo,
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

	// Reset any epoch task left queued/running by a previous process exit
	// to failed before we accept traffic — detached epoch goroutines have
	// no resume path, so an orphaned row would otherwise stay running
	// forever. Runs before srv.ListenAndServe so it can't sweep a task a
	// fresh request just created.
	if n, err := taskRepo.SweepOrphans(ctx); err != nil {
		log.Printf("saas: epoch orphan sweep: %v", err)
	} else if n > 0 {
		log.Printf("saas: swept %d orphaned epoch task(s) → failed", n)
	}

	// Phase 6.3: auth + instance lifecycle + scheduler.
	authSvc, err := auth.NewService(cfg.JWT)
	if err != nil {
		log.Fatalf("saas: auth service: %v", err)
	}

	// Phase 7 WS Hub. Agent tokens live in agent_tokens; hub uses the
	// gorm-backed store so revocation + last_seen are persistent across
	// restarts. OnAgentMessage routes Ack / OrderUpdate envelopes to
	// TradeRepo so SaaS persists the order lifecycle.
	agentAuthSvc := agentauth.NewService(agentauth.NewGormTokenStore(db))
	agentMsgs := newAgentMessageHandler(tradeRepo, instanceRepo, portfolioRepo, reconRepo, nil)
	// Auto-freeze thresholds from config (default 200bps / N=2); tunable per
	// deployment as real drift data arrives (see config.ReconcileConfig).
	agentMsgs.freezeToleranceBps = cfg.Reconcile.FreezeToleranceBps
	agentMsgs.freezeDebounceReports = cfg.Reconcile.FreezeDebounceReports

	hub := wshub.New(agentAuthSvc, wshub.Config{
		OnAgentMessage: agentMsgs.Hook,
		// Reconcile positions immediately on reconnect — no need to wait 60s
		// for the first delta_report to see the agent's latest holdings.
		OnStateSync: agentMsgs.handleStateSync,
		// backlog ⑥: reject a misconfigured agent (wrong env) at handshake.
		// Hard-fail only on prod; dev/lab warns so the testnet workflow
		// (mainnet klines + testnet agent) keeps running.
		ExpectedEnvironment: cfg.Live.ExpectedEnvironment,
		RejectEnvMismatch:   cfg.AppRole == config.AppRoleSaaS,
		// backlog ⑥ observability: record the rejection as an AgentError so
		// it surfaces in the /live recent_errors panel.
		OnHandshakeReject: makeHandshakeRejectHook(instanceRepo, reconRepo),
	})
	// Auto-freeze control plane (kill_switch Option 3): the delta_report
	// drift detector reaches back through the hub to halt a drifting agent.
	// Injected post-construction to break the hub↔handler cycle.
	agentMsgs.SetKillSwitchSender(hub)
	// kill_switch action trail (Option 3 step 5) — first AuditLog writer.
	auditRepo := repository.NewAuditRepo(db)
	agentMsgs.auditor = auditRepo

	tickManager := instance.New(
		instanceRepo, portfolioRepo, tradeRepo, runtimeRepo,
		&instance.DefaultBarLoader{DB: db},
		&instance.DefaultStrategyResolver{Registry: registry},
		&instance.DefaultChampionGeneLoader{Challengers: challengerRepo},
		newRecordingDispatcher(hub, tradeRepo, nil), // TradeCommandDispatcher with pre-insert
		nil, // logger: slog.Default
	)
	// ⑤ Don't trade on stale klines: if the datafeeder falls behind, the
	// Tick skips dispatch rather than pricing orders off a stale close.
	// Zero → instance.DefaultMaxBarStaleness.
	tickManager.SetMaxBarStaleness(cfg.DataFeed.MaxBarStaleness)
	scheduler := cron.New(instanceRepo, tickManager, cron.Config{})

	gapRepo := repository.NewKLineGapRepo(db)
	klineRepo := repository.NewKLineRepo(db)
	userRepo := repository.NewUserRepo(db)

	h := &api.Handlers{
		Epoch:       svc,
		Tasks:       taskRepo,
		Challengers: challengerRepo,
		Champions:   championRepo,
		Instances:   instanceRepo,
		IDIssuer:    ulidIssuer{},
		// Phase 9 batch 1 — read-only diagnostics. Each repo doubles
		// as the interface impl; nothing extra to construct.
		TaskLister:      taskRepo,
		ChampionHistory: championRepo,
		Gaps:            gapRepo,
		Klines:          klineCoverageAdapter{repo: klineRepo},
		Trades:          tradeRepo,
		SharpeBank:      sharpeBankAdapter{repo: sharpeRepo},
		// Live-monitor (场景② F2). Repos double as the interfaces;
		// Presence reads the Hub registry, so it is wired only here
		// (the process that holds the Hub).
		InstanceList: instanceRepo,
		Portfolios:   portfolioRepo,
		Presence:     hubPresence{reg: hub.Registry()},
		Executions:   tradeRepo,
		Prices:       klineRepo,
		Recon:        reconRepo,
		Kills:        auditRepo, // /live frozen banner (Option 3 step 4)
		DataStalenessMs: func() int64 {
			if cfg.DataFeed.MaxBarStaleness > 0 {
				return cfg.DataFeed.MaxBarStaleness.Milliseconds()
			}
			return instance.DefaultMaxBarStaleness.Milliseconds()
		}(),
		AuthRequired: middleware.AuthRequired(authSvc),
		RequireOperator: middleware.RequireRole(
			store.UserRoleOperator, store.UserRoleAdmin,
		),
		RequireAdmin: middleware.RequireRole(store.UserRoleAdmin),
		// Manual kill_switch (Option 3 step 3b): reverse-map instance→
		// account + Hub.SendKillSwitch. Admin-gated via RequireAdmin above.
		Killer: &hubInstanceKiller{instances: instanceRepo, hub: hub, audit: auditRepo, logger: slog.Default()},
		// Resume (§5.13 v2): the inverse — lift the latch + re-arm
		// auto-freeze (streaks: agentMsgs.ClearDriftStreak). Admin-gated.
		Resumer: &hubInstanceResumer{instances: instanceRepo, hub: hub, audit: auditRepo, streaks: agentMsgs, logger: slog.Default()},
		// Sudo-style login: viewer-default JWTs with the long TTL,
		// admin JWTs auto-expire on JWT.AdminTTL (cfg default 10min).
		Users:  userRepo,
		Tokens: authSvc,
	}

	// Async kline import (Phase 9, docs/phase9-data-import-v1.md §2.3):
	// research/ops only — never exposed on a production saas instance.
	// The AppRole != saas gate wires the routes (via h.Imports), the
	// startup orphan sweep, and the single serial worker together; on
	// saas all three stay dark.
	if cfg.AppRole != config.AppRoleSaaS {
		h.Imports = importJobRepo
		if n, err := importJobRepo.SweepOrphans(ctx); err != nil {
			log.Printf("saas: import orphan sweep: %v", err)
		} else if n > 0 {
			log.Printf("saas: swept %d orphaned import job(s) → failed", n)
		}
		importFn := data.OrchestratorImportFunc(data.NewOrchestrator(db))
		go data.NewImportWorker(importJobRepo, importFn, nil).Run(ctx)
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
	// WS Hub serves Agent connections (the live-trading control plane). Per
	// the app_role matrix (docs/系统总体拓扑结构.md §2): saas + dev start the
	// listener, lab (pure backtest, no agents) skips it. The hub object is
	// still constructed either way so the API handlers (Presence/Killer/
	// Resumer) stay wired — only the public :8081 listener is gated.
	if cfg.AppRole != config.AppRoleLab {
		go func() {
			log.Printf("saas: ws listening on %s", cfg.Server.WSListen)
			if err := wsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("saas: ws server: %v", err)
			}
		}()
	} else {
		log.Printf("saas: app_role=lab — WS Hub listener disabled (no agents in backtest)")
	}

	// Cron Tick scheduler (Phase 6.2). It drives live-instance Step()s, so
	// only saas runs it; lab (backtest) and dev have no live instances to
	// tick (app_role matrix, docs/系统总体拓扑结构.md §2). Runs in-process;
	// stops cleanly when ctx is cancelled. In-flight Ticks complete on their
	// own (per-instance mutex in Manager).
	if cfg.AppRole == config.AppRoleSaaS {
		go scheduler.Run(ctx)
	} else {
		log.Printf("saas: app_role=%s — Cron Tick scheduler disabled (no live instances)", cfg.AppRole)
	}

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

	// HTTP is drained (no new CreateAndRunTask can race wg.Add against
	// wg.Wait), so it's safe to abort + wait for in-flight epochs. Any
	// that don't finish within shutdownCtx are abandoned and swept on the
	// next boot.
	svc.Shutdown(shutdownCtx)
}
