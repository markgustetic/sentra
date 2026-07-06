package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRecoveryKitFlow_RunsAndRendersMarkdown(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r) // one snapshot so the kit has a latest entry

	v := NewRecoveryKitView(Deps{Repo: r, ConfigPath: "sentra.yaml"})
	// Enter kicks off Build: view moves to running and returns a batch
	// (spinner tick + the build goroutine).
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RecoveryKitView)
	if v.stage != rkRunning {
		t.Fatalf("stage = %v, want rkRunning", v.stage)
	}
	if cmd == nil {
		t.Fatal("enter must start the build")
	}

	var done tea.Msg
	for _, msg := range execCmds(t, cmd) {
		if _, ok := msg.(recoveryKitDoneMsg); ok {
			done = msg
		}
	}
	if done == nil {
		t.Fatal("build command did not produce a recoveryKitDoneMsg")
	}
	// A recoveryKitDoneMsg must NOT be an opResultMsg — Build is read-only.
	if _, ok := done.(opResultMsg); ok {
		t.Fatal("recoveryKitDoneMsg must not implement opResult()")
	}
	m, _ = v.Update(done)
	v = m.(RecoveryKitView)
	if v.stage != rkDone {
		t.Fatalf("stage after result = %v, want rkDone", v.stage)
	}
	out := v.View()
	for _, want := range []string{"Sentra Recovery Kit", "Recovery Commands"} {
		if !strings.Contains(out, want) {
			t.Errorf("kit view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "flow-test-pass") {
		t.Fatalf("kit view leaked the passphrase:\n%s", out)
	}
}

func TestRecoveryKitFlow_SaveWritesFile(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)

	v := NewRecoveryKitView(Deps{Repo: r, ConfigPath: "sentra.yaml"})

	// Simulate a real terminal size the way the App does: it forwards an
	// *inner* WindowSizeMsg to each view. On a ~100x30 terminal the inner
	// height is small enough that the viewport can only show ~20 of the
	// kit's ~31 lines — so the save MUST persist the raw markdown, not the
	// clipped/space-padded viewport render, or the Recovery Commands and
	// disclaimer get dropped from the disaster-recovery artifact.
	m, _ := v.Update(tea.WindowSizeMsg{Width: 98, Height: 26})
	v = m.(RecoveryKitView)

	// Drive to done.
	var cmd tea.Cmd
	m, cmd = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RecoveryKitView)
	for _, msg := range execCmds(t, cmd) {
		if _, ok := msg.(recoveryKitDoneMsg); ok {
			m, _ = v.Update(msg)
			v = m.(RecoveryKitView)
		}
	}
	if v.stage != rkDone {
		t.Fatalf("stage = %v, want rkDone", v.stage)
	}

	// 's' reveals the save path input.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	v = m.(RecoveryKitView)
	if v.stage != rkSaving {
		t.Fatalf("stage after 's' = %v, want rkSaving", v.stage)
	}

	// Type a path and confirm.
	dst := filepath.Join(t.TempDir(), "kit.md")
	for _, ch := range dst {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		v = m.(RecoveryKitView)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RecoveryKitView)

	body, err := os.ReadFile(dst) //nolint:gosec // path under t.TempDir()
	if err != nil {
		t.Fatalf("read saved kit: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "# Sentra Recovery Kit") {
		t.Fatalf("saved file is not a rendered kit:\n%s", got)
	}
	// The whole kit must be present — the Recovery Commands section and its
	// concrete restore command line live near the bottom and are exactly
	// what a clipped viewport render would drop.
	if !strings.Contains(got, "## Recovery Commands") {
		t.Fatalf("saved kit is truncated: missing '## Recovery Commands' section:\n%s", got)
	}
	if !strings.Contains(got, "sentra restore ") || !strings.Contains(got, "--verify") {
		t.Fatalf("saved kit is truncated: missing the restore command line:\n%s", got)
	}
	if !strings.Contains(got, "This file intentionally excludes") {
		t.Fatalf("saved kit is truncated: missing the trailing disclaimer:\n%s", got)
	}
	// A viewport render right-pads every visible line to its width; the raw
	// markdown does not. No line may carry trailing-space padding.
	for _, line := range strings.Split(got, "\n") {
		if line != strings.TrimRight(line, " ") {
			t.Fatalf("saved kit has trailing-space padding (viewport render leaked): %q", line)
		}
	}
	// 0o600 — no group/other bits.
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("saved kit perms = %o, want 600", perm)
	}
	if v.stage != rkDone {
		t.Fatalf("stage after save = %v, want rkDone", v.stage)
	}
	if !strings.Contains(v.View(), dst) {
		t.Fatalf("done view should confirm the written path:\n%s", v.View())
	}
}

func TestRecoveryKitFlow_SaveErrorSurfaced(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)

	v := NewRecoveryKitView(Deps{Repo: r})
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RecoveryKitView)
	for _, msg := range execCmds(t, cmd) {
		if _, ok := msg.(recoveryKitDoneMsg); ok {
			m, _ = v.Update(msg)
			v = m.(RecoveryKitView)
		}
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	v = m.(RecoveryKitView)

	// A path whose parent dir does not exist forces os.WriteFile to fail.
	bad := filepath.Join(t.TempDir(), "no-such-dir", "kit.md")
	for _, ch := range bad {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		v = m.(RecoveryKitView)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RecoveryKitView)

	// Stays in the saving stage with an error banner rather than crashing.
	if v.stage != rkSaving {
		t.Fatalf("stage after failed save = %v, want rkSaving", v.stage)
	}
	if v.saveErr == "" {
		t.Fatal("failed save must set saveErr")
	}
}

func TestRecoveryKitFlow_NilRepoPlaceholder(t *testing.T) {
	v := NewRecoveryKitView(Deps{})
	if !strings.Contains(v.View(), "no repository") {
		t.Errorf("nil-repo view should show a placeholder:\n%s", v.View())
	}
}

// NOTE: registration of RecoveryKitView into NewApp's views slice is
// deliberately deferred to the plan's later "Part 9" task, which owns all
// view-slice registration in one place to avoid conflicting concurrent
// edits to app.go across Phase 2c's per-flow tasks. A registration test
// belongs with that task, not here.
