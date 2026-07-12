package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The synthwave header banner: a blank breathing row, a full scanlined sun, and
// the SENTRA logotype — a terminal rendition of the outrun sunset, shown at the
// top of every page. It is the "logo" the one-row ✦ S E N T R A ✦ header grows
// into when the terminal is tall enough.
//
// Only the sun moves, and only briefly: driven by the ambient chrome clock
// (animFrame), it shimmers warm through bannerSunRounds cycles and then settles,
// after which the whole banner is static. The wordmark is a fixed sideways
// sunset gradient. Every bit of it is COLOR over FIXED glyphs, so under the
// Ascii profile (tests, NO_COLOR, a pipe) the styling strips, the art remains,
// and the output is identical at every frame — which keeps the geometry
// deterministic and the golden stable. The deep-space ground is painted by
// paintBackground on truecolor, so the banner needs no background of its own.

const (
	// bannerArtRows is the drawn scene: the sun plus the one-row wordmark.
	bannerArtRows = bannerSunHeight + 1
	// bannerTopMargin is a blank row above the art so the banner doesn't sit
	// flush against the terminal's top edge.
	bannerTopMargin = 1
	// bannerRows is the header's total height when the banner is showing.
	bannerRows = bannerTopMargin + bannerArtRows
	// bannerSlowdown divides the ambient frame so the sun advances one step
	// every bannerSlowdown ticks (~240ms each) — a slow, calm shimmer.
	bannerSlowdown = 3
	// bannerSunRounds is how many full warm-ramp cycles the sun shimmers before
	// it freezes. A whole number of rounds lands back on its home colors, so the
	// settle is seamless.
	bannerSunRounds = 3
	// bannerSunHeight is the sun art's row count.
	bannerSunHeight = 7
)

// bannerMinHeight gates the banner: show it only when the terminal is tall
// enough that reserving bannerRows still leaves every view at least the 16
// content rows it gets at the 80x20 minimum (contentH = H - bannerRows - 3).
// Kept above the common 30-row test height so the geometry tests that run at 30
// keep their one-line header and contentH budget unchanged.
const bannerMinHeight = 32

// titleRows is how many rows the header occupies at terminal height h: the full
// synthwave banner when there is room, otherwise the one-line logo. resize()'s
// content budget and headerView() both read it, so they never disagree.
func titleRows(h int) int {
	if h >= bannerMinHeight {
		return bannerRows
	}
	return 1
}

// bannerTick slows the ambient frame to the sun's drift rate.
func bannerTick(frame int) int { return frame / bannerSlowdown }

// bannerSunArt is a full round sun — a filled disc whose lower half is cut by
// horizontal scanlines, the unmistakable outrun sunset. Every row is the same
// width so the columns stay aligned when each line is centered independently.
// bannerSunRamp is a warm palindrome (gold ↔ magenta) the rows slide through as
// the frame advances, so the sun glows and shimmers without leaving warm tones.
var (
	bannerSunArt = []string{
		"      ▄▄▄▄      ",
		"   ▄▄██████▄▄   ",
		"  ████████████  ",
		"  ▀▀▀▀▀▀▀▀▀▀▀▀  ",
		"  ████████████  ",
		"   ▀▀▀▀▀▀▀▀▀▀   ",
		"     ▀████▀     ",
	}
	bannerSunRamp = []string{
		"#FFDA5E", "#FFC24D", "#FF9E4D", "#FF6BAA",
		"#FF4FC3", "#FF6BAA", "#FF9E4D", "#FFC24D",
	}
)

// bannerWord is the compact logotype under the sun — the same spaced caps as the
// one-line header, so the banner reads as that logo grown a sun rather than a
// different mark.
const bannerWord = "✦  S E N T R A  ✦"

// synthwaveBanner renders the bannerRows-line header centered to width. Only the
// sun reads the frame (and only until it settles); the wordmark is static. A
// width <= 0 (before the first WindowSizeMsg) skips centering. The widest line
// is the sun art, comfortably under minWidth (80), so a centered line never
// overflows.
func synthwaveBanner(width, frame int) string {
	lines := make([]string, 0, bannerRows)
	for range bannerTopMargin {
		lines = append(lines, "")
	}
	lines = append(lines, bannerSunLines(frame)...)
	lines = append(lines, bannerWordmarkLine())

	if width > 0 {
		for i, ln := range lines {
			lines[i] = lipgloss.PlaceHorizontal(width, lipgloss.Center, ln)
		}
	}
	return strings.Join(lines, "\n")
}

// bannerSunLines colors the sun's rows from the warm ramp, offset by the (slowed)
// frame so the glow slides top-to-bottom. The offset is clamped after
// bannerSunRounds cycles, so the sun completes its rounds and then holds — the
// only animated element, and only for a while.
func bannerSunLines(frame int) []string {
	round := len(bannerSunRamp)
	tick := min(bannerTick(frame), round*bannerSunRounds)
	out := make([]string, len(bannerSunArt))
	for i, art := range bannerSunArt {
		c := bannerSunRamp[wrap(i+tick, round)]
		out[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true).Render(art)
	}
	return out
}

// bannerWordmarkLine renders the SENTRA logotype with a static sideways sunset
// gradient across its glyphs. Spaces are left unstyled; every glyph keeps its
// cell, so the width is fixed.
func bannerWordmarkLine() string {
	var b strings.Builder
	glyph := 0
	for _, r := range bannerWord {
		if r == ' ' {
			b.WriteByte(' ')
			continue
		}
		c := splashSunset[wrap(glyph, len(splashSunset))]
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true).Render(string(r)))
		glyph++
	}
	return b.String()
}

// wrap returns i mod n in [0,n), correct for negative i (defensive — the ambient
// frame only ever increases, but a wrapped index must never panic).
func wrap(i, n int) int { return ((i % n) + n) % n }
