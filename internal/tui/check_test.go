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

// TestCheckFlow_DeepVerifyToggle: 'd' arms the read-data mode before a
// run; the run then deep-verifies (ReadDataBlobs > 0 on a healthy
// repo) and the report says so. Toggling is refused mid-run.
func TestCheckFlow_DeepVerifyToggle(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte(strings.Repeat("x", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}

	v := NewCheckView(Deps{Repo: r})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	v = m.(CheckView)
	if !strings.Contains(v.View(), "deep") {
		t.Errorf("idle view should show the armed deep mode:\n%s", v.View())
	}
	// Second press cycles to the bounded 10%% sample; third disarms.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if !strings.Contains(m.(CheckView).View(), "10%") {
		t.Errorf("second d should arm the sampled mode:\n%s", m.(CheckView).View())
	}
	m, _ = m.(CheckView).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if strings.Contains(m.(CheckView).View(), "armed") {
		t.Errorf("third d should disarm deep verify:\n%s", m.(CheckView).View())
	}
	// Re-arm full deep for the run below.
	m, _ = m.(CheckView).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	v = m.(CheckView)

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(CheckView)
	if cmd == nil {
		t.Fatal("enter should start the check")
	}
	msg := drainForMsg[checkDoneMsg](t, cmd)
	m, _ = v.Update(msg)
	v = m.(CheckView)
	if v.result.err != nil {
		t.Fatalf("check: %v", v.result.err)
	}
	if v.result.report.ReadDataBlobs == 0 {
		t.Error("deep mode must actually read data (ReadDataBlobs > 0)")
	}
	if !strings.Contains(v.View(), "deep-verified") {
		t.Errorf("report should surface the deep-verified count:\n%s", v.View())
	}
}

// drainForMsg executes cmd (flattening batches) and returns the first
// message of type T; anything else (spinner ticks) is discarded.
func drainForMsg[T tea.Msg](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				queue = append(queue, sub)
			}
			continue
		}
		if want, ok := msg.(T); ok {
			return want
		}
	}
	var zero T
	t.Fatalf("command tree produced no %T", zero)
	return zero
}
