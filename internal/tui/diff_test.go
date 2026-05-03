package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
)

// TestDiff_RendersAdded surfaces every Added path in the rendered
// view. We don't pin the column order — left vs right doesn't
// matter for v1 — just the content.
func TestDiff_RendersAdded(t *testing.T) {
	d := NewDiff(Deps{})
	d = d.SetResult("snap-a", "snap-b", repo.DiffResult{
		Added: []string{"new-1.txt", "subdir/new-2.go"},
	})
	view := d.View()
	if !strings.Contains(view, "new-1.txt") {
		t.Errorf("view missing added path new-1.txt: %s", view)
	}
	if !strings.Contains(view, "subdir/new-2.go") {
		t.Errorf("view missing added path subdir/new-2.go: %s", view)
	}
}

// TestDiff_RendersRemoved surfaces every Removed path.
func TestDiff_RendersRemoved(t *testing.T) {
	d := NewDiff(Deps{})
	d = d.SetResult("snap-a", "snap-b", repo.DiffResult{
		Removed: []string{"old.txt", "stale/path.go"},
	})
	view := d.View()
	if !strings.Contains(view, "old.txt") {
		t.Errorf("view missing removed path: %s", view)
	}
	if !strings.Contains(view, "stale/path.go") {
		t.Errorf("view missing removed path: %s", view)
	}
}

// TestDiff_RendersChanged surfaces every Changed path.
func TestDiff_RendersChanged(t *testing.T) {
	d := NewDiff(Deps{})
	d = d.SetResult("snap-a", "snap-b", repo.DiffResult{
		Changed: []string{"src/a.go", "config/x.yml"},
	})
	view := d.View()
	if !strings.Contains(view, "src/a.go") {
		t.Errorf("view missing changed path: %s", view)
	}
	if !strings.Contains(view, "config/x.yml") {
		t.Errorf("view missing changed path: %s", view)
	}
}

// TestDiff_NoResultPlaceholder asserts the empty-state copy when
// the user hasn't selected two snapshots yet.
func TestDiff_NoResultPlaceholder(t *testing.T) {
	d := NewDiff(Deps{})
	view := d.View()
	if !strings.Contains(strings.ToLower(view), "select two snapshots") {
		t.Errorf("expected hint about selecting snapshots: %s", view)
	}
}

// TestDiff_DoesNotPanicOnUpdate ensures arrow / enter keys don't
// crash the model. The view has no input bindings yet beyond
// "show me what was set" — but a missing default case in a switch
// would break the parent App, so we exercise a few common keys.
func TestDiff_DoesNotPanicOnUpdate(t *testing.T) {
	d := NewDiff(Deps{})
	d = d.SetResult("a", "b", repo.DiffResult{Added: []string{"x"}})
	for _, msg := range []tea.Msg{
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyEsc},
	} {
		_, _ = d.Update(msg)
	}
}

// TestDiff_RendersHeader shows both snapshot IDs the diff is between
// so the user knows what the columns are comparing.
func TestDiff_RendersHeader(t *testing.T) {
	d := NewDiff(Deps{})
	d = d.SetResult("snap-aaaa", "snap-bbbb", repo.DiffResult{})
	view := d.View()
	if !strings.Contains(view, "snap-aaaa") || !strings.Contains(view, "snap-bbbb") {
		t.Errorf("diff header missing snapshot IDs: %s", view)
	}
}
