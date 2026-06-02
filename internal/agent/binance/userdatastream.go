// userdatastream.go — Binance Spot User Data Stream subscription over
// the WebSocket API.
//
// Binance removed the listenKey REST endpoints (POST/PUT/DELETE
// /api/v3/userDataStream) on 2026-02-20; they now return HTTP 410 Gone.
// The replacement is a *signature subscription* sent on the WS API
// connection itself:
//
//	→ {"id":…,"method":"userDataStream.subscribe.signature",
//	   "params":{apiKey,timestamp,recvWindow,signature}}
//	← {"id":…,"status":200,"result":{"subscriptionId":N}}        (ack)
//	← {"subscriptionId":N,"event":{"e":"executionReport",…}}     (events)
//
// HMAC keys are sufficient here — the Ed25519-only requirement applies
// to session.logon (the authenticated-session variant), not to
// userDataStream.subscribe.signature. The inner `event` payload is the
// same shape the legacy raw stream delivered bare, so the
// executionReport decoder is unchanged.
package binance

import "encoding/json"

// wsMethodSubscribeSignature is the WS API method that opens a user
// data stream using a per-request API-key signature (no prior
// session.logon, so HMAC keys work).
const wsMethodSubscribeSignature = "userDataStream.subscribe.signature"

// wsRequest is one outbound WS API request envelope.
type wsRequest struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

// wsResponse is the ack envelope for a WS API request. Status 200 means
// success; result.subscriptionId identifies the subscription. On
// failure Binance populates `error` and a non-200 status.
type wsResponse struct {
	ID     string `json:"id"`
	Status int    `json:"status"`
	Result struct {
		SubscriptionID int64 `json:"subscriptionId"`
	} `json:"result"`
	Error *struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	} `json:"error"`
}

// wsEventEnvelope wraps a user data stream event on the WS API. The
// inner Event payload is delivered verbatim to the executionReport
// decoder. subscriptionId is absent on connection events (e.g.
// serverShutdown), hence the pointer.
type wsEventEnvelope struct {
	SubscriptionID *int64          `json:"subscriptionId"`
	Event          json.RawMessage `json:"event"`
}

// buildSubscribeRequest constructs a signed
// userDataStream.subscribe.signature request. The signature is computed
// inside Client so apiSecret never leaves it.
func (c *Client) buildSubscribeRequest(id string) (wsRequest, error) {
	params, err := c.WSSubscribeParams()
	if err != nil {
		return wsRequest{}, err
	}
	return wsRequest{
		ID:     id,
		Method: wsMethodSubscribeSignature,
		Params: params,
	}, nil
}
