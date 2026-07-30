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

// TestStatsFlow_RunsAndRendersReport: enter loads repo.Stats off the
// UI goroutine and the report shows the dedup story (logical vs
// stored, factor, per-snapshot unique bytes).
func TestStatsFlow_RunsAndRendersReport(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte(strings.Repeat("dedup-me-", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}

	v := NewStatsView(Deps{Repo: r})
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(StatsView)
	if cmd == nil {
		t.Fatal("enter should start the stats load")
	}
	msg := drainForMsg[statsDoneMsg](t, cmd)
	m, _ = v.Update(msg)
	v = m.(StatsView)
	out := v.View()
	for _, want := range []string{"logical", "stored", "dedup", "snap-"} {
		if !strings.Contains(strings.ToLower(out), want) {
			t.Errorf("stats report missing %q:\n%s", want, out)
		}
	}
}

// TestStatsFlow_NilRepoPlaceholder: constructing without a repo must
// render the standard placeholder instead of panicking.
func TestStatsFlow_NilRepoPlaceholder(t *testing.T) {
	v := NewStatsView(Deps{})
	if !strings.Contains(v.View(), "no repository") {
		t.Errorf("placeholder missing: %q", v.View())
	}
}
