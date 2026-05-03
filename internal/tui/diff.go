package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Diff is a placeholder for Task 12.4.
type Diff struct {
	deps Deps
}

// NewDiff returns the v1 Diff model.
func NewDiff(deps Deps) Diff {
	return Diff{deps: deps}
}

// Init is a no-op.
func (Diff) Init() tea.Cmd { return nil }

// Update accepts any message; filled in by Task 12.4.
func (d Diff) Update(_ tea.Msg) (tea.Model, tea.Cmd) {
	return d, nil
}

// View renders a placeholder until Task 12.4 lands.
func (d Diff) View() string {
	return "diff\n"
}
