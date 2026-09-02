// Package ui provides the shared Charm-expressive theme used by both
// inline-mode CLI output and the TUI. Every exported style is safe to
// copy and tweak via lipgloss's builder API without mutating defaults.
//
// All colors are lipgloss.AdaptiveColor pairs so light-background
// terminals stay readable; lipgloss handles NO_COLOR and color-profile
// degradation automatically.
package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette — synthwave / outrun. Dark variants are saturated neons that glow
// against a dark terminal (electric purple, hot magenta, laser cyan, a sunset
// gold); Light variants stay legible on white terminals. lipgloss picks per
// background and strips to nothing under Ascii/NO_COLOR, so the neons are a
// dark-terminal flourish, never a legibility risk elsewhere.
var (
	AccentViolet = lipgloss.AdaptiveColor{Dark: "#CB8CFF", Light: "#7C3AED"} // electric purple
	AccentPink   = lipgloss.AdaptiveColor{Dark: "#FF6BDD", Light: "#DB2777"} // hot magenta
	AccentAqua   = lipgloss.AdaptiveColor{Dark: "#5CEBFF", Light: "#0E7490"} // laser cyan
	GoodGreen    = lipgloss.AdaptiveColor{Dark: "#5CFFB4", Light: "#059669"} // neon mint
	WarnAmber    = lipgloss.AdaptiveColor{Dark: "#FFD84D", Light: "#D97706"} // sunset gold
	BadRed       = lipgloss.AdaptiveColor{Dark: "#FF6B86", Light: "#DC2626"} // neon red
	FgMuted      = lipgloss.AdaptiveColor{Dark: "#8E7DC8", Light: "#6B7280"} // dim indigo
	FgSubtle     = lipgloss.AdaptiveColor{Dark: "#CBB8FF", Light: "#9CA3AF"} // soft lilac
)

// Semantic styles (pre-existing names; CLI callers depend on them). The status
// accents are bold so a neon foreground reads like lit signage rather than a
// thin stroke — the "glow" of the synthwave theme. Muted/Subtle stay unweighted;
// secondary text should recede, not glow.
var (
	Primary = lipgloss.NewStyle().Foreground(AccentViolet).Bold(true)
	Success = lipgloss.NewStyle().Foreground(GoodGreen).Bold(true)
	Warn    = lipgloss.NewStyle().Foreground(WarnAmber).Bold(true)
	Danger  = lipgloss.NewStyle().Foreground(BadRed).Bold(true)
	Muted   = lipgloss.NewStyle().Foreground(FgMuted)
	Subtle  = lipgloss.NewStyle().Foreground(FgSubtle)
	Panel   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(AccentViolet).Padding(0, 1)
)

// Shell styles (TUI chrome).
var (
	// TitleBar renders the top brand bar: pink brand on a violet-
	// bordered row. (Gradients aren't native to lipgloss v1; the
	// pink-on-violet pairing is the approved approximation.)
	TitleBar = lipgloss.NewStyle().Foreground(AccentPink).Bold(true).Padding(0, 1)

	// SidebarItem / SidebarActive style nav-rail entries. The active
	// marker "▍" is part of the style contract (selection stays visible
	// with colors stripped) — the sidebar prepends it when rendering.
	SidebarItem   = lipgloss.NewStyle().Foreground(FgSubtle).PaddingLeft(2)
	SidebarActive = lipgloss.NewStyle().Foreground(AccentPink).Bold(true).
			SetString("▍ ")

	// StatusBar styles the bottom hint row.
	StatusBar = lipgloss.NewStyle().Foreground(FgMuted).Padding(0, 1)

	// ModalBox frames modal overlays (confirm / error dialogs).
	ModalBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(AccentPink).Padding(1, 2)

	// PanelFocused is Panel with the aqua focus accent, for the pane
	// that currently owns keyboard focus.
	PanelFocused = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(AccentAqua).Padding(0, 1)

	// FieldBox frames the one text field currently accepting input. The
	// border is the focus affordance itself — a glyph, visible without
	// color — so views must apply it only to the focused field. Padding
	// matches Panel so boxed fields align with panel content.
	//
	// DO NOT add a Foreground (or any other text-level attribute) here.
	// Callers wrap an ALREADY-STYLED textinput.View(), which is only safe
	// while this style touches the border alone: Render then emits SGR on
	// the border runes and passes the content through untouched. Give it a
	// Foreground and the content comes back as
	// "[92m[91mINNER[0m[0m" — the inner reset closes the
	// OUTER style too, which is exactly the house "never wrap an
	// already-styled string" bug.
	FieldBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(AccentAqua).Padding(0, 1)

	// FieldBoxOverhead is the horizontal cost of wrapping a field in
	// FieldBox (2 border columns + 2 padding). A call site that sizes its
	// textinput from the pane's interior must subtract this, or the framed
	// line overflows the panel the moment the field takes focus. Derived
	// from the style rather than written as a literal 4 so it can't drift
	// if FieldBox's border or padding ever changes.
	FieldBoxOverhead = FieldBox.GetHorizontalFrameSize()
)

// Severity returns the style for an agent finding's severity level.
// Unrecognized levels (including "") map to Muted so callers can pass
// raw values without special-casing.
func Severity(level string) lipgloss.Style {
	switch level {
	case "critical":
		return Danger.Bold(true)
	case "warn":
		return Warn
	case "info":
		return Subtle
	default:
		return Muted
	}
}

// rowSelected / rowPlain back SelectRow. They mirror the sidebar's contract so
// a selectable row inside a content view reads the same as a nav-rail entry.
var (
	rowSelected = lipgloss.NewStyle().Foreground(AccentPink).Bold(true)
	rowPlain    = lipgloss.NewStyle().Foreground(FgSubtle)
)

// SectionTitle renders a panel/section heading so it reads as chrome, not
// data: UPPERCASE in a bold accent. Uppercasing is the load-bearing half —
// bold and color vanish under NO_COLOR, a pipe, and the Ascii profile the
// tests run in; the capitals survive everywhere. Same rationale as
// SelectRow's glyph marker below.
func SectionTitle(label string) string {
	return sectionTitleStyle.Render(strings.ToUpper(label))
}

var sectionTitleStyle = lipgloss.NewStyle().Foreground(AccentPink).Bold(true)

// SelectRow renders one row of a selectable list: the "▍" marker plus the
// accent color when selected, two spaces and the subtle color when not. Both
// states are two cells wide before the label, so the column never shifts as the
// cursor moves.
//
// The marker is load-bearing, not decoration. Colors vanish under NO_COLOR, a
// pipe, or a 2-color terminal — and lipgloss renders no ANSI at all under the
// Ascii profile every unit test runs in. The marker is what keeps the selection
// legible, and testable, in all of them.
//
// label must be UNSTYLED. Wrapping an already-styled string embeds an ANSI reset
// that terminates the outer style mid-line, silently un-coloring the tail of the
// row. Append muted help text after the call, outside the styled span.
func SelectRow(selected bool, label string) string {
	if selected {
		return rowSelected.Render("▍ " + label)
	}
	return rowPlain.Render("  " + label)
}

// ActionLine renders a view's footer: the primary action in the accent style,
// then the secondary keys demoted to muted on the line below. It is the one
// place the "Press enter to …" convention lives, so every view reads the same.
//
// primary is a verb phrase without the ⏎ glyph — "start the backup", "run the
// integrity check" — which ActionLine renders as "⏎  Press enter to start the
// backup". An empty primary drops the accent line (a view with no enter action,
// only secondary keys). secondary may be empty.
//
// It returns the two lines with NO leading newline; the caller places it.
func ActionLine(primary, secondary string) string {
	var out string
	if primary != "" {
		out = Primary.Render("⏎  Press enter to " + primary)
	}
	if secondary != "" {
		if out != "" {
			out += "\n"
		}
		out += Muted.Render("   " + secondary)
	}
	return out
}
