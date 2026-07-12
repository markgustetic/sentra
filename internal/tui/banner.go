package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The synthwave header banner: a scanlined sun, the large SENTRA wordmark, and a
// neon grid horizon — a terminal rendition of the outrun sunset, shown at the
// top of every page. It is the "logo" the one-row ✦ S E N T R A ✦ header grows
// into when the terminal is tall enough to spare the rows.
//
// Everything here is COLOR over fixed glyphs: under the Ascii profile (tests,
// NO_COLOR, a pipe) the styling strips and the block letters, sun, and grid all
// remain — the shape carries it, exactly like the splash. The deep-space ground
// is painted by paintBackground on truecolor, so the banner needs no background
// of its own and blends on every profile.

// bannerRows is the banner's fixed height: sun(3) + wordmark(6) + grid(1).
const bannerRows = 3 + splashBigRows + 1

// bannerMinHeight gates the banner: show it only when the terminal is tall
// enough that reserving bannerRows still leaves every view at least the 16
// content rows it gets at the 80x20 minimum (contentH = H - bannerRows - 3, so
// H >= 29 keeps 16). Set above the common test height (30) so the geometry tests
// that run at 30 keep the one-line header and their contentH budget unchanged.
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

// bannerSunArt / bannerSunColors draw a small glowing sun lit top-to-bottom
// gold → magenta like the mock's, its lower edge broken by ▀ half-blocks so a
// scanline gap reads across it.
var (
	bannerSunArt = []string{
		"▟█████████▙",
		"███████████",
		"▜█▀▀▀█▀▀▀█▛",
	}
	bannerSunColors = []string{"#FFDA5E", "#FF8A4D", "#FF4FC3"}
)

// bannerGridArt is the neon grid horizon: perspective lines converging on a
// center vanishing point. bannerGridColor is its violet neon.
const (
	bannerGridArt   = "▔╲▔▔╲▔▔╲▔▔╲▔▔│▔▔╱▔▔╱▔▔╱▔▔╱▔"
	bannerGridColor = "#B678FF"
)

// synthwaveBanner renders the bannerRows-line header centered to width. A
// width <= 0 (before the first WindowSizeMsg) skips centering and returns the
// raw block. The widest line is the ~56-cell wordmark, and the shell only
// reaches this path above minWidth (80), so a centered line never overflows.
func synthwaveBanner(width int) string {
	lines := make([]string, 0, bannerRows)
	for i, art := range bannerSunArt {
		lines = append(lines, lipgloss.NewStyle().
			Foreground(lipgloss.Color(bannerSunColors[i])).Bold(true).Render(art))
	}
	lines = append(lines, bannerWordmarkLines()...)
	lines = append(lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color(bannerGridColor)).Render(bannerGridArt))

	if width > 0 {
		for i, ln := range lines {
			lines[i] = lipgloss.PlaceHorizontal(width, lipgloss.Center, ln)
		}
	}
	return strings.Join(lines, "\n")
}

// bannerWordmarkLines renders the six-row block SENTRA with a static vertical
// sunset gradient (magenta crest → cyan base), reusing the splash's glyph data
// so there is one wordmark definition. No forced background: paintBackground
// fills the deep-space ground on truecolor, and on other profiles the neon sits
// on the terminal's own background with no boxed-in block.
func bannerWordmarkLines() []string {
	rows := make([]string, splashBigRows)
	for li, glyph := range splashBigLetters {
		for r := range splashBigRows {
			if li > 0 {
				rows[r] += splashBigGap
			}
			c := splashSunset[r%len(splashSunset)]
			rows[r] += lipgloss.NewStyle().
				Foreground(lipgloss.Color(c)).Bold(true).Render(glyph[r])
		}
	}
	return rows
}
