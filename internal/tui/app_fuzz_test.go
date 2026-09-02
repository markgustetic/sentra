package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
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
	fkEsc
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
	// The keyboard-trap bug: walk to Backup (rail row 1), activate it, then
	// enter twice more to reach the wizard's Confirm step, where the tag field
	// captures text. Invariant 6 must find esc still works there. Without this
	// seed the corpus never reaches a text-capturing view at all, and the trap
	// goes unnoticed — TestFuzzSeed_ReachesATextCapturingView asserts the seed
	// still lands in a field, so it cannot go stale in silence.
	f.Add(keyTrapSeed)
	// The re-enter case invariant 4 once misread as a bug: dive into Backup,
	// esc back to the rail, and enter AGAIN on the same row. The view is
	// already active, so the index never changes, yet focus must move back
	// into its pane — that Enter is the only way to resume the wizard.
	f.Add([]byte{fkDown, fkEnter, fkEsc, fkEnter})

	// ONE repo for the whole run, not one per iteration: repo.Init runs
	// Argon2id. Without it every flow that stats a directory refuses ("no
	// repository configured"), which pins the backup wizard on its first step
	// and starves the states below of anything to check.
	r := newFlowRepo(f)

	f.Fuzz(func(t *testing.T, seq []byte) {
		if len(seq) > 64 {
			seq = seq[:64] // keep sequences short; deep ones add no new states
		}
		app := fuzzApp(t, r)

		for _, b := range seq {
			key := fuzzKeys[int(b)%len(fuzzKeys)]

			railFocused := app.focus == focusSidebar
			activeBefore := app.active
			railIdxBefore := app.sidebar.list.Index()
			railViewBefore := app.railView()
			// Captured BEFORE the update: could the focused view have used an
			// arrow, and did an overlay own the keyboard?
			viewCouldTakeArrow := !railFocused && app.contentConsumesArrows()
			overlayOwnedKeys := len(app.modals) > 0 || app.paletteOpen ||
				app.inStartupGate() || app.contentCapturesText() || app.splashActive ||
				app.tooSmall()

			app = fuzzPress(app, key)

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
			// Invariant 4: a rail Enter lands focus exactly where the highlighted
			// view can use it. The view under the rail's cursor becomes active,
			// and focus moves into its pane if and only if that view is
			// focusable — a view that declares InertContent (the Dashboard)
			// keeps focus on the rail.
			//
			// Both directions shipped as bugs. Focus moving into the inert
			// Dashboard is the dashboard-freeze: nothing visibly happened, and
			// ↑/↓ then went to a pane that ignores them. Focus NOT moving into a
			// focusable view is the swallowed-Enter: the activate handler once
			// skipped the focus move when the view was "already active", which
			// under live rail preview is every view you scroll to.
			//
			// That is also why this is not stated as "an Enter that navigates
			// nowhere must not move focus", as it first was: live preview
			// (navPreviewMsg, which fuzzPress delivers) makes the highlighted
			// view active BEFORE Enter, so on the real shell a rail Enter
			// never changes m.active, and "navigated nowhere" is the normal
			// case, not a symptom. Whether focus should move is decided by the
			// view, not by whether the index changed.
			if railFocused && key.Type == tea.KeyEnter && !overlayOwnedKeys && railViewBefore >= 0 {
				if app.active != railViewBefore {
					t.Fatalf("enter on rail row %d (view %q) activated view %q instead",
						railIdxBefore, app.views[railViewBefore].id, app.views[app.active].id)
				}
				// The oracle reads the view's declaration itself rather than
				// asking the shell's contentFocusable: a shell that ignored
				// the declaration would agree with an oracle that shared its
				// helper, and the mutation would pass. (Whether the Dashboard
				// SHOULD declare itself inert is the Dashboard's contract,
				// pinned by TestApp_EnterOnAlreadyActiveRailItemKeepsRailUsable.)
				ic, declares := app.views[railViewBefore].model.(inertContent)
				wantContent := !declares || !ic.InertContent()
				if gotContent := app.focus == focusContent; gotContent != wantContent {
					t.Fatalf("enter on view %q (focusable=%v) left focus=%v — want content focus iff the view can use it",
						app.views[railViewBefore].id, wantContent, app.focus)
				}
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

		// Invariant 6: the shell is never a dead end — esc always makes progress.
		// A focused view that doesn't own esc returns focus to the rail; a running
		// op is cancelled in place. esc pops no confirm — quit is the one guarded
		// action — so it never leaves the operator stranded on a screen.
		//
		// The earlier version asserted "tab always moves focus" and EXCLUDED
		// text-capturing views. That exclusion was the bug: on Backup's tag field
		// and Password, esc/tab/ctrl+p were all swallowed and only ctrl+c escaped.
		// An invariant that carves out the broken case cannot catch it.
		overlay := len(app.modals) > 0 || app.paletteOpen || app.inStartupGate() ||
			app.splashActive || app.tooSmall()
		viewOwnsEscape := app.focus == focusContent && app.contentConsumesEscape()
		if !overlay && !viewOwnsEscape {
			m3, _ := app.Update(tea.KeyMsg{Type: tea.KeyEsc})
			after := m3.(App)
			// Progress = focus reached the rail, OR a running op was cancelled in
			// place (esc keeps focus on the screen so the cancel is visible).
			leftToRail := after.focus == focusSidebar
			cancelingOp := app.opRunning != ""
			if !leftToRail && !cancelingOp {
				t.Fatalf("esc made no progress from view %q — the shell is a dead end",
					app.views[app.active].id)
			}
		}
	})
}

// keyTrapSeed walks the rail to Backup, activates it, then presses enter
// twice more to reach the wizard's Confirm step and its tag field. It is the
// corpus's only route to a text-capturing view, so it is a named value the
// guard test below can replay.
var keyTrapSeed = []byte{fkDown, fkEnter, fkEnter, fkEnter}

// railView returns the index into m.views of the view under the rail's
// cursor — the one a rail Enter activates. It can differ from m.active: a
// launcher can activate a hidden view (restore, diff) the rail has no row
// for, and Select leaves the cursor where it was.
func (m App) railView() int {
	it, ok := m.sidebar.list.SelectedItem().(sidebarItem)
	if !ok {
		return -1
	}
	for i, v := range m.views {
		if v.id == it.cmd.ID {
			return i
		}
	}
	return -1
}

// fuzzApp builds the shell the fuzz target drives: a real repo (so the flows
// that validate against one actually advance) and a real window size.
func fuzzApp(tb testing.TB, r *repo.Repo) App {
	tb.Helper()
	app := NewApp(Deps{RepoName: "fuzz", Repo: r})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m.(App)
}

// fuzzPress delivers one key and then exactly the navigation message a real
// tea.Program would deliver next, and nothing else. Running arbitrary cmds
// would sleep on tick timers and, in the setup wizard, shell out to `aws` via
// tea.ExecProcess.
//
// Two rail messages are delivered: activateMsg from Enter, and navPreviewMsg
// from an arrow the rail handled — whether the rail was focused or took the
// arrow through the never-dead fallback (focus ends on the rail either way).
// The preview matters: it is what makes the highlighted view active BEFORE
// Enter on the real shell, so without it the fuzzer never sees a rail Enter
// on an already-active view, and invariant 4 checks a state the shell can
// never reach.
func fuzzPress(app App, key tea.KeyMsg) App {
	railFocused := app.focus == focusSidebar
	shellOwnsKeys := len(app.modals) == 0 && !app.paletteOpen &&
		!app.inStartupGate() && !app.splashActive && !app.tooSmall()
	next, cmd := app.Update(key)
	app = next.(App)
	if cmd == nil {
		return app
	}
	enterOnRail := railFocused && key.Type == tea.KeyEnter
	arrowToRail := shellOwnsKeys && isArrowKey(key) && app.focus == focusSidebar
	if !enterOnRail && !arrowToRail {
		return app
	}
	return fuzzDeliverNav(app, cmd())
}

// fuzzDeliverNav delivers a rail navigation message to the App, looking one
// batch deep because the sidebar batches its preview with the list's own cmd.
// Every other message is dropped, per fuzzPress's contract.
func fuzzDeliverNav(app App, msg tea.Msg) App {
	switch msg := msg.(type) {
	case activateMsg, navPreviewMsg:
		m2, _ := app.Update(msg)
		return m2.(App)
	case tea.BatchMsg:
		for _, c := range msg {
			if c != nil {
				app = fuzzDeliverNav(app, c())
			}
		}
	}
	return app
}

// TestFuzzSeed_ReachesATextCapturingView keeps the corpus honest. Invariant 6
// — esc always makes progress — only bites where a view captures text, and
// exactly one seed reaches such a state. That seed has gone stale twice
// already (the rail grew a row; the backup view stopped focusing a field on
// tab), and a stale seed fails silently: the fuzzer still passes, having
// checked nothing. Assert the destination, not the keystrokes.
func TestFuzzSeed_ReachesATextCapturingView(t *testing.T) {
	app := fuzzApp(t, newFlowRepo(t))
	for _, b := range keyTrapSeed {
		app = fuzzPress(app, fuzzKeys[int(b)%len(fuzzKeys)])
	}
	if !app.contentCapturesText() {
		t.Fatalf("the keyboard-trap seed no longer reaches a text-capturing view (active=%q, focus=%v) — invariant 6 is checking nothing",
			app.views[app.active].id, app.focus)
	}
}
