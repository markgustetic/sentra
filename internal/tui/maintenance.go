package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/ui"
)

// maintenanceEntry is one row of the Maintenance launcher: a label, the
// one-line "why would I run this", and the view id enter routes to.
type maintenanceEntry struct {
	label    string
	desc     string
	targetID string
}

// MaintenanceView is the launcher that holds the occasional-care jobs —
// check, prune, sync, doctor — under a single rail slot. They share a
// shape (run → progress → result) and a cadence (rarely), so they don't
// each earn a place on the main menu; the views themselves are unchanged
// and still own their flows. Like Settings' navigate entries, enter merely
// emits an activateMsg the shell already routes — this view owns no
// goroutines and takes no op guard.
type MaintenanceView struct {
	deps    Deps
	entries []maintenanceEntry
	cursor  int
	width   int
}

func NewMaintenanceView(deps Deps) MaintenanceView {
	return MaintenanceView{
		deps: deps,
		entries: []maintenanceEntry{
			{label: "Check", desc: "verify snapshot integrity", targetID: "check"},
			{label: "Prune", desc: "apply retention and reclaim space", targetID: "prune"},
			{label: "Sync", desc: "replicate this repository to another bucket", targetID: "sync"},
			{label: "Doctor", desc: "diagnose AWS access and repository health", targetID: "doctor"},
		},
	}
}

func (MaintenanceView) Init() tea.Cmd { return nil }

// ConsumesArrows: the entry cursor is always present.
func (v MaintenanceView) ConsumesArrows() bool { return true }

func (v MaintenanceView) Title() string { return "Maintenance" }

func (v MaintenanceView) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "job")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "open")),
	}
}

func (v MaintenanceView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyUp:
			if v.cursor > 0 {
				v.cursor--
			}
			return v, nil
		case tea.KeyDown:
			if v.cursor < len(v.entries)-1 {
				v.cursor++
			}
			return v, nil
		case tea.KeyEnter:
			id := v.entries[v.cursor].targetID
			return v, func() tea.Msg { return activateMsg{id: id} }
		}
	}
	return v, nil
}

func (v MaintenanceView) View() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Maintenance"))
	fmt.Fprintf(&b, "\n%s", ui.Muted.Render("occasional care for the repository"))
	b.WriteString("\n")
	for i, e := range v.entries {
		fmt.Fprintf(&b, "\n%s  %s",
			ui.SelectRow(i == v.cursor, e.label), ui.Muted.Render(e.desc))
	}
	fmt.Fprintf(&b, "\n\n%s", ui.Muted.Render("↑/↓ select · ⏎ open"))
	return b.String()
}
