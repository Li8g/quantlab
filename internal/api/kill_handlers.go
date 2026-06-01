// kill_handlers.go — manual kill_switch route (kill_switch Option 3,
// step 3b). POST /instances/:instance_id/kill lets a human admin halt a
// live agent: the handler resolves the operator from JWT claims and hands
// off to the InstanceKiller collaborator, which (in cmd/saas) reverse-maps
// instance→account and calls wshub.Hub.SendKillSwitch(reason=
// manual_admin_action). The agent-side freeze + the auto-trigger path are
// steps 1 + 3a; this is the human entry point onto the same control plane.
package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"quantlab/internal/api/middleware"
)

// InstanceKiller halts the live agent that owns instanceID, attributing
// the action to operatorUserID (0 when no auth context — test bypass).
// The cmd/saas impl reverse-maps instance→account and sends a
// manual_admin_action kill_switch through the Hub.
type InstanceKiller interface {
	Kill(ctx context.Context, instanceID string, operatorUserID uint) error
}

// Sentinels the cmd/saas InstanceKiller translates underlying errors into,
// so KillInstance can map them to HTTP status without importing wshub/gorm.
var (
	// ErrKillInstanceNotFound ⇒ 404: no instance with that id.
	ErrKillInstanceNotFound = errors.New("instance not found")
	// ErrKillAgentOffline ⇒ 409: instance exists but its agent has no
	// Ready connection, so the kill can't be delivered right now.
	ErrKillAgentOffline = errors.New("agent not connected")
)

// KillInstance: POST /api/v1/instances/:instance_id/kill (admin only).
// Sends a manual kill_switch to the instance's agent, latching it frozen
// (HALTED). 200 on delivery, 404 unknown instance, 409 agent offline.
func (h *Handlers) KillInstance(c *gin.Context) {
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

	if err := h.Killer.Kill(c.Request.Context(), id, operatorUserID); err != nil {
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
	c.JSON(http.StatusOK, gin.H{"instance_id": id, "status": "kill_sent"})
}
