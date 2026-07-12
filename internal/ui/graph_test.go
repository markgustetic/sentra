package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// braille block runes used by the assertions below.
const (
	brailleBlank = '⠀' // ⠀ no dots
	brailleFull  = '⣿' // ⣿ all 8 dots
)

// TestBrailleGraph_Geometry locks the output shape: exactly height lines,
// each exactly width braille cells wide (measured both as runes and as
// display cells, since a braille pattern must be a single-width glyph or the
// panel arithmetic that sizes it would be wrong).
func TestBrailleGraph_Geometry(t *testing.T) {
	lines := BrailleGraph([]int64{1, 5, 2, 8, 3, 6}, 10, 4)
	if len(lines) != 4 {
		t.Fatalf("want 4 lines (height), got %d", len(lines))
	}
	for i, ln := range lines {
		if n := utf8.RuneCountInString(ln); n != 10 {
			t.Errorf("line %d: want 10 runes (width), got %d: %q", i, n, ln)
		}
		if w := lipgloss.Width(ln); w != 10 {
			t.Errorf("line %d: want display width 10, got %d — braille must be single-width", i, w)
		}
	}
}

// TestBrailleGraph_Degenerate covers the empty / zero-dimension inputs a live
// dashboard hands in on a fresh repo or a first frame before sizing.
func TestBrailleGraph_Degenerate(t *testing.T) {
	if got := BrailleGraph(nil, 0, 4); got != nil {
		t.Errorf("zero width must return nil, got %v", got)
	}
	if got := BrailleGraph([]int64{1}, 10, 0); got != nil {
		t.Errorf("zero height must return nil, got %v", got)
	}
	// Empty series still renders a height×width blank canvas so the panel
	// keeps its shape rather than collapsing.
	blank := BrailleGraph(nil, 3, 2)
	if len(blank) != 2 {
		t.Fatalf("empty series must still render height lines, got %d", len(blank))
	}
	for _, ln := range blank {
		for _, r := range ln {
			if r != brailleBlank {
				t.Errorf("empty series must be all blank braille, found %q in %q", r, ln)
			}
		}
	}
}

// TestBrailleGraph_FillsFromBottom: the area is filled up from the baseline,
// so the BOTTOM line of a positive series must carry dots and — for a flat
// max series — every cell on the bottom line is the full block. The scale is
// zero-based (like Sparkline): a flat nonzero series fills a constant height,
// it does not get min-max amplified to full height.
func TestBrailleGraph_FillsFromBottom(t *testing.T) {
	lines := BrailleGraph([]int64{5, 5, 5, 5}, 4, 3)
	bottom := lines[len(lines)-1]
	for _, r := range bottom {
		if r != brailleFull {
			t.Errorf("flat max series must fill the bottom row solid, got %q in %q", r, bottom)
		}
	}
	// A flat series that is the max everywhere fills the WHOLE canvas solid
	// (every value equals the series max → full column height).
	for i, ln := range lines {
		for _, r := range ln {
			if r != brailleFull {
				t.Errorf("line %d: flat-max series fills solid, got %q", i, r)
			}
		}
	}
}

// TestBrailleGraph_TallColumnReachesTop: a lone peak equal to the max must
// light dots on the TOP line (it spans the full height), while a zero column
// stays blank on the bottom line. This is the property that makes the graph
// read as "how big did each backup get".
func TestBrailleGraph_TallColumnReachesTop(t *testing.T) {
	// width 3 → 6 sub-columns. A full-height peak occupies the middle CELL
	// (both its sub-columns) with zero cells on either side, so the top row
	// reads blank · solid · blank.
	lines := BrailleGraph([]int64{0, 0, 100, 100, 0, 0}, 3, 3)
	top := lines[0]
	if !strings.ContainsRune(top, brailleBlank) {
		t.Errorf("top row should still have blank cells beside the peak: %q", top)
	}
	if !strings.ContainsRune(top, brailleFull) {
		t.Errorf("a max-height peak must reach the top row solid: %q", top)
	}
	// The zero columns stay blank all the way down to the bottom row.
	bottom := lines[len(lines)-1]
	if !strings.ContainsRune(bottom, brailleBlank) {
		t.Errorf("zero columns must stay blank on the bottom row: %q", bottom)
	}
}

// TestBrailleGraph_DownsamplesKeepingPeaks: a long series compresses to the
// requested width and a single spike survives (bucket max, not mean) — the
// same anti-smoothing contract the block Sparkline has.
func TestBrailleGraph_DownsamplesKeepingPeaks(t *testing.T) {
	values := make([]int64, 400)
	for i := range values {
		values[i] = 1
	}
	values[200] = 10000

	lines := BrailleGraph(values, 20, 4)
	if len(lines) != 4 {
		t.Fatalf("height mismatch: %d", len(lines))
	}
	// The spike dwarfs the baseline (10000 vs 1), so its bucket must reach
	// the TOP row: bucket-max keeps it, a mean would bury it. The top row is
	// therefore not entirely blank.
	allBlank := true
	for _, r := range lines[0] {
		if r != brailleBlank {
			allBlank = false
		}
	}
	if allBlank {
		t.Errorf("bucket-max downsampling must carry the spike to the top row: %q", lines[0])
	}
}

// TestGradientColors locks the interpolation: evenly spaced samples through
// the stop list, endpoints exact, midpoint a true blend. Colors are the
// dashboard's btop-style vertical ramp; they must be valid #rrggbb so lipgloss
// emits truecolor (and strips cleanly under the Ascii test profile).
func TestGradientColors(t *testing.T) {
	if got := GradientColors(nil, 3); got != nil {
		t.Errorf("no stops must return nil, got %v", got)
	}
	if got := GradientColors([]string{"#123456"}, 0); got != nil {
		t.Errorf("zero count must return nil, got %v", got)
	}

	got := GradientColors([]string{"#000000", "#ffffff"}, 3)
	want := []string{"#000000", "#808080", "#ffffff"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("GradientColors two-stop = %v, want %v", got, want)
	}

	// A single stop repeats; every entry is a valid 7-char hex string.
	solid := GradientColors([]string{"#5cebff"}, 4)
	if len(solid) != 4 {
		t.Fatalf("want 4 colors, got %d", len(solid))
	}
	for _, c := range solid {
		if len(c) != 7 || c[0] != '#' {
			t.Errorf("invalid hex color %q", c)
		}
	}

	// Three stops: the middle sample lands exactly on the middle stop.
	mid := GradientColors([]string{"#5cebff", "#cb8cff", "#ff6bdd"}, 3)
	if mid[1] != "#cb8cff" {
		t.Errorf("middle sample should hit the middle stop, got %q", mid[1])
	}
}
