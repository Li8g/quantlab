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

	"quantlab/internal/api/middleware"
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

// InstanceStore is the storage surface Phase 6.3 instance handlers
// need. repository.InstanceRepo satisfies this naturally.
type InstanceStore interface {
	Create(ctx context.Context, inst *store.StrategyInstance) error
	Get(ctx context.Context, instanceID string) (*store.StrategyInstance, error)
	UpdateStatus(ctx context.Context, instanceID string, status store.InstanceStatus) error
	SetActiveChampion(ctx context.Context, instanceID string, challengerID string) error
}

// ===== Phase 9 batch 1 collaborators =====
//
// Each list/lookup endpoint owns a tiny interface so tests can fake
// the data layer without standing up a DB. Production wiring uses the
// concrete repos in internal/repository/.

// KLineGapLister reads kline_gaps rows. repository.KLineGapRepo
// satisfies this.
type KLineGapLister interface {
	List(ctx context.Context, symbol, interval string, limit int) ([]store.KLineGap, error)
}

// EvolutionTaskLister returns recently-created tasks for the index
// view (newest first).
type EvolutionTaskLister interface {
	List(ctx context.Context, limit int) ([]store.EvolutionTask, error)
}

// ChampionHistoryReader powers /champions/history + /genome/champion.
// strategyID + pair can both be empty for history listing (no filter);
// GetActive REQUIRES both to be non-empty.
type ChampionHistoryReader interface {
	List(ctx context.Context, strategyID, pair string, limit int) ([]store.ChampionHistory, error)
	GetActive(ctx context.Context, strategyID, pair string) (*store.ChampionHistory, error)
}

// TradeLister returns TradeRecord rows for one instance, newest first.
type TradeLister interface {
	ListByInstance(ctx context.Context, instanceID string, limit int) ([]store.TradeRecord, error)
}

// IDIssuer hands out new InstanceIDs. store.NewULID is the production
// implementation; tests inject a deterministic fake.
type IDIssuer interface {
	NewID() string
}

// Handlers carries the collaborators every endpoint group needs.
// cmd/saas builds it with real implementations; tests build it with
// fakes that satisfy the same interfaces.
type Handlers struct {
	Epoch       EpochCreator
	Tasks       TaskGetter
	Challengers ChallengerReader
	Champions   ChampionUpdater
	Instances   InstanceStore
	IDIssuer    IDIssuer

	// Phase 9 batch 1: read-only diagnostics + lists. Nil-valued
	// fields disable the corresponding routes during Register —
	// tests that focus on Phase 5D / 6.3 endpoints don't need to
	// wire fake stores for the new listings.
	TaskLister      EvolutionTaskLister
	ChampionHistory ChampionHistoryReader
	Gaps            KLineGapLister
	Trades          TradeLister

	// AuthRequired wraps protected routes. When non-nil, it is
	// installed on the /instances/* group during Register. Tests
	// that exercise handlers without auth leave this nil.
	AuthRequired gin.HandlerFunc
	// RequireOperator gates write endpoints to operator+admin.
	// Same nil-skip behaviour as AuthRequired.
	RequireOperator gin.HandlerFunc
}

// Register attaches all routes under /api/v1 to the supplied Gin
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

	// Phase 9 batch 1: read-only diagnostics + listings. Each route
	// requires its collaborator non-nil — tests that don't wire the
	// store quietly skip registration, which is the same pattern the
	// AuthRequired-gated routes follow.
	if h.TaskLister != nil {
		g.GET("/evolution/tasks", h.ListTasks)
	}
	if h.ChampionHistory != nil {
		g.GET("/champions/history", h.ListChampionHistory)
		g.GET("/genome/champion", h.GetChampionGenome)
	}
	if h.Gaps != nil {
		g.GET("/data/gaps", h.ListGaps)
	}

	// Phase 6.3 instance routes — JWT-protected when middleware is
	// wired. Read endpoint accepts viewer+; mutating endpoints
	// require operator+ (see docs/saas-tier2-schema-v1.md §3.2 A2).
	inst := g.Group("/instances")
	if h.AuthRequired != nil {
		inst.Use(h.AuthRequired)
	}
	if h.RequireOperator != nil {
		// Apply per-route for the write endpoints; the GET is left
		// open to viewer role through the AuthRequired middleware
		// alone.
		inst.POST("", h.RequireOperator, h.CreateInstance)
		inst.POST("/:instance_id/start", h.RequireOperator, h.StartInstance)
		inst.POST("/:instance_id/stop", h.RequireOperator, h.StopInstance)
		inst.POST("/:instance_id/deploy-champion", h.RequireOperator, h.DeployChampion)
	} else {
		inst.POST("", h.CreateInstance)
		inst.POST("/:instance_id/start", h.StartInstance)
		inst.POST("/:instance_id/stop", h.StopInstance)
		inst.POST("/:instance_id/deploy-champion", h.DeployChampion)
	}
	inst.GET("/:instance_id", h.GetInstance)
	if h.Trades != nil {
		inst.GET("/:instance_id/trades", h.ListInstanceTrades)
	}
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

// ===================================================================
// Phase 6.3: Instance lifecycle handlers
// ===================================================================

// ErrInstanceTransitionRefused signals an attempt to push an instance
// through a state edge the §4.2 graph doesn't allow (e.g. retired →
// live). Maps to 422.
var ErrInstanceTransitionRefused = errors.New("instance status transition refused")

// CreateInstance: POST /api/v1/instances. 201 + InstanceResponse.
// OwnerUserID is taken from the authenticated caller's JWT claims;
// the request body does not let clients spoof another user.
func (h *Handlers) CreateInstance(c *gin.Context) {
	var req CreateInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	ownerID, ok := ownerFromContext(c)
	if !ok {
		writeError(c, http.StatusInternalServerError, errors.New("missing auth claims"))
		return
	}
	inst := &store.StrategyInstance{
		InstanceID:  h.IDIssuer.NewID(),
		StrategyID:  req.StrategyID,
		Pair:        req.Pair,
		AccountID:   req.AccountID,
		OwnerUserID: ownerID,
		Status:      store.InstanceStatusIdle,
	}
	if err := h.Instances.Create(c.Request.Context(), inst); err != nil {
		writeError(c, mapInstanceCreateErr(err), err)
		return
	}
	c.JSON(http.StatusCreated, toInstanceResponse(inst))
}

// GetInstance: GET /api/v1/instances/:instance_id.
func (h *Handlers) GetInstance(c *gin.Context) {
	id := c.Param("instance_id")
	if id == "" {
		writeError(c, http.StatusBadRequest, errors.New("instance_id is required"))
		return
	}
	inst, err := h.Instances.Get(c.Request.Context(), id)
	if err != nil {
		writeError(c, mapReadErr(err), err)
		return
	}
	c.JSON(http.StatusOK, toInstanceResponse(inst))
}

// StartInstance: POST /api/v1/instances/:instance_id/start.
// Transitions idle / paused → live. Forbidden from retired (terminal).
func (h *Handlers) StartInstance(c *gin.Context) {
	h.transitionInstance(c, func(cur store.InstanceStatus) (store.InstanceStatus, error) {
		switch cur {
		case store.InstanceStatusIdle, store.InstanceStatusPaused, store.InstanceStatusLive:
			return store.InstanceStatusLive, nil
		case store.InstanceStatusRetired:
			return "", ErrInstanceTransitionRefused
		default:
			return "", ErrInstanceTransitionRefused
		}
	})
}

// StopInstance: POST /api/v1/instances/:instance_id/stop.
// Transitions live → paused (manual pause, recoverable). Idempotent
// on paused. Forbidden from retired.
func (h *Handlers) StopInstance(c *gin.Context) {
	h.transitionInstance(c, func(cur store.InstanceStatus) (store.InstanceStatus, error) {
		switch cur {
		case store.InstanceStatusLive, store.InstanceStatusPaused, store.InstanceStatusIdle:
			return store.InstanceStatusPaused, nil
		case store.InstanceStatusRetired:
			return "", ErrInstanceTransitionRefused
		default:
			return "", ErrInstanceTransitionRefused
		}
	})
}

// DeployChampion: POST /api/v1/instances/:instance_id/deploy-champion.
// Sets ActiveChampID on the instance. Per B2 frozen: Promote ⊥ Deploy
// — this is the only API that touches ActiveChampID.
func (h *Handlers) DeployChampion(c *gin.Context) {
	id := c.Param("instance_id")
	if id == "" {
		writeError(c, http.StatusBadRequest, errors.New("instance_id is required"))
		return
	}
	var req DeployChampionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(c, http.StatusBadRequest, err)
		return
	}
	if err := h.Instances.SetActiveChampion(c.Request.Context(), id, req.ChallengerID); err != nil {
		writeError(c, mapReadErr(err), err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"instance_id":         id,
		"active_champion_id":  req.ChallengerID,
	})
}

// transitionInstance is the shared body of StartInstance / StopInstance.
// Computes the next status via the supplied function, then writes it.
func (h *Handlers) transitionInstance(c *gin.Context, nextStatus func(store.InstanceStatus) (store.InstanceStatus, error)) {
	id := c.Param("instance_id")
	if id == "" {
		writeError(c, http.StatusBadRequest, errors.New("instance_id is required"))
		return
	}
	inst, err := h.Instances.Get(c.Request.Context(), id)
	if err != nil {
		writeError(c, mapReadErr(err), err)
		return
	}
	next, err := nextStatus(inst.Status)
	if err != nil {
		writeError(c, http.StatusUnprocessableEntity, err)
		return
	}
	if next == inst.Status {
		// No-op transition (e.g. start on a live instance) is OK;
		// surface current state for caller convenience.
		c.JSON(http.StatusOK, gin.H{
			"instance_id": id,
			"status":      string(next),
			"noop":        true,
		})
		return
	}
	if err := h.Instances.UpdateStatus(c.Request.Context(), id, next); err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"instance_id": id,
		"status":      string(next),
	})
}

// mapInstanceCreateErr distinguishes the partial-unique violation
// from a generic DB failure. The partial unique on
// (owner_user_id, strategy_id, pair, account_id) WHERE status !=
// 'retired' triggers when admin tries to create a duplicate active
// instance — that's 409, not 500.
func mapInstanceCreateErr(err error) int {
	if err == nil {
		return http.StatusInternalServerError
	}
	msg := err.Error()
	// pgx surfaces 23505 (unique_violation); GORM wraps it. We don't
	// have a typed sentinel from gorm, so substring match the
	// constraint name we set in db.go.
	if containsUnique(msg) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

func containsUnique(s string) bool {
	for _, needle := range []string{"unique", "duplicate key", "idx_inst_unique_active"} {
		if substrFold(s, needle) {
			return true
		}
	}
	return false
}

// substrFold is a tiny ASCII-fold contains. We don't import strings
// twice — the package already pulled it out in the typed-sentinels
// commit; reintroducing here for one helper.
func substrFold(s, needle string) bool {
	if len(needle) > len(s) {
		return false
	}
	// fast path: case-sensitive
	for i := 0; i+len(needle) <= len(s); i++ {
		if equalFold(s[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func toInstanceResponse(inst *store.StrategyInstance) InstanceResponse {
	out := InstanceResponse{
		InstanceID:    inst.InstanceID,
		StrategyID:    inst.StrategyID,
		Pair:          inst.Pair,
		AccountID:     inst.AccountID,
		OwnerUserID:   inst.OwnerUserID,
		Status:        string(inst.Status),
		ActiveChampID: inst.ActiveChampID,
	}
	if inst.LastTickWallTime != nil {
		ms := inst.LastTickWallTime.UnixMilli()
		out.LastTickWallTime = &ms
	}
	return out
}

// ownerFromContext reads the JWT-injected Claims and returns the
// caller's gorm uint user ID. Returns (0, false) when no claims are
// present (tests without auth middleware can still exercise handlers
// by setting middleware.ClaimsFrom-compatible state directly).
func ownerFromContext(c *gin.Context) (uint, bool) {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		return 0, false
	}
	return claims.UserID, true
}
