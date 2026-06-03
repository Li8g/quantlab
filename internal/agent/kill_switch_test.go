package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"quantlab/internal/wire"
)

// TestKillSwitch_FreezesAndRejectsTradeCommands pins the Option 3 HALTED
// data-plane core: once a kill_switch arrives, every subsequent
// trade_command is rejected without reaching the exchange. The kill is
// acked accepted; the trade is acked rejected with a "frozen" reason.
func TestKillSwitch_FreezesAndRejectsTradeCommands(t *testing.T) {
	pc := newPipeConn()
	ex := NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(50000)})
	c := newTestClient(t, []*pipeConn{pc}, ex)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	runHubHandshake(t, pc, c.cfg.AccountID)

	// Kill the agent (auto-trigger path: discrepancy_detected).
	pc.hubSendEnv(t, wire.TypeKillSwitch, c.cfg.AccountID, wire.KillSwitch{
		Reason:         wire.KillSwitchDiscrepancyDetected,
		OperatorUserID: "01HKOPER00000000000000000A",
		Scope:          wire.KillSwitchScopeAll,
	})
	killAck, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t))
	if killAck.Status != wire.AckStatusAccepted {
		t.Fatalf("kill_switch ack.status = %q, want accepted", killAck.Status)
	}

	// A trade_command issued after the kill must be rejected. A rejected
	// status (not accepted/filled) is itself proof the order never
	// submitted — the exchange would have returned accepted + an
	// order_update otherwise.
	pc.hubSendEnv(t, wire.TypeTradeCommand, c.cfg.AccountID, wire.TradeCommand{
		IntentKind:      wire.IntentKindMacro,
		ClientOrderID:   "01HKCOID00000000000000000C",
		InstanceID:      "01HKINSTANCE0000000000000A",
		Symbol:          "BTCUSDT",
		Side:            "buy",
		OrderType:       "market",
		QuantityDecimal: "0.01",
		ValidUntilMs:    time.Now().UnixMilli() + 60000,
		NowMsAtSaaS:     time.Now().UnixMilli(),
	})
	ack, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t))
	if ack.Status != wire.AckStatusRejected {
		t.Errorf("post-kill trade_command ack.status = %q, want rejected", ack.Status)
	}
	if !strings.Contains(ack.RejectReason, "frozen") {
		t.Errorf("reject reason = %q, want it to mention 'frozen'", ack.RejectReason)
	}

	cancel()
	_ = pc.Close()
	<-errCh
}

// TestKillSwitch_ResumeLiftsFreezeAndAcceptsTradeCommands pins the §5.13 v2
// resume path: a resume kill_switch (Symbol="resume") clears the frozen
// latch so a subsequent trade_command is accepted again — no process
// restart needed.
func TestKillSwitch_ResumeLiftsFreezeAndAcceptsTradeCommands(t *testing.T) {
	pc := newPipeConn()
	ex := NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(50000)})
	c := newTestClient(t, []*pipeConn{pc}, ex)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	runHubHandshake(t, pc, c.cfg.AccountID)

	// Freeze, then confirm a trade is rejected.
	pc.hubSendEnv(t, wire.TypeKillSwitch, c.cfg.AccountID, wire.KillSwitch{
		Reason: wire.KillSwitchDiscrepancyDetected, Scope: wire.KillSwitchScopeAll,
	})
	if ack, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t)); ack.Status != wire.AckStatusAccepted {
		t.Fatalf("kill ack.status = %q, want accepted", ack.Status)
	}
	pc.hubSendEnv(t, wire.TypeTradeCommand, c.cfg.AccountID, wire.TradeCommand{
		IntentKind: wire.IntentKindMacro, ClientOrderID: "01HKCOID0000000000000FROZEN",
		InstanceID: "01HKINSTANCE0000000000000A", Symbol: "BTCUSDT", Side: "buy",
		OrderType: "market", QuantityDecimal: "0.01",
		ValidUntilMs: time.Now().UnixMilli() + 60000, NowMsAtSaaS: time.Now().UnixMilli(),
	})
	if ack, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t)); ack.Status != wire.AckStatusRejected {
		t.Fatalf("pre-resume trade ack.status = %q, want rejected", ack.Status)
	}

	// Resume: Symbol="resume" lifts the latch.
	pc.hubSendEnv(t, wire.TypeKillSwitch, c.cfg.AccountID, wire.KillSwitch{
		Reason:         wire.KillSwitchManualAdminAction,
		OperatorUserID: "01HKOPER00000000000000000A",
		Scope:          wire.KillSwitchScopeAll,
		Symbol:         wire.KillSwitchSymbolResume,
	})
	if ack, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t)); ack.Status != wire.AckStatusAccepted {
		t.Fatalf("resume ack.status = %q, want accepted", ack.Status)
	}

	// A fresh trade_command (distinct client_order_id) is now accepted.
	pc.hubSendEnv(t, wire.TypeTradeCommand, c.cfg.AccountID, wire.TradeCommand{
		IntentKind: wire.IntentKindMacro, ClientOrderID: "01HKCOID0000000000000RESUME",
		InstanceID: "01HKINSTANCE0000000000000A", Symbol: "BTCUSDT", Side: "buy",
		OrderType: "market", QuantityDecimal: "0.01",
		ValidUntilMs: time.Now().UnixMilli() + 60000, NowMsAtSaaS: time.Now().UnixMilli(),
	})
	ack, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t))
	if ack.Status != wire.AckStatusAccepted {
		t.Errorf("post-resume trade ack.status = %q, want accepted; reason=%q", ack.Status, ack.RejectReason)
	}

	cancel()
	_ = pc.Close()
	<-errCh
}
