package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"gorm.io/gorm"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// fakeImportStore is a scriptable ImportJobStore.
type fakeImportStore struct {
	created   *store.ImportJob
	createErr error
	getJob    *store.ImportJob
	getErr    error
	listRows  []store.ImportJob
	cancelOK  bool
	cancelErr error
}

func (f *fakeImportStore) Create(_ context.Context, j *store.ImportJob) error {
	f.created = j
	return f.createErr
}
func (f *fakeImportStore) Get(_ context.Context, _ string) (*store.ImportJob, error) {
	return f.getJob, f.getErr
}
func (f *fakeImportStore) List(_ context.Context, _ int) ([]store.ImportJob, error) {
	return f.listRows, nil
}
func (f *fakeImportStore) SetCancelRequested(_ context.Context, _ string) (bool, error) {
	return f.cancelOK, f.cancelErr
}

func importHandlers(s *fakeImportStore) *Handlers {
	return &Handlers{Imports: s, IDIssuer: &fakeIssuer{next: "01J0000000000000000000000A"}}
}

func TestCreateImport_202(t *testing.T) {
	s := &fakeImportStore{}
	r := newRouter(importHandlers(s))
	body := CreateImportRequest{Symbol: "BTCUSDT", Interval: "1h", StartMs: 100, EndMs: 200}
	w := doJSON(r, http.MethodPost, "/api/v1/data/import", body)

	if w.Code != http.StatusAccepted {
		t.Fatalf("Code = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["import_job_id"] != "imp_01J0000000000000000000000A" {
		t.Errorf("import_job_id = %q, want imp_<ulid>", resp["import_job_id"])
	}
	if s.created == nil || s.created.Status != resultpkg.TaskStatusQueued {
		t.Errorf("created job = %+v, want status queued", s.created)
	}
	if s.created.Symbol != "BTCUSDT" || s.created.Interval != "1h" {
		t.Errorf("created job pair = %s/%s", s.created.Symbol, s.created.Interval)
	}
}

func TestCreateImport_409_ActivePair(t *testing.T) {
	s := &fakeImportStore{createErr: ErrImportActive}
	r := newRouter(importHandlers(s))
	body := CreateImportRequest{Symbol: "BTCUSDT", Interval: "1h", StartMs: 100, EndMs: 200}
	w := doJSON(r, http.MethodPost, "/api/v1/data/import", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("Code = %d, want 409; body=%s", w.Code, w.Body.String())
	}
}

func TestCreateImport_400(t *testing.T) {
	cases := []struct {
		name string
		body CreateImportRequest
	}{
		{"missing symbol", CreateImportRequest{Interval: "1h", StartMs: 1, EndMs: 2}},
		{"missing interval", CreateImportRequest{Symbol: "BTCUSDT", StartMs: 1, EndMs: 2}},
		{"start after end", CreateImportRequest{Symbol: "BTCUSDT", Interval: "1h", StartMs: 9, EndMs: 2}},
		{"unknown interval", CreateImportRequest{Symbol: "BTCUSDT", Interval: "3h", StartMs: 1, EndMs: 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &fakeImportStore{}
			r := newRouter(importHandlers(s))
			w := doJSON(r, http.MethodPost, "/api/v1/data/import", c.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("Code = %d, want 400; body=%s", w.Code, w.Body.String())
			}
			if s.created != nil {
				t.Error("must not Create on a validation failure")
			}
		})
	}
}

func TestGetImport_200_and_404(t *testing.T) {
	got := &fakeImportStore{getJob: &store.ImportJob{
		JobID: "imp_1", Symbol: "BTCUSDT", Interval: "1h",
		Status: resultpkg.TaskStatusRunning, MonthsDone: 12, MonthsTotal: 89,
	}}
	r := newRouter(importHandlers(got))
	w := doJSON(r, http.MethodGet, "/api/v1/data/import/imp_1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp ImportJobResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != "running" || resp.Progress.MonthsDone != 12 || resp.Progress.MonthsTotal != 89 {
		t.Errorf("resp = %+v, want running 12/89", resp)
	}

	missing := &fakeImportStore{getErr: gorm.ErrRecordNotFound}
	r = newRouter(importHandlers(missing))
	w = doJSON(r, http.MethodGet, "/api/v1/data/import/nope", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("Code = %d, want 404", w.Code)
	}
}

func TestListImports_200(t *testing.T) {
	s := &fakeImportStore{listRows: []store.ImportJob{
		{JobID: "imp_2", Status: resultpkg.TaskStatusSucceeded},
		{JobID: "imp_1", Status: resultpkg.TaskStatusFailed},
	}}
	r := newRouter(importHandlers(s))
	w := doJSON(r, http.MethodGet, "/api/v1/data/imports", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("Code = %d, want 200", w.Code)
	}
	var resp ListImportsResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Count != 2 || len(resp.Items) != 2 {
		t.Errorf("count = %d, want 2", resp.Count)
	}
}

func TestCancelImport_202_404_409(t *testing.T) {
	// 202: job exists + still active.
	active := &fakeImportStore{getJob: &store.ImportJob{JobID: "imp_1"}, cancelOK: true}
	r := newRouter(importHandlers(active))
	w := doJSON(r, http.MethodPost, "/api/v1/data/import/imp_1/cancel", nil)
	if w.Code != http.StatusAccepted {
		t.Errorf("active cancel Code = %d, want 202; body=%s", w.Code, w.Body.String())
	}

	// 404: job missing.
	missing := &fakeImportStore{getErr: gorm.ErrRecordNotFound}
	r = newRouter(importHandlers(missing))
	w = doJSON(r, http.MethodPost, "/api/v1/data/import/nope/cancel", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("missing cancel Code = %d, want 404", w.Code)
	}

	// 409: job exists but is terminal (SetCancelRequested → ok=false).
	terminal := &fakeImportStore{getJob: &store.ImportJob{JobID: "imp_1"}, cancelOK: false}
	r = newRouter(importHandlers(terminal))
	w = doJSON(r, http.MethodPost, "/api/v1/data/import/imp_1/cancel", nil)
	if w.Code != http.StatusConflict {
		t.Errorf("terminal cancel Code = %d, want 409", w.Code)
	}
}
