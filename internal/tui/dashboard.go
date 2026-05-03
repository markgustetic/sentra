package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Dashboard is a placeholder for Task 12.2. The minimal surface here
// is what the App skeleton (Task 12.1) needs to compile and route to
// the view — Task 12.2 fills it in with real panels.
type Dashboard struct {
	deps Deps
}

// NewDashboard returns a v1 Dashboard model. Construction is cheap;
// data is read on demand in View.
func NewDashboard(deps Deps) Dashboard {
	return Dashboard{deps: deps}
}

// Init is a no-op for the dashboard — it has no background work.
func (Dashboard) Init() tea.Cmd { return nil }

// Update accepts any message and returns the model unchanged. The
// dashboard has no input bindings yet (the parent App handles tabs
// and quit). Future drill-ins will live here.
func (d Dashboard) Update(_ tea.Msg) (tea.Model, tea.Cmd) {
	return d, nil
}

// View renders the dashboard. Filled in by Task 12.2.
func (d Dashboard) View() string {
	return "dashboard\n"
}
