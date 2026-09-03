package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	tea "github.com/charmbracelet/bubbletea"
)

func testSidebar() Sidebar {
	return NewSidebar(testRegistry(), 18, 12)
}

func TestSidebar_RendersAllTitles(t *testing.T) {
	s := testSidebar()
	out := s.View()
	for _, want := range []string{"Dashboard", "Snapshots", "Diff"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing %q:\n%s", want, out)
		}
	}
}

func TestSidebar_ArrowMovesSelectionAndEnterActivates(t *testing.T) {
	s := testSidebar()
	s, _ = s.Update(tea.KeyMsg{Type: tea.KeyDown})
	s, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a sidebar item must produce a command")
	}
	msg := cmd()
	act, ok := msg.(activateMsg)
	if !ok {
		t.Fatalf("expected activateMsg, got %T", msg)
	}
	if act.id != "snapshots" {
		t.Fatalf("activated %q, want snapshots", act.id)
	}
	// Enter emits the activation but must not move the highlight — the
	// rail keeps tracking the row the user chose.
	if it, ok := s.list.SelectedItem().(sidebarItem); !ok || it.cmd.ID != "snapshots" {
		t.Fatalf("enter moved the sidebar selection: %+v", s.list.SelectedItem())
	}
}

// App.View pins the rail to sidebarWidth, and lipgloss wraps any row wider
// than that onto a second line: "Scheduled backups" once rendered as
// "Scheduled" / "backups", pushing every row below it down by one. The
// registry is the rule, not a view: every rail title, rendered through the
// delegate in both its inactive (2-cell indent) and active ("▍ " marker)
// forms, must fit on one line at sidebarWidth.
func TestSidebar_EveryRowFitsTheRailWidth(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	s := NewSidebar(app.registry, sidebarWidth, 12)
	for i, c := range app.registry.Commands() {
		s.list.Select(i)
		for _, state := range []struct {
			name  string
			index int
		}{{"active", i}, {"inactive", (i + 1) % len(app.registry.Commands())}} {
			var b strings.Builder
			sidebarDelegate{}.Render(&b, s.list, state.index, sidebarItem{cmd: c})
			if w := lipgloss.Width(b.String()); w > sidebarWidth {
				t.Errorf("%s row %q is %d cells wide; the rail is %d, so it wraps", state.name, c.Title, w, sidebarWidth)
			}
		}
	}
}
