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

// driftStreakResetter lets the resumer re-arm the server-side auto-freeze
// safety net (satisfied by *agentMessageHandler.ClearDriftStreak). Kept as
// an interface so hubInstanceResumer stays unit-testable with a fake, same
// pattern as killSwitchSender.
type driftStreakResetter interface {
	ClearDriftStreak(accountID string)
}

// hubInstanceResumer implements api.InstanceResumer (kill_switch resume,
// §5.13 v2): the inverse of hubInstanceKiller. It reverse-maps
// instance→account, sends a resume kill_switch (Symbol="resume") through
// the Hub to lift the agent's frozen latch, re-arms auto-freeze by
// clearing the drift streak, and records an instance.resume audit event.
// Same error→sentinel translation as the killer so the HTTP handler maps
// status without importing wshub/gorm.
type hubInstanceResumer struct {
	instances *repository.InstanceRepo
	hub       *wshub.Hub
	audit     auditSink
	streaks   driftStreakResetter
	logger    *slog.Logger
}

func (r *hubInstanceResumer) Resume(ctx context.Context, instanceID string, operatorUserID uint) error {
	inst, err := r.instances.Get(ctx, instanceID)
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
		Symbol:         wire.KillSwitchSymbolResume,
	}
	err = r.hub.SendKillSwitch(inst.AccountID, ks)
	if errors.Is(err, wshub.ErrAccountNotConnected) {
		return api.ErrKillAgentOffline
	}
	if err != nil {
		return err
	}
	// Lift the auto-freeze latch so a recurring drift can re-trigger.
	r.streaks.ClearDriftStreak(inst.AccountID)
	recordResumeAudit(ctx, r.audit, r.logger,
		fmt.Sprintf("user:%d", operatorUserID), inst.AccountID, ks,
		map[string]any{"trigger": "manual", "instance_id": instanceID})
	return nil
}
