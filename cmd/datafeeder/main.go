// datafeeder: CLI for importing Binance public K-line archives into the
// local Postgres + TimescaleDB instance and inspecting coverage.
//
// Subcommands (phase plan §五):
//
//	datafeeder import --symbol BTCUSDT --interval 1m \
//	                  --from 2025-01-01 --to 2025-01-07
//	datafeeder verify --symbol BTCUSDT --interval 1m
//	datafeeder stats
//
// All flags use date strings in YYYY-MM-DD UTC. --from is inclusive at
// 00:00:00 UTC; --to is inclusive at 23:59:59.999 UTC of that day.
//
// Configuration: reads config.yaml from --config (default ./config.yaml
// or CONFIG_PATH env var). Database credentials and the binance archive
// base URL come from there.
//
// Subcommand parsing uses the stdlib flag package with one FlagSet per
// subcommand — simple, no third-party CLI deps. If we ever need rich
// features (subcommand help, completion), swap to cobra.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"gorm.io/gorm"

	"quantlab/internal/data"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var err error
	switch sub {
	case "import":
		err = runImport(ctx, args)
	case "verify":
		err = runVerify(ctx, args)
	case "stats":
		err = runStats(ctx, args)
	case "last-bar":
		err = runLastBar(ctx, args)
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "datafeeder: unknown subcommand %q\n\n", sub)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "datafeeder: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `datafeeder — import / verify / inspect K-line archives

Usage:
  datafeeder import   --symbol BTCUSDT --interval 1m --from 2025-01-01 --to 2025-01-07
  datafeeder verify   --symbol BTCUSDT --interval 1m
  datafeeder stats
  datafeeder last-bar --symbol BTCUSDT --interval 1m

Common flags:
  --config PATH    config.yaml path (default ./config.yaml or $CONFIG_PATH)
`)
}

func runImport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	cfgPath := fs.String("config", "", "config.yaml path")
	symbol := fs.String("symbol", "", "instrument symbol, e.g. BTCUSDT")
	interval := fs.String("interval", "1m", "kline interval (1m, 5m, 1h, 1d, …)")
	from := fs.String("from", "", "inclusive start date, YYYY-MM-DD (UTC)")
	to := fs.String("to", "", "inclusive end date, YYYY-MM-DD (UTC)")
	_ = fs.Parse(args)

	if *symbol == "" || *from == "" || *to == "" {
		return fmt.Errorf("import: --symbol, --from, --to are required")
	}
	startTS, err := parseDateStartUTC(*from)
	if err != nil {
		return fmt.Errorf("import: --from: %w", err)
	}
	endTS, err := parseDateEndUTC(*to)
	if err != nil {
		return fmt.Errorf("import: --to: %w", err)
	}

	db, err := openDB(ctx, *cfgPath)
	if err != nil {
		return err
	}
	defer closeDB(db)

	orch := data.NewOrchestrator(db)
	orch.Logger = newLogger()

	summary, err := orch.ImportSymbol(ctx, *symbol, *interval, startTS, endTS)
	if err != nil {
		// Print partial summary even on failure — useful for debugging
		// "got 3 of 5 months then died".
		if summary != nil {
			printSummary(summary)
		}
		return err
	}
	printSummary(summary)
	return nil
}

func runVerify(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	cfgPath := fs.String("config", "", "config.yaml path")
	symbol := fs.String("symbol", "", "instrument symbol, e.g. BTCUSDT")
	interval := fs.String("interval", "1m", "kline interval")
	_ = fs.Parse(args)

	if *symbol == "" {
		return fmt.Errorf("verify: --symbol required")
	}

	db, err := openDB(ctx, *cfgPath)
	if err != nil {
		return err
	}
	defer closeDB(db)

	type kRow struct {
		Min, Max int64
		Count    int64
	}
	var k kRow
	if err := db.WithContext(ctx).Model(&store.KLine{}).
		Select("COALESCE(MIN(open_time),0) AS min, COALESCE(MAX(open_time),0) AS max, COUNT(*) AS count").
		Where("symbol = ? AND interval = ?", *symbol, *interval).
		Scan(&k).Error; err != nil {
		return fmt.Errorf("verify: query klines: %w", err)
	}

	var gaps []store.KLineGap
	if err := db.WithContext(ctx).
		Where("symbol = ? AND interval = ?", *symbol, *interval).
		Order("gap_start_ms ASC").Find(&gaps).Error; err != nil {
		return fmt.Errorf("verify: query gaps: %w", err)
	}

	fmt.Printf("symbol:   %s\ninterval: %s\nrows:     %d\n", *symbol, *interval, k.Count)
	if k.Count > 0 {
		fmt.Printf("range:    %s → %s (UTC)\n",
			time.UnixMilli(k.Min).UTC().Format(time.RFC3339),
			time.UnixMilli(k.Max).UTC().Format(time.RFC3339))
	}
	fmt.Printf("gaps:     %d\n", len(gaps))
	for i, g := range gaps {
		if i >= 10 {
			fmt.Printf("  … and %d more\n", len(gaps)-10)
			break
		}
		fmt.Printf("  [%s → %s]\n",
			time.UnixMilli(g.GapStartMs).UTC().Format(time.RFC3339),
			time.UnixMilli(g.GapEndMs).UTC().Format(time.RFC3339))
	}
	return nil
}

func runStats(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	cfgPath := fs.String("config", "", "config.yaml path")
	_ = fs.Parse(args)

	db, err := openDB(ctx, *cfgPath)
	if err != nil {
		return err
	}
	defer closeDB(db)

	type sRow struct {
		Symbol   string
		Interval string
		Count    int64
		Min      int64
		Max      int64
	}
	var rows []sRow
	if err := db.WithContext(ctx).Raw(`
        SELECT symbol, interval,
               COUNT(*) AS count,
               COALESCE(MIN(open_time), 0) AS min,
               COALESCE(MAX(open_time), 0) AS max
        FROM klines
        GROUP BY symbol, interval
        ORDER BY symbol, interval
    `).Scan(&rows).Error; err != nil {
		return fmt.Errorf("stats: query: %w", err)
	}
	if len(rows) == 0 {
		fmt.Println("no klines imported yet")
		return nil
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Symbol != rows[j].Symbol {
			return rows[i].Symbol < rows[j].Symbol
		}
		return rows[i].Interval < rows[j].Interval
	})
	fmt.Printf("%-12s %-6s %-12s %-25s %-25s\n", "SYMBOL", "INT", "ROWS", "FIRST (UTC)", "LAST (UTC)")
	for _, r := range rows {
		fmt.Printf("%-12s %-6s %-12d %-25s %-25s\n",
			r.Symbol, r.Interval, r.Count,
			time.UnixMilli(r.Min).UTC().Format(time.RFC3339),
			time.UnixMilli(r.Max).UTC().Format(time.RFC3339))
	}
	return nil
}

func runLastBar(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("last-bar", flag.ExitOnError)
	cfgPath := fs.String("config", "", "config.yaml path")
	symbol := fs.String("symbol", "", "instrument symbol, e.g. BTCUSDT")
	interval := fs.String("interval", "1m", "kline interval")
	_ = fs.Parse(args)

	if *symbol == "" {
		return fmt.Errorf("last-bar: --symbol required")
	}

	db, err := openDB(ctx, *cfgPath)
	if err != nil {
		return err
	}
	defer closeDB(db)

	var maxTime int64
	if err := db.WithContext(ctx).Model(&store.KLine{}).
		Select("COALESCE(MAX(open_time), 0)").
		Where("symbol = ? AND interval = ?", *symbol, *interval).
		Scan(&maxTime).Error; err != nil {
		return fmt.Errorf("last-bar: query: %w", err)
	}
	if maxTime == 0 {
		return fmt.Errorf("last-bar: no klines found for %s/%s — run an initial import first", *symbol, *interval)
	}

	fmt.Println(time.UnixMilli(maxTime).UTC().Format("2006-01-02"))
	return nil
}

// ---- helpers ----

func openDB(ctx context.Context, cfgPath string) (*gorm.DB, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	gdb, err := store.NewDB(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	return gdb, nil
}

func closeDB(db *gorm.DB) {
	sqlDB, err := db.DB()
	if err != nil {
		return
	}
	_ = sqlDB.Close()
}

func parseDateStartUTC(s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected YYYY-MM-DD: %w", err)
	}
	return t, nil
}

func parseDateEndUTC(s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected YYYY-MM-DD: %w", err)
	}
	return t.Add(24*time.Hour - time.Millisecond), nil
}

func printSummary(s *data.ImportSummary) {
	fmt.Println("import summary")
	fmt.Printf("  symbol:   %s\n", s.Symbol)
	fmt.Printf("  interval: %s\n", s.Interval)
	fmt.Printf("  range:    %s → %s (UTC)\n",
		time.UnixMilli(s.StartMs).UTC().Format(time.RFC3339),
		time.UnixMilli(s.EndMs).UTC().Format(time.RFC3339))
	fmt.Printf("  months:   %d\n", s.MonthsFetched)
	fmt.Printf("  inserted: %d rows\n", s.RowsInserted)
	fmt.Printf("  gaps:     %d\n", s.GapsDetected)
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
