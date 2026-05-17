package api

import (
	"encoding/json"
	"testing"

	"quantlab/internal/resultpkg"
)

func intPtr(i int) *int       { return &i }
func f64Ptr(f float64) *float64 { return &f }

func TestCreateEvolutionTaskRequest_Validate(t *testing.T) {
	good := CreateEvolutionTaskRequest{
		StrategyID:     "demo",
		Pair:           "BTCUSDT",
		PopSize:        200,
		MaxGenerations: 30,
		EliteRatio:     0.05,
		FatalMDD:       0.7,
		TakerFeeBPS:    10,
		SlippageBPS:    5,
		SpawnMode:      resultpkg.SpawnModeRandomOnce,
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("expected valid request, got: %v", err)
	}

	cases := []struct {
		name string
		mut  func(r *CreateEvolutionTaskRequest)
	}{
		{"missing strategy_id", func(r *CreateEvolutionTaskRequest) { r.StrategyID = "" }},
		{"missing pair", func(r *CreateEvolutionTaskRequest) { r.Pair = "" }},
		{"pop_size zero", func(r *CreateEvolutionTaskRequest) { r.PopSize = 0 }},
		{"max_generations zero", func(r *CreateEvolutionTaskRequest) { r.MaxGenerations = 0 }},
		{"elite_ratio neg", func(r *CreateEvolutionTaskRequest) { r.EliteRatio = -0.1 }},
		{"elite_ratio too big", func(r *CreateEvolutionTaskRequest) { r.EliteRatio = 1.1 }},
		{"fatal_mdd neg", func(r *CreateEvolutionTaskRequest) { r.FatalMDD = -0.1 }},
		{"taker_fee neg", func(r *CreateEvolutionTaskRequest) { r.TakerFeeBPS = -1 }},
		{"slippage neg", func(r *CreateEvolutionTaskRequest) { r.SlippageBPS = -1 }},
		{"bogus spawn_mode", func(r *CreateEvolutionTaskRequest) { r.SpawnMode = "bogus" }},
		{"manual without spawn_point", func(r *CreateEvolutionTaskRequest) {
			r.SpawnMode = resultpkg.SpawnModeManual
		}},
		{"spawn_point without manual", func(r *CreateEvolutionTaskRequest) {
			rm := json.RawMessage(`{}`)
			r.SpawnPoint = &rm
		}},
		{"audit_rate too big", func(r *CreateEvolutionTaskRequest) {
			r.FatalAuditSampleRate = f64Ptr(1.5)
		}},
		{"oos_days zero", func(r *CreateEvolutionTaskRequest) { r.OosDays = intPtr(0) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := good
			tc.mut(&r)
			if err := r.Validate(); err == nil {
				t.Errorf("expected error for %q, got nil", tc.name)
			}
		})
	}
}

func TestCreateEvolutionTaskRequest_ManualSpawnPointOK(t *testing.T) {
	raw := json.RawMessage(`{"spawn_mode":"manual"}`)
	r := CreateEvolutionTaskRequest{
		StrategyID:     "demo",
		Pair:           "BTCUSDT",
		PopSize:        100,
		MaxGenerations: 10,
		EliteRatio:     0.05,
		FatalMDD:       0.7,
		TakerFeeBPS:    10,
		SlippageBPS:    5,
		SpawnMode:      resultpkg.SpawnModeManual,
		SpawnPoint:     &raw,
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("expected manual+spawn_point to validate, got %v", err)
	}
}

func TestPromoteAndRetireRequire_ReviewedBy(t *testing.T) {
	if err := (&PromoteChallengerRequest{}).Validate(); err == nil {
		t.Error("Promote without reviewed_by should fail")
	}
	if err := (&RetireChampionRequest{}).Validate(); err == nil {
		t.Error("Retire without reviewed_by should fail")
	}
	if err := (&PromoteChallengerRequest{ReviewedBy: "alice"}).Validate(); err != nil {
		t.Errorf("Promote with reviewed_by should pass, got %v", err)
	}
}
