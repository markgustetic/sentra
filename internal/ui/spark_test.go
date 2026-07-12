package ui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSparkline locks the scaling contract: values scale from zero (not the
// series minimum) so a snapshot twice the size renders visibly taller, and a
// flat series does not get noise-amplified into a mountain range.
func TestSparkline(t *testing.T) {
	cases := []struct {
		name   string
		values []int64
		width  int
		want   string
	}{
		{"empty series", nil, 10, ""},
		{"zero width", []int64{1, 2}, 0, ""},
		{"single value maxes out", []int64{100}, 10, "█"},
		{"zero renders floor block", []int64{0, 100}, 10, "▁█"},
		{"all zeros stay on floor", []int64{0, 0, 0}, 10, "▁▁▁"},
		{"flat nonzero series is flat", []int64{5, 5, 5}, 10, "███"},
		{"monotonic ramp uses full ladder", []int64{1, 2, 3, 4, 5, 6, 7, 8}, 10, "▁▂▃▄▅▆▇█"},
		{"negative clamps to floor", []int64{-3, 8}, 10, "▁█"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sparkline(tc.values, tc.width); got != tc.want {
				t.Errorf("Sparkline(%v, %d) = %q, want %q", tc.values, tc.width, got, tc.want)
			}
		})
	}
}

// TestSparkline_Downsamples: a series longer than the width must compress into
// exactly width cells and keep peaks visible (bucket max, not mean) — a spike
// in backup size is precisely the thing the operator should see.
func TestSparkline_Downsamples(t *testing.T) {
	values := make([]int64, 100)
	for i := range values {
		values[i] = 1
	}
	values[50] = 1000 // the spike a mean would flatten

	got := Sparkline(values, 20)
	if n := utf8.RuneCountInString(got); n != 20 {
		t.Fatalf("downsampled sparkline must be exactly 20 cells, got %d: %q", n, got)
	}
	if !strings.Contains(got, "█") {
		t.Errorf("bucket-max downsampling must preserve the spike: %q", got)
	}
}

// TestGauge locks the fill arithmetic. The gauge carries meaning through
// glyph shape (█ vs ░), never color alone — tests run in the Ascii profile
// where color does not exist.
func TestGauge(t *testing.T) {
	cases := []struct {
		name  string
		frac  float64
		width int
		want  string
	}{
		{"empty at zero", 0, 4, "░░░░"},
		{"full at one", 1, 4, "████"},
		{"half rounds", 0.5, 4, "██░░"},
		{"clamps above one", 1.7, 4, "████"},
		{"clamps below zero", -0.3, 4, "░░░░"},
		{"zero width", 0.5, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Gauge(tc.frac, tc.width); got != tc.want {
				t.Errorf("Gauge(%v, %d) = %q, want %q", tc.frac, tc.width, got, tc.want)
			}
		})
	}
}
