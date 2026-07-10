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
	// The snapshots-freeze bug: walk the rail to a DIFFERENT view, activate it
	// (focus moves to the content pane), then press Down. With Deps{} that view
	// has no rows and cannot use the arrow, so the rail must take it. Without a
	// seed that actually reaches a content-focused inert view, invariant 5 never
	// fires.
	f.Add([]byte{fkDown, fkEnter, fkDown})
	f.Add([]byte{fkDown, fkDown, fkEnter, fkDown, fkDown})
	// The keyboard-trap bug: walk to Backup (rail row 2), activate it, then tab
	// into the tag field, where the view captures text. Invariant 6 must find esc
	// still works there. Without this seed the corpus never reaches a
	// text-capturing view at all, and the trap goes unnoticed.
	f.Add([]byte{fkDown, fkDown, fkEnter, fkTab})

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
			railIdxBefore := app.sidebar.list.Index()
			// Captured BEFORE the update: could the focused view have used an
			// arrow, and did an overlay own the keyboard?
			viewCouldTakeArrow := !railFocused && app.contentConsumesArrows()
			overlayOwnedKeys := len(app.modals) > 0 || app.paletteOpen ||
				app.inStartupGate() || app.contentCapturesText() || app.splashActive ||
				app.tooSmall()

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

			// Invariant 5: a Down key never does nothing. Eight views never use
			// ↑/↓ and six more use them only when they have rows. Dropping the
			// key in those states — while the unfocused rail had also stopped
			// responding — is what made the app look frozen, twice, to a real
			// user. So if the focused view could not have used it, the rail must
			// have taken it.
			//
			// Down rather than Up because the rail starts at index 0, where Up is
			// legitimately a no-op. The bound is the RAIL's item count, not the
			// view count: `unlock` is hidden from the rail (16 items, 17 views),
			// so at the last rail row Down is correctly a no-op. Fuzzing found
			// this — the first version of this invariant used len(views)-1 and
			// reported a bug that did not exist.
			if key.Type == tea.KeyDown && !overlayOwnedKeys && !viewCouldTakeArrow &&
				railIdxBefore < len(app.sidebar.list.Items())-1 &&
				app.sidebar.list.Index() == railIdxBefore {
				t.Fatalf("down did nothing: view %q could not use it and the rail stayed at %d",
					app.views[activeBefore].id, railIdxBefore)
			}
		}

		// Invariant 6: the shell is never a dead end — esc always gets you back to
		// the rail unless the focused view means something by it.
		//
		// The earlier version of this invariant asserted "tab always moves focus"
		// and EXCLUDED text-capturing views. That exclusion was the bug: on
		// Backup's tag field and on Password, esc, tab and ctrl+p were all
		// swallowed and only ctrl+c escaped, which quits the app. An invariant
		// that carves out the broken case cannot catch it.
		overlay := len(app.modals) > 0 || app.paletteOpen || app.inStartupGate() ||
			app.splashActive || app.tooSmall()
		viewOwnsEscape := app.focus == focusContent && app.contentConsumesEscape()
		if !overlay && !viewOwnsEscape {
			m3, _ := app.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if after := m3.(App); after.focus != focusSidebar {
				t.Fatalf("esc did not return focus to the rail from view %q — the shell is a dead end",
					app.views[app.active].id)
			}
		}
	})
}
