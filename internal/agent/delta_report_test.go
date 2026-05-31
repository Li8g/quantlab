package agent

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"quantlab/internal/wire"
)

// readUntilDeltaReport drains frames the Agent writes (directly off the
// pipe channel, so a nested helper's t.Fatal can't fire from a non-test
// goroutine) until a delta_report arrives or the deadline passes. Other
// frame types are ignored.
func readUntilDeltaReport(t *testing.T, pc *pipeConn) wire.DeltaReport {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case frame := <-pc.serverWrites:
			env, err := wire.DecodeEnvelope(frame)
			if err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if env.Type != wire.TypeDeltaReport {
				continue
			}
			dr, err := wire.DecodePayload[wire.DeltaReport](env)
			if err != nil {
				t.Fatalf("decode delta_report: %v", err)
			}
			return *dr
		case <-deadline:
			t.Fatal("timed out waiting for delta_report")
		}
	}
}

func TestDeltaReport_CarriesPositions(t *testing.T) {
	pc := newPipeConn()
	ex := NewMockExchange(nil)
	ex.SetPosition(Position{
		Symbol: "BTC",
		Free:   decimal.RequireFromString("0.5"),
		Locked: decimal.RequireFromString("0.1"),
	})
	c := newTestClient(t, []*pipeConn{pc}, ex)
	c.deltaInterval = 20 * time.Millisecond

	cancel, _ := runClientInBg(t, c)
	defer cancel()
	runHubHandshake(t, pc, c.cfg.AccountID)

	dr := readUntilDeltaReport(t, pc)
	if len(dr.Positions) != 1 {
		t.Fatalf("positions = %d, want 1", len(dr.Positions))
	}
	p := dr.Positions[0]
	if p.Symbol != "BTC" {
		t.Errorf("symbol = %q, want BTC", p.Symbol)
	}
	// FreeDecimal/LockedDecimal are formatted strings (8dp); compare by
	// value, not exact text.
	if got := decimal.RequireFromString(p.FreeDecimal); !got.Equal(decimal.RequireFromString("0.5")) {
		t.Errorf("free = %s, want 0.5", p.FreeDecimal)
	}
	if got := decimal.RequireFromString(p.LockedDecimal); !got.Equal(decimal.RequireFromString("0.1")) {
		t.Errorf("locked = %s, want 0.1", p.LockedDecimal)
	}
}

func TestDeltaReport_DrainsBufferedFills(t *testing.T) {
	pc := newPipeConn()
	ex := NewMockExchange(nil)
	c := newTestClient(t, []*pipeConn{pc}, ex)
	c.deltaInterval = 20 * time.Millisecond

	cancel, _ := runClientInBg(t, c)
	defer cancel()
	runHubHandshake(t, pc, c.cfg.AccountID)

	// Seed a fill into the buffer as if an order had just filled.
	c.delta.addFill(wire.Fill{
		ClientOrderID:       "coid-1",
		ExchangeOrderID:     "xoid-1",
		FillQuantityDecimal: "0.25",
		FillPriceDecimal:    "30000",
		FilledAtExchangeMs:  1700000000000,
	})

	// The report that carries the fill may not be the very next frame;
	// scan reports until one has fills.
	var got wire.DeltaReport
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for a delta_report carrying the fill")
		default:
		}
		dr := readUntilDeltaReport(t, pc)
		if len(dr.SinceLastReport.Fills) > 0 {
			got = dr
			break
		}
	}

	if n := len(got.SinceLastReport.Fills); n != 1 {
		t.Fatalf("fills = %d, want 1", n)
	}
	f := got.SinceLastReport.Fills[0]
	if f.ClientOrderID != "coid-1" || f.FillQuantityDecimal != "0.25" {
		t.Errorf("fill = %+v, want coid-1 0.25", f)
	}

	// Buffer must be empty after the drain.
	fills, errs := c.delta.drain()
	if len(fills) != 0 || len(errs) != 0 {
		t.Errorf("buffer not drained: %d fills, %d errs", len(fills), len(errs))
	}
}
