// handlers_import.go — async kline-import endpoints (Phase 9, the 8th and
// last REST endpoint; docs/phase9-data-import-v1.md). The HTTP-pollable
// twin of CLI `datafeeder import`: POST queues an ImportJob, a background
// worker runs data.Orchestrator.ImportSymbol, the GET endpoints poll.
//
// AppRole gating (!= saas) is applied at wiring time — cmd/saas sets the
// Imports collaborator only on lab/dev, so the routes simply don't exist
// on a production saas instance. RequireAdmin gates them at the request
// layer (operator excluded, same as promote/retire).
package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"quantlab/internal/api/middleware"
	"quantlab/internal/data"
	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

const (
	defaultImportsLimit = 50
	maxImportsLimit     = 200
)

// ImportJobStore is the slice of repository.ImportJobRepo the HTTP layer
// needs (the worker uses the wider repo). Tiny interface → zero-DB fakes.
type ImportJobStore interface {
	Create(ctx context.Context, j *store.ImportJob) error
	Get(ctx context.Context, jobID string) (*store.ImportJob, error)
	List(ctx context.Context, limit int) ([]store.ImportJob, error)
	SetCancelRequested(ctx context.Context, jobID string) (bool, error)
}

// CreateImportRequest is the POST /data/import body.
type CreateImportRequest struct {
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	StartMs  int64  `json:"start_ms"`
	EndMs    int64  `json:"end_ms"`
}

// ImportProgress is months_done/months_total (decision 2 — not a %).
type ImportProgress struct {
	MonthsDone  int `json:"months_done"`
	MonthsTotal int `json:"months_total"`
}

// ImportJobResponse is the GET /data/import/:job_id body.
type ImportJobResponse struct {
	ImportJobID   string         `json:"import_job_id"`
	Symbol        string         `json:"symbol"`
	Interval      string         `json:"interval"`
	StartMs       int64          `json:"start_ms"`
	EndMs         int64          `json:"end_ms"`
	Status        string         `json:"status"`
	Progress      ImportProgress `json:"progress"`
	RowsInserted  int64          `json:"rows_inserted"`
	GapsDetected  int            `json:"gaps_detected"`
	FailureReason *string        `json:"failure_reason"`
	RequestedBy   string         `json:"requested_by"`
	StartedAtMs   *int64         `json:"started_at_ms"`
	FinishedAtMs  *int64         `json:"finished_at_ms"`
}

// ListImportsResponse is the GET /data/imports body.
type ListImportsResponse struct {
	Items []ImportJobResponse `json:"items"`
	Count int                 `json:"count"`
}

// CreateImport: POST /data/import → 202 {import_job_id} | 409 | 400.
func (h *Handlers) CreateImport(c *gin.Context) {
	var req CreateImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if req.Symbol == "" || req.Interval == "" {
		writeError(c, http.StatusBadRequest, errors.New("symbol and interval are required"))
		return
	}
	if req.StartMs > req.EndMs {
		writeError(c, http.StatusBadRequest, errors.New("start_ms must be <= end_ms"))
		return
	}
	// Interval whitelist is the orchestrator's own (single source of truth).
	if _, err := data.IntervalToMs(req.Interval); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}

	job := &store.ImportJob{
		JobID:       "imp_" + h.IDIssuer.NewID(),
		Symbol:      req.Symbol,
		Interval:    req.Interval,
		StartMs:     req.StartMs,
		EndMs:       req.EndMs,
		Status:      resultpkg.TaskStatusQueued,
		RequestedBy: requestingUserID(c),
	}
	if err := h.Imports.Create(c.Request.Context(), job); err != nil {
		if errors.Is(err, ErrImportActive) {
			writeError(c, http.StatusConflict, err)
			return
		}
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"import_job_id": job.JobID})
}

// GetImport: GET /data/import/:job_id → 200 | 404.
func (h *Handlers) GetImport(c *gin.Context) {
	jobID := c.Param("job_id")
	job, err := h.Imports.Get(c.Request.Context(), jobID)
	if err != nil {
		writeError(c, mapReadErr(err), err)
		return
	}
	c.JSON(http.StatusOK, toImportJobResponse(job))
}

// ListImports: GET /data/imports?limit=N → 200 {items, count}.
func (h *Handlers) ListImports(c *gin.Context) {
	limit := parseLimit(c.Query("limit"), defaultImportsLimit, maxImportsLimit)
	rows, err := h.Imports.List(c.Request.Context(), limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	items := make([]ImportJobResponse, 0, len(rows))
	for i := range rows {
		items = append(items, toImportJobResponse(&rows[i]))
	}
	c.JSON(http.StatusOK, ListImportsResponse{Items: items, Count: len(items)})
}

// CancelImport: POST /data/import/:job_id/cancel → 202 | 404 | 409.
// Cancellation takes effect at the next month boundary (decision 5).
func (h *Handlers) CancelImport(c *gin.Context) {
	jobID := c.Param("job_id")
	// Get first so a missing job is 404, not 409.
	if _, err := h.Imports.Get(c.Request.Context(), jobID); err != nil {
		writeError(c, mapReadErr(err), err)
		return
	}
	ok, err := h.Imports.SetCancelRequested(c.Request.Context(), jobID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(c, http.StatusConflict,
			errors.New("job is already in a terminal state; cannot cancel"))
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"import_job_id": jobID, "cancel_requested": true})
}

// requestingUserID returns the caller's user id for the audit column, or
// "" when no auth claims are present (handler tests run without middleware).
func requestingUserID(c *gin.Context) string {
	if claims, ok := middleware.ClaimsFrom(c); ok {
		return strconv.FormatUint(uint64(claims.UserID), 10)
	}
	return ""
}

func toImportJobResponse(j *store.ImportJob) ImportJobResponse {
	resp := ImportJobResponse{
		ImportJobID:   j.JobID,
		Symbol:        j.Symbol,
		Interval:      j.Interval,
		StartMs:       j.StartMs,
		EndMs:         j.EndMs,
		Status:        string(j.Status),
		Progress:      ImportProgress{MonthsDone: j.MonthsDone, MonthsTotal: j.MonthsTotal},
		RowsInserted:  j.RowsInserted,
		GapsDetected:  j.GapsDetected,
		FailureReason: j.FailureReason,
		RequestedBy:   j.RequestedBy,
	}
	if j.StartedAt != nil {
		ms := j.StartedAt.UnixMilli()
		resp.StartedAtMs = &ms
	}
	if j.FinishedAt != nil {
		ms := j.FinishedAt.UnixMilli()
		resp.FinishedAtMs = &ms
	}
	return resp
}
