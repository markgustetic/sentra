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
	bindings := append(append([]key.Binding{}, viewKeys...), s.keys.shortHelp()...)
	hints := s.help.ShortHelpView(bindings)

	left := ui.Subtle.Render(repoLabel)
	if running != "" {
		left += "  " + ui.Warn.Render("⟳ "+running)
	}
	gap := s.width - lipgloss.Width(left) - lipgloss.Width(hints) - 2
	if gap < 1 {
		gap = 1
	}
	return ui.StatusBar.Render(left + lipgloss.NewStyle().Width(gap).Render("") + hints)
}

// SetWidth resizes the bar (called on WindowSizeMsg).
func (s *StatusBar) SetWidth(w int) {
	s.width = w
	s.help.Width = w
}
