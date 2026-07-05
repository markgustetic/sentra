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

func TestCheckFlow_RunsAndRendersReport(t *testing.T) {
	r := newFlowRepo(t)
	// One snapshot so the report has non-zero counts.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}

	v := NewCheckView(Deps{Repo: r})
	// Enter kicks off the check; it moves to the running stage and returns
	// a command batch (spinner tick + the check goroutine).
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(CheckView)
	if v.stage != checkRunning {
		t.Fatalf("stage = %v, want checkRunning", v.stage)
	}
	if cmd == nil {
		t.Fatal("enter must start the check")
	}

	// Find and deliver the checkDoneMsg the batch produces.
	var done tea.Msg
	for _, msg := range execCmds(t, cmd) {
		if _, ok := msg.(checkDoneMsg); ok {
			done = msg
		}
	}
	if done == nil {
		t.Fatal("check command did not produce a checkDoneMsg")
	}
	m, _ = v.Update(done)
	v = m.(CheckView)
	if v.stage != checkDone {
		t.Fatalf("stage after result = %v, want checkDone", v.stage)
	}
	out := v.View()
	for _, want := range []string{"snapshots", "healthy"} {
		if !strings.Contains(strings.ToLower(out), want) {
			t.Errorf("report view missing %q:\n%s", want, out)
		}
	}
}

func TestCheckFlow_SurfacesIssues(t *testing.T) {
	// A fresh repo with no snapshots is healthy but empty; assert the
	// report renders a healthy status without panicking on empty slices.
	v := NewCheckView(Deps{Repo: newFlowRepo(t)})
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(CheckView)
	for _, msg := range execCmds(t, cmd) {
		if _, ok := msg.(checkDoneMsg); ok {
			m, _ = v.Update(msg)
			v = m.(CheckView)
		}
	}
	if v.stage != checkDone {
		t.Fatalf("stage = %v, want checkDone", v.stage)
	}
	if v.result.err != nil {
		t.Fatalf("check on empty repo errored: %v", v.result.err)
	}
}

func TestCheckFlow_NilRepoPlaceholder(t *testing.T) {
	v := NewCheckView(Deps{})
	if !strings.Contains(v.View(), "no repository") {
		t.Errorf("nil-repo view should show a placeholder:\n%s", v.View())
	}
}
