package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"gorm.io/gorm"

	"quantlab/internal/api"
	"quantlab/internal/repository"
	"quantlab/internal/saas/wshub"
	"quantlab/internal/wire"
)

// hubInstanceKiller implements api.InstanceKiller (manual kill_switch,
// Option 3 step 3b): reverse-map instance→account, then send a
// manual_admin_action kill_switch through the Hub onto the same control
// plane the auto-trigger (step 3a) uses. Underlying storage/transport
// errors are translated into the api sentinels so the HTTP handler maps
// status codes without importing wshub/gorm.
type hubInstanceKiller struct {
	instances *repository.InstanceRepo
	hub       *wshub.Hub
	audit     auditSink
	logger    *slog.Logger
}

func (k *hubInstanceKiller) Kill(ctx context.Context, instanceID string, operatorUserID uint) error {
	inst, err := k.instances.Get(ctx, instanceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return api.ErrKillInstanceNotFound
		}
		return err
	}
	ks := wire.KillSwitch{
		Reason:         wire.KillSwitchManualAdminAction,
		OperatorUserID: strconv.FormatUint(uint64(operatorUserID), 10),
		Scope:          wire.KillSwitchScopeAll,
	}
	err = k.hub.SendKillSwitch(inst.AccountID, ks)
	if errors.Is(err, wshub.ErrAccountNotConnected) {
		return api.ErrKillAgentOffline
	}
	if err != nil {
		return err
	}
	recordKillAudit(ctx, k.audit, k.logger,
		fmt.Sprintf("user:%d", operatorUserID), inst.AccountID, ks,
		map[string]any{"trigger": "manual", "instance_id": instanceID})
	return nil
}
