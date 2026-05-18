package quant

import (
	"testing"

	"quantlab/internal/domain"
)

// TestApplyBuyFriction_Frictionless: with BPS=0, filledQty = notional/price
// and fee + slippage are 0. The "test_mode" case must fall out of the same
// formula, not a separate branch.
func TestApplyBuyFriction_Frictionless(t *testing.T) {
	fp := domain.FrictionParams{TakerFeeBPS: 0, SlippageBPS: 0}
	qty, fee, slip := ApplyBuyFriction(1000.0, 100.0, fp)
	if !approx(qty, 10.0, 1e-12) {
		t.Errorf("filledQty = %v, want 10.0", qty)
	}
	if fee != 0 || slip != 0 {
		t.Errorf("fee=%v slip=%v, want both 0", fee, slip)
	}
}

func TestApplyBuyFriction_FeeOnly(t *testing.T) {
	// 10 bps = 0.1% taker fee, no slippage.
	fp := domain.FrictionParams{TakerFeeBPS: 10, SlippageBPS: 0}
	qty, fee, slip := ApplyBuyFriction(1000.0, 100.0, fp)
	// preFeeQty = 1000/100 = 10; filledQty = 10 * 0.999 = 9.99
	if !approx(qty, 9.99, 1e-12) {
		t.Errorf("filledQty = %v, want 9.99", qty)
	}
	// feeUSD = 1000 * 0.001 = 1.0
	if !approx(fee, 1.0, 1e-12) {
		t.Errorf("fee = %v, want 1.0", fee)
	}
	if slip != 0 {
		t.Errorf("slip = %v, want 0", slip)
	}
}

func TestApplyBuyFriction_SlippageOnly(t *testing.T) {
	// 50 bps slippage, no fee.
	fp := domain.FrictionParams{TakerFeeBPS: 0, SlippageBPS: 50}
	qty, fee, slip := ApplyBuyFriction(1000.0, 100.0, fp)
	// exec = 100 * 1.005 = 100.5; preFeeQty = 1000/100.5 ≈ 9.9502487...
	// filledQty == preFeeQty (no fee). slip = 1000 - 9.9502...*100 = 1000 - 995.024...
	wantQty := 1000.0 / 100.5
	if !approx(qty, wantQty, 1e-12) {
		t.Errorf("filledQty = %v, want %v", qty, wantQty)
	}
	if fee != 0 {
		t.Errorf("fee = %v, want 0", fee)
	}
	wantSlip := 1000.0 - wantQty*100.0
	if !approx(slip, wantSlip, 1e-12) {
		t.Errorf("slip = %v, want %v", slip, wantSlip)
	}
}

func TestApplyBuyFriction_FeeAndSlippage(t *testing.T) {
	fp := domain.FrictionParams{TakerFeeBPS: 10, SlippageBPS: 50}
	qty, fee, slip := ApplyBuyFriction(1000.0, 100.0, fp)
	// exec = 100.5; preFeeQty = 1000/100.5; filledQty = preFeeQty * 0.999
	preFeeQty := 1000.0 / 100.5
	wantQty := preFeeQty * 0.999
	if !approx(qty, wantQty, 1e-12) {
		t.Errorf("filledQty = %v, want %v", qty, wantQty)
	}
	if !approx(fee, 1.0, 1e-12) {
		t.Errorf("fee = %v, want 1.0", fee)
	}
	wantSlip := 1000.0 - preFeeQty*100.0
	if !approx(slip, wantSlip, 1e-12) {
		t.Errorf("slip = %v, want %v", slip, wantSlip)
	}
}

func TestApplyBuyFriction_NonPositiveInputs(t *testing.T) {
	fp := domain.FrictionParams{TakerFeeBPS: 10, SlippageBPS: 5}
	for _, tc := range []struct{ notional, price float64 }{
		{0, 100}, {-1, 100}, {1000, 0}, {1000, -1},
	} {
		q, f, s := ApplyBuyFriction(tc.notional, tc.price, fp)
		if q != 0 || f != 0 || s != 0 {
			t.Errorf("Buy(%v,%v): expected all-zero, got (%v,%v,%v)",
				tc.notional, tc.price, q, f, s)
		}
	}
}

// ----- Sell -----

func TestApplySellFriction_Frictionless(t *testing.T) {
	fp := domain.FrictionParams{TakerFeeBPS: 0, SlippageBPS: 0}
	quote, fee, slip := ApplySellFriction(10.0, 100.0, fp)
	if !approx(quote, 1000.0, 1e-12) {
		t.Errorf("filledQuote = %v, want 1000.0", quote)
	}
	if fee != 0 || slip != 0 {
		t.Errorf("fee=%v slip=%v, want both 0", fee, slip)
	}
}

func TestApplySellFriction_FeeAndSlippage(t *testing.T) {
	fp := domain.FrictionParams{TakerFeeBPS: 10, SlippageBPS: 50}
	quote, fee, slip := ApplySellFriction(10.0, 100.0, fp)
	// exec = 100 * 0.995 = 99.5; preFeeQuote = 10 * 99.5 = 995
	// filledQuote = 995 * 0.999 = 994.005
	if !approx(quote, 994.005, 1e-9) {
		t.Errorf("filledQuote = %v, want 994.005", quote)
	}
	// fee = 995 * 0.001 = 0.995
	if !approx(fee, 0.995, 1e-12) {
		t.Errorf("fee = %v, want 0.995", fee)
	}
	// slip = 10*100 - 995 = 5
	if !approx(slip, 5.0, 1e-12) {
		t.Errorf("slip = %v, want 5.0", slip)
	}
}

// Round-trip: buy then sell should lose roughly 2*fee + 2*slippage of value.
func TestApplyFriction_RoundTripLossBounded(t *testing.T) {
	fp := domain.FrictionParams{TakerFeeBPS: 10, SlippageBPS: 5}
	notional := 1000.0
	price := 50000.0

	qty, _, _ := ApplyBuyFriction(notional, price, fp)
	quoteBack, _, _ := ApplySellFriction(qty, price, fp)

	// Round-trip loss as fraction; should be ~ 2*(fee+slip) = 30 bps = 0.003.
	loss := (notional - quoteBack) / notional
	if loss <= 0 || loss > 0.005 {
		t.Errorf("round-trip loss = %v, expected (0, 0.005]", loss)
	}
}
