package sigmoid_v1

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"regexp"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/strategy"
)

// stepTestSigmoid returns a *Sigmoid pinned to 1m bars. Most Step()
// tests want fine timestamp control which 1m enables; the
// barIntervalMs doesn't actually leak into Step() (RuntimeState window
// uses duration ms, not bar count), but pinning a value documents
// intent.
func stepTestSigmoid() *Sigmoid { return New(60_000) }

// stepTestGene returns a Clamp'd Gene matching defaultChromosome with
// shrunk EMA/MAV periods so 40-bar fixtures suffice.
func stepTestGene() domain.Gene {
	c := defaultChromosome()
	c.EMALongPeriod = 10
	c.EMAShortPeriod = 5
	c.MAVLongPeriod = 8
	c.MAVShortPeriod = 4
	return EncodeChromosome(c)
}

// stepTestInput builds a StrategyInput with sensible defaults; tests
// override the specific fields they care about. Closes default to a
// flat 50_000 series so the signal-stage doesn't produce noise that
// drowns out the trigger being exercised.
func stepTestInput(nowMs int64, portfolio strategy.PortfolioSnapshot) strategy.StrategyInput {
	return strategy.StrategyInput{
		NowMs:                nowMs,
		Closes:               flatCloses(40, 50_000),
		Timestamps:           nil,
		Portfolio:            portfolio,
		Chromosome:           stepTestGene(),
		LastProcessedBarTime: 0,
		RuntimeState:         nil,
	}
}

// ----- Macro engine integration -----

func TestStep_ColdStartFiresDeadlineMacro(t *testing.T) {
	s := stepTestSigmoid()
	in := stepTestInput(refMs, strategy.PortfolioSnapshot{USDT: 10_000})

	out, err := s.Step(in)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(out.MacroOrders) != 1 {
		t.Fatalf("MacroOrders = %d, want 1 (cold-start deadline)", len(out.MacroOrders))
	}
	o := out.MacroOrders[0]
	// macro_inject_usd default = 100 → deadline = 100 * 0.5 = 50.
	if o.QuantityUSD != 50 {
		t.Errorf("Quantity = %v, want 50 (half of macro_inject_usd)", o.QuantityUSD)
	}
	if o.Kind != strategy.OrderKindMacro || o.Side != strategy.OrderSideBuy {
		t.Errorf("kind=%q side=%q, want macro/buy", o.Kind, o.Side)
	}
	if o.ValidUntilMs != refMs+orderTTLMs {
		t.Errorf("ValidUntilMs=%d, want %d", o.ValidUntilMs, refMs+orderTTLMs)
	}
	// rs.LastMacroBuyMs should have been stamped → decoded round-trip
	// must reflect that.
	rs, err := decodeRuntimeState(out.RuntimeState)
	if err != nil {
		t.Fatalf("decode RuntimeState: %v", err)
	}
	if rs.LastMacroBuyMs != refMs {
		t.Errorf("rs.LastMacroBuyMs = %d, want %d", rs.LastMacroBuyMs, refMs)
	}
}

func TestStep_MacroSkippedWhenCashInsufficient(t *testing.T) {
	s := stepTestSigmoid()
	// USDT = $10. MicroReservePct default = 0.25.
	// TotalEquity = (0+0)*price + 10 = 10. ReserveFloor = 2.5.
	// Spendable = 7.5. Deadline injection = 50 > 7.5 → skip.
	in := stepTestInput(refMs, strategy.PortfolioSnapshot{USDT: 10})

	out, err := s.Step(in)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(out.MacroOrders) != 0 {
		t.Errorf("MacroOrders = %d, want 0 (insufficient cash)", len(out.MacroOrders))
	}
	// Critically: rs.LastMacroBuyMs must NOT be stamped on skip,
	// otherwise the next bar wouldn't retry.
	rs, _ := decodeRuntimeState(out.RuntimeState)
	if rs.LastMacroBuyMs != 0 {
		t.Errorf("rs.LastMacroBuyMs = %d on skip, want 0", rs.LastMacroBuyMs)
	}
}

func TestStep_MacroMonthlyFullAmount(t *testing.T) {
	s := stepTestSigmoid()
	// April 30 → May 1: monthly boundary. Pre-stamp LastMacroBuyMs so
	// the deadline path is suppressed; this isolates the monthly trigger.
	prevMonth := ms(2024, 4, 30, 23)
	now := ms(2024, 5, 1, 0)
	rs := freshRuntimeState()
	rs.LastMacroBuyMs = ms(2024, 4, 15, 0)
	encoded, _ := encodeRuntimeState(rs)

	in := stepTestInput(now, strategy.PortfolioSnapshot{USDT: 10_000})
	in.LastProcessedBarTime = prevMonth
	in.RuntimeState = encoded

	out, err := s.Step(in)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(out.MacroOrders) != 1 {
		t.Fatalf("MacroOrders = %d, want 1 (monthly)", len(out.MacroOrders))
	}
	if out.MacroOrders[0].QuantityUSD != 100 {
		t.Errorf("Quantity = %v, want 100 (full macro_inject_usd)",
			out.MacroOrders[0].QuantityUSD)
	}
}

// ----- Micro / wedge filter integration -----

func TestStep_MicroOrderEmittedAboveDust(t *testing.T) {
	s := stepTestSigmoid()
	// Big portfolio + recent macro buy so macro doesn't fire. Force a
	// non-trivial signal by crafting closes with a strong logReturn.
	rs := freshRuntimeState()
	rs.LastMacroBuyMs = refMs - 1*dayMs
	encoded, _ := encodeRuntimeState(rs)

	in := stepTestInput(refMs, strategy.PortfolioSnapshot{
		FloatBTC: 0.1, USDT: 5000,
	})
	in.LastProcessedBarTime = refMs - 60_000
	in.RuntimeState = encoded
	// Late-spike series: 30 bars flat then a 10% surge over the last 10.
	in.Closes = make([]float64, 40)
	for i := 0; i < 30; i++ {
		in.Closes[i] = 50_000
	}
	for i := 30; i < 40; i++ {
		in.Closes[i] = 50_000 * (1 + 0.01*float64(i-29))
	}

	out, err := s.Step(in)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(out.MicroOrders) != 1 {
		t.Fatalf("MicroOrders = %d, want 1 (theoreticalUSD above dust)", len(out.MicroOrders))
	}
	o := out.MicroOrders[0]
	if o.Kind != strategy.OrderKindMicro {
		t.Errorf("Kind=%q, want micro", o.Kind)
	}
	if o.QuantityUSD < minMicroOrderUSD {
		t.Errorf("QuantityUSD=%v, want ≥ minMicroOrderUSD=%v",
			o.QuantityUSD, minMicroOrderUSD)
	}
}

func TestStep_MicroDustDroppedInQuietState(t *testing.T) {
	s := stepTestSigmoid()
	// To exercise the §2.3 "quiet → 归零" branch we need:
	//   - quiet state (volRatio < quietThreshold)
	//   - non-zero theoreticalUSD strictly below the $5 dust floor
	//   - delta below the wedge-break threshold (so the order would
	//     be dropped even in active state — isolates the quiet branch)
	//
	// Setup: zero γ disables inventory bias; zero a-weights make
	// signal = 0 → target = 0.5 exactly.
	// alternatingCloses(40, 100, 101) ends on closes[39]=101 (39 odd).
	// FloatBTC=0.5, USDT=51 ⇒ FloatBTC·price = 50.5, totalEquity =
	// 101.5, currentWeight = 50.5/101.5 ≈ 0.4975, delta ≈ 0.00246
	// (below the 0.005 wedge threshold), theoreticalUSD = 0.5/2 =
	// $0.25 — solidly inside (0, $5 dust).
	c := defaultChromosome()
	c.EMALongPeriod = 10
	c.EMAShortPeriod = 5
	c.MAVLongPeriod = 8
	c.MAVShortPeriod = 4
	c.QuietThreshold = 1.2 // > 1.0 ⇒ alternating-close volRatio (1.0) is quiet
	c.A1, c.A2, c.A3 = 0, 0, 0
	c.Gamma = 0
	gene := EncodeChromosome(c)

	rs := freshRuntimeState()
	rs.LastMacroBuyMs = refMs - 1*dayMs
	encoded, _ := encodeRuntimeState(rs)

	in := strategy.StrategyInput{
		NowMs:                refMs,
		Closes:               alternatingCloses(40, 100, 101),
		Portfolio:            strategy.PortfolioSnapshot{FloatBTC: 0.5, USDT: 51},
		Chromosome:           gene,
		LastProcessedBarTime: refMs - 60_000,
		RuntimeState:         encoded,
	}

	out, err := s.Step(in)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(out.MicroOrders) != 0 {
		t.Errorf("MicroOrders = %d, want 0 (dust in quiet state)", len(out.MicroOrders))
	}
	if out.DebugSnapshot == nil || *out.DebugSnapshot.MarketState != string(MarketStateQuiet) {
		t.Errorf("setup precondition: market state should be quiet, got %v", out.DebugSnapshot)
	}
}

func TestStep_MicroDustForcedThroughOnWedgeBreak(t *testing.T) {
	s := stepTestSigmoid()
	// Construct an ACTIVE-state scenario with deltaWeight just above
	// the wedge threshold (0.005). currentWeight 0 + tiny target →
	// delta ≈ target. For delta ≈ 0.01, with TotalEquity = 100,
	// theoreticalUSD = 1 < $5 dust threshold → must be forced.
	c := defaultChromosome()
	c.EMALongPeriod = 10
	c.EMAShortPeriod = 5
	c.MAVLongPeriod = 8
	c.MAVShortPeriod = 4
	c.QuietThreshold = 0.5 // ensure active
	c.Beta = 5             // strong sigmoid response so a small negative
	c.A1, c.A2, c.A3 = -0.05, 0, 0 // small negative signal → push BTC weight up
	gene := EncodeChromosome(c)

	rs := freshRuntimeState()
	rs.LastMacroBuyMs = refMs - 1*dayMs
	encoded, _ := encodeRuntimeState(rs)

	in := strategy.StrategyInput{
		NowMs:                refMs,
		Closes:               alternatingCloses(40, 100, 101), // active
		Portfolio:            strategy.PortfolioSnapshot{USDT: 100},
		Chromosome:           gene,
		LastProcessedBarTime: refMs - 60_000,
		RuntimeState:         encoded,
	}

	out, err := s.Step(in)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	// Either the wedge-break fires (1 order at exactly minMicroOrderUSD)
	// or theoreticalUSD ≥ dust naturally. Either way the order count
	// should be 1 — the *absence* of an order would mean dust + no
	// wedge break, which contradicts our setup (deltaWeight >= 0.005).
	if len(out.MicroOrders) != 1 {
		t.Fatalf("MicroOrders = %d, want 1 (wedge break path)", len(out.MicroOrders))
	}
	// If theoreticalUSD was below dust, the forced quantity = $5 exact.
	q := out.MicroOrders[0].QuantityUSD
	if q < minMicroOrderUSD-1e-9 {
		t.Errorf("QuantityUSD=%v < minMicroOrderUSD=%v", q, minMicroOrderUSD)
	}
}

// ----- Release engine integration -----

func TestStep_ReleaseFiresOnDrawdown(t *testing.T) {
	s := stepTestSigmoid()
	// Seed RuntimeState with a NAV peak well above current NAV so
	// drawdown > threshold (default 0.3). LastReleaseMs = 0 → cooldown
	// bypassed.
	rs := freshRuntimeState()
	rs.LastMacroBuyMs = refMs - 1*dayMs // suppress macro
	rs.NAVPeakWindowMs = []int64{refMs - 5*dayMs}
	rs.NAVPeakWindowValue = []float64{100_000} // peak
	encoded, _ := encodeRuntimeState(rs)

	// Current portfolio: DeadBTC=1, FloatBTC=0.5, USDT=0, price=50_000.
	// NAV = (1+0.5)*50_000 = 75_000. Drawdown from 100_000 = 25% — but
	// that's < 30% threshold. Lower price to force higher drawdown.
	in := stepTestInput(refMs, strategy.PortfolioSnapshot{
		DeadBTC: 1, FloatBTC: 0.5,
	})
	for i := range in.Closes {
		in.Closes[i] = 40_000 // NAV = 1.5 * 40_000 = 60_000 → drawdown = 40%
	}
	in.LastProcessedBarTime = refMs - 60_000
	in.RuntimeState = encoded

	out, err := s.Step(in)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(out.ReleaseIntents) != 1 {
		t.Fatalf("ReleaseIntents = %d, want 1 (drawdown > threshold)", len(out.ReleaseIntents))
	}
	r := out.ReleaseIntents[0]
	if r.NowMs != refMs {
		t.Errorf("ReleaseIntent.NowMs = %d, want %d", r.NowMs, refMs)
	}
	// quantity = min(DeadBTC*0.10, FloatBTC*0.20) = min(0.10, 0.10) = 0.10.
	if math.Abs(r.Quantity-0.10) > 1e-9 {
		t.Errorf("Quantity=%v, want 0.10", r.Quantity)
	}
	// rs.LastReleaseMs stamped.
	rs2, _ := decodeRuntimeState(out.RuntimeState)
	if rs2.LastReleaseMs != refMs {
		t.Errorf("rs.LastReleaseMs = %d, want %d", rs2.LastReleaseMs, refMs)
	}
}

func TestStep_ReleaseSuppressedByCooldown(t *testing.T) {
	s := stepTestSigmoid()
	rs := freshRuntimeState()
	rs.LastMacroBuyMs = refMs - 1*dayMs
	rs.LastReleaseMs = refMs - 3*dayMs // 3d < 7d cooldown
	rs.NAVPeakWindowMs = []int64{refMs - 5*dayMs}
	rs.NAVPeakWindowValue = []float64{100_000}
	encoded, _ := encodeRuntimeState(rs)

	in := stepTestInput(refMs, strategy.PortfolioSnapshot{
		DeadBTC: 1, FloatBTC: 0.5,
	})
	for i := range in.Closes {
		in.Closes[i] = 40_000
	}
	in.LastProcessedBarTime = refMs - 60_000
	in.RuntimeState = encoded

	out, err := s.Step(in)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(out.ReleaseIntents) != 0 {
		t.Errorf("ReleaseIntents = %d, want 0 (cooldown)", len(out.ReleaseIntents))
	}
	// LastReleaseMs unchanged on skip.
	rs2, _ := decodeRuntimeState(out.RuntimeState)
	if rs2.LastReleaseMs != refMs-3*dayMs {
		t.Errorf("rs.LastReleaseMs mutated on skip: got %d", rs2.LastReleaseMs)
	}
}

// ----- General Step() contract -----

func TestStep_DebugSnapshotPopulated(t *testing.T) {
	s := stepTestSigmoid()
	in := stepTestInput(refMs, strategy.PortfolioSnapshot{USDT: 10_000})
	out, err := s.Step(in)
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	if out.DebugSnapshot == nil {
		t.Fatal("DebugSnapshot is nil")
	}
	d := out.DebugSnapshot
	if d.Signal == nil || d.TargetWeight == nil || d.MarketState == nil {
		t.Errorf("DebugSnapshot has nil fields: %+v", d)
	}
	if *d.MarketState != string(MarketStateQuiet) && *d.MarketState != string(MarketStateActive) {
		t.Errorf("DebugSnapshot.MarketState = %q, want quiet|active", *d.MarketState)
	}
}

func TestStep_Deterministic(t *testing.T) {
	s := stepTestSigmoid()
	in := stepTestInput(refMs, strategy.PortfolioSnapshot{
		DeadBTC: 0.5, FloatBTC: 0.5, USDT: 5000,
	})

	out1, err := s.Step(in)
	if err != nil {
		t.Fatalf("Step #1: %v", err)
	}
	out2, err := s.Step(in)
	if err != nil {
		t.Fatalf("Step #2: %v", err)
	}

	// Marshal to JSON for a structural compare that's robust against
	// slice header differences but catches every observable bit.
	j1, _ := json.Marshal(out1)
	j2, _ := json.Marshal(out2)
	if !bytes.Equal(j1, j2) {
		t.Errorf("non-deterministic output\nout1=%s\nout2=%s", j1, j2)
	}
}

func TestStep_RuntimeStateRoundTrip(t *testing.T) {
	s := stepTestSigmoid()
	// Round 1: cold-start, capture the post-Step RuntimeState.
	in1 := stepTestInput(refMs, strategy.PortfolioSnapshot{USDT: 10_000})
	out1, err := s.Step(in1)
	if err != nil {
		t.Fatalf("Step #1: %v", err)
	}

	// Round 2: feed out1.RuntimeState back in. Same UTC month + recent
	// macro buy → no macro fire this round.
	in2 := stepTestInput(refMs+60_000, strategy.PortfolioSnapshot{USDT: 10_000})
	in2.LastProcessedBarTime = refMs
	in2.RuntimeState = out1.RuntimeState
	out2, err := s.Step(in2)
	if err != nil {
		t.Fatalf("Step #2: %v", err)
	}
	if len(out2.MacroOrders) != 0 {
		t.Errorf("Step #2 MacroOrders = %d, want 0 (same month + recent buy)",
			len(out2.MacroOrders))
	}
	rs, _ := decodeRuntimeState(out2.RuntimeState)
	// NAV peak window has 2 entries by round 2.
	if len(rs.NAVPeakWindowMs) != 2 {
		t.Errorf("NAVPeakWindow len = %d after 2 Steps, want 2",
			len(rs.NAVPeakWindowMs))
	}
}

func TestStep_ErrorOnMalformedRuntimeState(t *testing.T) {
	s := stepTestSigmoid()
	in := stepTestInput(refMs, strategy.PortfolioSnapshot{USDT: 10_000})
	in.RuntimeState = json.RawMessage("{not-json")

	_, err := s.Step(in)
	if err == nil {
		t.Fatal("Step on garbage RuntimeState: want error, got nil")
	}
}

func TestStep_NoWallClockInSource(t *testing.T) {
	// Spec §10 / CLAUDE.md §10.1: Step() must derive every time from
	// input.NowMs. Comments documenting the prohibition can themselves
	// contain the literal "time.Now" — we strip line + block comments
	// before scanning so the test catches real code, not its own
	// documentation.
	//
	// step.go is the only file in scope. macro.go uses time.UnixMilli
	// (a pure conversion from input.NowMs) which is allowed.
	src, err := os.ReadFile("step.go")
	if err != nil {
		t.Fatalf("read step.go: %v", err)
	}
	noLineComments := regexp.MustCompile(`//[^\n]*`).ReplaceAllString(string(src), "")
	noBlockComments := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(noLineComments, "")

	banned := regexp.MustCompile(`\btime\.(Now|Since|Until)\b`)
	if loc := banned.FindStringIndex(noBlockComments); loc != nil {
		t.Errorf("step.go contains banned wall-clock call: %s",
			noBlockComments[loc[0]:loc[1]])
	}
}
