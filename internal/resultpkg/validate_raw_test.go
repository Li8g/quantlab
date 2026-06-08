package resultpkg

import (
	"strings"
	"testing"
)

// helpers

func f64p(v float64) *float64 { return &v }

func normalWindow(name WindowName) CrucibleResult {
	return CrucibleResult{Window: name, Score: SliceScore{Value: f64p(1.0)}, BarsEvaluated: 1}
}

func fatalWindow(name WindowName) CrucibleResult {
	return CrucibleResult{Window: name, Score: SliceScore{Fatal: true}, BarsEvaluated: 1}
}

func skippedWindow(name WindowName, by SkippedBy) CrucibleResult {
	return CrucibleResult{Window: name, Score: SliceScore{}, SkippedBy: &by, BarsEvaluated: 0}
}

// TestValidateForIS_ValidCases confirms the validator accepts well-formed IS raws.
func TestValidateForIS_ValidCases(t *testing.T) {
	cases := []struct {
		name string
		raw  RawEvaluateResult
	}{
		{
			name: "all_normal",
			raw: RawEvaluateResult{Windows: []CrucibleResult{
				normalWindow(Window6M),
				normalWindow(Window2Y),
				normalWindow(Window5Y),
				normalWindow(Window10Y),
			}},
		},
		{
			name: "cascade_from_6m",
			raw: RawEvaluateResult{Windows: []CrucibleResult{
				fatalWindow(Window6M),
				skippedWindow(Window2Y, SkippedByCascadeFrom6M),
				skippedWindow(Window5Y, SkippedByCascadeFrom6M),
				skippedWindow(Window10Y, SkippedByCascadeFrom6M),
			}},
		},
		{
			name: "cascade_from_2y",
			raw: RawEvaluateResult{Windows: []CrucibleResult{
				normalWindow(Window6M),
				fatalWindow(Window2Y),
				skippedWindow(Window5Y, SkippedByCascadeFrom2Y),
				skippedWindow(Window10Y, SkippedByCascadeFrom2Y),
			}},
		},
		{
			name: "partial_windows_normal",
			raw: RawEvaluateResult{Windows: []CrucibleResult{
				normalWindow(Window6M),
				normalWindow(Window2Y),
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.raw.ValidateForIS(); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestValidateForIS_InvalidCases verifies every rejection rule from the spec.
func TestValidateForIS_InvalidCases(t *testing.T) {
	cases := []struct {
		name    string
		raw     *RawEvaluateResult
		wantMsg string
	}{
		{
			name:    "nil_raw",
			raw:     nil,
			wantMsg: "nil",
		},
		{
			name:    "empty_windows",
			raw:     &RawEvaluateResult{},
			wantMsg: "empty",
		},
		{
			name: "oos_window_in_is",
			raw: &RawEvaluateResult{Windows: []CrucibleResult{
				normalWindow(Window6M),
				{Window: WindowOOS, Score: SliceScore{Value: f64p(1.0)}, BarsEvaluated: 1},
			}},
			wantMsg: "WindowOOS",
		},
		{
			name: "duplicate_window",
			raw: &RawEvaluateResult{Windows: []CrucibleResult{
				normalWindow(Window6M),
				normalWindow(Window6M),
			}},
			wantMsg: "duplicate",
		},
		{
			name: "non_canonical_order",
			raw: &RawEvaluateResult{Windows: []CrucibleResult{
				normalWindow(Window2Y),
				normalWindow(Window6M),
			}},
			wantMsg: "non-canonical",
		},
		{
			name: "skipped_without_prior_fatal",
			raw: &RawEvaluateResult{Windows: []CrucibleResult{
				normalWindow(Window6M),
				skippedWindow(Window2Y, SkippedByCascadeFrom6M),
			}},
			wantMsg: "no prior Fatal",
		},
		{
			name: "skipped_by_wrong_cause",
			raw: &RawEvaluateResult{Windows: []CrucibleResult{
				normalWindow(Window6M),
				fatalWindow(Window2Y),
				skippedWindow(Window5Y, SkippedByCascadeFrom6M), // wrong: cause is 2y, not 6m
			}},
			wantMsg: "actual cause",
		},
		{
			name: "normal_after_cascade",
			raw: &RawEvaluateResult{Windows: []CrucibleResult{
				fatalWindow(Window6M),
				skippedWindow(Window2Y, SkippedByCascadeFrom6M),
				normalWindow(Window5Y), // wrong: cascade started
			}},
			wantMsg: "normal after cascade",
		},
		{
			name: "fatal_after_cascade",
			raw: &RawEvaluateResult{Windows: []CrucibleResult{
				fatalWindow(Window6M),
				skippedWindow(Window2Y, SkippedByCascadeFrom6M),
				fatalWindow(Window5Y), // invalid: can't have second Fatal after cascade
			}},
			wantMsg: "Fatal after cascade",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.raw.ValidateForIS()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

// TestValidateForOOS_Valid confirms single-window acceptance.
func TestValidateForOOS_Valid(t *testing.T) {
	raw := &RawEvaluateResult{Windows: []CrucibleResult{
		normalWindow(Window6M), // OOS is relabeled as 6m before strategy eval
	}}
	if err := raw.ValidateForOOS(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateForOOS_Invalid(t *testing.T) {
	cases := []struct {
		name    string
		raw     *RawEvaluateResult
		wantMsg string
	}{
		{name: "nil", raw: nil, wantMsg: "nil"},
		{name: "empty", raw: &RawEvaluateResult{}, wantMsg: "empty"},
		{
			name: "two_windows",
			raw: &RawEvaluateResult{Windows: []CrucibleResult{
				normalWindow(Window6M),
				normalWindow(Window2Y),
			}},
			wantMsg: "expects 1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.raw.ValidateForOOS()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

// TestValidateForStress_DelegatesToIS confirms ValidateForStress rejects the
// same inputs that ValidateForIS rejects (it is an alias).
func TestValidateForStress_DelegatesToIS(t *testing.T) {
	raw := &RawEvaluateResult{Windows: []CrucibleResult{
		normalWindow(Window2Y),
		normalWindow(Window6M), // reversed order — invalid
	}}
	if err := raw.ValidateForStress(); err == nil {
		t.Error("expected error for non-canonical order, got nil")
	}
}
