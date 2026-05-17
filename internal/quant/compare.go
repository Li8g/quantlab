package quant

import "quantlab/internal/resultpkg"

// CompareFitness compares two ScoreTotal values for population ranking.
// Nil-safe: Value is nil when Fatal=true or cascaded-skip; this function
// never dereferences a nil pointer. Returns negative/zero/positive as
// expected by sort.SliceStable.
//
// Priority (high = better = smaller index after sort):
//  1. Both Fatal → tie (0); sort.SliceStable preserves prior insertion order.
//  2. a Fatal, b Normal → a is worse (+1).
//  3. b Fatal, a Normal → b is worse (-1).
//  4. Both Normal → descending Value order.
//
// Never use *a.Value > *b.Value directly — that panics on nil.
// Never write sentinel values (-99999, -1e18) into Value to avoid this.
func CompareFitness(a, b resultpkg.ScoreTotal) int {
	switch {
	case a.Fatal && b.Fatal:
		return 0
	case a.Fatal:
		return 1
	case b.Fatal:
		return -1
	default:
		av, bv := *a.Value, *b.Value
		if av > bv {
			return -1
		}
		if av < bv {
			return 1
		}
		return 0
	}
}
