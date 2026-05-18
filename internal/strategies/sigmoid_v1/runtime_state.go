// RuntimeState is sigmoid_v1's strategy-private memory across ticks.
// Authoritative schema: docs/strategies/sigmoid_v1.md §6.
//
// Engine treats the wire form as opaque bytes (json.RawMessage on
// StrategyInput.RuntimeState / StrategyOutput.RuntimeState); this file owns
// the encode/decode boundary plus the schema-version downgrade rule that
// §6 mandates ("schema mismatch → reset to empty state, log").
//
// Adapter.Reset(plan) is required by §6.1 and 上游 §8.4 to clear this state
// entirely; the helper here is just the codec, not the lifecycle owner.
package sigmoid_v1

import (
	"encoding/json"
)

// runtimeStateSchemaV1 is the current schema version. Any field
// addition/removal must bump this constant per §11 decision #13.
const runtimeStateSchemaV1 = 1

// RuntimeState mirrors the §6 schema verbatim. The NAVPeakWindow* slices
// roll the most-recent 30 days of (timestamp, NAV) entries; size bound is
// dynamic per §8.3 and enforced by the release.go helpers, not by this
// struct.
type RuntimeState struct {
	SchemaVersion      int       `json:"schema_version"`
	LastMacroBuyMs     int64     `json:"last_macro_buy_ms"`
	LastReleaseMs      int64     `json:"last_release_ms"`
	NAVPeakWindowMs    []int64   `json:"nav_peak_window_ms"`
	NAVPeakWindowValue []float64 `json:"nav_peak_window_value"`
}

// freshRuntimeState returns the cold-start zero value. SchemaVersion is
// stamped so the next decodeRuntimeState round-trip stays in v1 territory.
func freshRuntimeState() RuntimeState {
	return RuntimeState{SchemaVersion: runtimeStateSchemaV1}
}

// decodeRuntimeState parses the opaque bytes Engine handed to Step().
//
// Three benign inputs all return (fresh state, nil):
//   - len(raw) == 0          — cold start (no previous tick)
//   - JSON literal "null"    — older callers may serialize a nil
//     json.RawMessage this way
//   - SchemaVersion mismatch — §6 graceful downgrade
//
// Any other malformed JSON is a real bug (engine should never hand the
// strategy garbage) and surfaces as an error so the worker pool can fail
// the gene loudly rather than silently reset.
func decodeRuntimeState(raw json.RawMessage) (RuntimeState, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return freshRuntimeState(), nil
	}
	var rs RuntimeState
	if err := json.Unmarshal(raw, &rs); err != nil {
		return RuntimeState{}, err
	}
	if rs.SchemaVersion != runtimeStateSchemaV1 {
		return freshRuntimeState(), nil
	}
	return rs, nil
}

// encodeRuntimeState serializes rs for the StrategyOutput. SchemaVersion
// is forced to the current constant so callers cannot accidentally leak
// a stale version through.
func encodeRuntimeState(rs RuntimeState) (json.RawMessage, error) {
	rs.SchemaVersion = runtimeStateSchemaV1
	return json.Marshal(rs)
}
