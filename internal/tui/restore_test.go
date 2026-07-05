package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
)

// seedSnapshotReal backs up a one-file directory and returns the
// snapshot ID plus the original file's content for byte-compare after
// restore. Mirrors backup_test.go's use of the real in-memory repo.
func seedSnapshotReal(t *testing.T, r *repo.Repo) (string, string) {
	t.Helper()
	src := t.TempDir()
	content := "restore-me-" + t.Name()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{})
	if err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	return info.ID, content
}

func TestRestoreFlow_FullPath(t *testing.T) {
	r := newFlowRepo(t)
	snapID, content := seedSnapshotReal(t, r)

	v := NewRestoreView(Deps{Repo: r})
	// The view loads snapshots on Init (synchronous Phase 1-style hydrate).
	if len(v.snaps) != 1 {
		t.Fatalf("snaps loaded = %d, want 1", len(v.snaps))
	}

	// Stage 1: pick the snapshot.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RestoreView)
	if v.stage != restoreDest {
		t.Fatalf("stage = %v, want restoreDest", v.stage)
	}

	// Stage 2: type an empty destination dir.
	dest := filepath.Join(t.TempDir(), "out")
	for _, r := range dest {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(RestoreView)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RestoreView)
	if v.stage != restoreConfirm {
		t.Fatalf("stage = %v, want restoreConfirm (plan preview)", v.stage)
	}
	if !strings.Contains(v.View(), "1 file") && !strings.Contains(v.View(), "files") {
		t.Errorf("plan preview should show file count:\n%s", v.View())
	}

	// Stage 3: confirm starts the op.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RestoreView)
	if cmd == nil {
		t.Fatal("confirm must emit startOpMsg")
	}
	start, ok := cmd().(startOpMsg)
	if !ok || start.name != "restore" {
		t.Fatalf("got %#v, want startOpMsg{restore}", cmd())
	}
	res := start.run(context.Background())
	done, ok := res.(restoreDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("restore result: %#v", res)
	}

	// Bytes actually landed.
	got, err := os.ReadFile(filepath.Join(dest, "f.txt"))
	if err != nil || string(got) != content {
		t.Fatalf("restored content = %q (%v), want %q", got, err, content)
	}

	m, _ = v.Update(res)
	v = m.(RestoreView)
	if v.stage != restoreDone {
		t.Fatalf("stage after result = %v", v.stage)
	}
	_ = snapID
}

func TestRestoreFlow_NonEmptyDestSurfacedBeforeStart(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)
	v := NewRestoreView(Deps{Repo: r})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // pick
	v = m.(RestoreView)

	dest := t.TempDir() // non-empty? make it so:
	os.WriteFile(filepath.Join(dest, "existing.txt"), []byte("x"), 0o600)
	for _, r := range dest {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(RestoreView)
	}
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RestoreView)
	if v.stage == restoreConfirm && cmd != nil {
		t.Fatal("non-empty destination must not reach a startable confirm")
	}
	if !strings.Contains(strings.ToLower(v.View()), "empty") {
		t.Errorf("view should explain the non-empty destination:\n%s", v.View())
	}
}
