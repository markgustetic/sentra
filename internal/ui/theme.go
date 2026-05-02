// Package ui provides shared lipgloss-based styles and components used
// by both inline-mode CLI output and the future TUI. Every exported
// style is intended to be safe to copy and tweak via lipgloss's
// builder API without affecting the package-level defaults.
package ui

import "github.com/charmbracelet/lipgloss"

// Semantic styles. These are package-level lipgloss.Style values, not
// pointers — lipgloss styles are intentionally cheap to copy, and the
// builder methods (Bold, Italic, etc.) return new copies, so callers
// can derive variants without mutating the defaults.
var (
	// Primary is the brand colour, used for headings and emphasis.
	Primary = lipgloss.NewStyle().Foreground(lipgloss.Color("#7C3AED")).Bold(true)

	// Success indicates positive outcomes (snapshot OK, restore complete).
	Success = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))

	// Warn signals non-fatal issues that the user should notice.
	Warn = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))

	// Danger signals critical errors and security findings.
	Danger = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))

	// Muted is for de-emphasised text — timestamps, paths, hashes.
	Muted = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))

	// Subtle is a slightly lighter Muted, used for hints and labels.
	Subtle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))

	// Panel wraps grouped content in a rounded border with light padding.
	Panel = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

// Severity returns the lipgloss style appropriate for an agent
// finding's severity level. Recognised levels are "info", "warn",
// and "critical"; any other input (including the empty string) maps
// to Muted so callers can pass raw, possibly-malformed values without
// special-casing.
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
