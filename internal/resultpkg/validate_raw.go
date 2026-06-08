package resultpkg

import (
	"errors"
	"fmt"
)

// skippedBySource maps each cascade-skip cause to the WindowName that
// triggered it. Used by ValidateForIS to verify cascade consistency.
var skippedBySource = map[SkippedBy]WindowName{
	SkippedByCascadeFrom6M: Window6M,
	SkippedByCascadeFrom2Y: Window2Y,
	SkippedByCascadeFrom5Y: Window5Y,
}

// ValidateForIS checks that r is a structurally valid in-sample
// RawEvaluateResult: non-empty, canonical 6m→2y→5y→10y ordering, no
// duplicate windows, no OOS window, and a coherent cascade sequence.
//
// Wired into engine.evaluatePopulation and best-gene re-evaluation to
// enforce the strategy→engine boundary contract.
func (r *RawEvaluateResult) ValidateForIS() error {
	if r == nil {
		return errors.New("RawEvaluateResult is nil")
	}
	if len(r.Windows) == 0 {
		return errors.New("RawEvaluateResult: Windows is empty")
	}
	// Per-window three-state invariant.
	for i := range r.Windows {
		if err := r.Windows[i].Validate(); err != nil {
			return fmt.Errorf("RawEvaluateResult.Windows[%d]: %w", i, err)
		}
	}
	// No OOS window in IS raw.
	for i, w := range r.Windows {
		if w.Window == WindowOOS {
			return fmt.Errorf("RawEvaluateResult: Windows[%d] has WindowOOS in IS raw", i)
		}
	}
	// No duplicate windows.
	seen := make(map[WindowName]bool, len(r.Windows))
	for i, w := range r.Windows {
		if seen[w.Window] {
			return fmt.Errorf("RawEvaluateResult: duplicate window %q at index %d", w.Window, i)
		}
		seen[w.Window] = true
	}
	// Canonical order: each successive window must rank later in the
	// 6m→2y→5y→10y sequence than the one before it.
	canonPos := make(map[WindowName]int, 4)
	for i, w := range AllWindowsInEvalOrder() {
		canonPos[w] = i
	}
	for i := 1; i < len(r.Windows); i++ {
		prev, curr := r.Windows[i-1].Window, r.Windows[i].Window
		pp, prevOK := canonPos[prev]
		cp, currOK := canonPos[curr]
		if !prevOK || !currOK || cp <= pp {
			return fmt.Errorf("RawEvaluateResult: non-canonical order at index %d (%q after %q)", i, curr, prev)
		}
	}
	// Cascade consistency:
	//   • SkippedBy must reference a Fatal that appeared at an earlier index.
	//   • Once cascade starts, all subsequent windows must be SkippedBy.
	//   • A second Fatal window is not allowed after cascade has started.
	causeWindow := WindowName("") // "" = no Fatal encountered yet
	for i, w := range r.Windows {
		switch {
		case w.Score.Fatal:
			if causeWindow != "" {
				return fmt.Errorf(
					"RawEvaluateResult: Windows[%d] is Fatal after cascade already triggered by %q",
					i, causeWindow)
			}
			causeWindow = w.Window
		case w.SkippedBy != nil:
			if causeWindow == "" {
				return fmt.Errorf(
					"RawEvaluateResult: Windows[%d] has SkippedBy=%q but no prior Fatal window",
					i, *w.SkippedBy)
			}
			if src := skippedBySource[*w.SkippedBy]; src != causeWindow {
				return fmt.Errorf(
					"RawEvaluateResult: Windows[%d] SkippedBy=%q references %q but actual cause is %q",
					i, *w.SkippedBy, src, causeWindow)
			}
		default: // normal window
			if causeWindow != "" {
				return fmt.Errorf(
					"RawEvaluateResult: Windows[%d] is normal after cascade triggered by %q",
					i, causeWindow)
			}
		}
	}
	return nil
}

// ValidateForOOS checks that r contains exactly one structurally valid
// window result, as required by the OOS single-window evaluation plan.
//
// Note: RunOOS relabels the OOS window as Window6M before strategy
// evaluation (so cascade iteration covers it), so ValidateForOOS checks
// count and per-window invariants rather than the WindowName label.
//
// Wired into verification.RunOOS to fail-close on nil or malformed raw.
func (r *RawEvaluateResult) ValidateForOOS() error {
	if r == nil {
		return errors.New("RawEvaluateResult is nil")
	}
	if len(r.Windows) == 0 {
		return errors.New("RawEvaluateResult: Windows is empty")
	}
	if len(r.Windows) != 1 {
		return fmt.Errorf("RawEvaluateResult: OOS expects 1 window, got %d", len(r.Windows))
	}
	return r.Windows[0].Validate()
}

// ValidateForStress delegates to ValidateForIS: the stress re-run uses
// the IS plan with CaptureReturns=true and must produce the same window
// structure. The "nil or no-returns → skip" business rule stays in
// RunStress and is not this validator's concern.
func (r *RawEvaluateResult) ValidateForStress() error {
	return r.ValidateForIS()
}
