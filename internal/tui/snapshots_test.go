package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
)

func sampleSnaps() []repo.SnapshotInfo {
	return []repo.SnapshotInfo{
		{
			ID:        "snap-aaaa",
			CreatedAt: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
			Tag:       "weekly",
			Stats:     repo.SnapshotStats{Files: 100, Bytes: 1024},
		},
		{
			ID:        "snap-bbbb",
			CreatedAt: time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC),
			Tag:       "daily",
			Stats:     repo.SnapshotStats{Files: 200, Bytes: 2048},
		},
	}
}

func sampleManifest() repo.Manifest {
	return repo.Manifest{
		Version:   1,
		ID:        "snap-aaaa",
		CreatedAt: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		Tag:       "weekly",
		Tree: []repo.FileEntry{
			{Path: "src/a.go", Size: 100},
			{Path: "src/b.go", Size: 200},
			{Path: "README.md", Size: 50},
		},
		Stats: repo.SnapshotStats{Files: 3, Bytes: 350},
	}
}

// TestSnapshots_RendersAllSnapshots verifies every row in the model
// renders into the table view. We hydrate via SetSnapshots so tests
// don't need a live repo.
func TestSnapshots_RendersAllSnapshots(t *testing.T) {
	s := NewSnapshots(Deps{})
	s = s.SetSnapshots(sampleSnaps())
	view := s.View()
	if !strings.Contains(view, "snap-aaaa") {
		t.Errorf("view missing snap-aaaa: %s", view)
	}
	if !strings.Contains(view, "snap-bbbb") {
		t.Errorf("view missing snap-bbbb: %s", view)
	}
}

// TestSnapshots_NavigatesWithArrows asserts the cursor moves down
// when a Down key arrives. We don't pin the absolute index because
// table internals may differ; just check movement happened from 0.
func TestSnapshots_NavigatesWithArrows(t *testing.T) {
	s := NewSnapshots(Deps{})
	s = s.SetSnapshots(sampleSnaps())
	if s.cursor() != 0 {
		t.Fatalf("initial cursor: got %d, want 0", s.cursor())
	}
	updated, _ := s.Update(tea.KeyMsg{Type: tea.KeyDown})
	got := updated.(Snapshots)
	if got.cursor() == 0 {
		t.Errorf("cursor did not advance on Down key")
	}
}

// TestSnapshots_EnterOpensDetail asserts that Enter on a row sets
// the selected snapshot ID and switches to the detail sub-view. The
// detail view's contents are loaded via the detailLoader hook.
func TestSnapshots_EnterOpensDetail(t *testing.T) {
	loaderCalled := ""
	manifest := sampleManifest()
	s := NewSnapshotsWithLoader(Deps{}, func(id string) (repo.Manifest, error) {
		loaderCalled = id
		return manifest, nil
	})
	s = s.SetSnapshots(sampleSnaps())
	updated, _ := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Snapshots)
	if !got.detailOpen {
		t.Fatalf("detail not opened after enter")
	}
	if loaderCalled == "" {
		t.Errorf("detail loader was not invoked")
	}
	view := got.View()
	if !strings.Contains(view, "src/a.go") {
		t.Errorf("detail view missing tree entry: %s", view)
	}
	if !strings.Contains(view, "src/b.go") {
		t.Errorf("detail view missing tree entry: %s", view)
	}
}

// TestSnapshots_EscClosesDetail rounds out the navigation cycle:
// from inside detail, esc returns to the table.
func TestSnapshots_EscClosesDetail(t *testing.T) {
	s := NewSnapshotsWithLoader(Deps{}, func(_ string) (repo.Manifest, error) {
		return sampleManifest(), nil
	})
	s = s.SetSnapshots(sampleSnaps())
	updated, _ := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !updated.(Snapshots).detailOpen {
		t.Fatal("detail did not open")
	}
	updated, _ = updated.(Snapshots).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(Snapshots).detailOpen {
		t.Errorf("detail did not close on esc")
	}
}

// TestSnapshots_EmptyRepo asserts the view renders a placeholder
// rather than crashing on a zero-length snapshot list.
func TestSnapshots_EmptyRepo(t *testing.T) {
	s := NewSnapshots(Deps{})
	view := s.View()
	if !strings.Contains(strings.ToLower(view), "no snapshots") {
		t.Errorf("empty snapshots did not render placeholder: %s", view)
	}
}
