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
// given reveal frame. A letter that has not appeared yet renders as spaces of
// its own width, so every row keeps its final length and the block never slides.
func splashBigWordmarkLines(elapsed time.Duration, brand lipgloss.Style) []string {
	rows := make([]string, splashBigRows)
	for li, glyph := range splashBigLetters {
		revealed := elapsed >= splashLettersAt+time.Duration(li)*splashLetterStep
		for r := 0; r < splashBigRows; r++ {
			if li > 0 {
				rows[r] += splashBigGap
			}
			if revealed {
				rows[r] += brand.Render(glyph[r])
			} else {
				rows[r] += strings.Repeat(" ", splashBigLetterWidths[li])
			}
		}
	}
	return rows
}
