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

// ViewWith renders the bar with a caller-chosen set of global hints. App decides
// which globals actually work in the current state (see App.statusGlobals): a
// bar that offers "tab focus · ? help · q quit" while a passphrase is being typed
// is lying — none of those reach the shell there.
func (s StatusBar) ViewWith(repoLabel string, viewKeys, globals []key.Binding, running string) string {
	return s.render(repoLabel, viewKeys, globals, running)
}

// render composes the bar from the view keys and the caller's global hints.
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
