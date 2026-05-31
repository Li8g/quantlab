package sigmoid_v1

// C5: automated guard that the chromosome layout in chromosome.go stays in
// sync with its authoritative spec, docs/strategies/sigmoid_v1.md §4. The
// doc is the source of truth (chromosome.go top comment); these tests parse
// the §4.1 dimension table and §4.2 segment block straight out of the
// markdown and compare them to the code constants, so a silent drift on
// either side fails the build instead of relying on human discipline.
//
// Description fields are intentionally NOT compared: the doc uses fullwidth
// CJK punctuation (（）／；) while the code mirrors them with ASCII, so they
// are cosmetically divergent by design. Only load-bearing fields (index,
// bounds, defaults, segment partition, quantization/gene steps, IsCritical)
// are checked.

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const docPath = "../../../docs/strategies/sigmoid_v1.md"

// sliceBetween returns the doc text from the first line containing start up
// to (excluding) the first subsequent line containing end.
func sliceBetween(t *testing.T, doc, start, end string) string {
	t.Helper()
	i := strings.Index(doc, start)
	if i < 0 {
		t.Fatalf("section %q not found in %s", start, docPath)
	}
	rest := doc[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("section end %q not found after %q in %s", end, start, docPath)
	}
	return rest[:j]
}

func parseFloats(t *testing.T, csv string) []float64 {
	t.Helper()
	fields := strings.Split(csv, ",")
	out := make([]float64, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			t.Fatalf("parse float %q: %v", f, err)
		}
		out = append(out, v)
	}
	return out
}

func parseInts(t *testing.T, csv string) []int {
	t.Helper()
	out := []int{}
	for _, f := range strings.Split(csv, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		v, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("parse int %q: %v", f, err)
		}
		out = append(out, v)
	}
	return out
}

// TestGeneLayoutMatchesDocTable parses the §4.1 dimension table and asserts
// that every row's [min, max] range and default match the code's bounds[]
// and defaultChromosome(), keyed by the table's own index column.
func TestGeneLayoutMatchesDocTable(t *testing.T) {
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read doc: %v", err)
	}
	sec := sliceBetween(t, string(raw), "### 4.1", "### 4.2")

	// | idx | `name` | type | [min, max] | default | semantics |
	rowRe := regexp.MustCompile(
		"^\\|\\s*(\\d+)\\s*\\|\\s*`([a-z0-9_]+)`\\s*\\|[^|]*\\|\\s*\\[([^\\]]+)\\]\\s*\\|\\s*([^|]+?)\\s*\\|")

	defGene := EncodeChromosome(defaultChromosome())

	seenIdx := map[int]string{}
	for _, line := range strings.Split(sec, "\n") {
		m := rowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("row index %q: %v", m[1], err)
		}
		name := m[2]
		rng := parseFloats(t, m[3])
		def := parseFloats(t, m[4])

		if idx < 0 || idx >= GeneDim {
			t.Fatalf("doc row %q: index %d out of [0,%d)", name, idx, GeneDim)
		}
		if prev, dup := seenIdx[idx]; dup {
			t.Fatalf("doc index %d appears twice (%q and %q)", idx, prev, name)
		}
		seenIdx[idx] = name

		if len(rng) != 2 {
			t.Fatalf("doc row %q: range has %d values, want 2", name, len(rng))
		}
		if bounds[idx][0] != rng[0] || bounds[idx][1] != rng[1] {
			t.Errorf("dim %d (%s): doc range [%g, %g] != code bounds [%g, %g]",
				idx, name, rng[0], rng[1], bounds[idx][0], bounds[idx][1])
		}
		if len(def) != 1 {
			t.Fatalf("doc row %q: default has %d values, want 1", name, len(def))
		}
		if defGene[idx] != def[0] {
			t.Errorf("dim %d (%s): doc default %g != code default %g",
				idx, name, def[0], defGene[idx])
		}
	}

	if len(seenIdx) != GeneDim {
		t.Fatalf("doc §4.1 has %d dimension rows, want GeneDim=%d", len(seenIdx), GeneDim)
	}
}

// TestSegmentsMatchDocBlock parses the §4.2 SegmentInfo literal and asserts
// it matches segmentInfos() field-by-field (Name, Dimensions, both step
// arrays, IsCritical) in order. Description is excluded (see file header).
func TestSegmentsMatchDocBlock(t *testing.T) {
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read doc: %v", err)
	}
	sec := sliceBetween(t, string(raw), "### 4.2", "### 4.3")

	nameRe := regexp.MustCompile(`Name:\s*"([^"]+)"`)
	dimsRe := regexp.MustCompile(`Dimensions:\s*\[\]int\{([^}]*)\}`)
	qstepRe := regexp.MustCompile(`QuantizationStep:\s*\[\]float64\{([^}]*)\}`)
	gstepRe := regexp.MustCompile(`GeneStep:\s*\[\]float64\{([^}]*)\}`)
	critRe := regexp.MustCompile(`IsCritical:\s*(true|false)`)

	names := nameRe.FindAllStringSubmatch(sec, -1)
	dims := dimsRe.FindAllStringSubmatch(sec, -1)
	qsteps := qstepRe.FindAllStringSubmatch(sec, -1)
	gsteps := gstepRe.FindAllStringSubmatch(sec, -1)
	crits := critRe.FindAllStringSubmatch(sec, -1)

	code := segmentInfos()
	n := len(code)
	if len(names) != n || len(dims) != n || len(qsteps) != n || len(gsteps) != n || len(crits) != n {
		t.Fatalf("doc §4.2 parsed counts name=%d dims=%d qstep=%d gstep=%d crit=%d, code has %d segments",
			len(names), len(dims), len(qsteps), len(gsteps), len(crits), n)
	}

	for i := 0; i < n; i++ {
		c := code[i]
		if got := names[i][1]; got != c.Name {
			t.Errorf("segment[%d]: doc Name %q != code %q", i, got, c.Name)
		}
		if got := parseInts(t, dims[i][1]); !equalInts(got, c.Dimensions) {
			t.Errorf("segment %q: doc Dimensions %v != code %v", c.Name, got, c.Dimensions)
		}
		if got := parseFloats(t, qsteps[i][1]); !equalFloats(got, c.QuantizationStep) {
			t.Errorf("segment %q: doc QuantizationStep %v != code %v", c.Name, got, c.QuantizationStep)
		}
		if got := parseFloats(t, gsteps[i][1]); !equalFloats(got, c.GeneStep) {
			t.Errorf("segment %q: doc GeneStep %v != code %v", c.Name, got, c.GeneStep)
		}
		if got := crits[i][1] == "true"; got != c.IsCritical {
			t.Errorf("segment %q: doc IsCritical %v != code %v", c.Name, got, c.IsCritical)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-12 {
			return false
		}
	}
	return true
}
