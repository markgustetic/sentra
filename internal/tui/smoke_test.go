package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// smokeApp builds a fully-wired App on a real in-memory repo that already holds
// one snapshot with a small directory tree, so read-only views (dashboard,
// snapshots, files, diff…) have real content to render.
func smokeApp(t *testing.T, w, h int) (App, *repo.Repo) {
	t.Helper()
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a/b/one.txt", "a/two.txt", "top.txt"} {
		if err := os.WriteFile(filepath.Join(src, p), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{Tag: "nightly"}); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	app := NewApp(Deps{
		Repo: r, RepoName: "smoke", Config: &cfg,
		Actions: action.NewDefaultRegistry(), Ctx: context.Background(),
	})
	m, _ := app.Update(tea.WindowSizeMsg{Width: w, Height: h})
	app = m.(App)
	// Note: we deliberately do NOT run app.Init() here — its batch includes the
	// dashboard's 30s refresh tick, which execCmds would run synchronously and
	// block. The data views hydrate in their constructors; Files loads lazily on
	// activate (see the activate helper). teatest runs Init itself for the real
	// end-to-end test.
	return app, r
}

// activate switches to a view through the real activateMsg path (which also
// fires showActive → the lazy-load for Files), draining any resulting commands.
func activate(t *testing.T, app App, id string) App {
	t.Helper()
	m, cmd := app.Update(activateMsg{id: id})
	app = m.(App)
	for _, msg := range execCmds(t, cmd) {
		m, _ = app.Update(msg)
		app = m.(App)
	}
	return app
}

// TestSmoke_EveryViewFitsTheFrame walks the shell to each registered view and
// asserts the rendered frame is EXACTLY the terminal height with no line
// overflowing the width. This is the integration backstop that catches
// layout/overflow regressions in any view at once — the class of bug a
// per-view unit test can miss (e.g. the dashboard one-row overflow).
func TestSmoke_EveryViewFitsTheFrame(t *testing.T) {
	const w, h = 100, 40
	app, _ := smokeApp(t, w, h)

	ids := make([]string, 0, len(app.views))
	for _, v := range app.views {
		if v.id == "unlock" { // hidden startup gate, not navigable
			continue
		}
		ids = append(ids, v.id)
	}

	for _, id := range ids {
		app = activate(t, app, id)
		out := app.View()
		lines := strings.Split(out, "\n")
		if len(lines) != h {
			t.Errorf("view %q: frame is %d lines, want exactly %d", id, len(lines), h)
		}
		for i, ln := range lines {
			if lw := lipgloss.Width(ln); lw > w {
				t.Errorf("view %q: line %d width %d exceeds %d", id, i, lw, w)
			}
		}
	}
}

// TestSmoke_BackupThenBrowse drives a realistic flow through the real App:
// take a backup (confirmation gate), then inspect it in the snapshots and files
// views — exercising key routing, the modal broadcast, the op guard, sort/filter,
// and the lazy Files load together, the way a per-view test cannot.
func TestSmoke_BackupThenBrowse(t *testing.T) {
	const w, h = 100, 40
	app, _ := smokeApp(t, w, h)

	// --- Backup with confirmation ---
	app = activate(t, app, "backup")
	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter}) // choose current dir → confirm modal
	app = m.(App)
	for _, msg := range execCmds(t, cmd) {
		m, _ = app.Update(msg)
		app = m.(App)
	}
	if len(app.modals) != 1 || !strings.Contains(app.modals[0].View(), "Confirm backup") {
		t.Fatalf("enter must raise the backup confirmation, modals=%d", len(app.modals))
	}
	m, cmd = app.Update(tea.KeyMsg{Type: tea.KeyEnter}) // confirm → confirmedMsg
	app = m.(App)
	for _, msg := range execCmds(t, cmd) { // run the confirm: pop modal + broadcast + start op
		m, _ = app.Update(msg)
		app = m.(App)
	}
	if len(app.modals) != 0 {
		t.Errorf("confirming must clear the modal, modals=%d", len(app.modals))
	}

	// --- Snapshots: sort, filter, open detail ---
	app = activate(t, app, "snapshots")
	sv := app.views[indexOf(app, "snapshots")].model.(Snapshots)
	sv.copyFn = func(string) error { return nil } // don't touch the real clipboard
	app.views[indexOf(app, "snapshots")].model = sv

	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("s")}, // sort
		{Type: tea.KeyRunes, Runes: []rune("y")}, // copy id
		{Type: tea.KeyRunes, Runes: []rune("/")}, // filter open
		{Type: tea.KeyRunes, Runes: []rune("nightly")},
		{Type: tea.KeyEsc},   // clear filter
		{Type: tea.KeyEnter}, // open detail
	} {
		m, _ = app.Update(k)
		app = m.(App)
	}
	if out := app.View(); !strings.Contains(out, "files") {
		t.Errorf("snapshot detail should render the directory summary:\n%s", out)
	}
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyEsc}) // detail → list
	app = m.(App)

	// --- Files: the box-and-arrows tree of the latest snapshot ---
	app = activate(t, app, "files")
	if out := app.View(); !strings.Contains(out, "▶") || !strings.Contains(out, "┌") {
		t.Errorf("files view should render the directory graph (boxes + arrows):\n%s", out)
	}

	// Nothing panicked and the frame stayed within bounds throughout.
	if lines := strings.Split(app.View(), "\n"); len(lines) != h {
		t.Errorf("frame drifted to %d lines, want %d", len(lines), h)
	}
}

// indexOf returns the view slot for id, or -1.
func indexOf(app App, id string) int {
	for i, v := range app.views {
		if v.id == id {
			return i
		}
	}
	return -1
}

// TestSmoke_RealProgramE2E drives the actual bubbletea program (not just
// App.Update): teatest runs the event loop, so Init commands fire, tea.Batch
// executes, and the real renderer produces frames. It navigates by number key
// to the snapshots view, waits for that view to render, then quits and asserts
// on the final model — a true end-to-end smoke of the runtime wiring.
func TestSmoke_RealProgramE2E(t *testing.T) {
	app, _ := smokeApp(t, 100, 40)

	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(100, 40))

	// Wait for the shell to be up (the rail lists views).
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Snapshots"))
	}, teatest.WithDuration(3*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlP}) // open palette
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})   // close it
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC}) // quit
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	final, ok := tm.FinalModel(t).(App)
	if !ok {
		t.Fatalf("final model is %T, want App", tm.FinalModel(t))
	}
	if final.paletteOpen {
		t.Error("palette should have been closed before quit")
	}
}
