//go:build integration

// Integration test for KLineRepo.LatestClose against a live Postgres
// instance. Mirror of challenger_integration_test.go. Run with:
//
//	go test -tags=integration ./internal/repository/ \
//	    -args -config=/absolute/path/to/config.yaml
//
// reuses the configPath flag defined in challenger_integration_test.go.
package repository

import (
	"context"
	"testing"

	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

func TestKLineRepo_LatestClose(t *testing.T) {
	cfg, err := config.Load(*configPath)
	if err != nil {
		t.Fatalf("load config %s: %v", *configPath, err)
	}
	db, err := store.NewDB(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	repo := NewKLineRepo(db)
	ctx := context.Background()

	const symbol = "KLITESTUSDT" // synthetic symbol, won't collide with real data
	const interval = "1m"

	cleanup := func() { _ = db.Where("symbol = ?", symbol).Delete(&store.KLine{}).Error }
	cleanup()
	t.Cleanup(cleanup)

	// No bar yet → (nil, nil), not an error.
	got, err := repo.LatestClose(ctx, symbol, interval)
	if err != nil {
		t.Fatalf("LatestClose (empty): %v", err)
	}
	if got != nil {
		t.Errorf("empty: got %+v, want nil", got)
	}

	// Insert three bars out of time order; LatestClose must return the
	// one with the greatest open_time (300), not the last inserted.
	bars := []store.KLine{
		{Symbol: symbol, Interval: interval, OpenTime: 300, Close: 60300},
		{Symbol: symbol, Interval: interval, OpenTime: 100, Close: 60100},
		{Symbol: symbol, Interval: interval, OpenTime: 200, Close: 60200},
	}
	if err := db.Create(&bars).Error; err != nil {
		t.Fatalf("seed klines: %v", err)
	}

	got, err = repo.LatestClose(ctx, symbol, interval)
	if err != nil {
		t.Fatalf("LatestClose: %v", err)
	}
	if got == nil {
		t.Fatalf("LatestClose: got nil, want bar at open_time 300")
	}
	if got.OpenTime != 300 || got.Close != 60300 {
		t.Errorf("LatestClose = (open=%d, close=%v), want (300, 60300)", got.OpenTime, got.Close)
	}

	// A different interval for the same symbol must not leak in.
	if other, err := repo.LatestClose(ctx, symbol, "5m"); err != nil {
		t.Fatalf("LatestClose (other interval): %v", err)
	} else if other != nil {
		t.Errorf("interval 5m: got %+v, want nil", other)
	}
}
