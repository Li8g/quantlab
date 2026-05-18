package quant

import "quantlab/internal/domain"

// ApplyBuyFriction simulates a market buy of `notionalUSD` worth of the
// asset at reference `price` under taker fee + slippage. The model is:
//
//   - Slippage moves the execution price against the buyer:
//     exec = price * (1 + slippageBPS/1e4)
//   - Pre-fee qty bought = notionalUSD / exec
//   - Taker fee is taken from the asset received:
//     filledQty = preFeeQty * (1 - takerFeeBPS/1e4)
//
// All three return values are non-negative. When both BPS are 0 (test_mode),
// filledQty == notionalUSD/price and the cost components are 0 — making the
// frictionless path a literal special case of the same code, not a branch.
//
// Per I-3.11: every simulator (Ghost DCA, Evaluate, OOS, MC stress) must
// route through this function; do not reimplement the formula elsewhere.
func ApplyBuyFriction(notionalUSD, price float64, fp domain.FrictionParams) (filledQty, feeUSD, slippageCost float64) {
	if notionalUSD <= 0 || price <= 0 {
		return 0, 0, 0
	}
	slipRate := fp.SlippageBPS / 1e4
	feeRate := fp.TakerFeeBPS / 1e4

	execPrice := price * (1.0 + slipRate)
	preFeeQty := notionalUSD / execPrice

	filledQty = preFeeQty * (1.0 - feeRate)
	feeUSD = notionalUSD * feeRate
	// slippage cost (USD): you spent `notionalUSD` but got asset whose
	// quote-price value is preFeeQty*price = notionalUSD / (1+slipRate).
	slippageCost = notionalUSD - preFeeQty*price
	return filledQty, feeUSD, slippageCost
}

// ApplySellFriction simulates a market sell of `qty` asset at reference
// `price` under taker fee + slippage. The model mirrors ApplyBuyFriction:
//
//   - Slippage moves the execution price against the seller:
//     exec = price * (1 - slippageBPS/1e4)
//   - Pre-fee quote receivable = qty * exec
//   - Taker fee is taken from the quote received:
//     filledQuoteUSD = preFeeQuote * (1 - takerFeeBPS/1e4)
func ApplySellFriction(qty, price float64, fp domain.FrictionParams) (filledQuoteUSD, feeUSD, slippageCost float64) {
	if qty <= 0 || price <= 0 {
		return 0, 0, 0
	}
	slipRate := fp.SlippageBPS / 1e4
	feeRate := fp.TakerFeeBPS / 1e4

	execPrice := price * (1.0 - slipRate)
	preFeeQuote := qty * execPrice

	filledQuoteUSD = preFeeQuote * (1.0 - feeRate)
	feeUSD = preFeeQuote * feeRate
	slippageCost = qty*price - preFeeQuote
	return filledQuoteUSD, feeUSD, slippageCost
}
