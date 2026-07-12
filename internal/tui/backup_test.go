package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/repo"
)

// newFlowRepo creates a real in-memory repo for flow tests.
func newFlowRepo(t *testing.T) *repo.Repo {
	t.Helper()
	r, err := repo.Init(context.Background(), blobstore.NewMemory(), []byte("flow-test-pass"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// backupAtRepo returns a configure-stage BackupView on repo r, its folder picker
// browsing dir so the Start button commits dir.
func backupAtRepo(t *testing.T, r *repo.Repo, dir string) BackupView {
	t.Helper()
	v := NewBackupView(Deps{Repo: r})
	v.picker = newDirPicker(dir)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m.(BackupView)
}

// onStartButton puts the picker's cursor on the Start button — the top, default
// position (cursor 0) from which enter raises the confirmation gate. A fresh
// picker already opens here; this is explicit for tests that navigate first.
func onStartButton(v BackupView) BackupView {
	v.picker.cursor = 0
	return v
}

// confirmModalFrom runs cmd and returns the ConfirmModal it pushes via a
// pushModalMsg, failing if there isn't exactly one.
func confirmModalFrom(t *testing.T, cmd tea.Cmd) ConfirmModal {
	t.Helper()
	var found *ConfirmModal
	for _, msg := range execCmds(t, cmd) {
		pm, ok := msg.(pushModalMsg)
		if !ok {
			continue
		}
		cm, ok := pm.modal.(ConfirmModal)
		if !ok {
			t.Fatalf("pushed modal is %T, want ConfirmModal", pm.modal)
		}
		found = &cm
	}
	if found == nil {
		t.Fatal("expected a pushModalMsg carrying a ConfirmModal")
	}
	return *found
}

// TestBackupFlow_EnterOnStartButtonStarts: enter on the pinned Start button must
// raise the confirmation gate — naming the directory — and only a confirm
// actually starts the backup. Enter on the folder rows only navigates.
func TestBackupFlow_EnterOnStartButtonStarts(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Cursor on the Start button = choose src. Enter asks for confirmation; it
	// must NOT start the backup yet.
	v := onStartButton(backupAtRepo(t, r, src))
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	if v.stage != backupConfigure {
		t.Fatalf("enter must not start the backup before confirmation; stage = %v", v.stage)
	}
	if cmd == nil {
		t.Fatal("enter on the Start button must raise the confirmation modal")
	}
	// It pushes a backup ConfirmModal whose body names the directory.
	cm := confirmModalFrom(t, cmd)
	if cm.id != backupConfirmID {
		t.Fatalf("confirmation modal id = %q, want %q", cm.id, backupConfirmID)
	}
	if !strings.Contains(cm.body, src) {
		t.Errorf("confirmation must name the directory %q:\n%s", src, cm.body)
	}

	// Confirming (the App broadcasts confirmedMsg to every view) starts it.
	m, cmd = v.Update(confirmedMsg{id: backupConfirmID})
	v = m.(BackupView)
	if cmd == nil {
		t.Fatal("confirming must start the backup")
	}
	// The command batches the startOpMsg with the seeded first opTickMsg.
	// Both must be present: the startOpMsg launches the op, and the
	// opTickMsg seeds the progress-repaint self-loop. A missing tick is
	// the regression this asserts against — without it the progress bar
	// never repaints during a real run.
	msgs := execCmds(t, cmd)
	var start startOpMsg
	var foundStart, foundTick bool
	for _, msg := range msgs {
		switch mm := msg.(type) {
		case startOpMsg:
			start, foundStart = mm, true
		case opTickMsg:
			foundTick = true
		}
	}
	if !foundStart {
		t.Fatalf("expected a startOpMsg in the batch, got %#v", msgs)
	}
	if !foundTick {
		t.Fatalf("expected a seeded opTickMsg in the batch (progress repaint seed), got %#v", msgs)
	}
	if start.name != "backup" {
		t.Fatalf("op name = %q", start.name)
	}
	if v.stage != backupRunning {
		t.Fatalf("stage = %v, want backupRunning", v.stage)
	}

	// Execute the op synchronously (the App would run it as a tea.Cmd).
	res := start.run(context.Background())
	done, ok := res.(backupDoneMsg)
	if !ok {
		t.Fatalf("expected backupDoneMsg, got %#v", res)
	}
	if done.err != nil {
		t.Fatalf("backup failed: %v", done.err)
	}
	if done.info.Stats.Files != 1 {
		t.Fatalf("files = %d, want 1", done.info.Stats.Files)
	}

	// Delivering the result moves the flow to the done stage and renders stats.
	m, _ = v.Update(res)
	v = m.(BackupView)
	if v.stage != backupDone {
		t.Fatalf("stage after result = %v, want backupDone", v.stage)
	}
	if out := v.View(); !strings.Contains(out, done.info.ID) {
		t.Errorf("result panel should show the snapshot ID:\n%s", out)
	}

	// The snapshot really exists in the store.
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("ListSnapshots after flow = %v, %v", snaps, err)
	}
}

// TestApp_BackupConfirmationFlowEndToEnd drives the whole gate through the real
// App: enter raises the confirmation modal, and a second enter (routed to the
// modal, whose confirmedMsg the App pops and broadcasts) starts the backup and
// clears the overlay. This is the integration the view-level tests can't reach —
// the App's modal-first key routing and the confirmedMsg broadcast.
func TestApp_BackupConfirmationFlowEndToEnd(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	app := NewApp(Deps{Repo: r, RepoName: "x"})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = m.(App)

	bi := -1
	for i, v := range app.views {
		if v.id == "backup" {
			bi = i
		}
	}
	if bi < 0 {
		t.Fatal("backup view not registered in App")
	}
	app.active = bi
	app.focus = focusContent
	bv := app.views[bi].model.(BackupView)
	bv.picker = newDirPicker(src) // point the picker at src, cursor on Start
	app.views[bi].model = bv

	// First enter → the confirmation modal is raised.
	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = m.(App)
	for _, msg := range execCmds(t, cmd) {
		m, _ = app.Update(msg)
		app = m.(App)
	}
	if len(app.modals) != 1 {
		t.Fatalf("enter must raise a confirmation modal, got %d", len(app.modals))
	}
	if !strings.Contains(app.modals[0].View(), "Confirm backup") {
		t.Errorf("the overlay must be the backup confirmation:\n%s", app.modals[0].View())
	}
	if got := app.views[bi].model.(BackupView).stage; got != backupConfigure {
		t.Fatalf("the backup must not start until confirmed; stage = %v", got)
	}

	// Second enter is routed to the modal → confirmedMsg → pop + broadcast →
	// the backup starts and the overlay clears.
	m, cmd = app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = m.(App)
	for _, msg := range execCmds(t, cmd) {
		m, _ = app.Update(msg)
		app = m.(App)
	}
	if len(app.modals) != 0 {
		t.Errorf("confirming must clear the overlay, got %d modals", len(app.modals))
	}
	if got := app.views[bi].model.(BackupView).stage; got != backupRunning {
		t.Errorf("confirming must start the backup; stage = %v", got)
	}
}

// Esc on the confirmation modal must cancel it — no backup starts and the shell
// returns to the picker.
func TestApp_BackupConfirmationEscCancels(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp(Deps{Repo: r, RepoName: "x"})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = m.(App)

	bi := -1
	for i, v := range app.views {
		if v.id == "backup" {
			bi = i
		}
	}
	app.active, app.focus = bi, focusContent
	bv := app.views[bi].model.(BackupView)
	bv.picker = newDirPicker(src)
	app.views[bi].model = bv

	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter}) // raise the modal
	app = m.(App)
	for _, msg := range execCmds(t, cmd) {
		m, _ = app.Update(msg)
		app = m.(App)
	}
	if len(app.modals) != 1 {
		t.Fatalf("precondition: expected the confirmation modal, got %d", len(app.modals))
	}

	m, cmd = app.Update(tea.KeyMsg{Type: tea.KeyEsc}) // cancel it
	app = m.(App)
	for _, msg := range execCmds(t, cmd) {
		m, _ = app.Update(msg)
		app = m.(App)
	}
	if len(app.modals) != 0 {
		t.Errorf("esc must dismiss the confirmation, got %d modals", len(app.modals))
	}
	if got := app.views[bi].model.(BackupView).stage; got != backupConfigure {
		t.Errorf("cancelling must leave the backup unstarted; stage = %v", got)
	}
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 0 {
		t.Errorf("cancelling must take no snapshot, found %d", len(snaps))
	}
}

// The picker only ever offers real directories, so startBackup's stat guard
// exists for one case: the browsed folder disappears before enter. Pin it.
func TestBackupFlow_VanishedFolderRefusesToStart(t *testing.T) {
	dir := t.TempDir()
	v := onStartButton(backupAtRepo(t, newFlowRepo(t), dir))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // enter on the Start button
	v = m.(BackupView)
	if cmd != nil {
		t.Fatal("a folder that no longer exists must not start an op")
	}
	if v.stage != backupConfigure {
		t.Fatal("flow must stay in configure when the folder is gone")
	}
	if !strings.Contains(v.View(), "not found") {
		t.Errorf("view should surface the error:\n%s", v.View())
	}
}

func TestBackupFlow_EscDuringRunEmitsCancel(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := onStartButton(backupAtRepo(t, r, src))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // raise the confirmation
	v = m.(BackupView)
	m, _ = v.Update(confirmedMsg{id: backupConfirmID}) // confirm → running
	v = m.(BackupView)
	if v.stage != backupRunning {
		t.Fatalf("confirming must start the backup; stage = %v", v.stage)
	}
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc while running must emit a command")
	}
	if _, ok := cmd().(cancelOpMsg); !ok {
		t.Fatalf("expected cancelOpMsg, got %#v", cmd())
	}
}

// TestBackupFlow_RunAnotherKeepsSizing: "run another" must not reset the
// progress-bar width to its default — bubbletea won't re-emit a
// WindowSizeMsg after a model swap, so the fresh view must carry the last
// known size forward.
func TestBackupFlow_RunAnotherKeepsSizing(t *testing.T) {
	v := NewBackupView(Deps{Repo: newFlowRepo(t)})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = m.(BackupView)
	v.stage = backupDone // simulate a finished backup
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	fresh := m.(BackupView)
	if got, want := fresh.bar.Width, min(100-8, 60); got != want {
		t.Errorf("bar width after 'run another' = %d, want %d (sizing lost on reset)", got, want)
	}
}

// backupAt returns a configure-stage BackupView rooted at a temp tree.
func backupAt(t *testing.T, root string) BackupView {
	t.Helper()
	v := NewBackupView(Deps{Repo: newFlowRepo(t)})
	v.picker = newDirPicker(root)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m.(BackupView)
}

// While the picker has focus the arrows belong to it and NOT to the shell, and
// no text field is capturing — so 'q' still quits and ctrl+p still opens the
// palette. Once tab moves to the tag field, that inverts.
func TestBackupFocusSeamsFollowTheFocusedControl(t *testing.T) {
	v := backupAt(t, tempTree(t))

	if !v.ConsumesArrows() {
		t.Error("with the picker focused, the view must consume arrows")
	}
	if v.CapturesText() {
		t.Error("the picker is not a text field; it must not capture text")
	}

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(BackupView)
	if v.ConsumesArrows() {
		t.Error("with the tag field focused, arrows belong to the shell")
	}
	if !v.CapturesText() {
		t.Error("the tag field must capture text")
	}
}

// The picker opens on the "backup the current directory" option, so a fresh
// Backup view reaches the confirmation gate on the very first enter — no
// navigating down to a button — and only a confirm starts the snapshot.
func TestBackupFirstEnterRaisesConfirmation(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	v := backupAtRepo(t, r, src) // fresh picker, no navigation
	if !v.picker.onStart() {
		t.Fatal("a fresh backup picker must rest on the Start button (the top option)")
	}

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	if v.stage != backupConfigure {
		t.Errorf("the first enter must confirm, not start; stage = %v", v.stage)
	}
	if cmd == nil {
		t.Fatal("the first enter must raise the confirmation gate")
	}
	if cm := confirmModalFrom(t, cmd); cm.id != backupConfirmID {
		t.Fatalf("confirmation modal id = %q, want %q", cm.id, backupConfirmID)
	}
}

// Down moves the highlight; enter on a folder row descends (does not start).
func TestBackupPickerNavigatesWithoutStarting(t *testing.T) {
	root := tempTree(t)
	v := backupAt(t, root)

	// The picker opens on the top Start button; step down past ".." onto the
	// first folder (alpha).
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown}) // Start button -> ".."
	v = m.(BackupView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown}) // ".." -> alpha
	v = m.(BackupView)
	if got := v.picker.rows[v.picker.cursor-1].label; got != "alpha" {
		t.Fatalf("cursor on %q, want alpha", got)
	}

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // descend into alpha
	v = m.(BackupView)
	if cmd != nil {
		t.Error("enter on a folder row must navigate, not start a backup")
	}
	if filepath.Base(v.picker.cwd) != "alpha" {
		t.Fatalf("enter on a child must descend, cwd = %q", v.picker.cwd)
	}
	if v.stage != backupConfigure {
		t.Error("navigating must not leave the configure stage")
	}
}

// Backspace climbs out of a directory.
func TestBackupPickerBackspaceGoesUp(t *testing.T) {
	root := tempTree(t)
	v := backupAt(t, filepath.Join(root, "beta"))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	v = m.(BackupView)
	if v.picker.cwd != root {
		t.Fatalf("cwd after backspace = %q, want %q", v.picker.cwd, root)
	}
}

// Enter in the tag field also reaches the confirmation gate (for the browsed
// folder), so a tag can be set first without hunting for a separate submit;
// confirming then starts the op through the one-op guard.
func TestBackupTagFieldEnterRaisesConfirmation(t *testing.T) {
	root := tempTree(t)
	v := backupAt(t, root)

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // focus the tag field
	v = m.(BackupView)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	if cmd == nil {
		t.Fatal("enter in the tag field must raise the confirmation gate")
	}
	if cm := confirmModalFrom(t, cmd); cm.id != backupConfirmID {
		t.Fatalf("confirmation modal id = %q, want %q", cm.id, backupConfirmID)
	}

	// Confirming batches the startOpMsg with the seeded first opTickMsg. Both
	// must be present, or the progress bar never repaints during a real run.
	_, cmd = v.Update(confirmedMsg{id: backupConfirmID})
	var foundStart, foundTick bool
	for _, msg := range execCmds(t, cmd) {
		switch mm := msg.(type) {
		case startOpMsg:
			foundStart = true
			if mm.name != "backup" {
				t.Errorf("op name = %q, want backup", mm.name)
			}
		case opTickMsg:
			foundTick = true
		}
	}
	if !foundStart {
		t.Error("backup must start through the App's one-op guard")
	}
	if !foundTick {
		t.Error("the first opTickMsg must still be seeded, or progress never repaints")
	}
}

// On the Start button the footer must promise starting the backup of the
// browsed folder — a footer that says "open" while the cursor rests on the
// button would be lying.
func TestBackupStartButtonFooterSaysStart(t *testing.T) {
	root := tempTree(t)
	v := onStartButton(backupAt(t, root))
	want := "start the backup of " + filepath.Base(root)
	if v.picker.enterVerb() != want {
		t.Errorf("Start-button verb = %q, want %q", v.picker.enterVerb(), want)
	}
	if !strings.Contains(v.View(), "Press enter to "+want) {
		t.Errorf("footer must say %q:\n%s", "Press enter to "+want, v.View())
	}
}
