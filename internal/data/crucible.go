// Package data builds engine-side data structures from raw K-line input.
// crucible.go: BuildCrucibleWindows — slice a sorted bar series into the
// four IS windows (6m/2y/5y/10y) and an optional Anchored OOS window.
//
// References:
//   - docs/进化计算引擎.md §4.2 (window table, Span column)
//   - docs/进化计算引擎.md §4.3 (warmup rule)
//   - phase plan Coding-plan-...-v3.2.2.md Phase 5C
//
// Anchored Holdout (v3): the OOS window is the trailing oosDays of the
// full series; IS = everything before that. No embargo.
package data

import (
	"errors"
	"fmt"
	"sort"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
)

// DayMs is the number of milliseconds per UTC calendar day. Window
// lengths in days are converted to time ranges via this constant; the
// builder is therefore agnostic to bar granularity (works for 1m, 5m,
// 1h, etc., as long as bars are strictly ascending by OpenTime).
const DayMs = int64(86_400_000)

// windowSpec encodes one IS window's eval length.
//
// Resolution of the source-doc ambiguity around the 10y window
// (§4.2 says Span = "全量最长序列"; §4.3 says "评估区间前可预留最多 1200 天
// warmup"):
//
//	6m / 2y / 5y → eval = last N days of IS; warmup ≤ warmupDays before.
//	10y          → eval = entire IS minus warmup (i.e. evalDays =
//	                isDays - warmupDays); warmup = first warmupDays of IS.
//
// Skip rule is uniform: `evalDays + warmupDays > isDays` ⇒ drop window.
type windowSpec struct {
	Name     resultpkg.WindowName
	EvalDays int // 0 sentinel = "fill remaining IS minus warmup"
}

var windowSpecs = []windowSpec{
	{resultpkg.Window6M, 183},
	{resultpkg.Window2Y, 730},
	{resultpkg.Window5Y, 1825},
	{resultpkg.Window10Y, 0},
}

// BuildCrucibleWindows splits a strictly-ascending bar series into IS
// windows and an optional Anchored OOS window.
//
// Inputs:
//   - bars: full available history, sorted ascending by OpenTime, no
//     duplicate timestamps. Gap detection is NOT this function's job;
//     callers must ensure the series is gap-aware-validated upstream
//     (KLineGap surfacing lives in Phase 1.5 datafeeder).
//   - warmupDays: ≥0; the maximum warmup window to reserve before each
//     IS window's evaluation start. Per §4.3 this is capped at 1200.
//   - oosDays: if non-nil and >0, the trailing oosDays of bars become
//     OOS; IS = everything before. If nil, OOS is not constructed.
//
// Returns:
//   - is:  IS windows in cascade order (6m → 2y → 5y → 10y), with windows
//     too short to fit `evalDays + warmupDays` omitted (no renormalization
//     of weights — see fitness.AggregateScoreTotal).
//   - oos: the OOS window (nil if oosDays is nil).
//
// Future-leakage guarantees enforced:
//   - For each IS window, the last Bars[i].OpenTime ≤ IS-end timestamp.
//   - When OOS is active, every IS window's EndTS < OOS start timestamp.
//   - Warmup bars precede that window's evaluation start.
func BuildCrucibleWindows(
	bars []domain.Bar,
	warmupDays int,
	oosDays *int,
) (is []domain.CrucibleWindow, oos *domain.CrucibleWindow, err error) {
	if len(bars) == 0 {
		return nil, nil, errors.New("crucible: empty bars")
	}
	if warmupDays < 0 {
		return nil, nil, fmt.Errorf("crucible: warmupDays=%d, must be >= 0", warmupDays)
	}
	for i := 1; i < len(bars); i++ {
		if bars[i].OpenTime <= bars[i-1].OpenTime {
			return nil, nil, fmt.Errorf(
				"crucible: bars not strictly ascending at index %d (OpenTime %d <= prev %d)",
				i, bars[i].OpenTime, bars[i-1].OpenTime,
			)
		}
	}

	isEndIdx := len(bars) // exclusive upper bound on IS bars
	if oosDays != nil {
		if *oosDays <= 0 {
			return nil, nil, fmt.Errorf("crucible: oosDays=%d, must be > 0 when set", *oosDays)
		}
		lastTS := bars[len(bars)-1].OpenTime
		oosStartTS := lastTS - int64(*oosDays)*DayMs
		oosStartIdx := sort.Search(len(bars), func(i int) bool {
			return bars[i].OpenTime >= oosStartTS
		})
		if oosStartIdx == 0 {
			return nil, nil, errors.New("crucible: oos request consumes entire history")
		}

		// Anchor a warmupDays prefix immediately before OOS evaluation
		// starts so the strategy's indicators (EMAs etc.) converge
		// before scoring begins — mirrors the IS window pattern. When
		// the pre-OOS history can't accommodate full warmup we drop OOS
		// entirely (same semantics as IS "skip if evalDays+warmupDays >
		// isSpanMs"): partial warmup would produce an "ok" status with
		// silently degraded scores. verification.RunOOS surfaces this
		// as status=insufficient_data with explanatory Notes.
		oosWarmupStartTS := oosStartTS - int64(warmupDays)*DayMs
		if oosWarmupStartTS >= bars[0].OpenTime {
			oosWarmupStartIdx := sort.Search(len(bars), func(i int) bool {
				return bars[i].OpenTime >= oosWarmupStartTS
			})
			oos = &domain.CrucibleWindow{
				Name:      resultpkg.WindowOOS,
				StartTS:   bars[oosStartIdx].OpenTime, // eval-start (post-warmup)
				EndTS:     bars[len(bars)-1].OpenTime,
				WarmupLen: oosStartIdx - oosWarmupStartIdx,
				Bars:      bars[oosWarmupStartIdx:],
			}
			isEndIdx = oosStartIdx
		}
		// else: oos stays nil; IS gets the full series (isEndIdx already
		// = len(bars)). The user requested OOS but the data couldn't
		// support it without sacrificing warmup fidelity.
	}

	isBars := bars[:isEndIdx]
	if len(isBars) == 0 {
		return nil, oos, nil
	}

	warmupMs := int64(warmupDays) * DayMs
	isStartTS := isBars[0].OpenTime
	isEndTS := isBars[len(isBars)-1].OpenTime
	isSpanMs := isEndTS - isStartTS

	for _, spec := range windowSpecs {
		var evalSpanMs int64
		if spec.EvalDays > 0 {
			evalSpanMs = int64(spec.EvalDays) * DayMs
		} else {
			// 10y: eval = whatever remains after warmup.
			evalSpanMs = isSpanMs - warmupMs
		}
		if evalSpanMs <= 0 || evalSpanMs+warmupMs > isSpanMs {
			continue
		}

		evalStartTS := isEndTS - evalSpanMs
		warmupStartTS := evalStartTS - warmupMs

		warmupStartIdx := sort.Search(len(isBars), func(i int) bool {
			return isBars[i].OpenTime >= warmupStartTS
		})
		evalStartIdx := sort.Search(len(isBars), func(i int) bool {
			return isBars[i].OpenTime >= evalStartTS
		})

		winBars := isBars[warmupStartIdx:]
		warmupLen := evalStartIdx - warmupStartIdx

		is = append(is, domain.CrucibleWindow{
			Name:      spec.Name,
			StartTS:   isBars[evalStartIdx].OpenTime,
			EndTS:     isBars[len(isBars)-1].OpenTime,
			WarmupLen: warmupLen,
			Bars:      winBars,
		})
	}

	return is, oos, nil
}
