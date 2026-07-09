package tui

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/ui"
)

// StatusBar renders the bottom row: repo identity, the running-
// operation indicator (Phase 2 populates it), and contextual key
// hints (view keys first, then global keys) via bubbles/help.
type StatusBar struct {
	keys  globalKeymap
	help  help.Model
	width int
}

func NewStatusBar(keys globalKeymap, width int) StatusBar {
	h := help.New()
	return StatusBar{keys: keys, help: h, width: width}
}

// View renders one line. viewKeys are the active view's ShortHelp
// bindings; running is the global operation indicator ("" when idle).
func (s StatusBar) View(repoLabel string, viewKeys []key.Binding, running string) string {
	return s.render(repoLabel, viewKeys, s.keys.shortHelp(), running)
}

// ViewGated renders the bar for a startup gate, where the nav globals
// (palette, focus) are suppressed and '?'/'q' route to the gate view or quit.
// It advertises only quit alongside the view's own keys, so the hints never
// point at keys the gate ignores.
func (s StatusBar) ViewGated(repoLabel string, viewKeys []key.Binding, running string) string {
	return s.render(repoLabel, viewKeys, []key.Binding{s.keys.Quit}, running)
}

// render composes the bar from the view keys and a caller-chosen set of
// global hints, so View (all globals) and ViewGated (quit only) share one body.
func (s StatusBar) render(repoLabel string, viewKeys, globals []key.Binding, running string) string {
	bindings := append(append([]key.Binding{}, viewKeys...), globals...)
	hints := s.help.ShortHelpView(bindings)

	left := ui.Subtle.Render(repoLabel)
	if running != "" {
		left += "  " + ui.Warn.Render("⟳ "+running)
	}
	gap := s.width - lipgloss.Width(left) - lipgloss.Width(hints) - 2
	if gap < 1 {
		gap = 1
	}
	// Clamp to the bar's width. When a view contributes enough ShortHelp
	// keys that left + hints exceed s.width, the gap floors at 1 and the
	// row would otherwise spill past the terminal (and, in the shell,
	// push every JoinVertical row out to the overflowing width). MaxWidth
	// truncates the rendered bar so it never exceeds its budget; the
	// hints on the right are what get clipped, which is the right
	// trade-off — the repo label and highest-priority keys stay visible.
	return ui.StatusBar.MaxWidth(s.width).
		Render(left + lipgloss.NewStyle().Width(gap).Render("") + hints)
}

// SetWidth resizes the bar (called on WindowSizeMsg).
func (s *StatusBar) SetWidth(w int) {
	s.width = w
	s.help.Width = w
}
