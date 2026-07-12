package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The synthwave header banner: a blank breathing row, a scanlined sun, the large
// SENTRA wordmark, and a neon grid horizon — a terminal rendition of the outrun
// sunset, shown at the top of every page. It is the "logo" the one-row
// ✦ S E N T R A ✦ header grows into when the terminal is tall enough.
//
// It is alive: driven by the ambient chrome clock (animFrame), the sunset flows
// down the wordmark, the sun shimmers warm, and a bright band sweeps the grid
// like a scanline. Every bit of that motion is COLOR over FIXED glyphs, so under
// the Ascii profile (tests, NO_COLOR, a pipe) the styling strips, the block art
// remains, and the output is identical at every frame — which is what keeps the
// geometry deterministic and the golden stable. The deep-space ground is painted
// by paintBackground on truecolor, so the banner needs no background of its own.

const (
	// bannerArtRows is the drawn scene: sun(3) + wordmark(6) + grid(1).
	bannerArtRows = 3 + splashBigRows + 1
	// bannerTopMargin is a blank row above the art so the banner doesn't sit
	// flush against the terminal's top edge.
	bannerTopMargin = 1
	// bannerRows is the header's total height when the banner is showing.
	bannerRows = bannerTopMargin + bannerArtRows
)

// bannerMinHeight gates the banner: show it only when the terminal is tall
// enough that reserving bannerRows still leaves every view at least the 16
// content rows it gets at the 80x20 minimum (contentH = H - bannerRows - 3, so
// H >= 30 keeps 16). Set above the common 30-row test height so the geometry
// tests that run at 30 keep their one-line header and contentH budget unchanged.
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

// bannerGridArt is the neon grid horizon: perspective lines converging on a
// center vanishing point. A bright band sweeps across it (see bannerGridLine).
const bannerGridArt = "▔╲▔▔╲▔▔╲▔▔╲▔▔│▔▔╱▔▔╱▔▔╱▔▔╱▔"

// synthwaveBanner renders the bannerRows-line header centered to width, animated
// at the given ambient frame. A width <= 0 (before the first WindowSizeMsg)
// skips centering. The widest line is the ~56-cell wordmark and the shell only
// reaches this path above minWidth (80), so a centered line never overflows.
func synthwaveBanner(width, frame int) string {
	lines := make([]string, 0, bannerRows)
	for range bannerTopMargin {
		lines = append(lines, "")
	}
	lines = append(lines, bannerSunLines(frame)...)
	lines = append(lines, bannerWordmarkLines(frame)...)
	lines = append(lines, bannerGridLine(frame))

	if width > 0 {
		for i, ln := range lines {
			lines[i] = lipgloss.PlaceHorizontal(width, lipgloss.Center, ln)
		}
	}
	return strings.Join(lines, "\n")
}

// bannerSunLines colors the sun's three rows from the warm ramp, offset by the
// frame so the glow slides top-to-bottom over time.
func bannerSunLines(frame int) []string {
	out := make([]string, len(bannerSunArt))
	for i, art := range bannerSunArt {
		c := bannerSunRamp[wrap(i+frame, len(bannerSunRamp))]
		out[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true).Render(art)
	}
	return out
}

// bannerWordmarkLines renders the six-row block SENTRA with the sunset gradient
// flowing DOWN the rows as the frame advances (magenta crest → cyan base,
// scrolling). It reuses the splash's glyph data so there is one wordmark
// definition. No forced background: paintBackground fills the deep-space ground
// on truecolor, and on other profiles the neon sits on the terminal's own
// background with no boxed-in block.
func bannerWordmarkLines(frame int) []string {
	rows := make([]string, splashBigRows)
	for li, glyph := range splashBigLetters {
		for r := range splashBigRows {
			if li > 0 {
				rows[r] += splashBigGap
			}
			c := splashSunset[wrap(r+frame, len(splashSunset))]
			rows[r] += lipgloss.NewStyle().
				Foreground(lipgloss.Color(c)).Bold(true).Render(glyph[r])
		}
	}
	return rows
}

// bannerGridLine colors the grid horizon with a bright crest sweeping across it
// — a scanline reading the line — over a dim violet base. Per-rune coloring
// preserves every cell so the width (and the geometry) is unchanged.
func bannerGridLine(frame int) string {
	runes := []rune(bannerGridArt)
	pos := wrap(frame, len(runes))
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
