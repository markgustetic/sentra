package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// seedTaggedSnaps backs up one throwaway directory per tag so the picker
// tables have real, ~30-char snapshot IDs (unlike sampleSnaps()' short
// "snap-aaaa"). Distinct file content keeps the snapshots distinct.
func seedTaggedSnaps(t *testing.T, r *repo.Repo, tags ...string) {
	t.Helper()
	for i, tag := range tags {
		src := t.TempDir()
		content := fmt.Sprintf("content-%d-%s", i, tag)
		if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{Tag: tag}); err != nil {
			t.Fatalf("seed snapshot %q: %v", tag, err)
		}
	}
}

// lineWithAll reports whether any single line contains every substring.
func lineWithAll(lines []string, subs ...string) bool {
	for _, line := range lines {
		all := true
		for _, s := range subs {
			if !strings.Contains(line, s) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// TestApp_SnapshotPickersAlignAtMinSize renders the Diff and Restore
// pickers active in the real shell at the documented minimum terminal
// (80x20) and asserts the tables neither overflow the frame nor wrap:
// the whole header (ID/Created/Tag) stays on one line, and each row keeps
// its Created timestamp and Tag together. Before the width-aware column
// sizing, the hard-coded ~69/76-col tables overflowed the ~59-col content
// interior and lipgloss wrapped the Tag column onto its own line,
// misaligning header vs rows.
func TestApp_SnapshotPickersAlignAtMinSize(t *testing.T) {
	for _, viewID := range []string{"diff", "restore"} {
		t.Run(viewID, func(t *testing.T) {
			r := newFlowRepo(t)
			// "weekly" is 6 chars — exactly the Tag column width the picker
			// shrinks to at the minimum size, so it exercises a near-full
			// column (where a wrap would bite) yet still renders verbatim.
			seedTaggedSnaps(t, r, "weekly", "daily")
			snaps, err := r.ListSnapshots(context.Background())
			if err != nil {
				t.Fatalf("ListSnapshots: %v", err)
			}

			app := NewApp(Deps{Repo: r, RepoName: "test-repo"})
			m, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
			a := m.(App)
			activated := false
			for i := range a.views {
				if a.views[i].id == viewID {
					a.active = i
					activated = true
				}
			}
			if !activated {
				t.Fatalf("no view registered under id %q", viewID)
			}
			a.focus = focusContent

			out := a.View()
			lines := strings.Split(out, "\n")

			for i, line := range lines {
				if w := lipgloss.Width(line); w > 80 {
					t.Errorf("line %d overflows 80 cols (%d): %q", i, w, line)
				}
			}

			// The header must not wrap: ID, Created and Tag on one line.
			if !lineWithAll(lines, "ID", "Created", "Tag") {
				t.Errorf("table header wrapped — no single line holds ID+Created+Tag:\n%s", out)
			}

			// Each data row must keep its Created timestamp and its Tag on
			// the same line — the exact symptom of the wrap is the Tag
			// column dropping onto its own line.
			for _, s := range snaps {
				created := s.CreatedAt.UTC().Format("2006-01-02 15:04")
				if !lineWithAll(lines, created, s.Tag) {
					t.Errorf("row for tag %q wrapped — no line holds both %q and %q:\n%s",
						s.Tag, created, s.Tag, out)
				}
			}
		})
	}
}

// TestPruneView_FitsWidthAtMinSize checks the retention preview is
// width-aware too: at the content interior the App forwards at 80 cols
// (~59), no rendered line may exceed that width. The un-truncated
// "ID  verdict  reason" lines were ~72 cols and reflowed in the panel.
func TestPruneView_FitsWidthAtMinSize(t *testing.T) {
	r := newFlowRepo(t)
	// Several snapshots with keep-last 1 so most are dropped and at least
	// one is kept with a long multi-rule reason.
	seedTaggedSnaps(t, r, "nightly", "nightly", "nightly", "nightly")
	cfg := config.Defaults()
	cfg.Retention.KeepLast = 1

	v := NewPruneView(Deps{Repo: r, Config: &cfg})
	const forwarded = 59 // contentW the App forwards at an 80-col terminal
	m, _ := v.Update(tea.WindowSizeMsg{Width: forwarded, Height: 16})
	v = m.(PruneView)

	// The panel reserves horizontal padding inside the forwarded width, so
	// the real text region is narrower; a line wider than that reflows.
	region := pickerContentWidth(forwarded)
	out := v.View()
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > region {
			t.Errorf("prune line %d exceeds content region (%d > %d): %q", i, w, region, line)
		}
	}
}
