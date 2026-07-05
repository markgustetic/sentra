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

func typeInto(v BackupView, s string) BackupView {
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(BackupView)
	}
	return v
}

func TestBackupFlow_EnterEmitsStartOpWithTypedPath(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	v := NewBackupView(Deps{Repo: r})
	v = typeInto(v, src)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	if cmd == nil {
		t.Fatal("enter on a valid path must emit a command")
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

func TestBackupFlow_MissingPathRefusesToStart(t *testing.T) {
	v := NewBackupView(Deps{Repo: newFlowRepo(t)})
	v = typeInto(v, "/definitely/not/a/real/path")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	if cmd != nil {
		t.Fatal("nonexistent path must not start an op")
	}
	if v.stage != backupConfigure {
		t.Fatal("flow must stay in configure on invalid path")
	}
	if !strings.Contains(v.View(), "not found") {
		t.Errorf("view should surface the path error:\n%s", v.View())
	}
}

func TestBackupFlow_EscDuringRunEmitsCancel(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("x"), 0o600)
	v := NewBackupView(Deps{Repo: r})
	v = typeInto(v, src)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc while running must emit a command")
	}
	if _, ok := cmd().(cancelOpMsg); !ok {
		t.Fatalf("expected cancelOpMsg, got %#v", cmd())
	}
}
