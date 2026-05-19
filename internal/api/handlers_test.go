package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// ===== fakes =====

type fakeEpoch struct {
	taskID string
	err    error
	gotReq CreateEvolutionTaskRequest
}

func (f *fakeEpoch) CreateAndRunTask(_ context.Context, req CreateEvolutionTaskRequest) (string, error) {
	f.gotReq = req
	return f.taskID, f.err
}

type fakeTasks struct {
	row *store.EvolutionTask
	err error
}

func (f *fakeTasks) Get(_ context.Context, taskID string) (*store.EvolutionTask, error) {
	return f.row, f.err
}

type fakeChallengers struct {
	rec  *store.GeneRecord
	err  error
	blob []byte
}

func (f *fakeChallengers) Get(_ context.Context, _ string) (*store.GeneRecord, error) {
	return f.rec, f.err
}
func (f *fakeChallengers) GetPackageBlob(_ context.Context, _ string) ([]byte, error) {
	return f.blob, f.err
}

type fakeChampions struct {
	promoteErr error
	retireErr  error
}

func (f *fakeChampions) Promote(_ context.Context, _ string, _ PromoteChallengerRequest) error {
	return f.promoteErr
}
func (f *fakeChampions) Retire(_ context.Context, _ string, _ RetireChampionRequest) error {
	return f.retireErr
}

// newRouter wires Handlers and returns a Gin engine ready for
// httptest. gin.TestMode silences the per-route stdout log.
func newRouter(h *Handlers) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h.Register(r)
	return r
}

func doJSON(r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func validCreateBody() CreateEvolutionTaskRequest {
	return CreateEvolutionTaskRequest{
		StrategyID:     "sigmoid_v1",
		Pair:           "BTCUSDT",
		Interval:       "1h",
		PopSize:        20,
		MaxGenerations: 5,
		EliteRatio:     0.05,
		FatalMDD:       0.5,
		TakerFeeBPS:    5,
		SlippageBPS:    2,
		SpawnMode:      resultpkg.SpawnModeRandomOnce,
	}
}

// ===== CreateTask =====

func TestCreateTask_HappyPath(t *testing.T) {
	h := &Handlers{Epoch: &fakeEpoch{taskID: "task-abc"}}
	w := doJSON(newRouter(h), http.MethodPost, "/api/v1/evolution/tasks", validCreateBody())
	if w.Code != http.StatusAccepted {
		t.Fatalf("Code = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	var resp CreateEvolutionTaskResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TaskID != "task-abc" {
		t.Errorf("TaskID = %q, want task-abc", resp.TaskID)
	}
}

func TestCreateTask_InvalidRequestReturns400(t *testing.T) {
	body := validCreateBody()
	body.Interval = "" // breaks validation
	h := &Handlers{Epoch: &fakeEpoch{}}
	w := doJSON(newRouter(h), http.MethodPost, "/api/v1/evolution/tasks", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Code = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestCreateTask_InProgressReturns409(t *testing.T) {
	h := &Handlers{Epoch: &fakeEpoch{err: ErrTaskInProgress}}
	w := doJSON(newRouter(h), http.MethodPost, "/api/v1/evolution/tasks", validCreateBody())
	if w.Code != http.StatusConflict {
		t.Errorf("Code = %d, want 409; body=%s", w.Code, w.Body.String())
	}
}

func TestCreateTask_UnknownStrategyReturns400(t *testing.T) {
	h := &Handlers{Epoch: &fakeEpoch{err: errors.New("unknown strategy_id")}}
	w := doJSON(newRouter(h), http.MethodPost, "/api/v1/evolution/tasks", validCreateBody())
	if w.Code != http.StatusBadRequest {
		t.Errorf("Code = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// ===== GetTaskStatus =====

func TestGetTaskStatus_HappyPath(t *testing.T) {
	score := 1.5
	row := &store.EvolutionTask{
		TaskID:            "task-001",
		Status:            resultpkg.TaskStatusSucceeded,
		CurrentGeneration: 7,
		BestScore:         &score,
	}
	h := &Handlers{Tasks: &fakeTasks{row: row}}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/evolution/tasks/task-001", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp EvolutionTaskStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TaskID != "task-001" || resp.Status != resultpkg.TaskStatusSucceeded || resp.CurrentGeneration != 7 {
		t.Errorf("decode mismatch: %+v", resp)
	}
	if resp.BestScore == nil || *resp.BestScore != 1.5 {
		t.Errorf("BestScore mismatch: %v", resp.BestScore)
	}
}

func TestGetTaskStatus_NotFoundReturns404(t *testing.T) {
	h := &Handlers{Tasks: &fakeTasks{err: gorm.ErrRecordNotFound}}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/evolution/tasks/missing", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// ===== GetChallenger + GetChallengerPackage =====

func TestGetChallenger_HappyPath(t *testing.T) {
	score := 2.1
	rec := &store.GeneRecord{
		ChallengerID:   "ch-001",
		StrategyID:     "sigmoid_v1",
		Pair:           "BTCUSDT",
		ScoreTotal:     &score,
		DecisionStatus: resultpkg.DecisionStatusPending,
		PlanHash:       "ph0",
		BarsHash:       "bh0",
	}
	h := &Handlers{Challengers: &fakeChallengers{rec: rec}}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/challengers/ch-001", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp ChallengerSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ChallengerID != "ch-001" || resp.DecisionStatus != resultpkg.DecisionStatusPending {
		t.Errorf("decode mismatch: %+v", resp)
	}
	if resp.ScoreTotal == nil || *resp.ScoreTotal != 2.1 {
		t.Errorf("ScoreTotal mismatch: %v", resp.ScoreTotal)
	}
}

func TestGetChallengerPackage_StreamsBlob(t *testing.T) {
	blob := []byte(`{"core":{"strategy_id":"sigmoid_v1"}}`)
	h := &Handlers{Challengers: &fakeChallengers{blob: blob}}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/challengers/ch-001/package", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), blob) {
		t.Errorf("body mismatch: got %s, want %s", w.Body.Bytes(), blob)
	}
	if got := w.Header().Get("Content-Type"); got == "" {
		t.Errorf("Content-Type empty, want application/json")
	}
}

func TestGetChallengerPackage_EmptyBlobReturns500(t *testing.T) {
	h := &Handlers{Challengers: &fakeChallengers{blob: nil}}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/challengers/ch-001/package", nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("Code = %d, want 500", w.Code)
	}
}

func TestGetChallengerPackage_NotFoundReturns404(t *testing.T) {
	h := &Handlers{Challengers: &fakeChallengers{err: gorm.ErrRecordNotFound}}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/challengers/missing/package", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404", w.Code)
	}
}

func TestGetChallenger_NotFoundReturns404(t *testing.T) {
	h := &Handlers{Challengers: &fakeChallengers{err: gorm.ErrRecordNotFound}}
	w := doJSON(newRouter(h), http.MethodGet, "/api/v1/challengers/missing", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404", w.Code)
	}
}

// ===== Promote + Retire =====

func TestPromoteChallenger_HappyPath(t *testing.T) {
	h := &Handlers{Champions: &fakeChampions{}}
	body := PromoteChallengerRequest{ReviewedBy: "alice"}
	w := doJSON(newRouter(h), http.MethodPost, "/api/v1/challengers/ch-001/promote", body)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestPromoteChallenger_InvalidBodyReturns400(t *testing.T) {
	h := &Handlers{Champions: &fakeChampions{}}
	body := PromoteChallengerRequest{} // missing ReviewedBy
	w := doJSON(newRouter(h), http.MethodPost, "/api/v1/challengers/ch-001/promote", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Code = %d, want 400", w.Code)
	}
}

func TestPromoteChallenger_TestModeReturns422(t *testing.T) {
	h := &Handlers{Champions: &fakeChampions{
		promoteErr: errors.New("cannot promote a TestMode=true challenger"),
	}}
	body := PromoteChallengerRequest{ReviewedBy: "alice"}
	w := doJSON(newRouter(h), http.MethodPost, "/api/v1/challengers/ch-001/promote", body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("Code = %d, want 422", w.Code)
	}
}

func TestPromoteChallenger_NotFoundReturns404(t *testing.T) {
	h := &Handlers{Champions: &fakeChampions{promoteErr: gorm.ErrRecordNotFound}}
	body := PromoteChallengerRequest{ReviewedBy: "alice"}
	w := doJSON(newRouter(h), http.MethodPost, "/api/v1/challengers/missing/promote", body)
	if w.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404", w.Code)
	}
}

func TestRetireChampion_HappyPath(t *testing.T) {
	h := &Handlers{Champions: &fakeChampions{}}
	body := RetireChampionRequest{ReviewedBy: "bob"}
	w := doJSON(newRouter(h), http.MethodPost, "/api/v1/champions/ch-001/retire", body)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestRetireChampion_AlreadyRetiredReturns422(t *testing.T) {
	h := &Handlers{Champions: &fakeChampions{retireErr: errors.New("champion already retired")}}
	body := RetireChampionRequest{ReviewedBy: "bob"}
	w := doJSON(newRouter(h), http.MethodPost, "/api/v1/champions/ch-001/retire", body)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("Code = %d, want 422", w.Code)
	}
}

func TestRetireChampion_NotFoundReturns404(t *testing.T) {
	h := &Handlers{Champions: &fakeChampions{retireErr: gorm.ErrRecordNotFound}}
	body := RetireChampionRequest{ReviewedBy: "bob"}
	w := doJSON(newRouter(h), http.MethodPost, "/api/v1/champions/missing/retire", body)
	if w.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404", w.Code)
	}
}
