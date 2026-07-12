package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
)

// TestFilesView_LoadingState: a fresh view is loading until the manifest lands,
// so it must show a loading hint rather than a blank or a panic.
func TestFilesView_LoadingState(t *testing.T) {
	if out := NewFilesView(Deps{}).View(); !strings.Contains(out, "loading") {
		t.Errorf("fresh files view must show a loading state: %q", out)
	}
}

// TestFilesView_RendersGraphWhenLoaded: once loaded, the view draws the box-and-
// arrows directory graph and a header naming the snapshot.
func TestFilesView_RendersGraphWhenLoaded(t *testing.T) {
	root := sampleFileTree()
	root.name = "backups"

	v := NewFilesView(Deps{})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = m.(FilesView)
	m, _ = v.Update(filesLoadedMsg{
		root: root, id: "snap-abc123",
		when: time.Date(2026, 6, 9, 3, 30, 0, 0, time.UTC), files: 243, bytes: 1 << 20,
	})
	v = m.(FilesView)

	out := v.View()
	if !strings.Contains(out, "snap-abc123") {
		t.Errorf("header must name the snapshot:\n%s", out)
	}
	for _, want := range []string{"backups", "photos", "code", "┌", "▶"} {
		if !strings.Contains(out, want) {
			t.Errorf("loaded view must draw the graph piece %q:\n%s", want, out)
		}
	}
}

// TestFilesView_EmptyState: a repo with no snapshots (nil root) shows guidance,
// not a broken empty graph.
func TestFilesView_EmptyState(t *testing.T) {
	v := NewFilesView(Deps{})
	m, _ := v.Update(filesLoadedMsg{}) // nil root
	if out := m.(FilesView).View(); !strings.Contains(out, "no files") {
		t.Errorf("empty repo must show the empty state: %q", out)
	}
}

// TestFilesView_ErrorState: a load error is surfaced, never swallowed.
func TestFilesView_ErrorState(t *testing.T) {
	v := NewFilesView(Deps{})
	m, _ := v.Update(filesLoadedMsg{err: errors.New("boom")})
	if out := m.(FilesView).View(); !strings.Contains(out, "boom") {
		t.Errorf("load error must be shown: %q", out)
	}
}

// TestFilesView_ReloadKey: ctrl+r re-enters loading and emits a fresh load.
func TestFilesView_ReloadKey(t *testing.T) {
	v := NewFilesView(Deps{Repo: newFlowRepo(t)})
	m, _ := v.Update(filesLoadedMsg{}) // settle out of the initial loading state
	v = m.(FilesView)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if !m.(FilesView).loading {
		t.Error("ctrl+r must re-enter the loading state")
	}
	if cmd == nil {
		t.Error("ctrl+r must emit a reload command")
	}
}

// TestFilesView_ReloadsAfterOp: a completed operation (backup/prune/…) refreshes
// the tree so it tracks the newest snapshot.
func TestFilesView_ReloadsAfterOp(t *testing.T) {
	v := NewFilesView(Deps{Repo: newFlowRepo(t)})
	m, _ := v.Update(filesLoadedMsg{})
	v = m.(FilesView)
	if _, cmd := v.Update(backupDoneMsg{}); cmd == nil {
		t.Error("a completed op must trigger a reload")
	}
}

// TestFilesLoadCmd_BuildsTreeFromRepo drives the real load path: back up a
// directory tree, then assert the command reconstructs it from the manifest.
func TestFilesLoadCmd_BuildsTreeFromRepo(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a", "b", "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "top.txt"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}

	msg, ok := filesLoadCmd(Deps{Repo: r, Ctx: context.Background()})().(filesLoadedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("load failed: %+v", msg)
	}
	if msg.root == nil || msg.root.totalFiles() != 2 {
		t.Fatalf("want a tree of 2 files, got %+v", msg.root)
	}
	a := msg.root.children["a"]
	if a == nil || a.children["b"] == nil || a.children["b"].totalFiles() != 1 {
		t.Errorf("nested a/b structure not reconstructed: %+v", msg.root.children)
	}
}
