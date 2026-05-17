// Package quant provides stateless mathematical utilities shared by the
// engine and strategy layers via domain types. No goroutines; all functions
// are pure and deterministic.
//
// # bars_hash serialization contract — FROZEN v5.4.1 P12
//
// bars_hash = lower-hex SHA256(json.Marshal([]barForHash))
//
// barForHash includes ONLY these Bar fields, in this exact order:
//
//	open_time  int64   (UTC milliseconds)
//	open       float64
//	high       float64
//	low        float64
//	close      float64
//	volume     float64
//
// Bar.IsGap and Bar.GapType are DELIBERATELY EXCLUDED. Gap-detection
// algorithm upgrades (which change only metadata) must not invalidate the
// reproducibility guarantee encoded in bars_hash.
//
// Any change to the set of included fields, their json tag names, or their
// order constitutes a breaking change requiring a bars_hash version bump.
// The corresponding test is TestBarsHashExcludesMetadata.
//
// # plan_hash serialization contract
//
// plan_hash = lower-hex SHA256(json.Marshal(*EvaluablePlan))
//
// AggregateCache is excluded via json:"-". All json.RawMessage fields
// (e.g. SpawnPointPayload.RiskBounds) are taken verbatim — callers must
// pre-normalize them to canonical JSON if cross-process reproducibility is
// required (the engine does this when building EvaluablePlan).
package quant

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"quantlab/internal/domain"
)

// barForHash is the frozen serialization struct for bars_hash.
// Field names and order are part of the v5.4.1 P12 contract (see above).
type barForHash struct {
	OpenTime int64   `json:"open_time"`
	Open     float64 `json:"open"`
	High     float64 `json:"high"`
	Low      float64 `json:"low"`
	Close    float64 `json:"close"`
	Volume   float64 `json:"volume"`
}

// BarsHash returns the lower-hex SHA256 bars_hash for the given bar slice.
// IsGap and GapType are excluded per the frozen contract above.
func BarsHash(bars []domain.Bar) (string, error) {
	subset := make([]barForHash, len(bars))
	for i, b := range bars {
		subset[i] = barForHash{
			OpenTime: b.OpenTime,
			Open:     b.Open,
			High:     b.High,
			Low:      b.Low,
			Close:    b.Close,
			Volume:   b.Volume,
		}
	}
	data, err := json.Marshal(subset)
	if err != nil {
		return "", fmt.Errorf("quant.BarsHash: %w", err)
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}

// PlanHash returns the lower-hex SHA256 plan_hash for an EvaluablePlan.
// AggregateCache is excluded via json:"-" on the struct field.
func PlanHash(plan *domain.EvaluablePlan) (string, error) {
	data, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("quant.PlanHash: %w", err)
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}
