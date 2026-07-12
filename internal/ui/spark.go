package ui

import "strings"

// sparkLevels is the 8-step block ladder shared by Sparkline. Glyph
// shape carries the value — the chart stays readable under NO_COLOR,
// in a pipe, and in the Ascii profile the unit tests render with.
var sparkLevels = []rune("▁▂▃▄▅▆▇█")

// Sparkline renders values as one block character per cell, at most
// width cells. Scaling is zero-based (0 → ▁, series max → █) rather
// than min-max: byte sizes have a true zero, and min-max would blow
// tiny fluctuations up into a mountain range on a repo whose backups
// are all roughly the same size.
//
// A series longer than width is downsampled by bucket max, not mean —
// an anomalous spike is exactly what the operator must not lose to
// smoothing. Negative values clamp to the floor block.
func Sparkline(values []int64, width int) string {
	if len(values) == 0 || width <= 0 {
		return ""
	}
	if len(values) > width {
		buckets := make([]int64, width)
		for i := range buckets {
			lo := i * len(values) / width
			hi := (i + 1) * len(values) / width
			for _, v := range values[lo:hi] {
				if v > buckets[i] {
					buckets[i] = v
				}
			}
		}
		values = buckets
	}

	var max int64
	for _, v := range values {
		if v > max {
			max = v
		}
	}

	var b strings.Builder
	for _, v := range values {
		level := 0
		if v > 0 && max > 0 {
			// ceil(v*8/max)-1 maps (0,max] onto [▁,█] with the max
			// always hitting the top block.
			level = int((v*int64(len(sparkLevels)) + max - 1) / max)
			level--
			if level < 0 {
				level = 0
			}
			if level >= len(sparkLevels) {
				level = len(sparkLevels) - 1
			}
		}
		b.WriteRune(sparkLevels[level])
	}
	return b.String()
}

// Gauge renders a horizontal fill bar: round(frac*width) filled cells
// (█) then empty cells (░). frac is clamped to [0,1]. Like Sparkline,
// the filled/empty distinction is glyph shape, never color alone.
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
