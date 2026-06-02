package binance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// fixedNowMs returns a NowFn pinned to a known millisecond so
// signatures can be asserted byte-for-byte.
func fixedNowMs(ms int64) func() time.Time {
	return func() time.Time { return time.UnixMilli(ms) }
}

func TestWSSubscribeParams_SignsCanonicalPayload(t *testing.T) {
	const apiKey = "PUBKEY"
	const secret = "SECRET"
	const nowMs = 1_700_000_000_000
	c := NewClient(apiKey, secret, Options{NowFn: fixedNowMs(nowMs)})

	params, err := c.WSSubscribeParams()
	if err != nil {
		t.Fatalf("WSSubscribeParams: %v", err)
	}

	// Recompute the expected signature over the canonical sorted query:
	// apiKey < recvWindow < timestamp (url.Values.Encode order).
	signed := url.Values{}
	signed.Set("apiKey", apiKey)
	signed.Set("recvWindow", strconv.Itoa(DefaultRecvWindowMs))
	signed.Set("timestamp", strconv.FormatInt(nowMs, 10))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed.Encode()))
	want := hex.EncodeToString(mac.Sum(nil))

	if got := params["signature"]; got != want {
		t.Errorf("signature = %v, want %v", got, want)
	}
	if got := params["apiKey"]; got != apiKey {
		t.Errorf("apiKey = %v, want %v", got, apiKey)
	}
	if got := params["timestamp"]; got != int64(nowMs) {
		t.Errorf("timestamp = %v, want %d", got, nowMs)
	}
	if got := params["recvWindow"]; got != DefaultRecvWindowMs {
		t.Errorf("recvWindow = %v, want %d", got, DefaultRecvWindowMs)
	}
}

func TestWSSubscribeParams_AppliesServerOffset(t *testing.T) {
	const nowMs = 1_700_000_000_000
	const offset = 250
	c := NewClient("k", "s", Options{NowFn: fixedNowMs(nowMs)})
	c.SetServerOffsetMs(offset)

	params, err := c.WSSubscribeParams()
	if err != nil {
		t.Fatalf("WSSubscribeParams: %v", err)
	}
	if got := params["timestamp"]; got != int64(nowMs+offset) {
		t.Errorf("timestamp = %v, want %d (now+offset)", got, nowMs+offset)
	}
}

func TestWSSubscribeParams_ErrorsOnEmptyKey(t *testing.T) {
	if _, err := NewClient("", "s", Options{}).WSSubscribeParams(); !errors.Is(err, ErrEmptyAPIKey) {
		t.Errorf("empty key: err = %v, want ErrEmptyAPIKey", err)
	}
	if _, err := NewClient("k", "", Options{}).WSSubscribeParams(); !errors.Is(err, ErrEmptyAPISecret) {
		t.Errorf("empty secret: err = %v, want ErrEmptyAPISecret", err)
	}
}

func TestBuildSubscribeRequest_Envelope(t *testing.T) {
	c := NewClient("PUBKEY", "SECRET", Options{NowFn: fixedNowMs(1_700_000_000_000)})
	req, err := c.buildSubscribeRequest("req-1")
	if err != nil {
		t.Fatalf("buildSubscribeRequest: %v", err)
	}
	if req.ID != "req-1" {
		t.Errorf("ID = %q, want req-1", req.ID)
	}
	if req.Method != wsMethodSubscribeSignature {
		t.Errorf("Method = %q, want %q", req.Method, wsMethodSubscribeSignature)
	}
	// Round-trip through JSON to confirm the params marshal as a nested
	// object the WS API expects.
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded wsRequest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Params["apiKey"] != "PUBKEY" {
		t.Errorf("params.apiKey = %v, want PUBKEY", decoded.Params["apiKey"])
	}
	if _, ok := decoded.Params["signature"]; !ok {
		t.Error("params missing signature")
	}
}

func TestBuildSubscribeRequest_PropagatesSigningError(t *testing.T) {
	c := NewClient("", "SECRET", Options{})
	if _, err := c.buildSubscribeRequest("x"); !errors.Is(err, ErrEmptyAPIKey) {
		t.Errorf("err = %v, want ErrEmptyAPIKey", err)
	}
}

func TestNewWSRequestID_UniqueNonEmpty(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := newWSRequestID()
		if id == "" {
			t.Fatal("empty request id")
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate request id %q", id)
		}
		seen[id] = struct{}{}
	}
}
