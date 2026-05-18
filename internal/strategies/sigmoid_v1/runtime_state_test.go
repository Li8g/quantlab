package sigmoid_v1

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRuntimeStateRoundTrip(t *testing.T) {
	want := RuntimeState{
		SchemaVersion:      runtimeStateSchemaV1,
		LastMacroBuyMs:     1_700_000_000_000,
		LastReleaseMs:      1_700_100_000_000,
		NAVPeakWindowMs:    []int64{1, 2, 3},
		NAVPeakWindowValue: []float64{10.0, 11.5, 9.25},
	}
	raw, err := encodeRuntimeState(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeRuntimeState(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch\nwant %+v\ngot  %+v", want, got)
	}
}

func TestRuntimeStateColdStart_EmptyBytes(t *testing.T) {
	rs, err := decodeRuntimeState(nil)
	if err != nil {
		t.Fatalf("nil decode: %v", err)
	}
	if !reflect.DeepEqual(rs, freshRuntimeState()) {
		t.Fatalf("nil bytes: got %+v, want fresh", rs)
	}
}

func TestRuntimeStateColdStart_JSONNull(t *testing.T) {
	rs, err := decodeRuntimeState(json.RawMessage("null"))
	if err != nil {
		t.Fatalf("null decode: %v", err)
	}
	if !reflect.DeepEqual(rs, freshRuntimeState()) {
		t.Fatalf("\"null\": got %+v, want fresh", rs)
	}
}

func TestRuntimeStateSchemaDowngrade(t *testing.T) {
	// Simulate a future-version RuntimeState that the v1 codec doesn't
	// understand. §6 says: don't error, reset.
	v2 := RuntimeState{
		SchemaVersion:  99,
		LastMacroBuyMs: 42,
	}
	raw, err := json.Marshal(v2)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rs, err := decodeRuntimeState(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(rs, freshRuntimeState()) {
		t.Fatalf("schema mismatch must reset; got %+v", rs)
	}
}

func TestRuntimeStateEncodeForcesCurrentSchema(t *testing.T) {
	// A caller hands us a state with a stale or zeroed SchemaVersion;
	// encode must stamp it back to v1 so future decoders accept it.
	rs := RuntimeState{SchemaVersion: 0, LastMacroBuyMs: 7}
	raw, err := encodeRuntimeState(rs)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var probe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if probe.SchemaVersion != runtimeStateSchemaV1 {
		t.Fatalf("encoded schema_version = %d, want %d", probe.SchemaVersion, runtimeStateSchemaV1)
	}
}

func TestRuntimeStateMalformedJSONIsError(t *testing.T) {
	// Garbage bytes are NOT the cold-start path — they signal an engine
	// bug. We want the error to surface, not a silent reset.
	_, err := decodeRuntimeState(json.RawMessage("{not-json"))
	if err == nil {
		t.Fatal("decode garbage: want error, got nil")
	}
}
