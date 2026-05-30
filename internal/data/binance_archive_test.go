package data

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// buildKlineZip wraps csvContent in a single-file zip named like a real
// Binance archive — sufficient for ParseKlineCSV round-trips.
func buildKlineZip(t *testing.T, csvFilename, csvContent string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(csvFilename)
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte(csvContent)); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestMonthlyKlineURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
	}{
		{"no trailing slash", "https://data.binance.vision"},
		{"trailing slash trimmed", "https://data.binance.vision/"},
	}
	want := "https://data.binance.vision/data/spot/monthly/klines/BTCUSDT/1m/BTCUSDT-1m-2025-01.zip"
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MonthlyKlineURL(c.baseURL, "BTCUSDT", "1m", 2025, 1)
			if got != want {
				t.Errorf("got %s, want %s", got, want)
			}
		})
	}
}

func TestMonthlyKlineURL_ZeroPads(t *testing.T) {
	// Single-digit month must zero-pad to "01"–"09".
	got := MonthlyKlineURL("https://x", "BTCUSDT", "1m", 2025, 3)
	want := "https://x/data/spot/monthly/klines/BTCUSDT/1m/BTCUSDT-1m-2025-03.zip"
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestDailyKlineURL(t *testing.T) {
	date := time.Date(2025, 1, 15, 14, 30, 0, 0, time.UTC) // time-of-day irrelevant
	got := DailyKlineURL("https://data.binance.vision", "BTCUSDT", "1m", date)
	want := "https://data.binance.vision/data/spot/daily/klines/BTCUSDT/1m/BTCUSDT-1m-2025-01-15.zip"
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestChecksumURL(t *testing.T) {
	got := ChecksumURL("https://x/y.zip")
	if got != "https://x/y.zip.CHECKSUM" {
		t.Errorf("got %s", got)
	}
}

func TestVerifyChecksum_Pass(t *testing.T) {
	data := []byte("hello world")
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	if err := VerifyChecksum(data, hexSum); err != nil {
		t.Errorf("lowercase exact: %v", err)
	}
	if err := VerifyChecksum(data, strings.ToUpper(hexSum)); err != nil {
		t.Errorf("uppercase: %v", err)
	}
	if err := VerifyChecksum(data, "  "+hexSum+"  "); err != nil {
		t.Errorf("with surrounding whitespace: %v", err)
	}
}

func TestVerifyChecksum_Fail(t *testing.T) {
	err := VerifyChecksum([]byte("hello"), "0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Error("expected mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error message = %q, want 'checksum mismatch' substring", err.Error())
	}
}

func TestParseChecksumFile_StandardFormat(t *testing.T) {
	// Format observed in the wild: "<hex>  <filename>\n".
	body := []byte("b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9  BTCUSDT-1m-2025-01.zip\n")
	got, err := ParseChecksumFile(body)
	if err != nil {
		t.Fatal(err)
	}
	if got != "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9" {
		t.Errorf("got %s", got)
	}
}

func TestParseChecksumFile_CaseNormalized(t *testing.T) {
	body := []byte("B94D27B9934D3E08A52E52D7DA7DABFAC484EFE37A5380EE9088F7ACE2EFCDE9\n")
	got, err := ParseChecksumFile(body)
	if err != nil {
		t.Fatal(err)
	}
	if got != "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9" {
		t.Errorf("got %s, want lowercased", got)
	}
}

func TestParseChecksumFile_NoHashRejected(t *testing.T) {
	if _, err := ParseChecksumFile([]byte("no hash in here")); err == nil {
		t.Error("expected error for no-hash content")
	}
}

func TestParseKlineCSV_HappyPath(t *testing.T) {
	// Two real-shape rows (12 cols incl. the ignored trailing zero).
	csvContent := `1736294400000,100000.0,100100.0,99900.0,100050.0,5.123,1736294459999,512345.6,42,3.0,300000.0,0
1736294460000,100050.0,100200.0,100000.0,100150.0,4.500,1736294519999,450500.0,38,2.5,250000.0,0
`
	zipBytes := buildKlineZip(t, "BTCUSDT-1m-2025-01.csv", csvContent)
	rows, err := ParseKlineCSV(zipBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	r0 := rows[0]
	if r0.OpenTime != 1736294400000 || r0.CloseTime != 1736294459999 {
		t.Errorf("row0 times: open=%d close=%d", r0.OpenTime, r0.CloseTime)
	}
	if r0.Open != 100000.0 || r0.High != 100100.0 || r0.Low != 99900.0 || r0.Close != 100050.0 {
		t.Errorf("row0 OHLC mismatch: %+v", r0)
	}
	if r0.Volume != 5.123 || r0.QuoteVolume != 512345.6 {
		t.Errorf("row0 volumes: vol=%g qv=%g", r0.Volume, r0.QuoteVolume)
	}
	if r0.NumTrades != 42 {
		t.Errorf("row0 NumTrades = %d", r0.NumTrades)
	}
	if r0.TakerBuyBase != 3.0 || r0.TakerBuyQuote != 300000.0 {
		t.Errorf("row0 taker-buys: %g / %g", r0.TakerBuyBase, r0.TakerBuyQuote)
	}
}

func TestParseKlineCSV_NormalizesMicrosecondTimestamps(t *testing.T) {
	// Observed in 2025-01 BTCUSDT archives: OpenTime/CloseTime in μs.
	// Parser must normalize to ms so downstream code (DB, crucible) sees
	// a single unit.
	csvContent := `1735689600000000,93576.0,93610.93,93537.5,93610.93,8.21827,1735689659999999,768978.75,2631,3.95,369757.32,0
`
	rows, err := ParseKlineCSV(buildKlineZip(t, "x.csv", csvContent))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	r := rows[0]
	if r.OpenTime != 1735689600000 { // ms for 2025-01-01T00:00:00Z
		t.Errorf("OpenTime not normalized: got %d", r.OpenTime)
	}
	if r.CloseTime != 1735689659999 {
		t.Errorf("CloseTime not normalized: got %d", r.CloseTime)
	}
}

func TestParseKlineCSV_ToleratesExtraColumns(t *testing.T) {
	// Imagine Binance adds a 13th column in the future — must not break.
	csvContent := `1736294400000,100000.0,100100.0,99900.0,100050.0,5.123,1736294459999,512345.6,42,3.0,300000.0,0,extra_field
`
	if _, err := ParseKlineCSV(buildKlineZip(t, "x.csv", csvContent)); err != nil {
		t.Errorf("should tolerate 13 cols, got %v", err)
	}
}

func TestParseKlineCSV_Rejects(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want string
	}{
		{"not a zip", []byte("definitely not a zip"), "open zip"},
		{"empty body", []byte{}, "open zip"},
		{"multi-file zip", func() []byte {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			_, _ = zw.Create("a.csv")
			_, _ = zw.Create("b.csv")
			_ = zw.Close()
			return buf.Bytes()
		}(), "want exactly 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseKlineCSV(c.body)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %q, want substring %q", err.Error(), c.want)
			}
		})
	}
}

func TestParseKlineCSV_TooFewColumns(t *testing.T) {
	csvContent := "1736294400000,100000.0,100100.0\n"
	_, err := ParseKlineCSV(buildKlineZip(t, "x.csv", csvContent))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "need >= 11") {
		t.Errorf("err = %q", err.Error())
	}
}

func TestParseKlineCSV_BadNumericRejected(t *testing.T) {
	csvContent := "NOT_A_NUMBER,100000.0,100100.0,99900.0,100050.0,5.123,1736294459999,512345.6,42,3.0,300000.0,0\n"
	if _, err := ParseKlineCSV(buildKlineZip(t, "x.csv", csvContent)); err == nil {
		t.Error("expected error for non-numeric open_time")
	}
}

// TestArchiveClient_FullRoundTrip wires URL → download → checksum verify
// → CSV parse against an in-process httptest server. This is the closest
// we get to a real Binance fetch without leaving the sandbox.
func TestArchiveClient_FullRoundTrip(t *testing.T) {
	csvContent := `1736294400000,100000.0,100100.0,99900.0,100050.0,5.123,1736294459999,512345.6,42,3.0,300000.0,0
`
	zipBytes := buildKlineZip(t, "BTCUSDT-1m-2025-01.csv", csvContent)
	sum := sha256.Sum256(zipBytes)
	sumHex := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/data/spot/monthly/klines/BTCUSDT/1m/BTCUSDT-1m-2025-01.zip",
		func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(zipBytes) })
	mux.HandleFunc("/data/spot/monthly/klines/BTCUSDT/1m/BTCUSDT-1m-2025-01.zip.CHECKSUM",
		func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s  BTCUSDT-1m-2025-01.zip\n", sumHex)
		})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewArchiveClient()
	c.BaseURL = srv.URL
	ctx := context.Background()

	body, err := c.DownloadMonthly(ctx, "BTCUSDT", "1m", 2025, 1)
	if err != nil {
		t.Fatalf("DownloadMonthly: %v", err)
	}
	if !bytes.Equal(body, zipBytes) {
		t.Error("downloaded bytes != served bytes")
	}

	archiveURL := MonthlyKlineURL(srv.URL, "BTCUSDT", "1m", 2025, 1)
	cs, err := c.DownloadChecksum(ctx, archiveURL)
	if err != nil {
		t.Fatalf("DownloadChecksum: %v", err)
	}
	if cs != sumHex {
		t.Errorf("got %s, want %s", cs, sumHex)
	}
	if err := VerifyChecksum(body, cs); err != nil {
		t.Errorf("verify: %v", err)
	}

	rows, err := ParseKlineCSV(body)
	if err != nil {
		t.Fatalf("ParseKlineCSV: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("got %d rows, want 1", len(rows))
	}
}

func TestArchiveClient_DownloadDaily(t *testing.T) {
	date := time.Date(2025, 3, 15, 0, 0, 0, 0, time.UTC)
	expectedPath := "/data/spot/daily/klines/BTCUSDT/1m/BTCUSDT-1m-2025-03-15.zip"

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != expectedPath {
			t.Errorf("unexpected path %s, want %s", r.URL.Path, expectedPath)
		}
		called = true
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()

	c := NewArchiveClient()
	c.BaseURL = srv.URL
	if _, err := c.DownloadDaily(context.Background(), "BTCUSDT", "1m", date); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("server handler was not invoked")
	}
}

func TestArchiveClient_404Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := NewArchiveClient()
	c.BaseURL = srv.URL
	_, err := c.DownloadMonthly(context.Background(), "BTCUSDT", "1m", 2025, 1)
	if err == nil {
		t.Fatal("expected 404 error")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("err = %q, want 'HTTP 404'", err.Error())
	}
	if !errors.Is(err, ErrArchiveNotFound) {
		t.Errorf("err = %v, want errors.Is ErrArchiveNotFound (orchestrator's API-fallback trigger)", err)
	}
}

func TestArchiveClient_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()
	c := NewArchiveClient()
	c.BaseURL = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.DownloadMonthly(ctx, "BTCUSDT", "1m", 2025, 1); err == nil {
		t.Error("expected context-cancel error")
	}
}
