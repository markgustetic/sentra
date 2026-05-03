package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Snapshots is a placeholder for Task 12.3.
type Snapshots struct {
	deps Deps
}

// NewSnapshots returns the v1 Snapshots model.
func NewSnapshots(deps Deps) Snapshots {
	return Snapshots{deps: deps}
}

// Init is a no-op.
func (Snapshots) Init() tea.Cmd { return nil }

// Update accepts any message; filled in by Task 12.3.
func (s Snapshots) Update(_ tea.Msg) (tea.Model, tea.Cmd) {
	return s, nil
}

// View renders a placeholder until Task 12.3 lands.
func (s Snapshots) View() string {
	return "snapshots\n"
}
