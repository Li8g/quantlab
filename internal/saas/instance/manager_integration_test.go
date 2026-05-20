//go:build integration

// Integration test for instance.Manager.Tick against a live
// Postgres+TimescaleDB instance. Verifies the wiring of all three
// new Tier 2 repos (InstanceRepo / PortfolioRepo / RuntimeRepo)
// + the DefaultChampionGeneLoader + DefaultBarLoader against real
// data.
//
// Uses a custom in-test strategy registered through an inline
// StrategyResolver so we don't need to seed thousands of klines just
// for sigmoid_v1's MinEvalBars budget. The point of the integration
// test is the DB round-trip, not strategy behaviour (which is
// covered by sigmoid_v1's own tests).
//
// Run:
//
//	go test -tags=integration ./internal/saas/instance/ \
//	    -args -config=/absolute/path/to/config.yaml
package instance_test

import (
	"context"
	"encoding/json"
	"flag"
	"math/rand"
	"testing"
	"time"

	"quantlab/internal/domain"
	"quantlab/internal/repository"
	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/instance"
	"quantlab/internal/saas/store"
	"quantlab/internal/strategy"
)

var configPath = flag.String("config", "config.yaml", "path to config.yaml for integration test")

const (
	itTestStrategyID = "tick_it_strategy"
	itTestInstanceID = "inst-tick-it-001"
	itTestChampID    = "ch-tick-it-001"
	itTestPair       = "TICKBTC"
)

func TestTick_EndToEnd_AgainstRealDB(t *testing.T) {
	cfg, err := config.Load(*configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	db, err := store.NewDB(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	ctx := context.Background()

	// === clean up any prior run ===
	// Unscoped() bypasses GORM soft-delete; the Tier 1 gene_records
	// table uses gorm.Model and its uniqueIndex on challenger_id is
	// NOT partial-on-deleted_at, so leftover soft-deleted rows would
	// collide on re-insert.
	cleanup := func() {
		_ = db.Unscoped().Where("instance_id = ?", itTestInstanceID).Delete(&store.PortfolioState{})
		_ = db.Unscoped().Where("instance_id = ?", itTestInstanceID).Delete(&store.RuntimeState{})
		_ = db.Unscoped().Where("instance_id = ?", itTestInstanceID).Delete(&store.StrategyInstance{})
		_ = db.Unscoped().Where("challenger_id = ?", itTestChampID).Delete(&store.GeneRecord{})
		_ = db.Unscoped().Where("symbol = ?", itTestPair).Delete(&store.KLine{})
	}
	cleanup()
	t.Cleanup(cleanup)

	// === seed: Champion result package (challengers row) ===
	pkg := itSamplePackage()
	challengerRepo := repository.NewChallengerRepo(db)
	if err := challengerRepo.Save(ctx, itTestChampID, pkg); err != nil {
		t.Fatalf("save champion: %v", err)
	}

	// === seed: StrategyInstance row, status=live, ActiveChampID set ===
	champID := itTestChampID
	instRepo := repository.NewInstanceRepo(db)
	if err := instRepo.Create(ctx, &store.StrategyInstance{
		InstanceID:    itTestInstanceID,
		StrategyID:    itTestStrategyID,
		Pair:          itTestPair,
		AccountID:     "acct-tick-it",
		OwnerUserID:   9_999_111,
		Status:        store.InstanceStatusLive,
		ActiveChampID: &champID,
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	// === seed: a handful of 1m klines ===
	now := time.Now().UnixMilli()
	const oneMin = int64(60_000)
	const nBars = 10
	bars := make([]store.KLine, nBars)
	for i := 0; i < nBars; i++ {
		bars[i] = store.KLine{
			Symbol:   itTestPair,
			Interval: "1m",
			OpenTime: now - int64(nBars-i)*oneMin,
			Open:     100, High: 101, Low: 99, Close: 100,
			Volume: 1, Source: "integration",
		}
	}
	if err := db.Create(&bars).Error; err != nil {
		t.Fatalf("seed klines: %v", err)
	}

	// === construct Manager with real repos + fake strategy resolver ===
	rec := &itRecordingDispatcher{}
	mgr := instance.New(
		instRepo,
		repository.NewPortfolioRepo(db),
		repository.NewRuntimeRepo(db),
		&instance.DefaultBarLoader{DB: db},
		&itStrategyResolver{strat: newITStrategy()},
		&instance.DefaultChampionGeneLoader{Challengers: challengerRepo},
		rec,
		nil,
	)

	loaded, err := instRepo.Get(ctx, itTestInstanceID)
	if err != nil {
		t.Fatalf("instance Get: %v", err)
	}

	if err := mgr.Tick(ctx, *loaded); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// === assertions ===
	portfolioRepo := repository.NewPortfolioRepo(db)
	ps, err := portfolioRepo.Latest(ctx, itTestInstanceID)
	if err != nil {
		t.Fatalf("PortfolioRepo.Latest: %v", err)
	}
	if ps == nil {
		t.Fatal("Tick did not append a PortfolioState row")
	}
	if ps.LastProcessedBarTime != bars[len(bars)-1].OpenTime {
		t.Errorf("LastProcessedBarTime = %d, want last bar OpenTime %d",
			ps.LastProcessedBarTime, bars[len(bars)-1].OpenTime)
	}

	runtimeRepo := repository.NewRuntimeRepo(db)
	rs, err := runtimeRepo.Get(ctx, itTestInstanceID)
	if err != nil {
		t.Fatalf("RuntimeRepo.Get: %v", err)
	}
	if rs == nil {
		t.Fatal("Tick did not UPSERT RuntimeState")
	}
	// PG normalises jsonb (adds whitespace, key reorder); semantic compare.
	var got map[string]string
	if err := json.Unmarshal(rs.StateJSON, &got); err != nil {
		t.Fatalf("unmarshal RuntimeState.StateJSON: %v", err)
	}
	if got["it"] != "first_tick" {
		t.Errorf("RuntimeState.StateJSON = %s, want {it: first_tick}", string(rs.StateJSON))
	}

	if len(rec.calls) != 1 {
		t.Fatalf("dispatcher calls = %d, want 1 (one macro order from itStrategy)", len(rec.calls))
	}
	if rec.calls[0].instanceID != itTestInstanceID {
		t.Errorf("dispatched instance id = %q, want %q", rec.calls[0].instanceID, itTestInstanceID)
	}

	// Re-read instance: LastTickWallTime should be stamped (within last 5s).
	loaded2, err := instRepo.Get(ctx, itTestInstanceID)
	if err != nil {
		t.Fatalf("instance Get post-tick: %v", err)
	}
	if loaded2.LastTickWallTime == nil {
		t.Fatal("LastTickWallTime not stamped")
	}
	if time.Since(*loaded2.LastTickWallTime) > 5*time.Second {
		t.Errorf("LastTickWallTime = %v, stale (>5s old)", *loaded2.LastTickWallTime)
	}
}

// === in-test fakes ===

type itRecordingDispatcher struct {
	calls []itDispatchCall
}

type itDispatchCall struct {
	instanceID  string
	accountID   string
	symbol      string
	latestClose float64
	orders      []strategy.OrderIntent
}

func (d *itRecordingDispatcher) Dispatch(_ context.Context, instID, acctID, symbol string, latestClose float64, orders []strategy.OrderIntent) error {
	d.calls = append(d.calls, itDispatchCall{instID, acctID, symbol, latestClose, append([]strategy.OrderIntent(nil), orders...)})
	return nil
}

type itStrategyResolver struct{ strat *itStrategy }

func (r *itStrategyResolver) Resolve(_ string) (strategy.EvolvableStrategy, strategy.RuntimeStrategy, error) {
	return r.strat, r.strat, nil
}

func newITStrategy() *itStrategy { return &itStrategy{} }

// itStrategy returns one macro order and a small RuntimeState blob.
type itStrategy struct{}

func (s *itStrategy) Step(_ strategy.StrategyInput) (strategy.StrategyOutput, error) {
	return strategy.StrategyOutput{
		MacroOrders: []strategy.OrderIntent{
			{
				Kind: strategy.OrderKindMacro, Side: strategy.OrderSideBuy,
				OrderType: strategy.OrderTypeMarket, QuantityUSD: 250,
				ClientOrderID: "it-co-1",
			},
		},
		RuntimeState: json.RawMessage(`{"it":"first_tick"}`),
	}, nil
}

// EvolvableStrategy stubs — only DecodeElite + MinEvalBars are used by
// the Tick path under this resolver; the rest panic.
func (s *itStrategy) StrategyID() string { return itTestStrategyID }
func (s *itStrategy) MinEvalBars() int   { return 5 }
func (s *itStrategy) DecodeElite(_ resultpkg.ChampionGenePayload) (domain.Gene, error) {
	return domain.Gene{0.1, 0.2}, nil
}
func (s *itStrategy) Segments() []domain.SegmentInfo                    { panic("not used") }
func (s *itStrategy) Sample(_ *rand.Rand) domain.Gene                   { panic("not used") }
func (s *itStrategy) Clamp(g domain.Gene) domain.Gene                   { return g }
func (s *itStrategy) Validate(_ domain.Gene) error                      { return nil }
func (s *itStrategy) Crossover(p1, _ domain.Gene, _ *rand.Rand) domain.Gene { return p1 }
func (s *itStrategy) Mutate(g domain.Gene, _, _ float64, _ *rand.Rand) domain.Gene {
	return g
}
func (s *itStrategy) Fingerprint(_ domain.Gene) string { return "it-fp" }
func (s *itStrategy) Evaluate(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.RawEvaluateResult, error) {
	panic("not used")
}
func (s *itStrategy) ReviewBacktest(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.ReviewSummary, error) {
	return nil, nil
}
func (s *itStrategy) EncodeResult(_ domain.Gene, _ resultpkg.SpawnPointPayload, _ resultpkg.ReproducibilityMetadata, _ resultpkg.GAConfigSnapshot, _ *resultpkg.EvaluationLayer, _ *resultpkg.VerificationLayer, _ *resultpkg.DiagnosticsLayer) (resultpkg.ChallengerResultPackage, error) {
	panic("not used")
}
func (s *itStrategy) NewAdapter(_ *domain.EvaluablePlan) (strategy.Adapter, error) {
	panic("not used")
}

// itSamplePackage is the minimum well-formed ChallengerResultPackage
// the ChampionGeneLoader needs to round-trip. Gene payload is a JSON
// array; itStrategy.DecodeElite just returns a fixed gene anyway.
func itSamplePackage() resultpkg.ChallengerResultPackage {
	score := 1.0
	scoreRaw := 1.0
	cons := 0.0
	return resultpkg.ChallengerResultPackage{
		Core: resultpkg.ResultCore{
			StrategyID: itTestStrategyID,
			ChampionGene: resultpkg.ChampionGenePayload{
				Encoding: resultpkg.GeneEncodingJSON,
				Payload:  json.RawMessage(`[0.1,0.2,0.3]`),
			},
			SpawnPoint: resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeRandomOnce},
			ReproducibilityMetadata: resultpkg.ReproducibilityMetadata{
				EpochSeed:          1,
				DataVersion:        "binance/v1",
				EngineVersion:      "engine-it",
				StrategyVersion:    "it-1.0",
				SchemaVersion:      resultpkg.SchemaVersionV533,
				FitnessVersion:     resultpkg.FitnessVersionV1RawStd,
				FingerprintVersion: resultpkg.FingerprintVersionV1,
				HardwareSignature:  "linux/amd64/test",
				GoVersion:          "go1.23",
				BuildID:            "it-build",
				PlanHash:           "deadbeef",
				BarsHash:           "cafef00d",
			},
			GAConfig: resultpkg.GAConfigSnapshot{
				StrategyID: itTestStrategyID, Pair: itTestPair,
				PopSize: 4, MaxGenerations: 1, EliteRatio: 0.25, FatalMDD: 0.5,
				SpawnMode: resultpkg.SpawnModeRandomOnce,
			},
			SchemaVersion:      resultpkg.SchemaVersionV533,
			FitnessVersion:     resultpkg.FitnessVersionV1RawStd,
			FingerprintVersion: resultpkg.FingerprintVersionV1,
		},
		Evaluation: resultpkg.EvaluationLayer{
			ScoreTotal: resultpkg.ScoreTotal{
				Value: &score, ScoreRaw: &scoreRaw, ConsistencyPenalty: &cons,
			},
		},
		Promote: resultpkg.PromoteLayer{DecisionStatus: resultpkg.DecisionStatusPromoted},
	}
}
