// Macro engine triggers for sigmoid_v1. Source of truth:
// docs/strategies/sigmoid_v1.md §3.
//
// Pure decision layer: given (nowMs, lastProcessedBarMs, lastMacroBuyMs,
// macroInjectUSD) return whether and how much to inject. Step() (Phase
// 4c) turns the decision into an OrderIntent + writes back
// rs.LastMacroBuyMs. Adapter (Phase 4d) does the actual cash flow into
// DeadBTC.
//
// Iron-rule #2 ("NowMs 唯一时间源"): we use time.UnixMilli on the bar
// timestamps to derive UTC months — no time.Now() leaks in.
package sigmoid_v1

import "time"

// macroDeadlineWindowMs is decision #4 in spec §11: 60 days, allowing
// one missed monthly tick before the safety-net trigger fires.
const macroDeadlineWindowMs = int64(60) * 24 * 60 * 60 * 1000

// macroDeadlineRatio is decision #5: deadline fallback injects half of
// the chromosome-configured monthly amount.
const macroDeadlineRatio = 0.5

// MacroReason names the trigger so the caller can stamp it on
// OrderIntent / DebugSnapshot fields. The constants are also written
// to logs, so changing them is a wire-compat event for anyone tailing
// stdout — keep them stable.
type MacroReason string

const (
	MacroReasonNone     MacroReason = ""
	MacroReasonMonthly  MacroReason = "monthly"
	MacroReasonDeadline MacroReason = "deadline"
)

// MacroDecision is the §3.1 verdict for a single bar.
type MacroDecision struct {
	ShouldInject bool
	AmountUSD    float64
	Reason       MacroReason
}

// utcMonthBucket maps a millisecond timestamp to a UTC year*12+month
// integer. Comparing buckets is cheaper and clearer than two .Year()
// + .Month() checks; the bucket is monotonic in time so the "different
// month" predicate is literally `!=`.
func utcMonthBucket(ms int64) int {
	t := time.UnixMilli(ms).UTC()
	return t.Year()*12 + int(t.Month())
}

// EvaluateMacroEngine decides §3.1 + §3.3 outcomes for the current
// bar.
//
//	monthly:  lastProcessedBarMs > 0 AND UTC month(nowMs) != UTC month(lastProcessedBarMs)
//	deadline: lastMacroBuyMs == 0 OR (nowMs - lastMacroBuyMs >= 60 days)
//
// Cold-start (lastProcessedBarMs <= 0) suppresses the monthly path
// because there is no "previous bar" to compare against — §3.1 spec
// explicitly says cold-start fires DEADLINE half-amount, not a
// month-boundary full-amount. This guarantees a position seed without
// double-injecting on bar #1.
//
// If both fire on the same bar (e.g. cross-month after a 60-day
// outage), monthly wins because its amount is strictly larger and
// represents the higher-information signal ("new month, normal DCA")
// over the safety-net ("60 days of silence").
//
// Cash-availability is checked downstream (§3.3): the decision is
// theoretical; Step() / Adapter will skip if SpendableUSDT < AmountUSD.
func EvaluateMacroEngine(nowMs, lastProcessedBarMs, lastMacroBuyMs int64, macroInjectUSD float64) MacroDecision {
	monthlyFires := lastProcessedBarMs > 0 &&
		utcMonthBucket(nowMs) != utcMonthBucket(lastProcessedBarMs)

	deadlineFires := lastMacroBuyMs == 0 ||
		nowMs-lastMacroBuyMs >= macroDeadlineWindowMs

	switch {
	case monthlyFires:
		return MacroDecision{
			ShouldInject: true,
			AmountUSD:    macroInjectUSD,
			Reason:       MacroReasonMonthly,
		}
	case deadlineFires:
		return MacroDecision{
			ShouldInject: true,
			AmountUSD:    macroInjectUSD * macroDeadlineRatio,
			Reason:       MacroReasonDeadline,
		}
	default:
		return MacroDecision{Reason: MacroReasonNone}
	}
}
