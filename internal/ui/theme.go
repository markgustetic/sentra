// Package ui provides the shared Charm-expressive theme used by both
// inline-mode CLI output and the TUI. Every exported style is safe to
// copy and tweak via lipgloss's builder API without mutating defaults.
//
// All colors are lipgloss.AdaptiveColor pairs so light-background
// terminals stay readable; lipgloss handles NO_COLOR and color-profile
// degradation automatically.
package ui

import "github.com/charmbracelet/lipgloss"

// Palette. Dark variants are the Charm-expressive rose-pine-adjacent
// tones approved in the design; Light variants are their saturated
// counterparts for white terminals.
var (
	AccentViolet = lipgloss.AdaptiveColor{Dark: "#C4A7E7", Light: "#7C3AED"}
	AccentPink   = lipgloss.AdaptiveColor{Dark: "#F6A8D8", Light: "#DB2777"}
	AccentAqua   = lipgloss.AdaptiveColor{Dark: "#9CCFD8", Light: "#0E7490"}
	GoodGreen    = lipgloss.AdaptiveColor{Dark: "#95E6B8", Light: "#059669"}
	WarnAmber    = lipgloss.AdaptiveColor{Dark: "#F6C177", Light: "#D97706"}
	BadRed       = lipgloss.AdaptiveColor{Dark: "#EB6F92", Light: "#DC2626"}
	FgMuted      = lipgloss.AdaptiveColor{Dark: "#6E6A86", Light: "#6B7280"}
	FgSubtle     = lipgloss.AdaptiveColor{Dark: "#908CAA", Light: "#9CA3AF"}
)

// Semantic styles (pre-existing names; CLI callers depend on them).
var (
	Primary = lipgloss.NewStyle().Foreground(AccentViolet).Bold(true)
	Success = lipgloss.NewStyle().Foreground(GoodGreen)
	Warn    = lipgloss.NewStyle().Foreground(WarnAmber)
	Danger  = lipgloss.NewStyle().Foreground(BadRed)
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
