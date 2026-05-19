package repository

import (
	"testing"

	"quantlab/internal/api"
	"quantlab/internal/resultpkg"
)

func sampleRequest() api.CreateEvolutionTaskRequest {
	oos := 90
	rate := 0.05
	return api.CreateEvolutionTaskRequest{
		StrategyID:           "sigmoid_v1",
		Pair:                 "BTCUSDT",
		Interval:             "1h",
		PopSize:              200,
		MaxGenerations:       25,
		EliteRatio:           0.05,
		FatalMDD:             0.5,
		TakerFeeBPS:          5,
		SlippageBPS:          2,
		SpawnMode:            resultpkg.SpawnModeRandomOnce,
		TestMode:             false,
		OosDays:              &oos,
		FatalAuditSampleRate: &rate,
	}
}

func TestBuildEvolutionTask_MirrorsRequest(t *testing.T) {
	req := sampleRequest()
	row := buildEvolutionTask("task-001", 12345, req)

	if row.TaskID != "task-001" {
		t.Errorf("TaskID = %q, want task-001", row.TaskID)
	}
	if row.EpochSeed != 12345 {
		t.Errorf("EpochSeed = %d, want 12345", row.EpochSeed)
	}
	if row.StrategyID != req.StrategyID {
		t.Errorf("StrategyID = %q, want %q", row.StrategyID, req.StrategyID)
	}
	if row.Pair != req.Pair || row.Interval != req.Interval {
		t.Errorf("Pair/Interval mismatch: %q/%q vs %q/%q",
			row.Pair, row.Interval, req.Pair, req.Interval)
	}
	if row.Status != resultpkg.TaskStatusQueued {
		t.Errorf("Status = %q, want queued", row.Status)
	}
	if row.SpawnMode != req.SpawnMode {
		t.Errorf("SpawnMode = %q, want %q", row.SpawnMode, req.SpawnMode)
	}
	if row.OosDays == nil || *row.OosDays != 90 {
		t.Errorf("OosDays mismatch: %v", row.OosDays)
	}
	if row.FatalAuditSampleRate == nil || *row.FatalAuditSampleRate != 0.05 {
		t.Errorf("FatalAuditSampleRate mismatch: %v", row.FatalAuditSampleRate)
	}
}

// TestBuildEvolutionTask_M16AuditFieldsPreserveUserIntent pins the
// CLAUDE.md M16 invariant: RequestedTakerFeeBPS and RequestedSlippageBPS
// hold the user's original intent, never the TestMode-coerced effective
// values. The coercion to 0 happens at the engine layer (GAConfigSnapshot
// inside the result package).
func TestBuildEvolutionTask_M16AuditFieldsPreserveUserIntent(t *testing.T) {
	req := sampleRequest()
	req.TestMode = true
	req.TakerFeeBPS = 15
	req.SlippageBPS = 8

	row := buildEvolutionTask("task-002", 1, req)
	if row.RequestedTakerFeeBPS != 15 || row.RequestedSlippageBPS != 8 {
		t.Errorf("M16 audit fields lost: taker=%v slippage=%v, want 15/8",
			row.RequestedTakerFeeBPS, row.RequestedSlippageBPS)
	}
	if !row.TestMode {
		t.Error("TestMode bit dropped")
	}
}

func TestBuildEvolutionTask_NilOptionalsStayNil(t *testing.T) {
	req := sampleRequest()
	req.OosDays = nil
	req.FatalAuditSampleRate = nil

	row := buildEvolutionTask("task-003", 1, req)
	if row.OosDays != nil {
		t.Errorf("OosDays = %v, want nil", row.OosDays)
	}
	if row.FatalAuditSampleRate != nil {
		t.Errorf("FatalAuditSampleRate = %v, want nil", row.FatalAuditSampleRate)
	}
}
