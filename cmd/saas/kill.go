package main

import (
	"context"
	"errors"
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
}

func (k *hubInstanceKiller) Kill(ctx context.Context, instanceID string, operatorUserID uint) error {
	inst, err := k.instances.Get(ctx, instanceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return api.ErrKillInstanceNotFound
		}
		return err
	}
	err = k.hub.SendKillSwitch(inst.AccountID, wire.KillSwitch{
		Reason:         wire.KillSwitchManualAdminAction,
		OperatorUserID: strconv.FormatUint(uint64(operatorUserID), 10),
		Scope:          wire.KillSwitchScopeAll,
	})
	if errors.Is(err, wshub.ErrAccountNotConnected) {
		return api.ErrKillAgentOffline
	}
	return err
}
