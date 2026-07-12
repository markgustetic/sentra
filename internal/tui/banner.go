package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The synthwave header banner: a blank breathing row, a scanlined sun, the
// SENTRA logotype, and a neon grid horizon — a terminal rendition of the outrun
// sunset, shown at the top of every page. It is the "logo" the one-row
// ✦ S E N T R A ✦ header grows into when the terminal is tall enough.
//
// It is alive, driven by the ambient chrome clock (animFrame), but slowly: the
// sun shimmers warm, the wordmark's sunset flows sideways, and a bright band
// drifts across the grid like a scanline. bannerSlowdown paces all of it well
// below the tick rate so it reads as a gentle breath, not motion. Every bit of
// it is COLOR over FIXED glyphs, so under the Ascii profile (tests, NO_COLOR, a
// pipe) the styling strips, the art remains, and the output is identical at
// every frame — which keeps the geometry deterministic and the golden stable.
// The deep-space ground is painted by paintBackground on truecolor, so the
// banner needs no background of its own.

const (
	// bannerArtRows is the drawn scene: sun(3) + wordmark(1) + grid(1).
	bannerArtRows = 3 + 1 + 1
	// bannerTopMargin is a blank row above the art so the banner doesn't sit
	// flush against the terminal's top edge.
	bannerTopMargin = 1
	// bannerRows is the header's total height when the banner is showing.
	bannerRows = bannerTopMargin + bannerArtRows
	// bannerSlowdown divides the ambient frame so the animation advances one
	// step every bannerSlowdown ticks (~240ms each) — a slow, calm drift.
	bannerSlowdown = 3
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

// bannerTick slows the ambient frame to the banner's drift rate.
func bannerTick(frame int) int { return frame / bannerSlowdown }

// bannerSunArt draws a small sun whose lower edge is broken by ▀ half-blocks so
// a scanline gap reads across it. bannerSunRamp is a warm cycle (gold → magenta
// → gold) the three rows slide through as the frame advances, so the sun glows
// and shimmers without ever leaving warm tones.
var (
	bannerSunArt = []string{
		"▟█████████▙",
		"███████████",
		"▜█▀▀▀█▀▀▀█▛",
	}
	bannerSunRamp = []string{
		"#FFDA5E", "#FFC24D", "#FF9E4D", "#FF6BAA",
		"#FF4FC3", "#FF6BAA", "#FF9E4D", "#FFC24D",
	}
)

// bannerWord is the compact logotype under the sun — the same spaced caps as the
// one-line header, so the banner reads as that logo grown a sun and a horizon
// rather than a different mark.
const bannerWord = "✦  S E N T R A  ✦"

// bannerGridArt is the neon grid horizon: perspective lines converging on a
// center vanishing point. A bright band drifts across it (see bannerGridLine).
const bannerGridArt = "▔╲▔▔╲▔▔╲▔▔╲▔▔│▔▔╱▔▔╱▔▔╱▔▔╱▔"

// synthwaveBanner renders the bannerRows-line header centered to width, animated
// at the given ambient frame. A width <= 0 (before the first WindowSizeMsg)
// skips centering. The widest line is the sun/grid art, comfortably under
// minWidth (80), so a centered line never overflows.
func synthwaveBanner(width, frame int) string {
	lines := make([]string, 0, bannerRows)
	for range bannerTopMargin {
		lines = append(lines, "")
	}
	lines = append(lines, bannerSunLines(frame)...)
	lines = append(lines, bannerWordmarkLine(frame))
	lines = append(lines, bannerGridLine(frame))

	if width > 0 {
		for i, ln := range lines {
			lines[i] = lipgloss.PlaceHorizontal(width, lipgloss.Center, ln)
		}
	}
	return strings.Join(lines, "\n")
}

// bannerSunLines colors the sun's three rows from the warm ramp, offset by the
// (slowed) frame so the glow slides top-to-bottom over time.
func bannerSunLines(frame int) []string {
	tick := bannerTick(frame)
	out := make([]string, len(bannerSunArt))
	for i, art := range bannerSunArt {
		c := bannerSunRamp[wrap(i+tick, len(bannerSunRamp))]
		out[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true).Render(art)
	}
	return out
}

// bannerWordmarkLine renders the SENTRA logotype with the sunset gradient
// flowing sideways across its glyphs as the (slowed) frame advances. Spaces are
// left unstyled; every glyph keeps its cell, so the width is fixed.
func bannerWordmarkLine(frame int) string {
	tick := bannerTick(frame)
	var b strings.Builder
	glyph := 0
	for _, r := range bannerWord {
		if r == ' ' {
			b.WriteByte(' ')
			continue
		}
		c := splashSunset[wrap(glyph+tick, len(splashSunset))]
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true).Render(string(r)))
		glyph++
	}
	return b.String()
}

// bannerGridLine colors the grid horizon with a bright crest drifting across it
// — a scanline reading the line — over a dim violet base. Per-rune coloring
// preserves every cell so the width (and the geometry) is unchanged.
func bannerGridLine(frame int) string {
	runes := []rune(bannerGridArt)
	pos := wrap(bannerTick(frame), len(runes))
	var b strings.Builder
	for i, r := range runes {
		var c string
		switch i - pos {
		case 0:
			c = "#EAFDFF" // crest
		case -1, 1:
			c = "#7FE6FF" // cyan shoulders
		case -2, 2:
			c = "#B678FF" // violet
		default:
			c = "#6E4FA8" // dim base
		}
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render(string(r)))
	}
	return b.String()
}

// wrap returns i mod n in [0,n), correct for negative i (defensive — the ambient
// frame only ever increases, but a wrapped index must never panic).
func wrap(i, n int) int { return ((i % n) + n) % n }
