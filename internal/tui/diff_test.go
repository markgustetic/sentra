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

// TestDiffFlow_EscClearsError guards against the sticky-error wedge: View()
// gates on d.err before the stage switch, so a failed diff hides the picker.
// Escaping back to pickA must clear d.err, otherwise the view is stuck on
// "Diff failed" for the rest of the session with no way back to a live picker.
func TestDiffFlow_EscClearsError(t *testing.T) {
	r := newFlowRepo(t)
	seedDiffPair(t, r)
	v := NewDiff(Deps{Repo: r})
	// Simulate a prior failed diff (reachable when a concurrent prune deletes
	// a listed snapshot between construction and the B pick).
	v.err = "repo: load snapshot B: blob not found"
	v.idA = v.snaps[0].ID
	v.stage = diffPickB
	if !strings.Contains(v.View(), "Diff failed") {
		t.Fatalf("precondition: error state should render Diff failed:\n%s", v.View())
	}

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	v = m.(Diff)
	if v.err != "" {
		t.Errorf("esc from a failed diff must clear the error; err = %q", v.err)
	}
	if v.stage != diffPickA {
		t.Fatalf("esc should return to pickA; stage = %v", v.stage)
	}
	if strings.Contains(v.View(), "Diff failed") {
		t.Errorf("the live picker should be visible after esc, not the stale error:\n%s", v.View())
	}
}

// TestDiffFlow_NewAttemptClearsStaleError guards the second vector of the same
// wedge: after a failure, choosing a fresh valid pair must render the new
// result. Because the successful transition previously left d.err set, the
// top-of-View gate masked the correct diff behind a stale "Diff failed".
func TestDiffFlow_NewAttemptClearsStaleError(t *testing.T) {
	r := newFlowRepo(t)
	seedDiffPair(t, r)
	v := NewDiff(Deps{Repo: r})
	// A stale error is set while we are back in the picker choosing a fresh,
	// valid pair (row 0 vs row 1, which differ by added.txt).
	v.err = "repo: load snapshot B: blob not found"
	v.idA = v.snaps[0].ID
	v.stage = diffPickB
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
	v = m.(Diff)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(Diff)

	if v.stage != diffShow {
		t.Fatalf("a valid pair should reach diffShow; stage = %v", v.stage)
	}
	if v.err != "" {
		t.Errorf("a fresh successful diff must clear the stale error; err = %q", v.err)
	}
	out := v.View()
	if strings.Contains(out, "Diff failed") {
		t.Errorf("view should render the fresh result, not the stale error:\n%s", out)
	}
	if !strings.Contains(out, "added.txt") {
		t.Errorf("fresh diff result should list the added file:\n%s", out)
	}
}
