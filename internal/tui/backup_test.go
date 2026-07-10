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
// browsing dir so the "use this folder" row targets dir.
func backupAtRepo(t *testing.T, r *repo.Repo, dir string) BackupView {
	t.Helper()
	v := NewBackupView(Deps{Repo: r})
	v.picker = newDirPicker(dir)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m.(BackupView)
}

// TestBackupFlow_EnterOnUseThisFolderStarts is the reported fix: enter on the
// picker's top row must START the backup of the browsed directory, not just
// re-select it. Before, the only way to start was to tab away to the tag field.
func TestBackupFlow_EnterOnUseThisFolderStarts(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Picker on "use this folder" (row 0) = src. One enter must start the backup.
	v := backupAtRepo(t, r, src)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	if cmd == nil {
		t.Fatal("enter on 'use this folder' must start the backup")
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

// The picker only ever offers real directories, so startBackup's stat guard
// exists for one case: the browsed folder disappears before enter. Pin it.
func TestBackupFlow_VanishedFolderRefusesToStart(t *testing.T) {
	dir := t.TempDir()
	v := backupAtRepo(t, newFlowRepo(t), dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // enter on "use this folder"
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
	v := backupAtRepo(t, r, src)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // enter on "use this folder" starts
	v = m.(BackupView)
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

// Down moves the highlight; enter on a folder row descends (does not start).
func TestBackupPickerNavigatesWithoutStarting(t *testing.T) {
	root := tempTree(t)
	v := backupAt(t, root)

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.(BackupView).Update(tea.KeyMsg{Type: tea.KeyDown}) // row 2 = alpha
	v = m.(BackupView)
	if v.picker.rows[v.picker.cursor].label != "alpha" {
		t.Fatalf("cursor on %q, want alpha", v.picker.rows[v.picker.cursor].label)
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

// Enter in the tag field also starts the backup (of the browsed folder), so a
// tag can be set first without hunting for a separate submit.
func TestBackupTagFieldEnterStarts(t *testing.T) {
	root := tempTree(t)
	v := backupAt(t, root)

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // focus the tag field
	v = m.(BackupView)
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter with a chosen folder must emit the start op")
	}
	// startBackup batches the startOpMsg with the seeded first opTickMsg. Both
	// must survive the picker rework, or the progress bar never repaints.
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

// The picker's top-row footer must promise starting the backup, not "back up
// this folder" — the old wording read like a start but only re-selected.
func TestBackupUseThisFolderFooterSaysStart(t *testing.T) {
	v := backupAt(t, tempTree(t)) // cursor on row 0 = "use this folder"
	if v.picker.enterVerb() != "start the backup" {
		t.Errorf("top-row verb = %q, want %q", v.picker.enterVerb(), "start the backup")
	}
	if !strings.Contains(v.View(), "Press enter to start the backup") {
		t.Errorf("footer must say start the backup:\n%s", v.View())
	}
}
