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

// seedDiffPair makes two snapshots that differ by one added file.
func seedDiffPair(t *testing.T, r *repo.Repo) {
	t.Helper()
	a := t.TempDir()
	if err := os.WriteFile(filepath.Join(a, "keep.txt"), []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshot(context.Background(), a, repo.SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
	b := t.TempDir()
	if err := os.WriteFile(filepath.Join(b, "keep.txt"), []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "added.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshot(context.Background(), b, repo.SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestDiffFlow_PickPairAndRender(t *testing.T) {
	r := newFlowRepo(t)
	seedDiffPair(t, r)

	v := NewDiff(Deps{Repo: r})
	if len(v.snaps) != 2 {
		t.Fatalf("snaps = %d, want 2", len(v.snaps))
	}
	// Pick A (first row), then B (move down one, enter).
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(Diff)
	if v.stage != diffPickB {
		t.Fatalf("stage = %v, want diffPickB", v.stage)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown})
	v = m.(Diff)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(Diff)
	if v.stage != diffShow {
		t.Fatalf("stage = %v, want diffShow", v.stage)
	}
	out := v.View()
	if !strings.Contains(out, "added.txt") {
		t.Errorf("diff should show the added file:\n%s", out)
	}
}

func TestDiffFlow_EscGoesBack(t *testing.T) {
	r := newFlowRepo(t)
	seedDiffPair(t, r)
	v := NewDiff(Deps{Repo: r})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // -> pickB
	v = m.(Diff)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEsc}) // back to pickA
	v = m.(Diff)
	if v.stage != diffPickA {
		t.Fatalf("esc should return to pickA; stage = %v", v.stage)
	}
}

func TestDiff_NilRepoPlaceholder(t *testing.T) {
	if !strings.Contains(NewDiff(Deps{}).View(), "no repository") {
		t.Error("nil-repo diff should render a placeholder")
	}
}
