package sigmoid_v1

import (
	"math"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/strategy"
)

// noFriction returns the zero-friction params used for invariant
// tests (matches GAConfigSnapshot test_mode=true).
func noFriction() domain.FrictionParams {
	return domain.FrictionParams{TakerFeeBPS: 0, SlippageBPS: 0}
}

// stdFriction returns the prototype taker fee + slippage used in
// fill-discount tests (5 BPS taker, 2 BPS slippage — same as
// gaConfigFixture in encode_test).
func stdFriction() domain.FrictionParams {
	return domain.FrictionParams{TakerFeeBPS: 5, SlippageBPS: 2}
}

func macroBuy(amountUSD float64) strategy.OrderIntent {
	return strategy.OrderIntent{
		Kind:        strategy.OrderKindMacro,
		Side:        strategy.OrderSideBuy,
		OrderType:   strategy.OrderTypeMarket,
		QuantityUSD: amountUSD,
	}
}

func microBuy(amountUSD float64) strategy.OrderIntent {
	return strategy.OrderIntent{
		Kind:        strategy.OrderKindMicro,
		Side:        strategy.OrderSideBuy,
		OrderType:   strategy.OrderTypeMarket,
		QuantityUSD: amountUSD,
	}
}

func microSell(amountUSD float64) strategy.OrderIntent {
	return strategy.OrderIntent{
		Kind:        strategy.OrderKindMicro,
		Side:        strategy.OrderSideSell,
		OrderType:   strategy.OrderTypeMarket,
		QuantityUSD: amountUSD,
	}
}

// ----- applyMacroBuy -----

func TestApplyMacroBuy_NoFrictionMovesUSDTToDeadBTC(t *testing.T) {
	p := strategy.PortfolioSnapshot{USDT: 1000}
	got, applied := applyMacroBuy(p, macroBuy(100), 50_000, noFriction())
	if !applied {
		t.Fatal("applied=false, want true")
	}
	if got.USDT != 900 {
		t.Errorf("USDT = %v, want 900", got.USDT)
	}
	// 100/50_000 = 0.002 BTC, no fee → DeadBTC = 0.002.
	if math.Abs(got.DeadBTC-0.002) > 1e-12 {
		t.Errorf("DeadBTC = %v, want 0.002", got.DeadBTC)
	}
	if got.FloatBTC != 0 {
		t.Errorf("FloatBTC = %v, want 0 (macro never touches FloatBTC)", got.FloatBTC)
	}
}

func TestApplyMacroBuy_WithFrictionReducesFill(t *testing.T) {
	p := strategy.PortfolioSnapshot{USDT: 1000}
	got, _ := applyMacroBuy(p, macroBuy(100), 50_000, stdFriction())
	// preFee = 100 / (50_000 * 1.0002) = ~0.0019996 BTC.
	// post-fee = preFee * 0.9995 = ~0.0019986 BTC.
	if got.DeadBTC <= 0 || got.DeadBTC >= 0.002 {
		t.Errorf("with friction DeadBTC = %v, want in (0, 0.002)", got.DeadBTC)
	}
	if got.USDT != 900 {
		t.Errorf("USDT = %v, want 900 (notional fixed, fee taken from asset)", got.USDT)
	}
}

func TestApplyMacroBuy_SkipOnInsufficientUSDT(t *testing.T) {
	p := strategy.PortfolioSnapshot{USDT: 50}
	got, applied := applyMacroBuy(p, macroBuy(100), 50_000, noFriction())
	if applied {
		t.Error("applied=true on insufficient USDT, want false")
	}
	if got != p {
		t.Errorf("portfolio mutated on skip: got %+v, want %+v", got, p)
	}
}

func TestApplyMacroBuy_RejectsWrongKindOrSide(t *testing.T) {
	p := strategy.PortfolioSnapshot{USDT: 1000}
	micro := microBuy(100)
	if _, applied := applyMacroBuy(p, micro, 50_000, noFriction()); applied {
		t.Error("micro order accepted by applyMacroBuy")
	}
	sell := strategy.OrderIntent{
		Kind: strategy.OrderKindMacro, Side: strategy.OrderSideSell, QuantityUSD: 100,
	}
	if _, applied := applyMacroBuy(p, sell, 50_000, noFriction()); applied {
		t.Error("macro sell accepted by applyMacroBuy")
	}
}

// ----- applyMicroBuy -----

func TestApplyMicroBuy_MovesUSDTToFloatBTC(t *testing.T) {
	p := strategy.PortfolioSnapshot{USDT: 1000}
	got, applied := applyMicroBuy(p, microBuy(200), 50_000, noFriction())
	if !applied {
		t.Fatal("applied=false, want true")
	}
	if got.USDT != 800 {
		t.Errorf("USDT = %v, want 800", got.USDT)
	}
	if math.Abs(got.FloatBTC-0.004) > 1e-12 {
		t.Errorf("FloatBTC = %v, want 0.004", got.FloatBTC)
	}
	if got.DeadBTC != 0 {
		t.Errorf("DeadBTC = %v, want 0 (micro never touches DeadBTC)", got.DeadBTC)
	}
}

func TestApplyMicroBuy_SkipOnInsufficientUSDT(t *testing.T) {
	p := strategy.PortfolioSnapshot{USDT: 50}
	got, applied := applyMicroBuy(p, microBuy(100), 50_000, noFriction())
	if applied {
		t.Error("applied=true on insufficient USDT")
	}
	if got != p {
		t.Errorf("portfolio mutated on skip: %+v", got)
	}
}

// ----- applyMicroSell -----

func TestApplyMicroSell_MovesFloatBTCToUSDT(t *testing.T) {
	p := strategy.PortfolioSnapshot{FloatBTC: 0.01, USDT: 100}
	// Sell $200 worth of BTC at $50_000 → 0.004 BTC.
	got, applied := applyMicroSell(p, microSell(200), 50_000, noFriction())
	if !applied {
		t.Fatal("applied=false, want true")
	}
	if math.Abs(got.FloatBTC-0.006) > 1e-12 {
		t.Errorf("FloatBTC = %v, want 0.006", got.FloatBTC)
	}
	if math.Abs(got.USDT-300) > 1e-9 {
		t.Errorf("USDT = %v, want 300", got.USDT)
	}
}

func TestApplyMicroSell_CapsAtAvailableFloatBTC(t *testing.T) {
	// Theoretical wants $500_000 worth (10 BTC) but FloatBTC = 0.01.
	// Should sell all 0.01 BTC instead of skipping.
	p := strategy.PortfolioSnapshot{FloatBTC: 0.01, USDT: 0}
	got, applied := applyMicroSell(p, microSell(500_000), 50_000, noFriction())
	if !applied {
		t.Fatal("applied=false, want true (should cap, not skip)")
	}
	if got.FloatBTC != 0 {
		t.Errorf("FloatBTC = %v, want 0 (fully drained)", got.FloatBTC)
	}
	if math.Abs(got.USDT-500) > 1e-9 {
		t.Errorf("USDT = %v, want 500 (all 0.01 BTC at $50k)", got.USDT)
	}
}

func TestApplyMicroSell_SkipOnZeroFloatBTC(t *testing.T) {
	p := strategy.PortfolioSnapshot{USDT: 100}
	got, applied := applyMicroSell(p, microSell(50), 50_000, noFriction())
	if applied {
		t.Error("applied=true on zero FloatBTC")
	}
	if got != p {
		t.Errorf("portfolio mutated on skip: %+v", got)
	}
}

// ----- applyRelease -----

func TestApplyRelease_PreservesDeadPlusFloat(t *testing.T) {
	p := strategy.PortfolioSnapshot{DeadBTC: 1.0, FloatBTC: 0.5}
	pre := p.DeadBTC + p.FloatBTC
	got, applied := applyRelease(p, strategy.ReleaseIntent{Quantity: 0.1})
	if !applied {
		t.Fatal("applied=false, want true")
	}
	if math.Abs(got.DeadBTC-0.9) > 1e-12 {
		t.Errorf("DeadBTC = %v, want 0.9", got.DeadBTC)
	}
	if math.Abs(got.FloatBTC-0.6) > 1e-12 {
		t.Errorf("FloatBTC = %v, want 0.6", got.FloatBTC)
	}
	post := got.DeadBTC + got.FloatBTC
	if math.Abs(pre-post) > 1e-12 {
		t.Errorf("conservation violated: pre=%v post=%v", pre, post)
	}
}

func TestApplyRelease_SkipOnInsufficientDeadBTC(t *testing.T) {
	p := strategy.PortfolioSnapshot{DeadBTC: 0.01, FloatBTC: 0.5}
	got, applied := applyRelease(p, strategy.ReleaseIntent{Quantity: 0.1})
	if applied {
		t.Error("applied=true on insufficient DeadBTC")
	}
	if got != p {
		t.Errorf("portfolio mutated on skip: %+v", got)
	}
}

func TestApplyRelease_SkipOnZeroQuantity(t *testing.T) {
	p := strategy.PortfolioSnapshot{DeadBTC: 1.0}
	if _, applied := applyRelease(p, strategy.ReleaseIntent{Quantity: 0}); applied {
		t.Error("applied=true on zero quantity")
	}
}

// ----- applyStrategyOutput -----

func TestApplyStrategyOutput_OrdersReleasesBeforeOrders(t *testing.T) {
	// Pre-bar portfolio has DeadBTC but no FloatBTC. Release frees
	// some FloatBTC; a same-bar micro sell can immediately tap it.
	p := strategy.PortfolioSnapshot{DeadBTC: 1.0, FloatBTC: 0, USDT: 0}
	out := strategy.StrategyOutput{
		ReleaseIntents: []strategy.ReleaseIntent{{Quantity: 0.05}},
		MicroOrders:    []strategy.OrderIntent{microSell(1000)}, // sell up to 1000$ at $50k = 0.02 BTC
	}
	got := applyStrategyOutput(p, out, 50_000, noFriction())
	// After release: DeadBTC=0.95, FloatBTC=0.05.
	// After sell of 0.02: FloatBTC=0.03, USDT=1000.
	if math.Abs(got.FloatBTC-0.03) > 1e-12 {
		t.Errorf("FloatBTC = %v, want 0.03", got.FloatBTC)
	}
	if math.Abs(got.USDT-1000) > 1e-9 {
		t.Errorf("USDT = %v, want 1000", got.USDT)
	}
	if math.Abs(got.DeadBTC-0.95) > 1e-12 {
		t.Errorf("DeadBTC = %v, want 0.95", got.DeadBTC)
	}
}

func TestApplyStrategyOutput_AllIntentsConserveInvariants(t *testing.T) {
	// Property: through any combination of intents, USDT ≥ 0,
	// DeadBTC ≥ 0, FloatBTC ≥ 0.
	p := strategy.PortfolioSnapshot{DeadBTC: 0.5, FloatBTC: 0.5, USDT: 1000}
	out := strategy.StrategyOutput{
		ReleaseIntents: []strategy.ReleaseIntent{{Quantity: 0.05}, {Quantity: 0.01}},
		MacroOrders:    []strategy.OrderIntent{macroBuy(200)},
		MicroOrders:    []strategy.OrderIntent{microBuy(300), microSell(150)},
	}
	got := applyStrategyOutput(p, out, 50_000, stdFriction())
	if got.USDT < 0 {
		t.Errorf("USDT went negative: %v", got.USDT)
	}
	if got.DeadBTC < 0 {
		t.Errorf("DeadBTC went negative: %v", got.DeadBTC)
	}
	if got.FloatBTC < 0 {
		t.Errorf("FloatBTC went negative: %v", got.FloatBTC)
	}
}

func TestApplyStrategyOutput_EmptyOutputIsNoOp(t *testing.T) {
	p := strategy.PortfolioSnapshot{DeadBTC: 0.5, FloatBTC: 0.5, USDT: 1000}
	got := applyStrategyOutput(p, strategy.StrategyOutput{}, 50_000, stdFriction())
	if got != p {
		t.Errorf("empty output mutated portfolio: got %+v want %+v", got, p)
	}
}
