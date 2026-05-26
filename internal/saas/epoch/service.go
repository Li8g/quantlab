// Epoch service: the SaaS-side orchestrator that turns a
// CreateEvolutionTaskRequest into a persisted ChallengerResultPackage.
// Source-of-truth: docs/Coding-plan-dev-phases-prompts_v3_2_2.md
// Phase 5D step 1-4.
//
// Lifecycle, end-to-end:
//
//	HTTP   POST /api/v1/evolution/tasks
//	  → Service.CreateAndRunTask
//	    → TryLock (strategy, pair) — reject with ErrTaskInProgress if busy
//	    → taskRepo.Create (Queued)
//	    → goroutine:
//	        MarkStarted
//	        LoadKLines           (data layer)
//	        BuildEvaluablePlan   (data layer)
//	        RunEpoch             (engine layer)
//	        SharpeBank.Add       (when LongestWindowStats != nil)
//	        ComputeDSR           (when SharpeBank N ≥ MinTrialsForDSR)
//	        BuildChallengerPackage  (engine layer)
//	        ChallengerRepo.Save
//	        MarkSucceeded
//	        — or any error path → MarkFailed
//
// One-at-a-time per (strategy, pair) is enforced by an in-process map
// of sync.Mutex. Multi-instance scaling needs an external lock
// (Redis SETNX / Postgres advisory lock); CLAUDE.md leaves the
// mechanism TBD, so the prototype uses the simplest thing that works.
package epoch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"runtime"
	"sync"
	"time"

	"gorm.io/gorm"

	"quantlab/internal/api"
	"quantlab/internal/data"
	"quantlab/internal/domain"
	"quantlab/internal/engine"
	"quantlab/internal/fitness"
	"quantlab/internal/repository"
	"quantlab/internal/resultpkg"
	"quantlab/internal/verification"
)

// ErrTaskInProgress mirrors api.ErrTaskInProgress for callers that
// import only this package. The sentinel itself is defined in api so
// the HTTP handler can perform errors.Is without re-importing epoch
// (which would form a cycle).
var ErrTaskInProgress = api.ErrTaskInProgress

// BuildMeta is the cmd-startup constants stamped onto
// ReproducibilityMetadata. cmd/saas fills it from build flags +
// runtime.Version + the GOOS/GOARCH/CPU triplet.
type BuildMeta struct {
	DataVersion       string
	EngineVersion     string
	StrategyVersion   string
	HardwareSignature string
	GoVersion         string
	BuildID           string
}

// Defaults bundles the per-task knobs that the request can optionally
// override. Each field has a server-side baseline; if the request leaves
// the matching pointer nil, the Defaults value is used as-is.
type Defaults struct {
	WarmupDays  int                    // §4.3 caps at 1200; 365 covers all v1 indicators
	LotStep     float64                // Binance spot BTCUSDT default
	LotMin      float64                // Binance spot BTCUSDT default
	InitialUSDT float64                // Per-CrucibleWindow cold-start cash for strategy simulator
	DCA         fitness.GhostDCAConfig // GhostDCA baseline parameters
}

// DefaultDefaults returns the prototype-phase Defaults baseline.
func DefaultDefaults() Defaults {
	return Defaults{
		WarmupDays:  365,
		LotStep:     0.00001,
		LotMin:      0.00001,
		InitialUSDT: 10_000,
		DCA: fitness.GhostDCAConfig{
			InitialCapital: 10_000,
			MonthlyInject:  0,
		},
	}
}

// resolveDefaults merges the request's optional override fields with
// the server-side Defaults. Non-nil request pointers win; nil falls
// back to the Defaults value.
func resolveDefaults(base Defaults, req api.CreateEvolutionTaskRequest) Defaults {
	eff := base
	if req.WarmupDays != nil {
		eff.WarmupDays = *req.WarmupDays
	}
	if req.LotStep != nil {
		eff.LotStep = *req.LotStep
	}
	if req.LotMin != nil {
		eff.LotMin = *req.LotMin
	}
	if req.InitialUSDT != nil {
		eff.InitialUSDT = *req.InitialUSDT
	}
	if req.DCA != nil {
		eff.DCA = fitness.GhostDCAConfig{
			InitialCapital: req.DCA.InitialCapital,
			MonthlyInject:  req.DCA.MonthlyInject,
		}
	}
	return eff
}

// Service is the per-process Epoch orchestrator. One instance per SaaS
// process; HTTP handlers share it.
type Service struct {
	db             *gorm.DB
	taskRepo       *repository.EvolutionTaskRepo
	challengerRepo *repository.ChallengerRepo
	sharpeRepo     *repository.SharpeBankRepo
	traceRepo      *repository.EvaluationTraceRepo
	registry       *Registry
	buildMeta      BuildMeta
	defaults       Defaults

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex
}

// New wires a Service. taskRepo / challengerRepo / sharpeRepo must be
// non-nil; traceRepo may be nil (callers that don't want per-individual
// EvaluationTrace persistence — e.g. unit tests — pass nil). New does
// not provision repos (cmd/saas does that against a single *gorm.DB).
func New(
	db *gorm.DB,
	taskRepo *repository.EvolutionTaskRepo,
	challengerRepo *repository.ChallengerRepo,
	sharpeRepo *repository.SharpeBankRepo,
	traceRepo *repository.EvaluationTraceRepo,
	registry *Registry,
	buildMeta BuildMeta,
	defaults Defaults,
) *Service {
	return &Service{
		db:             db,
		taskRepo:       taskRepo,
		challengerRepo: challengerRepo,
		sharpeRepo:     sharpeRepo,
		traceRepo:      traceRepo,
		registry:       registry,
		buildMeta:      buildMeta,
		defaults:       defaults,
		locks:          map[string]*sync.Mutex{},
	}
}

// CreateAndRunTask is the HTTP-facing entry point. The request is
// assumed validated (api.CreateEvolutionTaskRequest.Validate); this
// method only enforces the engine-level invariants (strategy known,
// interval known, no in-progress task for this tuple).
//
// On success the new taskID is returned synchronously and the Epoch
// runs in a detached goroutine. Callers poll GET /tasks/:id for
// status; the HTTP body is just {task_id: "..."}.
func (s *Service) CreateAndRunTask(
	ctx context.Context,
	req api.CreateEvolutionTaskRequest,
) (string, error) {
	if _, ok := s.registry.Get(req.StrategyID); !ok {
		return "", fmt.Errorf("epoch: unknown strategy_id %q: %w", req.StrategyID, api.ErrUnknownStrategy)
	}
	if _, err := data.IntervalToMs(req.Interval); err != nil {
		return "", fmt.Errorf("epoch: %w: %v", api.ErrUnsupportedInterval, err)
	}

	mu := s.lockFor(req.StrategyID, req.Pair)
	if !mu.TryLock() {
		return "", ErrTaskInProgress
	}

	taskID, err := newTaskID()
	if err != nil {
		mu.Unlock()
		return "", fmt.Errorf("epoch: gen taskID: %w", err)
	}
	epochSeed := time.Now().UnixNano()

	if err := s.taskRepo.Create(ctx, taskID, epochSeed, req); err != nil {
		mu.Unlock()
		return "", fmt.Errorf("epoch: create task row: %w", err)
	}

	go s.run(taskID, epochSeed, req, mu)
	return taskID, nil
}

// run executes the Epoch on a detached goroutine. The lock is held for
// the full duration and released by defer.
//
// All errors funnel into MarkFailed with a human-readable reason. The
// HTTP status endpoint surfaces FailureReason verbatim, so callers can
// debug without log access.
func (s *Service) run(
	taskID string,
	epochSeed int64,
	req api.CreateEvolutionTaskRequest,
	mu *sync.Mutex,
) {
	defer mu.Unlock()

	bgCtx := context.Background()
	defer func() {
		if rec := recover(); rec != nil {
			_ = s.taskRepo.MarkFailed(bgCtx, taskID, fmt.Sprintf("panic: %v", rec))
		}
	}()

	if err := s.taskRepo.MarkStarted(bgCtx, taskID); err != nil {
		// Mark-failed will fail the same way; nothing better to do.
		_ = s.taskRepo.MarkFailed(bgCtx, taskID, fmt.Sprintf("mark started: %v", err))
		return
	}

	if err := s.executeEpoch(bgCtx, taskID, epochSeed, req); err != nil {
		_ = s.taskRepo.MarkFailed(bgCtx, taskID, err.Error())
		return
	}
}

// executeEpoch is the sequential pipeline. Split out so panic recovery
// in run() doesn't entangle with the success path.
func (s *Service) executeEpoch(
	ctx context.Context,
	taskID string,
	epochSeed int64,
	req api.CreateEvolutionTaskRequest,
) error {
	barIntervalMs, err := data.IntervalToMs(req.Interval)
	if err != nil {
		return err
	}
	strat, err := s.registry.Build(req.StrategyID, barIntervalMs)
	if err != nil {
		return err
	}

	bars, err := data.LoadKLines(ctx, s.db, req.Pair, req.Interval, 0, math.MaxInt64)
	if err != nil {
		return fmt.Errorf("load klines: %w", err)
	}
	if len(bars) == 0 {
		return fmt.Errorf("no klines for %s @ %s", req.Pair, req.Interval)
	}

	// EFFECTIVE friction: TestMode=true zeroes both (M16 P03). The
	// user's original values stay on EvolutionTask.RequestedTaker/Slippage.
	effective := domain.FrictionParams{
		TakerFeeBPS: req.TakerFeeBPS,
		SlippageBPS: req.SlippageBPS,
	}
	if req.TestMode {
		effective.TakerFeeBPS = 0
		effective.SlippageBPS = 0
	}

	spawn := resultpkg.SpawnPointPayload{SpawnMode: req.SpawnMode}
	if req.SpawnPoint != nil {
		spawn.Meta = *req.SpawnPoint
	}

	effDefaults := resolveDefaults(s.defaults, req)
	planOpts := data.PlanOptions{
		Pair:        req.Pair,
		Spawn:       spawn,
		WarmupDays:  effDefaults.WarmupDays,
		OosDays:     req.OosDays,
		Friction:    effective,
		LotStep:     effDefaults.LotStep,
		LotMin:      effDefaults.LotMin,
		FatalMDD:    req.FatalMDD,
		InitialUSDT: effDefaults.InitialUSDT,
		DCA:         effDefaults.DCA,
	}
	plan, planHash, barsHash, err := data.BuildEvaluablePlan(bars, planOpts)
	if err != nil {
		return fmt.Errorf("build plan: %w", err)
	}

	cfg := s.engineConfigFromRequest(req, epochSeed)
	if s.traceRepo != nil {
		cfg.OnGenerationEvaluated = func(
			gen int,
			pop []domain.Gene,
			scores []resultpkg.ScoreTotal,
			raws []*resultpkg.RawEvaluateResult,
			fingerprints []string,
		) {
			rows, err := repository.BuildRows(taskID, gen, pop, scores, raws, fingerprints)
			if err != nil {
				log.Printf("epoch: trace build rows (task=%s gen=%d): %v", taskID, gen, err)
				return
			}
			if err := s.traceRepo.BulkInsert(ctx, rows); err != nil {
				log.Printf("epoch: trace bulk insert (task=%s gen=%d n=%d): %v", taskID, gen, len(rows), err)
			}
		}
	}
	eng := engine.New(strat, cfg)
	result, err := eng.RunEpoch(ctx, plan)
	if err != nil {
		return fmt.Errorf("run epoch: %w", err)
	}

	// SharpeBank + DSR. Skip the bank when the cascade produced no
	// stats (best individual was Fatal or no non-Fatal window
	// completed); the DSR pipeline can't ingest those.
	challengerID, err := newChallengerID()
	if err != nil {
		return fmt.Errorf("gen challengerID: %w", err)
	}

	var dsrBlob json.RawMessage
	if stats := result.BestRawEvaluate.LongestWindowStats; stats != nil {
		if err := s.sharpeRepo.Add(ctx, req.StrategyID, req.Pair, repository.SharpeBankEntry{
			ChallengerID: challengerID,
			SpawnMode:    req.SpawnMode,
			Stats:        *stats,
		}); err != nil {
			return fmt.Errorf("sharpe bank add: %w", err)
		}
		bankStats, err := s.sharpeRepo.Stats(ctx, req.StrategyID, req.Pair)
		if err != nil {
			return fmt.Errorf("sharpe bank stats: %w", err)
		}
		if bankStats.N >= verification.MinTrialsForDSR {
			dsr := verification.ComputeDSR(
				stats.ObservedSharpe, bankStats.SharpeVariance,
				bankStats.N, stats.HorizonT, stats.Skew, stats.ExcessKurt,
			)
			// DSR=NaN encodes "computed but unreliable" (variance≤0 or
			// skew/kurt degeneracy). Map to *float64 nil so json.Marshal
			// stays valid; the diagnostic fields below survive so the
			// front-end can explain why.
			var dsrPtr *float64
			if !math.IsNaN(dsr) {
				dsrPtr = &dsr
			}
			summary := verification.DSRSummary{
				DSR:            dsrPtr,
				ObservedSharpe: stats.ObservedSharpe,
				SharpeVariance: bankStats.SharpeVariance,
				NTrials:        bankStats.N,
				HorizonT:       stats.HorizonT,
				Skew:           stats.Skew,
				ExcessKurt:     stats.ExcessKurt,
			}
			b, err := json.Marshal(summary)
			if err != nil {
				return fmt.Errorf("marshal DSR summary: %w", err)
			}
			dsrBlob = b
		}
	}

	bc := engine.BuildContext{
		ChallengerID:         challengerID,
		Pair:                 req.Pair,
		TestMode:             req.TestMode,
		OosDays:              req.OosDays,
		FatalAuditSampleRate: req.FatalAuditSampleRate,
		DataVersion:          s.buildMeta.DataVersion,
		EngineVersion:        s.buildMeta.EngineVersion,
		StrategyVersion:      s.buildMeta.StrategyVersion,
		HardwareSignature:    s.buildMeta.HardwareSignature,
		GoVersion:            s.buildMeta.GoVersion,
		BuildID:              s.buildMeta.BuildID,
		PlanHash:             planHash,
		BarsHash:             barsHash,
		DSRSummary:           dsrBlob,
		FatalAuditSamples:    result.FatalAuditSamples,
	}
	pkg, err := engine.BuildChallengerPackage(
		strat, plan, result.BestGene, result.BestRawEvaluate, result.BestScore, cfg, bc,
	)
	if err != nil {
		return fmt.Errorf("build package: %w", err)
	}

	if err := s.challengerRepo.Save(ctx, challengerID, pkg); err != nil {
		return fmt.Errorf("save challenger: %w", err)
	}

	if err := s.taskRepo.MarkSucceeded(ctx, taskID, challengerID, result.Generations, result.BestScore.Value); err != nil {
		return fmt.Errorf("mark succeeded: %w", err)
	}
	return nil
}

// engineConfigFromRequest maps the HTTP request to EngineConfig, honoring
// the request's per-task tuning knobs (PopSize, MaxGen, EliteRatio) and
// falling back to DefaultConfig for everything else (mutation ramp,
// early-stop, fatal-audit defaults).
func (s *Service) engineConfigFromRequest(req api.CreateEvolutionTaskRequest, epochSeed int64) engine.EngineConfig {
	cfg := engine.DefaultConfig()
	cfg.PopSize = req.PopSize
	cfg.MaxGenerations = req.MaxGenerations
	cfg.EliteRatio = req.EliteRatio
	cfg.EpochSeed = epochSeed
	if req.FatalAuditSampleRate != nil {
		cfg.FatalAuditSampleRate = *req.FatalAuditSampleRate
	}
	return cfg
}

// lockFor returns the *sync.Mutex for (strategyID, pair), creating it
// on first access. Same-tuple callers serialize; different tuples run
// in parallel.
func (s *Service) lockFor(strategyID, pair string) *sync.Mutex {
	key := strategyID + ":" + pair
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	m, ok := s.locks[key]
	if !ok {
		m = &sync.Mutex{}
		s.locks[key] = m
	}
	return m
}

// HardwareSignature builds the "{GOOS}/{GOARCH}/cpu-default" string for
// BuildMeta. CPU model lookup is intentionally crude (runtime.NumCPU
// proxies) — the v3 P05 contract pins this as a soft cross-machine
// boundary, not a hash input.
func HardwareSignature() string {
	return fmt.Sprintf("%s/%s/cpu%d", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
}

// newTaskID + newChallengerID are 128-bit random hex strings. Their
// uniqueness is enforced by the DB unique indexes; collisions are
// astronomically improbable (~2^-64 birthday at billions of rows).
func newTaskID() (string, error)        { return randHex16() }
func newChallengerID() (string, error)  { return randHex16() }

func randHex16() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
