package ui

import "testing"

// TestGauge locks the fill arithmetic. The gauge carries meaning through glyph
// shape (█ vs ░), never color alone — tests run in the Ascii profile where color
// does not exist.
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
