package tui

import (
	"strings"
	"testing"

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
