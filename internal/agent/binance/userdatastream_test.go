package binance

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// newUDSRestClient is a test helper for the listenKey REST endpoints.
// Same shape as newTestClient but the handler is dispatched by method
// + path so one fixture can serve all three lifecycle calls.
type udsRESTHandler struct {
	postCalls   int
	putCalls    int
	deleteCalls int

	postReply   http.HandlerFunc
	putReply    http.HandlerFunc
	deleteReply http.HandlerFunc

	t *testing.T
}

func (h *udsRESTHandler) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v3/userDataStream" {
		h.t.Errorf("path = %q, want /api/v3/userDataStream", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	// Iron rule 3 + USER_STREAM tier: header is required, signature must NOT appear.
	if r.Header.Get("X-MBX-APIKEY") == "" {
		h.t.Errorf("X-MBX-APIKEY header missing on %s %s", r.Method, r.URL.Path)
	}
	if strings.Contains(r.URL.RawQuery, "signature=") {
		h.t.Errorf("USER_STREAM tier must NOT include signature; got query=%q", r.URL.RawQuery)
	}
	if strings.Contains(r.URL.RawQuery, "timestamp=") {
		h.t.Errorf("USER_STREAM tier must NOT include timestamp; got query=%q", r.URL.RawQuery)
	}
	switch r.Method {
	case http.MethodPost:
		h.postCalls++
		if h.postReply != nil {
			h.postReply(w, r)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"listenKey":"abc123"}`))
	case http.MethodPut:
		h.putCalls++
		if h.putReply != nil {
			h.putReply(w, r)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	case http.MethodDelete:
		h.deleteCalls++
		if h.deleteReply != nil {
			h.deleteReply(w, r)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	default:
		h.t.Errorf("unexpected method %s", r.Method)
		http.NotFound(w, r)
	}
}

func TestCreateListenKey_HappyPath(t *testing.T) {
	h := &udsRESTHandler{t: t}
	c, _ := newTestClient(t, h.handle)
	key, err := c.CreateListenKey(context.Background())
	if err != nil {
		t.Fatalf("CreateListenKey: %v", err)
	}
	if key != "abc123" {
		t.Errorf("listenKey = %q, want abc123", key)
	}
	if h.postCalls != 1 {
		t.Errorf("postCalls = %d, want 1", h.postCalls)
	}
}

func TestCreateListenKey_EmptyListenKeyError(t *testing.T) {
	h := &udsRESTHandler{
		t: t,
		postReply: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		},
	}
	c, _ := newTestClient(t, h.handle)
	_, err := c.CreateListenKey(context.Background())
	if err == nil || !strings.Contains(err.Error(), "empty listenKey") {
		t.Errorf("err = %v, want 'empty listenKey'", err)
	}
}

func TestCreateListenKey_APIError(t *testing.T) {
	h := &udsRESTHandler{
		t: t,
		postReply: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"code":-2014,"msg":"API-key format invalid."}`))
		},
	}
	c, _ := newTestClient(t, h.handle)
	_, err := c.CreateListenKey(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Code != -2014 {
		t.Errorf("APIError.Code = %d, want -2014", apiErr.Code)
	}
}

func TestKeepaliveListenKey_HappyPath(t *testing.T) {
	var gotQuery string
	h := &udsRESTHandler{
		t: t,
		putReply: func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		},
	}
	c, _ := newTestClient(t, h.handle)
	if err := c.KeepaliveListenKey(context.Background(), "xyz789"); err != nil {
		t.Fatalf("KeepaliveListenKey: %v", err)
	}
	if !strings.Contains(gotQuery, "listenKey=xyz789") {
		t.Errorf("query = %q, want listenKey=xyz789", gotQuery)
	}
	if h.putCalls != 1 {
		t.Errorf("putCalls = %d, want 1", h.putCalls)
	}
}

func TestKeepaliveListenKey_EmptyKeyError(t *testing.T) {
	c := NewClient("PUBKEY", "SECRET", Options{})
	err := c.KeepaliveListenKey(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "empty listenKey") {
		t.Errorf("err = %v, want 'empty listenKey'", err)
	}
}

func TestKeepaliveListenKey_PropagatesAPIError(t *testing.T) {
	h := &udsRESTHandler{
		t: t,
		putReply: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"code":-1125,"msg":"This listenKey does not exist."}`))
		},
	}
	c, _ := newTestClient(t, h.handle)
	err := c.KeepaliveListenKey(context.Background(), "stale")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Code != -1125 {
		t.Errorf("APIError.Code = %d, want -1125 (key not found)", apiErr.Code)
	}
}

func TestCloseListenKey_HappyPath(t *testing.T) {
	h := &udsRESTHandler{t: t}
	c, _ := newTestClient(t, h.handle)
	if err := c.CloseListenKey(context.Background(), "abc"); err != nil {
		t.Fatalf("CloseListenKey: %v", err)
	}
	if h.deleteCalls != 1 {
		t.Errorf("deleteCalls = %d, want 1", h.deleteCalls)
	}
}

func TestCloseListenKey_EmptyKeyError(t *testing.T) {
	c := NewClient("PUBKEY", "SECRET", Options{})
	err := c.CloseListenKey(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "empty listenKey") {
		t.Errorf("err = %v, want 'empty listenKey'", err)
	}
}

func TestWithAPIKey_ErrorsOnEmptyAPIKey(t *testing.T) {
	c := NewClient("", "SECRET", Options{
		BaseURL: "http://unreachable",
	})
	_, err := c.withAPIKey(context.Background(), http.MethodPost, "/api/v3/userDataStream", nil)
	if !errors.Is(err, ErrEmptyAPIKey) {
		t.Errorf("err = %v, want ErrEmptyAPIKey", err)
	}
}
