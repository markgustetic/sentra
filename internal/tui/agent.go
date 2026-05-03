package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// AgentView is a placeholder for Task 12.5.
type AgentView struct {
	deps Deps
}

// NewAgentView returns the v1 AgentView model.
func NewAgentView(deps Deps) AgentView {
	return AgentView{deps: deps}
}

// Init is a no-op for the placeholder.
func (AgentView) Init() tea.Cmd { return nil }

// Update accepts any message; filled in by Task 12.5.
func (a AgentView) Update(_ tea.Msg) (tea.Model, tea.Cmd) {
	return a, nil
}

// View renders a placeholder until Task 12.5 lands.
func (a AgentView) View() string {
	return "agent\n"
}
