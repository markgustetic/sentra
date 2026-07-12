package ui

import "strings"

// Gauge renders a horizontal fill bar: round(frac*width) filled cells (█) then
// empty cells (░). frac is clamped to [0,1]. The filled/empty distinction is
// glyph shape, never color alone, so it stays legible under NO_COLOR and in the
// Ascii profile the unit tests render in.
func Gauge(frac float64, width int) string {
	if width <= 0 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac*float64(width) + 0.5)
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}
