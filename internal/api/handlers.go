// HTTP handlers for the QuantLab public surface. Six endpoints, all
// under /api/v1 and all expressed against small interfaces so tests
// can swap in fakes without a DB. Source-of-truth: CLAUDE.md "HTTP
// API" + docs/Coding-plan Phase 5D step 5.
//
// Error mapping is deliberately uniform:
//
//	validation error          → 400 Bad Request
//	gorm.ErrRecordNotFound    → 404 Not Found
//	epoch.ErrTaskInProgress   → 409 Conflict
//	repository invariant      → 422 Unprocessable Entity (Promote/Retire)
//	other                     → 500 Internal Server Error
//
// The response body for every error is {"error": "<human readable>"},
// which matches what existing CLI / curl users will expect.
package api

import (
	"context"
	"errors"
	"net/http"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// Wire-boundary error sentinels. All cross-package errors that affect
// HTTP status mapping live here in api so the handler can `errors.Is`
// without forming import cycles (api → repository would cycle because
// repository → api for request types; api → epoch would cycle because
// epoch → api for the same; api → data would be a new edge with no
// other reason to exist). Callers wrap these with context via
// fmt.Errorf("...: %w", api.ErrXxx).
var (
	// ErrTaskInProgress: another evolution task already holds the
	// (strategy, pair) lock. → 409 Conflict.
	ErrTaskInProgress = errors.New("a task is already running for this (strategy, pair)")

	// ErrUnknownStrategy: CreateAndRunTask got a strategy_id not
	// in the registry. → 400 Bad Request.
	ErrUnknownStrategy = errors.New("unknown strategy_id")

	// ErrUnsupportedInterval: CreateAndRunTask got an interval that
	// data.IntervalToMs does not recognise. → 400 Bad Request.
	ErrUnsupportedInterval = errors.New("unsupported interval")

	// Promote/Retire transition invariants. All four map to 422
	// Unprocessable Entity — the request was well-formed but the
	// target's current state forbids the transition.
	ErrCannotPromoteTestMode = errors.New("cannot promote a TestMode=true challenger")
	ErrAlreadyPromoted       = errors.New("challenger already promoted")
	ErrAlreadyRejected       = errors.New("challenger already rejected")
	ErrAlreadyRetired        = errors.New("champion already retired")
)

// EpochCreator triggers a new evolution task. The HTTP layer holds
// only this single verb against the Epoch service.
type EpochCreator interface {
	CreateAndRunTask(ctx context.Context, req CreateEvolutionTaskRequest) (string, error)
}

// TaskGetter reads an EvolutionTask row by task_id.
type TaskGetter interface {
	Get(ctx context.Context, taskID string) (*store.EvolutionTask, error)
}

// ChallengerReader reads from gene_records — summary fields + raw blob.
type ChallengerReader interface {
	Get(ctx context.Context, challengerID string) (*store.GeneRecord, error)
	GetPackageBlob(ctx context.Context, challengerID string) ([]byte, error)
}

// ChampionUpdater performs Promote/Retire transitions atomically.
type ChampionUpdater interface {
	Promote(ctx context.Context, challengerID string, req PromoteChallengerRequest) error
	Retire(ctx context.Context, challengerID string, req RetireChampionRequest) error
}

// Handlers carries the four collaborators the six endpoints need.
// cmd/saas builds it with real implementations; tests build it with
// fakes that satisfy the same interfaces.
type Handlers struct {
	Epoch       EpochCreator
	Tasks       TaskGetter
	Challengers ChallengerReader
	Champions   ChampionUpdater
}

// Register attaches all six routes under /api/v1 to the supplied Gin
// engine. Idempotent within a process — call once after the engine is
// constructed.
func (h *Handlers) Register(r gin.IRouter) {
	g := r.Group("/api/v1")
	g.POST("/evolution/tasks", h.CreateTask)
	g.GET("/evolution/tasks/:task_id", h.GetTaskStatus)
	g.GET("/challengers/:challenger_id", h.GetChallenger)
	g.GET("/challengers/:challenger_id/package", h.GetChallengerPackage)
	g.POST("/challengers/:challenger_id/promote", h.PromoteChallenger)
	g.POST("/champions/:champion_id/retire", h.RetireChampion)
}

// ===== handlers =====

// CreateTask: POST /api/v1/evolution/tasks. 202 + {task_id} on
// success.
func (h *Handlers) CreateTask(c *gin.Context) {
	var req CreateEvolutionTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	taskID, err := h.Epoch.CreateAndRunTask(c.Request.Context(), req)
	if err != nil {
		writeError(c, mapCreateTaskErr(err), err)
		return
	}
	c.JSON(http.StatusAccepted, CreateEvolutionTaskResponse{TaskID: taskID})
}

// GetTaskStatus: GET /api/v1/evolution/tasks/:task_id.
func (h *Handlers) GetTaskStatus(c *gin.Context) {
	taskID := c.Param("task_id")
	if taskID == "" {
		writeError(c, http.StatusBadRequest, errors.New("task_id is required"))
		return
	}
	row, err := h.Tasks.Get(c.Request.Context(), taskID)
	if err != nil {
		writeError(c, mapReadErr(err), err)
		return
	}
	c.JSON(http.StatusOK, EvolutionTaskStatusResponse{
		TaskID:            row.TaskID,
		Status:            row.Status,
		CurrentGeneration: row.CurrentGeneration,
		BestScore:         row.BestScore,
		ChallengerID:      row.ChallengerID,
		FailureReason:     row.FailureReason,
	})
}

// GetChallenger: GET /api/v1/challengers/:challenger_id. Returns the
// lifted-column summary; the full package is on /package.
func (h *Handlers) GetChallenger(c *gin.Context) {
	id := c.Param("challenger_id")
	if id == "" {
		writeError(c, http.StatusBadRequest, errors.New("challenger_id is required"))
		return
	}
	rec, err := h.Challengers.Get(c.Request.Context(), id)
	if err != nil {
		writeError(c, mapReadErr(err), err)
		return
	}
	c.JSON(http.StatusOK, ChallengerSummaryResponse{
		ChallengerID:       rec.ChallengerID,
		StrategyID:         rec.StrategyID,
		Pair:               rec.Pair,
		ScoreTotal:         rec.ScoreTotal,
		ScoreRaw:           rec.ScoreRaw,
		ConsistencyPenalty: rec.ConsistencyPenalty,
		DecisionStatus:     rec.DecisionStatus,
		PlanHash:           rec.PlanHash,
		BarsHash:           rec.BarsHash,
		TestMode:           rec.TestMode,
		DSR:                rec.DSR,
	})
}

// GetChallengerPackage: GET /api/v1/challengers/:challenger_id/package.
// Streams the raw ChallengerResultPackage JSON blob.
func (h *Handlers) GetChallengerPackage(c *gin.Context) {
	id := c.Param("challenger_id")
	if id == "" {
		writeError(c, http.StatusBadRequest, errors.New("challenger_id is required"))
		return
	}
	blob, err := h.Challengers.GetPackageBlob(c.Request.Context(), id)
	if err != nil {
		writeError(c, mapReadErr(err), err)
		return
	}
	if len(blob) == 0 {
		// Row exists but FullPackageJSON column is empty — treat as
		// a 5xx since the persistence path always populates it.
		writeError(c, http.StatusInternalServerError, errors.New("empty package blob"))
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", blob)
}

// PromoteChallenger: POST /api/v1/challengers/:challenger_id/promote.
func (h *Handlers) PromoteChallenger(c *gin.Context) {
	id := c.Param("challenger_id")
	if id == "" {
		writeError(c, http.StatusBadRequest, errors.New("challenger_id is required"))
		return
	}
	var req PromoteChallengerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := h.Champions.Promote(c.Request.Context(), id, req); err != nil {
		writeError(c, mapTransitionErr(err), err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"challenger_id":   id,
		"decision_status": resultpkg.DecisionStatusPromoted,
	})
}

// RetireChampion: POST /api/v1/champions/:champion_id/retire. The URL
// param is the ChallengerID of the promoted gene (champions are stored
// keyed by ChallengerID).
func (h *Handlers) RetireChampion(c *gin.Context) {
	id := c.Param("champion_id")
	if id == "" {
		writeError(c, http.StatusBadRequest, errors.New("champion_id is required"))
		return
	}
	var req RetireChampionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := h.Champions.Retire(c.Request.Context(), id, req); err != nil {
		writeError(c, mapTransitionErr(err), err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"champion_id": id, "status": "retired"})
}

// ===== error mapping =====

func mapCreateTaskErr(err error) int {
	switch {
	case errors.Is(err, ErrTaskInProgress):
		return http.StatusConflict
	case errors.Is(err, ErrUnknownStrategy), errors.Is(err, ErrUnsupportedInterval):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func mapReadErr(err error) int {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

// mapTransitionErr distinguishes "row not found" (404) from invariant
// violations like "cannot promote TestMode" (422). All transition
// invariants are typed sentinels — see the var block at the top of
// this file.
func mapTransitionErr(err error) int {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrCannotPromoteTestMode),
		errors.Is(err, ErrAlreadyPromoted),
		errors.Is(err, ErrAlreadyRejected),
		errors.Is(err, ErrAlreadyRetired):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

func writeError(c *gin.Context, status int, err error) {
	c.AbortWithStatusJSON(status, gin.H{"error": err.Error()})
}
