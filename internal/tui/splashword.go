package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// splashBigRows is the number of rows in each block glyph.
const splashBigRows = 6

// splashBigLetters holds the six block glyphs (ANSI-Shadow figlet), one entry
// per wordmark letter, each exactly splashBigRows rows. Every row of a given
// letter is the same display width — splashBigLetterWidths asserts it at
// startup — so a space-padded unrevealed letter occupies its exact final cells
// and the lockup never shifts as it reveals.
var splashBigLetters = [][]string{
	{ // s
		"███████╗",
		"██╔════╝",
		"███████╗",
		"╚════██║",
		"███████║",
		"╚══════╝",
	},
	{ // e
		"███████╗",
		"██╔════╝",
		"█████╗  ",
		"██╔══╝  ",
		"███████╗",
		"╚══════╝",
	},
	{ // n
		"███╗   ██╗",
		"████╗  ██║",
		"██╔██╗ ██║",
		"██║╚██╗██║",
		"██║ ╚████║",
		"╚═╝  ╚═══╝",
	},
	{ // t
		"████████╗",
		"╚══██╔══╝",
		"   ██║   ",
		"   ██║   ",
		"   ██║   ",
		"   ╚═╝   ",
	},
	{ // r
		"██████╗ ",
		"██╔══██╗",
		"██████╔╝",
		"██╔══██╗",
		"██║  ██║",
		"╚═╝  ╚═╝",
	},
	{ // a
		" █████╗ ",
		"██╔══██╗",
		"███████║",
		"██╔══██║",
		"██║  ██║",
		"╚═╝  ╚═╝",
	},
}

// splashBigLetterWidths is each glyph's cell width, computed once. init panics
// if a glyph is not rectangular, so a mis-transcribed row is a build-time
// failure rather than a lockup that shifts at runtime.
var splashBigLetterWidths = func() []int {
	w := make([]int, len(splashBigLetters))
	for i, g := range splashBigLetters {
		if len(g) != splashBigRows {
			panic("splash glyph " + string(rune('0'+i)) + " has wrong row count")
		}
		w[i] = lipgloss.Width(g[0])
		for _, row := range g {
			if lipgloss.Width(row) != w[i] {
				panic("splash glyph " + string(rune('0'+i)) + " is not rectangular")
			}
		}
	}
	return w
}()

// splashBigGap is the blank column between adjacent block letters.
const splashBigGap = " "

// splashBigWordmarkLines composes the six rows of the block wordmark at the
// given frame. A letter that has not appeared yet renders as spaces of its own
// width, so every row keeps its final length and the block never slides.
//
// Each revealed cell is colored by splashLetterStyle, which flows the synthwave
// sunset down the rows and flashes a letter white as it lands — the wordmark is
// alive, not a flat fill. All of this is COLOR: under the Ascii profile tests
// render in, styling is stripped, so the block characters (and the splash
// golden) are identical to a single-color render.
func splashBigWordmarkLines(elapsed time.Duration) []string {
	rows := make([]string, splashBigRows)
	for li, glyph := range splashBigLetters {
		revealed := elapsed >= splashLettersAt+time.Duration(li)*splashLetterStep
		for r := 0; r < splashBigRows; r++ {
			if li > 0 {
				rows[r] += splashBigGap
			}
			if revealed {
				rows[r] += splashLetterStyle(r, li, elapsed).Render(glyph[r])
			} else {
				rows[r] += strings.Repeat(" ", splashBigLetterWidths[li])
			}
		}
	}
	return rows
}

// splashSunset is the neon ramp the wordmark flows through — magenta at the hot
// end sweeping to laser cyan — with enough stops that a six-row window shows a
// smooth gradient that shifts as its start index advances with time. Fixed neon
// hex (not adaptive): the splash is the launch flourish and commits to the dark
// palette.
var splashSunset = []string{
	"#FF4FC3", "#FF6BDD", "#E070FF", "#C98FFF",
	"#9B8CFF", "#6C9BFF", "#4DC8FF", "#5CEBFF",
}

// splashFlash is the white pop a letter shows for splashFlashDur as it lands,
// before it settles into the flowing gradient — the per-letter "flash".
var splashFlash = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Bold(true)

// splashLetterStyle picks the color for one letter's row at time elapsed: white
// while the letter is freshly revealed, otherwise the flowing sunset sampled at
// (row + time offset) so the gradient scrolls down the block.
func splashLetterStyle(row, letter int, elapsed time.Duration) lipgloss.Style {
	revealAt := splashLettersAt + time.Duration(letter)*splashLetterStep
	if since := elapsed - revealAt; since >= 0 && since < splashFlashDur {
		return splashFlash
	}
	phase := int(elapsed / splashFlowStep)
	c := splashSunset[((row+phase)%len(splashSunset)+len(splashSunset))%len(splashSunset)]
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true)
}

// splashGlyphColor pulses the ✦ through bright neons (and a white beat) so the
// crown glyph never sits still while the wordmark flows beneath it.
func splashGlyphColor(elapsed time.Duration) lipgloss.Color {
	beats := []string{"#FF6BDD", "#CB8CFF", "#5CEBFF", "#FFFFFF"}
	return lipgloss.Color(beats[int(elapsed/splashGlyphPulse)%len(beats)])
}

// Shimmer styles for the tagline and version lines. Fixed neon hex keeps the
// splash self-contained (like splashSunset); the base matches FgMuted's dark
// tone so the resting line reads the same as the rest of the chrome.
var (
	splashShimmerBase   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8E7DC8"))
	splashShimmerEdge   = lipgloss.NewStyle().Foreground(lipgloss.Color("#7FE6FF"))
	splashShimmerCrest  = lipgloss.NewStyle().Foreground(lipgloss.Color("#EAFDFF")).Bold(true)
	splashShimmerBright = lipgloss.NewStyle().Foreground(lipgloss.Color("#CBB8FF"))
)

// splashTextLine animates a secondary line (tagline, version): it flashes white
// as it first lands (since < splashFlashDur), then a bright crest sweeps across
// it forever. `since` gates the entry flash; `elapsed` drives the sweep so its
// phase stays continuous. All color — every rune keeps its cell, so the line
// width, the splash geometry, and the Ascii-profile golden are unchanged.
func splashTextLine(text string, elapsed, since time.Duration) string {
	if since >= 0 && since < splashFlashDur {
		return splashFlash.Render(text)
	}
	return splashShimmer(text, elapsed)
}

// splashShimmer renders text with a bright band sweeping across it — a crest cell
// with lit shoulders — over a dim base, looping with a gap so the pass repeats
// like a scanline reading the line. Per-rune coloring preserves every cell width.
func splashShimmer(text string, elapsed time.Duration) string {
	runes := []rune(text)
	n := len(runes)
	if n == 0 {
		return text
	}
	const gap = 12 // blank cells after the text before the crest wraps around
	pos := int(elapsed/splashShimmerStep) % (n + gap)
	var b strings.Builder
	for i, r := range runes {
		switch i - pos {
		case 0:
			b.WriteString(splashShimmerCrest.Render(string(r)))
		case -1, 1:
			b.WriteString(splashShimmerEdge.Render(string(r)))
		case -2, 2:
			b.WriteString(splashShimmerBright.Render(string(r)))
		default:
			b.WriteString(splashShimmerBase.Render(string(r)))
		}
	}
	return b.String()
}
