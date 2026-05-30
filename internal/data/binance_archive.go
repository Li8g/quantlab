// binance_archive.go: download, verify, and parse Binance public K-line
// archives published at data.binance.vision.
//
// Archive layout (per docs/Coding-plan-...-v3.2.2.md Phase 1.5 §二):
//
//	https://data.binance.vision/data/spot/monthly/klines/
//	    {SYMBOL}/{INTERVAL}/{SYMBOL}-{INTERVAL}-{YYYY}-{MM}.zip
//
// Daily archives use /data/spot/daily/klines/.../{SYMBOL}-{INTERVAL}-{YYYY-MM-DD}.zip.
// Each archive is accompanied by a `.CHECKSUM` file containing a sha256.
//
// Functions in this file are SIDE-EFFECT BOUNDED: pure URL/zip/CSV
// helpers + a thin ArchiveClient with an injectable *http.Client.
// Persistence and gap detection live in orchestrator.go (Phase 1.5-c).
package data

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultArchiveBaseURL is the public Binance archive endpoint.
	DefaultArchiveBaseURL = "https://data.binance.vision"

	// defaultHTTPTimeout: monthly zips for 1m intervals are < 5 MB and
	// usually download in seconds; 30s leaves headroom for the slow tail.
	defaultHTTPTimeout = 30 * time.Second

	// klineCSVMinCols: Binance K-line CSV has 11 required + 1 ignored
	// column. Older or newer archives sometimes add columns; we tolerate
	// trailing extras via csv.Reader.FieldsPerRecord=-1.
	klineCSVMinCols = 11
)

// KlineRow is one row of a Binance kline archive CSV.
// Symbol/Interval are NOT here — those come from the URL parameters and
// are stamped on by the ingest layer (orchestrator) per phase plan §四.
type KlineRow struct {
	OpenTime      int64
	Open          float64
	High          float64
	Low           float64
	Close         float64
	Volume        float64
	CloseTime     int64
	QuoteVolume   float64
	NumTrades     int32
	TakerBuyBase  float64
	TakerBuyQuote float64
}

// ArchiveClient downloads & verifies Binance archive bundles.
// Zero value is NOT ready — call NewArchiveClient.
type ArchiveClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewArchiveClient returns a client pointed at DefaultArchiveBaseURL
// with a 30-second timeout. Tests override BaseURL/HTTPClient directly.
func NewArchiveClient() *ArchiveClient {
	return &ArchiveClient{
		BaseURL:    DefaultArchiveBaseURL,
		HTTPClient: &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// MonthlyKlineURL builds the canonical archive URL for a (symbol, interval, year, month).
func MonthlyKlineURL(baseURL, symbol, interval string, year, month int) string {
	return fmt.Sprintf("%s/data/spot/monthly/klines/%s/%s/%s-%s-%04d-%02d.zip",
		strings.TrimRight(baseURL, "/"), symbol, interval, symbol, interval, year, month)
}

// DailyKlineURL builds the canonical archive URL for a (symbol, interval, date).
// The date is rendered in UTC as YYYY-MM-DD; callers must hand in a date
// already normalized to UTC.
func DailyKlineURL(baseURL, symbol, interval string, date time.Time) string {
	return fmt.Sprintf("%s/data/spot/daily/klines/%s/%s/%s-%s-%s.zip",
		strings.TrimRight(baseURL, "/"), symbol, interval, symbol, interval, date.Format("2006-01-02"))
}

// ChecksumURL appends ".CHECKSUM" to an archive URL.
func ChecksumURL(archiveURL string) string { return archiveURL + ".CHECKSUM" }

// DownloadMonthly fetches one month's klines zip body.
func (c *ArchiveClient) DownloadMonthly(ctx context.Context, symbol, interval string, year, month int) ([]byte, error) {
	return c.download(ctx, MonthlyKlineURL(c.BaseURL, symbol, interval, year, month))
}

// DownloadDaily fetches one day's klines zip body.
func (c *ArchiveClient) DownloadDaily(ctx context.Context, symbol, interval string, date time.Time) ([]byte, error) {
	return c.download(ctx, DailyKlineURL(c.BaseURL, symbol, interval, date))
}

// DownloadChecksum fetches the SHA256 hex for the given archive URL.
// Pass the archive URL (not the .CHECKSUM URL); the suffix is appended
// internally to keep callers honest about which file they verify.
func (c *ArchiveClient) DownloadChecksum(ctx context.Context, archiveURL string) (string, error) {
	body, err := c.download(ctx, ChecksumURL(archiveURL))
	if err != nil {
		return "", err
	}
	return ParseChecksumFile(body)
}

// ErrArchiveNotFound is returned (wrapped) by ArchiveClient downloads when
// the requested object does not exist yet (HTTP 404). For monthly klines
// this is the normal signal that an archive has not been published —
// Binance publishes month M's zip a few days into month M+1 — so the
// orchestrator treats it as the trigger to fall back to the REST API.
// Callers detect it with errors.Is(err, ErrArchiveNotFound).
var ErrArchiveNotFound = errors.New("archive: object not found")

func (c *ArchiveClient) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("archive: build request %s: %w", url, err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("archive: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("archive: GET %s: HTTP 404: %w", url, ErrArchiveNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive: GET %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("archive: read body %s: %w", url, err)
	}
	return body, nil
}

// VerifyChecksum compares sha256(data) against expectedSha256 (lower-hex,
// case-insensitive input).
func VerifyChecksum(data []byte, expectedSha256 string) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	want := strings.ToLower(strings.TrimSpace(expectedSha256))
	if got != want {
		return fmt.Errorf("archive: checksum mismatch: got %s, want %s", got, want)
	}
	return nil
}

// sha256HexRe matches a 64-char hex run (lowercase after ToLower normalize).
var sha256HexRe = regexp.MustCompile(`[0-9a-f]{64}`)

// ParseChecksumFile extracts the first 64-char hex SHA256 from a .CHECKSUM
// file body. Liberal parser: tolerates both "<hex>  <name>" and bare-hex
// formats. Returns the lowercase hex string.
func ParseChecksumFile(content []byte) (string, error) {
	m := sha256HexRe.Find(bytes.ToLower(content))
	if m == nil {
		return "", errors.New("archive: no 64-char SHA256 found in checksum file")
	}
	return string(m), nil
}

// ParseKlineCSV decodes the single CSV file inside a Binance kline archive
// zip and returns one KlineRow per data line. The 12th column ("ignore")
// is dropped; trailing extras are tolerated.
func ParseKlineCSV(zipData []byte) ([]KlineRow, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("archive: open zip: %w", err)
	}
	if len(zr.File) != 1 {
		return nil, fmt.Errorf("archive: zip contains %d files, want exactly 1", len(zr.File))
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		return nil, fmt.Errorf("archive: open csv %q in zip: %w", zr.File[0].Name, err)
	}
	defer rc.Close()

	cr := csv.NewReader(rc)
	cr.FieldsPerRecord = -1 // accept variable column count

	var out []KlineRow
	line := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("archive: csv read line %d: %w", line+1, err)
		}
		line++
		if len(rec) < klineCSVMinCols {
			return nil, fmt.Errorf("archive: csv line %d: have %d columns, need >= %d",
				line, len(rec), klineCSVMinCols)
		}
		row, err := parseKlineRow(rec)
		if err != nil {
			return nil, fmt.Errorf("archive: csv line %d: %w", line, err)
		}
		out = append(out, row)
	}
	return out, nil
}

func parseKlineRow(rec []string) (KlineRow, error) {
	var r KlineRow
	var err error

	parseField := func(name, raw string, target any) error {
		switch t := target.(type) {
		case *int64:
			v, e := strconv.ParseInt(raw, 10, 64)
			if e != nil {
				return fmt.Errorf("%s: %w", name, e)
			}
			*t = v
		case *float64:
			v, e := strconv.ParseFloat(raw, 64)
			if e != nil {
				return fmt.Errorf("%s: %w", name, e)
			}
			*t = v
		case *int32:
			v, e := strconv.ParseInt(raw, 10, 32)
			if e != nil {
				return fmt.Errorf("%s: %w", name, e)
			}
			*t = int32(v)
		default:
			return fmt.Errorf("%s: unsupported target type", name)
		}
		return nil
	}

	if err = parseField("open_time", rec[0], &r.OpenTime); err != nil {
		return r, err
	}
	if err = parseField("open", rec[1], &r.Open); err != nil {
		return r, err
	}
	if err = parseField("high", rec[2], &r.High); err != nil {
		return r, err
	}
	if err = parseField("low", rec[3], &r.Low); err != nil {
		return r, err
	}
	if err = parseField("close", rec[4], &r.Close); err != nil {
		return r, err
	}
	if err = parseField("volume", rec[5], &r.Volume); err != nil {
		return r, err
	}
	if err = parseField("close_time", rec[6], &r.CloseTime); err != nil {
		return r, err
	}
	if err = parseField("quote_volume", rec[7], &r.QuoteVolume); err != nil {
		return r, err
	}
	if err = parseField("num_trades", rec[8], &r.NumTrades); err != nil {
		return r, err
	}
	if err = parseField("taker_buy_base", rec[9], &r.TakerBuyBase); err != nil {
		return r, err
	}
	if err = parseField("taker_buy_quote", rec[10], &r.TakerBuyQuote); err != nil {
		return r, err
	}

	// Binance switched some monthly archives (observed in 2025-01 BTCUSDT
	// onwards) to microsecond OpenTime/CloseTime, while the REST API still
	// returns milliseconds. The rest of QuantLab (DB schema, EvaluablePlan,
	// crucible) is ms-only, so normalise here. Threshold 1e14 sits in the
	// ~1000× gap between any plausible ms (Binance era ≤ ~1.7e13) and any
	// plausible μs (Binance era ≥ ~1.5e15); good through year 2286.
	const microThresholdMs = int64(1e14)
	if r.OpenTime > microThresholdMs {
		r.OpenTime /= 1000
		r.CloseTime /= 1000
	}
	return r, nil
}
