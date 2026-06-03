// resume_handlers.go — kill_switch resume route (§5.13 v2). POST
// /instances/:instance_id/resume is the inverse of the kill route: it lets
// a human admin lift a frozen agent's latch without a process restart. The
// handler resolves the operator from JWT claims and hands off to the
// InstanceResumer collaborator, which (in cmd/saas) reverse-maps
// instance→account, sends a resume kill_switch through the Hub, re-arms
// auto-freeze, and records an instance.resume audit event.
package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"quantlab/internal/api/middleware"
)

// InstanceResumer un-freezes the live agent that owns instanceID,
// attributing the action to operatorUserID (0 when no auth context — test
// bypass). The cmd/saas impl sends a Symbol="resume" kill_switch through
// the Hub. Reuses the ErrKill* sentinels — they are transport-generic
// (instance-not-found / agent-offline apply identically to resume).
type InstanceResumer interface {
	Resume(ctx context.Context, instanceID string, operatorUserID uint) error
}

// ResumeInstance: POST /api/v1/instances/:instance_id/resume (admin only).
// Sends a resume kill_switch to the instance's agent, lifting the frozen
// (HALTED) latch. 200 on delivery, 404 unknown instance, 409 agent offline.
func (h *Handlers) ResumeInstance(c *gin.Context) {
	id := c.Param("instance_id")
	if id == "" {
		writeError(c, http.StatusBadRequest, errors.New("instance_id is required"))
		return
	}

	// Operator attribution from the JWT (RequireAdmin guarantees an admin
	// when the gate is wired). 0 only when auth is bypassed (handler tests).
	var operatorUserID uint
	if claims, ok := middleware.ClaimsFrom(c); ok {
		operatorUserID = claims.UserID
	}

	if err := h.Resumer.Resume(c.Request.Context(), id, operatorUserID); err != nil {
		switch {
		case errors.Is(err, ErrKillInstanceNotFound):
			writeError(c, http.StatusNotFound, err)
		case errors.Is(err, ErrKillAgentOffline):
			writeError(c, http.StatusConflict, err)
		default:
			writeError(c, http.StatusInternalServerError, err)
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"instance_id": id, "status": "resume_sent"})
}
