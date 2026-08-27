package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The Maintenance launcher exists so check/prune/sync/doctor can leave the
// rail without leaving the TUI: one rail slot, four occasional jobs. It is
// a pure launcher — enter emits activateMsg; the shell routes it, exactly
// like Settings' navigate entries.
func TestMaintenance_ListsAllFourJobs(t *testing.T) {
	v := NewMaintenanceView(Deps{})
	out := v.View()
	for _, want := range []string{"Check", "Prune", "Sync", "Doctor"} {
		if !strings.Contains(out, want) {
			t.Errorf("maintenance view missing %q:\n%s", want, out)
		}
	}
}

func TestMaintenance_EnterActivatesSelectedTarget(t *testing.T) {
	v := NewMaintenanceView(Deps{})
	// ↓↓ lands on the third entry (sync); enter must route there.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.(MaintenanceView).Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd := m.(MaintenanceView).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	act, ok := cmd().(activateMsg)
	if !ok {
		t.Fatalf("enter produced %T, want activateMsg", cmd())
	}
	if act.id != "sync" {
		t.Fatalf("enter activated %q, want %q", act.id, "sync")
	}
}

func TestMaintenance_CursorBoundsAndGlyph(t *testing.T) {
	v := NewMaintenanceView(Deps{})
	if view := v.View(); !lineSelected(view, "Check") {
		t.Fatalf("first frame must select the first row:\n%s", view)
	}
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyUp})
	if view := m.(MaintenanceView).View(); !lineSelected(view, "Check") {
		t.Fatalf("up at the top must stay on the first row:\n%s", view)
	}
	for range 9 {
		mm, _ := m.(MaintenanceView).Update(tea.KeyMsg{Type: tea.KeyDown})
		m = mm
	}
	if view := m.(MaintenanceView).View(); !lineSelected(view, "Doctor") {
		t.Fatalf("down past the end must stay on the last row:\n%s", view)
	}
}

// Arrow keys drive the entry cursor, so the shell must not steal them for
// the rail (see App.routeKey's arrow fallback).
func TestMaintenance_ConsumesArrows(t *testing.T) {
	if !NewMaintenanceView(Deps{}).ConsumesArrows() {
		t.Fatal("maintenance must consume arrows for its cursor")
	}
}
