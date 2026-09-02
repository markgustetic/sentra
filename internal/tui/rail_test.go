package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The rail contract: exactly seven destinations, in this order — the daily
// loop (backup, its schedules, snapshots) plus the three hubs. Everything
// else is hidden from the rail/palette and launched from a parent.
func TestApp_RailShowsExactlySevenViews(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	want := []string{"dashboard", "backup", "jobs", "snapshots", "maintenance", "settings", "help"}
	got := app.registry.Commands()
	if len(got) != len(want) {
		ids := make([]string, len(got))
		for i, c := range got {
			ids[i] = c.ID
		}
		t.Fatalf("registry has %d commands %v, want %v", len(got), ids, want)
	}
	for i, c := range got {
		if c.ID != want[i] {
			t.Errorf("rail[%d] = %q, want %q", i, c.ID, want[i])
		}
	}
}

// Hidden must never mean unreachable: every demoted view stays in the
// views slice and reachable through activateMsg — the message its
// launcher (Snapshots / Maintenance / Settings) emits.
func TestApp_DemotedViewsStayRoutable(t *testing.T) {
	for _, id := range []string{
		"diff", "restore", "check", "prune", "sync", "doctor",
		"recovery-kit", "password", "setup",
	} {
		t.Run(id, func(t *testing.T) {
			app := NewApp(Deps{RepoName: "x"})
			sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m, _ := sized.(App).Update(activateMsg{id: id})
			got := m.(App)
			if got.views[got.active].id != id {
				t.Fatalf("activateMsg{%q} left active on %q", id, got.views[got.active].id)
			}
		})
	}
}

// Number keys jump between RAIL views only. The views slice also holds
// the hidden views (demoted ones and startup gates), so a raw index jump
// would land on screens the rail never shows — 8 must be a no-op with
// seven rail entries, not a teleport to whatever sits at slice index 7.
func TestApp_NumberKeysSkipHiddenViews(t *testing.T) {
	r := newFlowRepo(t)
	app := NewApp(Deps{RepoName: "x", Repo: r})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ := sized.(App).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'8'}})
	if got := m.(App); got.views[got.active].id != app.views[app.active].id {
		t.Fatalf("number key 8 jumped to hidden view %q", got.views[got.active].id)
	}
	m, _ = sized.(App).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if got := m.(App); got.views[got.active].id != "jobs" {
		t.Fatalf("number key 3 = %q, want jobs", got.views[got.active].id)
	}
	m, _ = sized.(App).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	if got := m.(App); got.views[got.active].id != "maintenance" {
		t.Fatalf("number key 5 = %q, want maintenance", got.views[got.active].id)
	}
}
