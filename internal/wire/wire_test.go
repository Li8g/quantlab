package wire

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"quantlab/internal/resultpkg"
)

// TestSchemaVersionPinnedToResultpkg guards against a future change to
// resultpkg.SchemaVersionV533 silently desyncing the wire pin. The wire
// version must always equal the result-package version (the protocol doc
// §2.6 ties them together).
func TestSchemaVersionPinnedToResultpkg(t *testing.T) {
	if SchemaVersion != resultpkg.SchemaVersionV533 {
		t.Fatalf("wire.SchemaVersion = %q, want resultpkg.SchemaVersionV533 = %q",
			SchemaVersion, resultpkg.SchemaVersionV533)
	}
}

// TestMessageTypeIsKnown covers all 16 frozen types plus a negative case
// to ensure IsKnown rejects typos.
func TestMessageTypeIsKnown(t *testing.T) {
	known := []MessageType{
		TypeHello, TypeAuthRequired, TypeAuth, TypeAuthOK, TypeAuthFail,
		TypeStateSyncRequest, TypeStateSyncResponse,
		TypeTradeCommand, TypeAck, TypeOrderUpdate, TypeDeltaReport,
		TypePing, TypePong,
		TypeKillSwitch, TypeGracefulShutdown,
		TypeError,
	}
	if len(known) != 16 {
		t.Fatalf("expected 16 frozen message types, got %d", len(known))
	}
	for _, k := range known {
		if !k.IsKnown() {
			t.Errorf("IsKnown(%q) = false, want true", k)
		}
	}
	for _, bad := range []MessageType{"", "Hello", "TradeCommand", "trade-command", "unknown"} {
		if bad.IsKnown() {
			t.Errorf("IsKnown(%q) = true, want false", bad)
		}
	}
}

// TestEncodeEnvelopeFillsSchemaVersion checks that an empty SchemaVersion
// is auto-filled by EncodeEnvelope. Callers should not have to repeat the
// pin everywhere.
func TestEncodeEnvelopeFillsSchemaVersion(t *testing.T) {
	raw, err := EncodeEnvelope(Envelope{
		MsgID:       "01H000000000000000000000AA",
		Type:        TypePing,
		TimestampMs: 1700000000000,
		Payload:     json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	env, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.SchemaVersion != SchemaVersion {
		t.Errorf("got %q, want %q", env.SchemaVersion, SchemaVersion)
	}
}

// TestEncodeEnvelopeRejectsUnknownType makes sure programmer errors fail
// at encode time, not decode time.
func TestEncodeEnvelopeRejectsUnknownType(t *testing.T) {
	_, err := EncodeEnvelope(Envelope{
		MsgID:       "01H000000000000000000000AA",
		Type:        "bogus",
		TimestampMs: 1,
		Payload:     json.RawMessage(`{}`),
	})
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("got %v, want ErrUnknownType", err)
	}
}

// TestEncodeEnvelopeNilPayloadDefaults verifies that nil payload becomes
// `{}` so the wire never carries `"payload": null`.
func TestEncodeEnvelopeNilPayloadDefaults(t *testing.T) {
	raw, err := EncodeEnvelope(Envelope{
		MsgID:       "01H000000000000000000000AA",
		Type:        TypeAuthRequired,
		TimestampMs: 1,
		Payload:     nil,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(raw), `"payload":{}`) {
		t.Errorf("payload should default to {}, got: %s", raw)
	}
}

func TestDecodeEnvelopeSchemaMismatch(t *testing.T) {
	raw := []byte(`{"msg_id":"X","type":"ping","schema_version":"v9.9.9","timestamp_ms":1,"payload":{}}`)
	_, err := DecodeEnvelope(raw)
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("got %v, want ErrSchemaMismatch", err)
	}
}

func TestDecodeEnvelopeUnknownType(t *testing.T) {
	raw := []byte(`{"msg_id":"X","type":"foo","schema_version":"` + SchemaVersion + `","timestamp_ms":1,"payload":{}}`)
	_, err := DecodeEnvelope(raw)
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("got %v, want ErrUnknownType", err)
	}
}

func TestDecodeEnvelopeMissingMsgID(t *testing.T) {
	raw := []byte(`{"msg_id":"","type":"ping","schema_version":"` + SchemaVersion + `","timestamp_ms":1,"payload":{}}`)
	_, err := DecodeEnvelope(raw)
	if !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("got %v, want ErrInvalidEnvelope", err)
	}
}

func TestDecodeEnvelopeMalformedJSON(t *testing.T) {
	_, err := DecodeEnvelope([]byte(`{not json`))
	if !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("got %v, want ErrInvalidEnvelope", err)
	}
}

func TestDecodeEnvelopeMissingPayload(t *testing.T) {
	raw := []byte(`{"msg_id":"X","type":"ping","schema_version":"` + SchemaVersion + `","timestamp_ms":1}`)
	_, err := DecodeEnvelope(raw)
	if !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("got %v, want ErrInvalidEnvelope", err)
	}
}

// TestRoundTripAllTypes encodes a populated payload for each of the 16
// message types and verifies decode reproduces the same value. This is the
// minimum sanity check that field tags are consistent.
func TestRoundTripAllTypes(t *testing.T) {
	cases := []struct {
		name    string
		msgType MessageType
		payload any
	}{
		{"hello", TypeHello, Hello{
			AgentVersion: "0.1.0", AccountID: "01H...", SchemaVersion: SchemaVersion,
			Platform: "linux/amd64", Exchange: "binance_spot",
		}},
		{"auth_required", TypeAuthRequired, AuthRequired{}},
		{"auth", TypeAuth, Auth{Token: "agt_01H..._secret"}},
		{"auth_ok", TypeAuthOK, AuthOK{ServerNowMs: 1700000000000, AgentID: "01H..."}},
		{"auth_fail", TypeAuthFail, AuthFail{Code: AuthFailInvalidToken, Reason: "bad token"}},
		{"state_sync_request", TypeStateSyncRequest, StateSyncRequest{}},
		{"state_sync_response", TypeStateSyncResponse, StateSyncResponse{
			ReportedAtMs: 1700000000123,
			Positions: []Position{
				{Symbol: "BTC", FreeDecimal: "0.5", LockedDecimal: "0.0"},
			},
			OpenOrders: []OpenOrder{
				{
					ClientOrderID: "01H...", ExchangeOrderID: "42",
					Symbol: "BTCUSDT", Side: "buy", OrderType: "limit",
					QuantityDecimal: "0.001", FilledQuantityDecimal: "0.0003",
					LimitPriceDecimal: "65000.00", Status: "partial_filled",
					PlacedAtMs: 1700000000000,
				},
			},
			SinceLastFills: []Fill{
				{
					ClientOrderID: "01H...", ExchangeOrderID: "41",
					FillQuantityDecimal: "0.0005", FillPriceDecimal: "64900.00",
					FillFeeAsset: "BNB", FillFeeAmountDecimal: "0.00012",
					FilledAtExchangeMs: 1699999990000, ActualSlippageBps: 3.2,
				},
			},
			LastSeenMsgID: "01H...",
		}},
		{"trade_command", TypeTradeCommand, TradeCommand{
			IntentKind: IntentKindMacro, ClientOrderID: "01H...", InstanceID: "01H...",
			Symbol: "BTCUSDT", Side: "buy", OrderType: "market",
			QuantityDecimal: "0.001", ValidUntilMs: 1700000060000, NowMsAtSaaS: 1700000000000,
		}},
		{"ack", TypeAck, Ack{
			ClientOrderID: "01H...", Status: AckStatusAccepted,
			ExchangeOrderID: "42", ExchangeNowMs: 1700000000050,
		}},
		{"order_update", TypeOrderUpdate, OrderUpdate{
			ClientOrderID: "01H...", ExchangeOrderID: "42", Status: OrderStatusFilled,
			Fills: []Fill{{
				FillQuantityDecimal: "0.001", FillPriceDecimal: "65010.00",
				FillFeeAsset: "BNB", FillFeeAmountDecimal: "0.00012",
				FilledAtExchangeMs: 1700000000123, ActualSlippageBps: 1.54,
			}},
			CumulativeFilledQuantityDecimal: "0.001",
		}},
		{"delta_report", TypeDeltaReport, DeltaReport{
			ReportedAtMs: 1700000000000,
			Positions:    []Position{{Symbol: "BTC", FreeDecimal: "0.5", LockedDecimal: "0.0"}},
			SinceLastReport: DeltaReportSince{
				Fills:  []Fill{},
				Errors: []AgentError{{Code: "rate_limit", Message: "x", OccurredAtMs: 1}},
			},
		}},
		{"ping", TypePing, Ping{ServerNowMs: 1700000000000}},
		{"pong", TypePong, Pong{EchoMsgID: "01H...", AgentNowMs: 1700000000010, ExchangeReachable: true}},
		{"kill_switch", TypeKillSwitch, KillSwitch{
			Reason: KillSwitchManualAdminAction, OperatorUserID: "01H...",
			Scope: KillSwitchScopeAll, Symbol: "",
		}},
		{"graceful_shutdown", TypeGracefulShutdown, GracefulShutdown{
			Reason: GracefulShutdownSaaSRestart, RetryInMs: 5000,
		}},
		{"error", TypeError, Error{
			Code: ErrorCodeSchemaMismatch, Message: "x", RefMsgID: "01H...",
		}},
	}

	if len(cases) != 16 {
		t.Fatalf("round-trip table must cover all 16 types, got %d", len(cases))
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := EncodeMessage(c.msgType, "01H000000000000000000000AA",
				1700000000000, "01H000000000000000000ACCT", c.payload)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}

			env, err := DecodeEnvelope(raw)
			if err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if env.Type != c.msgType {
				t.Errorf("type: got %q want %q", env.Type, c.msgType)
			}

			// Decode payload back into a fresh value of the same type
			// and compare against the encoded value via JSON.
			origJSON, _ := json.Marshal(c.payload)
			if string(env.Payload) != string(origJSON) {
				t.Errorf("payload bytes differ:\n got: %s\nwant: %s", env.Payload, origJSON)
			}
		})
	}
}

// TestDecodePayloadTypedHelper exercises the generic DecodePayload helper
// for one happy path and one decode-failure path.
func TestDecodePayloadTypedHelper(t *testing.T) {
	raw, err := EncodeMessage(TypeTradeCommand, "01H000000000000000000000AA",
		1, "01H000000000000000000ACCT", TradeCommand{
			ClientOrderID:   "X",
			Symbol:          "BTCUSDT",
			Side:            "buy",
			OrderType:       "market",
			QuantityDecimal: "0.1",
		})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	env, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	tc, err := DecodePayload[TradeCommand](env)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if tc.ClientOrderID != "X" || tc.Symbol != "BTCUSDT" {
		t.Errorf("decoded payload mismatch: %+v", tc)
	}

	// Decode-failure: payload that doesn't match the target type. JSON
	// is permissive about extra fields, so we use a type that has a
	// required-shape mismatch: feed a number where a struct is expected.
	bad := Envelope{
		MsgID:         "01H...",
		Type:          TypeTradeCommand,
		SchemaVersion: SchemaVersion,
		TimestampMs:   1,
		Payload:       json.RawMessage(`42`),
	}
	if _, err := DecodePayload[TradeCommand](bad); !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("got %v, want ErrDecodeFailed", err)
	}
}

// TestLimitPriceOmitempty pins the omitempty behavior for market orders.
// The protocol doc §5.8 specifies absent (not "0") for market orders.
func TestLimitPriceOmitempty(t *testing.T) {
	market := TradeCommand{
		ClientOrderID:   "X",
		Symbol:          "BTCUSDT",
		Side:            "buy",
		OrderType:       "market",
		QuantityDecimal: "0.1",
	}
	b, err := json.Marshal(market)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "limit_price_decimal") {
		t.Errorf("market order serialized with limit_price_decimal: %s", b)
	}

	limit := market
	limit.OrderType = "limit"
	limit.LimitPriceDecimal = "65000.00"
	b2, _ := json.Marshal(limit)
	if !strings.Contains(string(b2), `"limit_price_decimal":"65000.00"`) {
		t.Errorf("limit order missing price: %s", b2)
	}
}
