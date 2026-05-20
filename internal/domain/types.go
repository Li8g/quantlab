// Package domain defines the core runtime types shared between the engine
// layer and the strategy layer. These types are not part of the external
// JSON boundary (see internal/resultpkg for that); they live in-process.
package domain

import "quantlab/internal/resultpkg"

// Gene is an abstract fixed-length parameter vector. The engine layer must
// not inspect individual elements — all field semantics live in the strategy.
type Gene []float64

// SegmentInfo describes one semantically coupled gene block. The strategy
// returns the complete set via Segments(); the engine uses it for block-wise
// crossover, Fingerprint quantization, and per-dimension mutation step sizes.
//
// Invariants:
//   - len(Dimensions) == len(QuantizationStep) == len(GeneStep)
//   - Each gene dimension appears in exactly one Segment.
//   - Segments() must return the same slice in the same order throughout the
//     strategy instance's lifetime.
type SegmentInfo struct {
	Name             string
	Dimensions       []int     // gene indices in this segment
	QuantizationStep []float64 // per-dimension Fingerprint quantization precision
	GeneStep         []float64 // per-dimension Mutate step size
	IsCritical       bool      // participates in OAT neighbourhood-stability tests
	Description      string
}

// Bar is a single OHLCV candlestick with optional gap metadata.
//
// ⚠️  IsGap and GapType are metadata fields that DO NOT enter bars_hash.
// Changing the gap-detection algorithm must not invalidate bars_hash.
// The exact bars_hash serialization contract is frozen in
// internal/quant/canonical_json.go (package-level comment).
type Bar struct {
	OpenTime int64   `json:"open_time"`
	Open     float64 `json:"open"`
	High     float64 `json:"high"`
	Low      float64 `json:"low"`
	Close    float64 `json:"close"`
	Volume   float64 `json:"volume"`

	// Gap metadata — excluded from bars_hash; see canonical_json.go.
	IsGap   bool   `json:"is_gap,omitempty"`
	GapType string `json:"gap_type,omitempty"`
}

// CrucibleWindow is a single evaluation window handed to Adapter.Evaluate.
// Bars includes the warmup prefix; WarmupLen marks where evaluation begins.
type CrucibleWindow struct {
	Name      resultpkg.WindowName `json:"name"`
	StartTS   int64                `json:"start_ts"`   // UTC ms, inclusive
	EndTS     int64                `json:"end_ts"`     // UTC ms, inclusive
	WarmupLen int                  `json:"warmup_len"` // bars before evaluation start
	Bars      []Bar                `json:"bars"`
}

// DCABaseline is the reference return for one DCA strategy variant.
type DCABaseline struct {
	FinalEquity   float64 `json:"final_equity"`
	TotalInjected float64 `json:"total_injected"`
	MaxDrawdown   float64 `json:"max_drawdown"`
}

// DCABaselines groups the two DCA reference baselines used per window.
type DCABaselines struct {
	Monthly DCABaseline `json:"monthly"`
	Weekly  DCABaseline `json:"weekly"`
}

// FrictionParams are the effective trading frictions in use during evaluation.
// Sourced from EvaluablePlan; strategy must never hardcode these values.
type FrictionParams struct {
	TakerFeeBPS float64 `json:"taker_fee_bps"`
	SlippageBPS float64 `json:"slippage_bps"`
}

// AggregateCache is a pure-memory in-process performance cache.
// It is excluded from plan_hash via json:"-" (regression-pinned by
// TestPlanHashExcludesAggregateCache in internal/quant).
// Workers may retain it across Adapter.Reset; strategy must not persist it
// beyond Adapter.Close.
//
// Intentionally empty: the fitness package landed without needing any
// memoization. Add fields here when a real reuse opportunity appears.
type AggregateCache struct{}

// EvaluablePlan is the per-Epoch read-only evaluation context. It is
// constructed before workers start; all workers share one plan instance.
// Adapter.Reset(plan) is called before every Evaluate call.
//
// plan_hash is SHA256(canonical_json(EvaluablePlan)), computed by
// internal/quant.PlanHash. AggregateCache is excluded via json:"-".
//
// InitialUSDT is the per-CrucibleWindow cold-start cash position used
// by strategy simulators (sigmoid_v1, etc.). Strategies that don't
// model a USDT-quoted portfolio may ignore it. Wired through the
// request → Defaults → PlanOptions chain.
type EvaluablePlan struct {
	Pair           string                      `json:"pair"`
	Spawn          resultpkg.SpawnPointPayload `json:"spawn"`
	LotStep        float64                     `json:"lot_step"`
	LotMin         float64                     `json:"lot_min"`
	FatalMDD       float64                     `json:"fatal_mdd"`
	InitialUSDT    float64                     `json:"initial_usdt"`
	Windows        []CrucibleWindow            `json:"windows"`
	DCABaselines   DCABaselines                `json:"dca_baselines"`
	OosWindow      *CrucibleWindow             `json:"oos_window,omitempty"`
	Friction       FrictionParams              `json:"friction"`
	AggregateCache AggregateCache              `json:"-"`
}
