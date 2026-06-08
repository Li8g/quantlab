// Sharpe statistics for the verification layer.
//
// ComputeSharpeStats has moved to internal/quant — it is a pure numeric
// helper with no engine-layer concerns, so it lives in quant to allow
// strategy-layer code to call it without importing verification.
//
// SharpeStats is kept as a type alias here for callers (e.g.
// internal/repository/sharpe_bank.go) that reference verification.SharpeStats
// by name. The canonical type definition is resultpkg.SharpeStats.
package verification

import "quantlab/internal/resultpkg"

// SharpeStats is an alias kept for source compatibility. The canonical
// type lives in resultpkg so code below the engine boundary can carry it.
type SharpeStats = resultpkg.SharpeStats
