package sigmoid_v1

import (
	"testing"
	"time"
)

// ms returns the UTC-fixed milliseconds for a calendar instant.
// Tests use it to write self-documenting bar timestamps.
func ms(year int, month time.Month, day, hour int) int64 {
	return time.Date(year, month, day, hour, 0, 0, 0, time.UTC).UnixMilli()
}

func TestMacroEngine_MonthlyTrigger(t *testing.T) {
	// LastProcessedBarTime = 2024-04-30 23:00, now = 2024-05-01 00:00.
	// Different UTC month → monthly fires, full amount.
	lastBar := ms(2024, 4, 30, 23)
	now := ms(2024, 5, 1, 0)
	lastBuy := ms(2024, 4, 1, 0) // 30 days ago → deadline NOT yet
	d := EvaluateMacroEngine(now, lastBar, lastBuy, 100)
	if !d.ShouldInject {
		t.Fatalf("monthly: ShouldInject = false, want true")
	}
	if d.AmountUSD != 100 {
		t.Errorf("monthly: AmountUSD = %v, want 100", d.AmountUSD)
	}
	if d.Reason != MacroReasonMonthly {
		t.Errorf("monthly: Reason = %q, want %q", d.Reason, MacroReasonMonthly)
	}
}

func TestMacroEngine_SameMonthNoTrigger(t *testing.T) {
	// Both in May, deadline well within 60 days → no fire.
	lastBar := ms(2024, 5, 10, 0)
	now := ms(2024, 5, 11, 0)
	lastBuy := ms(2024, 5, 1, 0)
	d := EvaluateMacroEngine(now, lastBar, lastBuy, 100)
	if d.ShouldInject {
		t.Errorf("same month + recent buy: ShouldInject = true, want false (%+v)", d)
	}
	if d.Reason != MacroReasonNone {
		t.Errorf("same month: Reason = %q, want empty", d.Reason)
	}
}

func TestMacroEngine_DeadlineTrigger(t *testing.T) {
	// Stay inside one UTC month so monthly doesn't mask the deadline
	// path. lastBuy = 60 days before now exactly → deadline fires.
	now := ms(2024, 6, 30, 0)
	lastBar := ms(2024, 6, 29, 0)
	lastBuy := now - macroDeadlineWindowMs // exactly 60 days
	d := EvaluateMacroEngine(now, lastBar, lastBuy, 100)
	if !d.ShouldInject {
		t.Fatalf("deadline boundary: ShouldInject = false, want true (>= 60 days)")
	}
	if d.AmountUSD != 50 {
		t.Errorf("deadline: AmountUSD = %v, want 50 (half)", d.AmountUSD)
	}
	if d.Reason != MacroReasonDeadline {
		t.Errorf("deadline: Reason = %q, want %q", d.Reason, MacroReasonDeadline)
	}
}

func TestMacroEngine_DeadlineNotYet(t *testing.T) {
	// 59 days, same month → no trigger.
	now := ms(2024, 6, 30, 0)
	lastBar := ms(2024, 6, 29, 0)
	lastBuy := now - macroDeadlineWindowMs + 1 // 1ms shy
	d := EvaluateMacroEngine(now, lastBar, lastBuy, 100)
	if d.ShouldInject {
		t.Errorf("59d59m59s999ms: ShouldInject = true, want false")
	}
}

func TestMacroEngine_ColdStartFiresDeadline(t *testing.T) {
	// lastMacroBuyMs == 0 → deadline; lastProcessedBarMs == 0 means
	// monthly is SUPPRESSED at cold start per spec §3.1 clarification.
	now := ms(2024, 5, 15, 12)
	d := EvaluateMacroEngine(now, 0, 0, 200)
	if !d.ShouldInject {
		t.Fatalf("cold start: ShouldInject = false, want true")
	}
	if d.Reason != MacroReasonDeadline {
		t.Errorf("cold start: Reason = %q, want %q (NOT monthly)",
			d.Reason, MacroReasonDeadline)
	}
	if d.AmountUSD != 100 { // 200 * 0.5
		t.Errorf("cold start: AmountUSD = %v, want 100 (half of 200)", d.AmountUSD)
	}
}

func TestMacroEngine_MonthlyWinsWhenBothFire(t *testing.T) {
	// Months differ AND 60+ days since last buy → monthly takes the
	// full amount, NOT deadline's half.
	lastBar := ms(2024, 4, 30, 23)
	now := ms(2024, 7, 1, 0) // 2 months later
	lastBuy := ms(2024, 4, 1, 0)
	d := EvaluateMacroEngine(now, lastBar, lastBuy, 100)
	if d.Reason != MacroReasonMonthly {
		t.Errorf("both fire: Reason = %q, want monthly (full amount)", d.Reason)
	}
	if d.AmountUSD != 100 {
		t.Errorf("both fire: AmountUSD = %v, want 100", d.AmountUSD)
	}
}

func TestMacroEngine_UTCBoundaryNotLocal(t *testing.T) {
	// 2024-01-31 18:00 UTC and 2024-02-01 00:00 UTC: different UTC
	// months. A naive `local time` impl would interpret these
	// differently in TZ=America/New_York. We're asserting the UTC
	// bucket is canonical.
	prev := ms(2024, 1, 31, 18)
	now := ms(2024, 2, 1, 0)
	d := EvaluateMacroEngine(now, prev, ms(2024, 1, 15, 0), 100)
	if d.Reason != MacroReasonMonthly {
		t.Errorf("UTC month-boundary: Reason = %q, want monthly", d.Reason)
	}
}

func TestMacroEngine_LastProcessedZeroBlocksMonthly(t *testing.T) {
	// lastMacroBuyMs is a recent timestamp (10 days ago), so
	// deadline does NOT fire. lastProcessedBarMs == 0 (cold start of
	// the bar stream). Without the spec-mandated suppression we'd see
	// epoch-month != current-month → spurious monthly fire. Assert
	// the suppression: no injection.
	now := ms(2024, 5, 15, 0)
	lastBuy := now - 10*24*60*60*1000 // 10 days ago
	d := EvaluateMacroEngine(now, 0, lastBuy, 100)
	if d.ShouldInject {
		t.Errorf("lastProcessed=0: ShouldInject = true (%+v), want false (spec suppression)", d)
	}
}
