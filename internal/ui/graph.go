package ui

import (
	"fmt"
	"strings"
)

// This is the big sibling of Sparkline. Where a sparkline is one row of block
// characters, BrailleGraph is a multi-row FILLED AREA graph drawn in braille —
// each cell packs a 2×4 dot matrix, so a graph W cells wide by H tall resolves
// 2*W samples across and 4*H levels up. That density is what gives system
// monitors like btop their smooth, dense waveform; it is the dashboard's hero
// "activity" graph.
//
// The output is plain braille runes — no color. Meaning lives in the dot
// heights (a glyph property that survives NO_COLOR, a pipe, and the Ascii
// profile the unit tests render in). The dashboard layers a vertical color
// gradient on top as a truecolor-only flourish; see GradientColors.

// brailleBits maps a (row-within-cell, sub-column) dot position to its bit in
// the U+2800 braille pattern. Rows run top→bottom, sub-columns left→right. The
// low three rows use the historical 6-dot layout (0x01,0x02,0x04 | 0x08,0x10,
// 0x20); the fourth row is the 8-dot extension (0x40 | 0x80).
var brailleBits = [4][2]byte{
	{0x01, 0x08},
	{0x02, 0x10},
	{0x04, 0x20},
	{0x40, 0x80},
}

// BrailleGraph renders values as a filled area graph, width cells wide and
// height cells tall, returning height lines top-first. Scaling is zero-based
// (0 → empty, series max → full height) rather than min-max: byte sizes have a
// true zero, and min-max would inflate tiny fluctuations into a full-height
// mountain range on a repo whose backups are all about the same size.
//
// The series is resampled to the 2*width sub-columns the braille grid provides:
// a longer series is downsampled by bucket MAX (an anomalous spike is exactly
// what must not smooth away), a shorter one is stretched so few snapshots still
// fill the panel instead of leaving it mostly empty. Negative values clamp to
// the floor.
func BrailleGraph(values []int64, width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}

	blankLine := strings.Repeat("⠀", width)
	if len(values) == 0 {
		lines := make([]string, height)
		for i := range lines {
			lines[i] = blankLine
		}
		return lines
	}

	// One sample per braille sub-column.
	samples := width * 2
	col := make([]int64, samples)
	if len(values) >= samples {
		for i := range col {
			lo := i * len(values) / samples
			hi := (i + 1) * len(values) / samples
			var mx int64
			for _, v := range values[lo:hi] {
				if v > mx {
					mx = v
				}
			}
			col[i] = mx
		}
	} else {
		// Stretch: nearest-sample so N<samples values repeat across the
		// width with no gaps.
		for i := range col {
			col[i] = values[i*len(values)/samples]
		}
	}

	var max int64
	for _, v := range col {
		if v > max {
			max = v
		}
	}

	// Fill height per sub-column, in dots [0, 4*height]. ceil so any positive
	// value lights at least one dot and the series max reaches the very top.
	levels := 4 * height
	fill := make([]int, samples)
	for i, v := range col {
		if v > 0 && max > 0 {
			fill[i] = min(int((v*int64(levels)+max-1)/max), levels)
		}
	}

	lines := make([]string, height)
	for row := range height {
		var b strings.Builder
		b.Grow(width * 3) // braille runes are 3 bytes in UTF-8
		for c := range width {
			cell := rune(0x2800)
			for sc := range 2 {
				f := fill[c*2+sc]
				for dr := range 4 {
					// globalRow counts dot-rows from the top; a dot is lit
					// when it sits within `f` dots of the baseline.
					globalRow := row*4 + dr
					if levels-globalRow <= f {
						cell |= rune(brailleBits[dr][sc])
					}
				}
			}
			b.WriteRune(cell)
		}
		lines[row] = b.String()
	}
	return lines
}

// GradientColors returns n hex colors sampled evenly through the given stop
// list — the vertical ramp the dashboard paints up the braille graph (bottom
// stop → top stop) and along its meter bars. Endpoints are exact; interior
// samples blend linearly between adjacent stops. Returns nil for n<=0 or no
// stops. A single stop repeats.
//
// Colors are #rrggbb so lipgloss emits truecolor SGR; under the Ascii profile
// (tests, NO_COLOR, pipes) lipgloss strips them and the graph falls back to its
// plain, still-legible braille.
func GradientColors(stops []string, n int) []string {
	if n <= 0 || len(stops) == 0 {
		return nil
	}
	out := make([]string, n)
	if len(stops) == 1 {
		for i := range out {
			out[i] = stops[0]
		}
		return out
	}
	if n == 1 {
		out[0] = stops[0]
		return out
	}
	for i := range n {
		p := float64(i) / float64(n-1) * float64(len(stops)-1)
		seg := min(int(p), len(stops)-2)
		out[i] = lerpHex(stops[seg], stops[seg+1], p-float64(seg))
	}
	return out
}

// lerpHex linearly interpolates between two #rrggbb colors, rounding each
// channel half-up. Malformed inputs fall back to the first color so a bad
// theme constant degrades to a flat tint rather than a panic.
func lerpHex(a, b string, t float64) string {
	ar, ag, ab, aok := parseHex(a)
	br, bg, bb, bok := parseHex(b)
	if !aok || !bok {
		return a
	}
	mix := func(x, y int) int { return int(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return fmt.Sprintf("#%02x%02x%02x", mix(ar, br), mix(ag, bg), mix(ab, bb))
}

// parseHex reads a #rrggbb string into its channels.
func parseHex(s string) (r, g, b int, ok bool) {
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, false
	}
	var v int
	if _, err := fmt.Sscanf(s[1:], "%06x", &v); err != nil {
		return 0, 0, 0, false
	}
	return (v >> 16) & 0xff, (v >> 8) & 0xff, v & 0xff, true
}
