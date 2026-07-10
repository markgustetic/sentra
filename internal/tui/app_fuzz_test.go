package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fuzzKeys is the alphabet the fuzzer draws from. Every entry is a key a real
// operator can press on the shell. ctrl+c is deliberately excluded: it quits,
// which would truncate every sequence after the first occurrence and starve the
// fuzzer of interesting states.
var fuzzKeys = []tea.KeyMsg{
	{Type: tea.KeyEnter},
	{Type: tea.KeyTab},
	{Type: tea.KeyUp},
	{Type: tea.KeyDown},
	{Type: tea.KeyEsc},
	{Type: tea.KeyCtrlP},
	{Type: tea.KeyRunes, Runes: []rune{'?'}},
	{Type: tea.KeyRunes, Runes: []rune{'q'}},
	{Type: tea.KeyRunes, Runes: []rune{'1'}},
	{Type: tea.KeyRunes, Runes: []rune{'3'}},
	{Type: tea.KeyRunes, Runes: []rune{'a'}},
	{Type: tea.KeySpace},
	{Type: tea.KeyBackspace},
	{Type: tea.KeyLeft},
	{Type: tea.KeyRight},
}

// key indices, for readable seeds.
const (
	fkEnter = iota
	fkTab
	fkUp
	fkDown
)

// FuzzAppKeyRouting drives random key sequences through the shell and asserts
// the invariants that must hold in EVERY reachable state.
//
// This layer exists because view-level tests structurally cannot see it: a view
// only sees the keys the shell chose to give it, so a bug in that choice — a
// global binding eating 'q' out of a passphrase field, or an Enter that steals
// focus and leaves the rail unreachable — is invisible from inside the view.
// Both of those shipped.
//
// The seed corpus runs on every `go test`, so the regressions stay pinned even
// when nobody is fuzzing.
func FuzzAppKeyRouting(f *testing.F) {
	// The dashboard-freeze bug: Enter on the already-active rail item, then a
	// Down that no longer moved the rail.
	f.Add([]byte{fkEnter, fkDown})
	f.Add([]byte{fkDown, fkEnter, fkTab, fkDown})
	f.Add([]byte{fkTab, fkTab, fkEnter})
	f.Add([]byte{fkEnter, fkEnter, fkEnter})

	f.Fuzz(func(t *testing.T, seq []byte) {
		if len(seq) > 64 {
			seq = seq[:64] // keep sequences short; deep ones add no new states
		}
		app := NewApp(Deps{RepoName: "fuzz"})
		m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		app = m.(App)

		for _, b := range seq {
			key := fuzzKeys[int(b)%len(fuzzKeys)]

			railFocused := app.focus == focusSidebar
			activeBefore := app.active

			next, cmd := app.Update(key)
			app = next.(App)

			// Deliver exactly the message a real tea.Program would deliver here,
			// and nothing else. Running arbitrary cmds would sleep on tick timers
			// and, in the setup wizard, shell out to `aws` via tea.ExecProcess.
			if railFocused && key.Type == tea.KeyEnter && cmd != nil {
				if msg, ok := cmd().(activateMsg); ok {
					m2, _ := app.Update(msg)
					app = m2.(App)
				}
			}

			// Invariant 1: focus is always one of the two panes.
			if app.focus != focusSidebar && app.focus != focusContent {
				t.Fatalf("focus escaped its enum: %v", app.focus)
			}
			// Invariant 2: the active view index stays addressable.
			if app.active < 0 || app.active >= len(app.views) {
				t.Fatalf("active = %d, out of range [0,%d)", app.active, len(app.views))
			}
			// Invariant 3: the rail's highlight stays addressable.
			if i := app.sidebar.list.Index(); i < 0 || i >= len(app.views) {
				t.Fatalf("rail index = %d, out of range [0,%d)", i, len(app.views))
			}
			// Invariant 4: an Enter on the rail that navigates NOWHERE must not
			// move focus. This is the dashboard-freeze bug stated as a rule: a
			// keystroke with no visible effect must not silently disable the
			// rail's ↑/↓.
			if railFocused && key.Type == tea.KeyEnter &&
				app.active == activeBefore && app.focus != focusSidebar {
				t.Fatalf("enter navigated nowhere (active stayed %d) yet moved focus off the rail",
					activeBefore)
			}
		}

		// Invariant 5: the shell is never a dead end. With no overlay owning the
		// keyboard, tab must always be able to put focus back on the rail.
		if len(app.modals) == 0 && !app.paletteOpen && !app.inStartupGate() && !app.contentCapturesText() {
			m3, _ := app.Update(tea.KeyMsg{Type: tea.KeyTab})
			after := m3.(App)
			if after.focus == app.focus {
				t.Fatalf("tab did not move focus (stuck on %v) — the shell is a dead end", app.focus)
			}
		}
	})
}
