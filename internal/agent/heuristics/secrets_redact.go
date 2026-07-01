package heuristics

import (
	"slices"
	"strings"
)

// previewMaxLen is the cap on the redacted preview returned in
// Finding.Details["preview"]. The preview is purely a UI hint
// ("here's roughly what context the match was in") — it must not
// contain the secret itself, so callers are limited to surrounding
// context only.
const previewMaxLen = 32

// expandToToken widens [start,end) to cover the surrounding run of
// non-whitespace bytes. A value regex may end mid-token when its
// character class excludes a delimiter (base64 stops at '.', '-', '_'),
// leaving trailing token bytes outside the redaction; widening to the
// whitespace boundaries redacts the full token so no secret fragment
// survives in the preview.
func expandToToken(line string, start, end int) (int, int) {
	for start > 0 && !isSpaceByte(line[start-1]) {
		start--
	}
	for end < len(line) && !isSpaceByte(line[end]) {
		end++
	}
	return start, end
}

func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	default:
		return false
	}
}

// redactPreview returns a short snippet of line with EVERY match in
// allLocs replaced by `[REDACTED]`, then windowed around the focal
// match. allLocs MUST include focalLoc. The preview is at most
// previewMaxLen characters of context so the user sees which line
// the match was on without seeing any secret on the line.
//
// Replacing all matches (not just the focal one) is critical: lines
// like `aws_a=AKIA... aws_b=AKIA...` have two secrets, and the focal
// match's preview must not leak the other one.
func redactPreview(line string, focalLoc []int, allLocs [][]int) string {
	const marker = "[REDACTED]"
	if len(focalLoc) != 2 || focalLoc[0] < 0 || focalLoc[1] > len(line) || focalLoc[0] >= focalLoc[1] {
		return marker
	}

	// Normalize and clamp every match span, dropping anything invalid,
	// and make sure the focal span is included so it's always redacted.
	type span struct{ start, end int }
	spans := make([]span, 0, len(allLocs)+1)
	addSpan := func(s, e int) {
		if s < 0 {
			s = 0
		}
		if e > len(line) {
			e = len(line)
		}
		if s < e {
			spans = append(spans, span{s, e})
		}
	}
	for _, l := range allLocs {
		if len(l) == 2 {
			addSpan(l[0], l[1])
		}
	}
	addSpan(focalLoc[0], focalLoc[1])

	// Sort by start, then merge overlapping/adjacent spans into disjoint
	// intervals. The previous reverse-order, in-place substitution used
	// stale indices and was incorrect for nested/overlapping spans — it
	// could leave raw secret bytes between two markers. A single
	// left-to-right pass over merged intervals is correct for ANY
	// overlap: every byte covered by a match is inside exactly one marker.
	slices.SortFunc(spans, func(a, b span) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return a.end - b.end
	})
	merged := make([]span, 0, len(spans))
	for _, s := range spans {
		if n := len(merged); n > 0 && s.start <= merged[n-1].end {
			if s.end > merged[n-1].end {
				merged[n-1].end = s.end
			}
			continue
		}
		merged = append(merged, s)
	}

	// Build the redacted line left-to-right, copying non-secret text
	// verbatim and emitting one marker per merged interval, recording
	// where the focal match's marker begins in the output.
	var b strings.Builder
	focalRedactedStart := 0
	prev := 0
	for _, s := range merged {
		b.WriteString(line[prev:s.start])
		if focalLoc[0] >= s.start && focalLoc[0] < s.end {
			focalRedactedStart = b.Len()
		}
		b.WriteString(marker)
		prev = s.end
	}
	b.WriteString(line[prev:])
	redacted := b.String()

	// Window previewMaxLen chars around the focal redaction.
	half := previewMaxLen / 2
	start := focalRedactedStart - half
	if start < 0 {
		start = 0
	}
	end := start + previewMaxLen
	if end > len(redacted) {
		end = len(redacted)
		if end-previewMaxLen > 0 {
			start = end - previewMaxLen
		}
	}
	return redacted[start:end]
}
