//go:build integration

// End-to-end integration test for the SaaS pipeline: HTTP handlers →
// epoch.Service → engine.RunEpoch → ChallengerRepo → JSON package.
//
// Run with:
//
//	go test -tags=integration ./internal/saas/epoch/ \
//	    -args -config=/absolute/path/to/config.yaml
//
// Seeds two test-only symbols ("TESTBTC", "TESTSHORT") so the test does
// not collide with real data. Cleans both klines and task/challenger
// rows on entry and via t.Cleanup so reruns are idempotent.
//
// Coverage rationale: handler unit tests with fakes verify route/status
// mappings; THIS file verifies the pipeline that wires real DB +
// goroutine + plan/engine layers — the slice of behaviour that no
// fake can simulate (and where today's "silent succeeded score=0"
// bug actually lived).
package epoch

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"quantlab/internal/api"
	"quantlab/internal/repository"
	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

var configPath = flag.String("config", "config.yaml", "path to config.yaml for integration test")

const (
	testSymbolOK    = "TESTBTC"
	testSymbolShort = "TESTSHORT"
	testInterval    = "1d"
	dayMs           = int64(86_400_000)

	// Sufficient bars: 3000 daily bars (~8.2y) makes all 4 IS windows
	// fit (6m+warmup=548d, 2y+warmup=1095d, 5y+warmup=2190d, 10y eval =
	// span minus warmup ≈ 2635d).
	sufficientBars = 3000
	// Insufficient bars: 30 daily bars (~30d) — even the 6m+warmup
	// minimum of 548d is unreachable. BuildEvaluablePlan must error.
	insufficientBars = 30
)

func setupE2E(t *testing.T) (server *httptest.Server, db *gorm.DB) {
	t.Helper()
	cfg, err := config.Load(*configPath)
	if err != nil {
		t.Fatalf("load config %s: %v", *configPath, err)
	}
	db, err = store.NewDB(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}

	cleanRows(t, db)
	seedKlines(t, db, testSymbolOK, sufficientBars)
	seedKlines(t, db, testSymbolShort, insufficientBars)

	taskRepo := repository.NewEvolutionTaskRepo(db)
	challengerRepo := repository.NewChallengerRepo(db)
	championRepo := repository.NewChampionRepo(db)
	sharpeRepo := repository.NewSharpeBankRepo(db)
	traceRepo := repository.NewEvaluationTraceRepo(db)

	svc := New(db, taskRepo, challengerRepo, sharpeRepo, traceRepo,
		DefaultRegistry(),
		BuildMeta{
			DataVersion:       "test",
			EngineVersion:     "test",
			StrategyVersion:   "test",
			BuildID:           "test",
			HardwareSignature: runtime.GOOS + "/" + runtime.GOARCH,
			GoVersion:         runtime.Version(),
		},
		DefaultDefaults(),
	)

	h := &api.Handlers{
		Epoch:       svc,
		Tasks:       taskRepo,
		Challengers: challengerRepo,
		Champions:   championRepo,
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.Register(r)

	server = httptest.NewServer(r)
	t.Cleanup(func() {
		server.Close()
		cleanRows(t, db)
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return server, db
}

func seedKlines(t *testing.T, db *gorm.DB, symbol string, nDays int) {
	t.Helper()
	rows := make([]store.KLine, nDays)
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	price := 100.0
	for i := 0; i < nDays; i++ {
		rows[i] = store.KLine{
			Symbol:   symbol,
			Interval: testInterval,
			OpenTime: base + int64(i)*dayMs,
			Open:     price,
			High:     price + 1,
			Low:      price - 1,
			Close:    price,
			Volume:   1000,
			Source:   "integration-test",
		}
		price *= 1.0001
	}
	if err := db.CreateInBatches(rows, 500).Error; err != nil {
		t.Fatalf("seed klines %s: %v", symbol, err)
	}
}

func cleanRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	pairs := []string{testSymbolOK, testSymbolShort}
	_ = db.Where("symbol IN ?", pairs).Delete(&store.KLine{}).Error
	_ = db.Where("pair IN ?", pairs).Delete(&store.EvolutionTask{}).Error
	_ = db.Where("pair IN ?", pairs).Delete(&store.GeneRecord{}).Error
	_ = db.Where("pair_id IN ?", pairs).Delete(&store.SharpeBank{}).Error
}

// postTask issues a CreateEvolutionTask request and returns the task_id.
// fatalMDD is exposed so callers can assert the request value flows
// all the way into GAConfigSnapshot.FatalMDD on the persisted package
// (regression for the silently-discarded fatal_mdd bug).
func postTask(t *testing.T, server *httptest.Server, pair string, fatalMDD float64) string {
	t.Helper()
	body := map[string]interface{}{
		"strategy_id":     "sigmoid_v1",
		"pair":            pair,
		"interval":        testInterval,
		"pop_size":        4,
		"max_generations": 2,
		"elite_ratio":     0.25,
		"fatal_mdd":       fatalMDD,
		"taker_fee_bps":   5,
		"slippage_bps":    2,
		"spawn_mode":      string(resultpkg.SpawnModeRandomOnce),
		"test_mode":       true,
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(server.URL+"/api/v1/evolution/tasks",
		"application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST tasks: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST tasks: status=%d body=%s", resp.StatusCode, b)
	}
	var got api.CreateEvolutionTaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if got.TaskID == "" {
		t.Fatal("CreateEvolutionTaskResponse missing task_id")
	}
	return got.TaskID
}

// pollUntilTerminal polls GET /tasks/:id every 200ms up to 30s; returns
// the final status response. The status is "terminal" when in
// {succeeded, failed}.
func pollUntilTerminal(t *testing.T, server *httptest.Server, taskID string) api.EvolutionTaskStatusResponse {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(server.URL + "/api/v1/evolution/tasks/" + taskID)
		if err != nil {
			t.Fatalf("GET task: %v", err)
		}
		var status api.EvolutionTaskStatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			resp.Body.Close()
			t.Fatalf("decode status: %v", err)
		}
		resp.Body.Close()
		switch status.Status {
		case resultpkg.TaskStatusSucceeded, resultpkg.TaskStatusFailed:
			return status
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach terminal state within 30s", taskID)
	return api.EvolutionTaskStatusResponse{} // unreachable
}

// TestE2E_InsufficientBarsFailsFast is the regression for the
// "silent succeeded with score=0" bug fixed in 46274a4. With only
// 30 days of bars the plan builder must error out, the task must
// transition to failed, and the FailureReason must carry the
// "no crucible window fits" message verbatim so a user can act on it.
func TestE2E_InsufficientBarsFailsFast(t *testing.T) {
	server, _ := setupE2E(t)
	taskID := postTask(t, server, testSymbolShort, 0.5)
	status := pollUntilTerminal(t, server, taskID)

	if status.Status != resultpkg.TaskStatusFailed {
		t.Fatalf("status = %q, want failed; full=%+v", status.Status, status)
	}
	if status.FailureReason == nil {
		t.Fatal("FailureReason is nil; expected a non-nil failure message")
	}
	if !strings.Contains(*status.FailureReason, "no crucible window fits") {
		t.Errorf("FailureReason does not mention the regression message:\n  got: %s",
			*status.FailureReason)
	}
	if status.ChallengerID != nil {
		t.Errorf("ChallengerID should be nil for failed task, got %q", *status.ChallengerID)
	}
}

// TestE2E_SufficientBarsCompletesAndPromoteRejectsTestMode walks the
// happy-path pipeline: task succeeds, gene_records row is queryable
// through both /challengers/:id (summary) and /challengers/:id/package
// (full JSON), and the TestMode=true Promote attempt returns 422 —
// the CLAUDE.md key invariant "test_mode results cannot be Promoted."
func TestE2E_SufficientBarsCompletesAndPromoteRejectsTestMode(t *testing.T) {
	server, _ := setupE2E(t)
	// Use a non-default fatal_mdd so the test catches the silent-discard
	// regression: the value must surface in core.ga_config.fatal_mdd
	// after the round-trip through PlanOptions → plan.FatalMDD →
	// GAConfigSnapshot.FatalMDD.
	const requestedFatalMDD = 0.42
	taskID := postTask(t, server, testSymbolOK, requestedFatalMDD)
	status := pollUntilTerminal(t, server, taskID)

	if status.Status != resultpkg.TaskStatusSucceeded {
		var reason string
		if status.FailureReason != nil {
			reason = *status.FailureReason
		}
		t.Fatalf("status = %q, want succeeded; failure_reason=%q", status.Status, reason)
	}
	if status.ChallengerID == nil || *status.ChallengerID == "" {
		t.Fatal("ChallengerID nil/empty on succeeded task")
	}
	chID := *status.ChallengerID

	summary := fetchJSON[api.ChallengerSummaryResponse](t, server,
		"/api/v1/challengers/"+chID)
	if summary.ChallengerID != chID {
		t.Errorf("summary ChallengerID mismatch: %q vs %q", summary.ChallengerID, chID)
	}
	if !summary.TestMode {
		t.Error("summary.TestMode should be true (request used test_mode=true)")
	}
	if summary.DecisionStatus != resultpkg.DecisionStatusPending {
		t.Errorf("DecisionStatus = %q, want pending", summary.DecisionStatus)
	}
	if len(summary.PlanHash) != 64 || len(summary.BarsHash) != 64 {
		t.Errorf("plan_hash/bars_hash should be 64-char hex: ph=%q bh=%q",
			summary.PlanHash, summary.BarsHash)
	}

	// Full package: parse just enough to verify reproducibility_metadata
	// stamps the BuildMeta we wired in setupE2E.
	pkgBlob := fetchRaw(t, server, "/api/v1/challengers/"+chID+"/package")
	var pkg struct {
		Core struct {
			GAConfig struct {
				FatalMDD float64 `json:"fatal_mdd"`
			} `json:"ga_config"`
			ReproducibilityMetadata struct {
				BuildID   string `json:"build_id"`
				PlanHash  string `json:"plan_hash"`
				BarsHash  string `json:"bars_hash"`
				GoVersion string `json:"go_version"`
			} `json:"reproducibility_metadata"`
		} `json:"core"`
	}
	if err := json.Unmarshal(pkgBlob, &pkg); err != nil {
		t.Fatalf("unmarshal package: %v", err)
	}
	if pkg.Core.GAConfig.FatalMDD != requestedFatalMDD {
		t.Errorf("ga_config.fatal_mdd = %v, want %v — the request value must round-trip end-to-end",
			pkg.Core.GAConfig.FatalMDD, requestedFatalMDD)
	}
	if pkg.Core.ReproducibilityMetadata.BuildID != "test" {
		t.Errorf("BuildID stamp lost: got %q, want test",
			pkg.Core.ReproducibilityMetadata.BuildID)
	}
	if pkg.Core.ReproducibilityMetadata.PlanHash != summary.PlanHash {
		t.Errorf("plan_hash inconsistent between summary and package")
	}
	if pkg.Core.ReproducibilityMetadata.GoVersion == "" {
		t.Error("GoVersion missing in reproducibility_metadata")
	}

	// Promote a TestMode=true challenger must be rejected with 422.
	promoteBody, _ := json.Marshal(api.PromoteChallengerRequest{ReviewedBy: "alice"})
	resp, err := http.Post(server.URL+"/api/v1/challengers/"+chID+"/promote",
		"application/json", bytes.NewReader(promoteBody))
	if err != nil {
		t.Fatalf("POST promote: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		b, _ := io.ReadAll(resp.Body)
		t.Errorf("Promote(test_mode=true): status=%d body=%s, want 422",
			resp.StatusCode, b)
	}
}

// TestE2E_OOSAnchoredHoldoutWrites covers Phase 5D: an oos_days=180
// task on the sufficient-bars symbol must succeed and the persisted
// package's verification.oos_result must satisfy:
//   - status="ok"
//   - oos_alpha_monthly + oos_alpha_weekly non-nil
//   - decision_color set (green/yellow/red — not gray; not_run is impossible)
//
// Regression target: bug_oos_phase5d_unimplemented memory — oos_days
// flowed through GAConfigSnapshot/OosWindow but no code consumed it,
// so oos_result.status stayed empty. This test pins the "status is
// not_run" before-state vs the "status is ok" after-state.
//
// test_mode is intentionally left false so the Promote-rejection path
// from the sibling test doesn't shadow this one; this test only
// verifies the verification.oos_result content, not Promote.
func TestE2E_OOSAnchoredHoldoutWrites(t *testing.T) {
	server, _ := setupE2E(t)
	body := map[string]interface{}{
		"strategy_id":     "sigmoid_v1",
		"pair":            testSymbolOK,
		"interval":        testInterval,
		"pop_size":        4,
		"max_generations": 2,
		"elite_ratio":     0.25,
		"fatal_mdd":       0.5,
		"taker_fee_bps":   5,
		"slippage_bps":    2,
		"spawn_mode":      string(resultpkg.SpawnModeRandomOnce),
		"test_mode":       false,
		"oos_days":        180,
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(server.URL+"/api/v1/evolution/tasks",
		"application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST tasks: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST tasks: status=%d body=%s", resp.StatusCode, b)
	}
	var created api.CreateEvolutionTaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	status := pollUntilTerminal(t, server, created.TaskID)
	if status.Status != resultpkg.TaskStatusSucceeded {
		var reason string
		if status.FailureReason != nil {
			reason = *status.FailureReason
		}
		t.Fatalf("status=%q, want succeeded; reason=%q", status.Status, reason)
	}
	if status.ChallengerID == nil {
		t.Fatal("ChallengerID nil on succeeded task")
	}
	chID := *status.ChallengerID

	pkgBlob := fetchRaw(t, server, "/api/v1/challengers/"+chID+"/package")
	var pkg struct {
		Verification struct {
			OOSResult struct {
				Status          string   `json:"status"`
				OOSAlphaMonthly *float64 `json:"oos_alpha_monthly,omitempty"`
				OOSAlphaWeekly  *float64 `json:"oos_alpha_weekly,omitempty"`
				DecisionColor   *string  `json:"decision_color,omitempty"`
				Notes           *string  `json:"notes,omitempty"`
			} `json:"oos_result"`
		} `json:"verification"`
		Diagnostics struct {
			SearchStats *resultpkg.SearchStats `json:"search_stats"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(pkgBlob, &pkg); err != nil {
		t.Fatalf("unmarshal package: %v", err)
	}
	got := pkg.Verification.OOSResult

	// SearchStats (multiple-testing footprint) must ride the persisted
	// package. pop_size=4 / max_generations=2 / elite_ratio=0.25 →
	// nElite=1; patience(5) > MaxGen(2) so early stop can't trip →
	// evaluations = 4 + 1×(4-1) = 7 exactly.
	if ss := pkg.Diagnostics.SearchStats; ss == nil {
		t.Error("diagnostics.search_stats is nil; want per-epoch search counts")
	} else {
		if ss.PopSize != 4 || ss.MaxGenerations != 2 || ss.Generations != 2 {
			t.Errorf("search_stats config echo = %+v, want pop=4 maxgen=2 gens=2", ss)
		}
		if ss.EvaluationsTotal != 7 {
			t.Errorf("search_stats.evaluations_total = %d, want 7", ss.EvaluationsTotal)
		}
		if ss.FatalEvaluations < 0 || ss.FatalEvaluations > ss.EvaluationsTotal {
			t.Errorf("search_stats.fatal_evaluations = %d out of range [0,%d]",
				ss.FatalEvaluations, ss.EvaluationsTotal)
		}
	}

	if got.Status != string(resultpkg.VerificationStatusOK) {
		t.Errorf("oos_result.status = %q, want %q; notes=%v",
			got.Status, resultpkg.VerificationStatusOK, got.Notes)
	}
	if got.OOSAlphaMonthly == nil {
		t.Error("oos_alpha_monthly is nil; want a numeric annualized excess return")
	}
	if got.OOSAlphaWeekly == nil {
		t.Error("oos_alpha_weekly is nil; want a numeric annualized excess return")
	}
	if got.DecisionColor == nil {
		t.Error("decision_color is nil; want green/yellow/red when status=ok")
	} else {
		switch resultpkg.DecisionColor(*got.DecisionColor) {
		case resultpkg.DecisionColorGreen, resultpkg.DecisionColorYellow, resultpkg.DecisionColorRed:
			// ok
		default:
			t.Errorf("decision_color = %q, want one of green/yellow/red", *got.DecisionColor)
		}
	}

	// Notes must reflect the warmup prefix from data.BuildCrucibleWindows.
	// Server defaults (DefaultDefaults) set WarmupDays=365 and the request
	// doesn't override it, so RunOOS reports "warmup_len=365 bars" verbatim.
	// Pinning the value (not just non-zero) catches regressions where the
	// warmup wiring silently reverts to 0 (the original Phase 5D defect).
	if got.Notes == nil {
		t.Error("oos_result.notes is nil; want warmup_len + alpha breakdown")
	} else if !strings.Contains(*got.Notes, "warmup_len=365 bars") {
		t.Errorf("oos_result.notes missing 'warmup_len=365 bars'; got %q", *got.Notes)
	}
}

// fetchJSON GETs path and decodes the response body into T. Fatals on
// non-200 or decode failure. Generic helper so each test reads cleanly.
func fetchJSON[T any](t *testing.T, server *httptest.Server, path string) T {
	t.Helper()
	resp, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status=%d body=%s", path, resp.StatusCode, b)
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return out
}

func fetchRaw(t *testing.T, server *httptest.Server, path string) []byte {
	t.Helper()
	resp, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status=%d body=%s", path, resp.StatusCode, b)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
