package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// recoveryKitAtDone drives a fresh RecoveryKitView through a build (no
// snapshot required — Build tolerates an empty repo) to the rkDone stage,
// the jumping-off point for 's' to open the save-path field.
func recoveryKitAtDone(t *testing.T) RecoveryKitView {
	t.Helper()
	r := newFlowRepo(t)
	v := NewRecoveryKitView(Deps{Repo: r, ConfigPath: "sentra.yaml"})
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RecoveryKitView)
	for _, msg := range execCmds(t, cmd) {
		if _, ok := msg.(recoveryKitDoneMsg); ok {
			m, _ = v.Update(msg)
			v = m.(RecoveryKitView)
		}
	}
	if v.stage != rkDone {
		t.Fatalf("recoveryKitAtDone: stage = %v, want rkDone", v.stage)
	}
	return v
}

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

// TestRecoveryKit_SavePathIsBoxedOnlyWhenFocused: the box appears only once
// 's' opens the save-path prompt — a delta assertion since the kit preview
// above it is free to grow its own chrome later.
func TestRecoveryKit_SavePathIsBoxedOnlyWhenFocused(t *testing.T) {
	v := recoveryKitAtDone(t)
	before := boxCount(v.View())

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	v = m.(RecoveryKitView)
	after := boxCount(v.View())

	if after-before != 1 {
		t.Fatalf("boxCount delta on save-path focus = %d, want 1 (before=%d after=%d)", after-before, before, after)
	}
}

// TestRecoveryKit_SaveKeySchedulesBlink: 's' opening the save-path prompt
// must start the cursor blinking. savePath already exists on v before 's'
// is sent, so BlinkSpeed can be dropped first — the returned cmd is the
// REAL one Focus() produced, and executing it (assertBlinkCmd does) would
// otherwise block for the default ~530ms.
func TestRecoveryKit_SaveKeySchedulesBlink(t *testing.T) {
	v := recoveryKitAtDone(t)
	v.savePath.Cursor.BlinkSpeed = time.Millisecond
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	assertBlinkCmd(t, cmd)
}

// TestRecoveryKit_RoutesBlinkTicksWhileSaving: blink ticks must reach the
// save-path field while it holds focus. A bare cursor.BlinkMsg{} won't do:
// bubbles/cursor tags each scheduled tick and rejects one whose tag doesn't
// match its current count (stale-tick guard), and Focus() already advanced
// that counter past zero — so the test captures a genuinely tag-matched
// tick from the field's own cursor instead of a zero-value literal.
// BlinkSpeed is dropped to make capturing one instant rather than a real
// ~530ms wait.
func TestRecoveryKit_RoutesBlinkTicksWhileSaving(t *testing.T) {
	v := recoveryKitAtDone(t)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	v = m.(RecoveryKitView)

	v.savePath.Cursor.BlinkSpeed = time.Millisecond
	tick := v.savePath.Cursor.BlinkCmd()
	_, cmd := v.Update(tick())
	if cmd == nil {
		t.Fatal("blink tick was not routed to the focused save-path field")
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

// TestRecoveryKit_LeavingSaveStageBlursTheField covers BOTH exits from the
// save prompt, because the rule is "leaving rkSaving blurs savePath", not
// "esc blurs savePath" — fixing only the exit that was reported would leave
// the identical defect on the other one.
//
// A field left focused after its stage is gone is not cosmetic. Its blink
// chain keeps rescheduling for something nobody renders, and because the
// box is drawn from Focused(), re-entering the stage would show a frame
// around a field the operator never focused.
func TestRecoveryKit_LeavingSaveStageBlursTheField(t *testing.T) {
	openPrompt := func(t *testing.T) RecoveryKitView {
		t.Helper()
		v := recoveryKitAtDone(t)
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
		v = m.(RecoveryKitView)
		if v.stage != rkSaving || !v.savePath.Focused() {
			t.Fatalf("precondition: want a focused savePath on rkSaving, got stage=%v focused=%v",
				v.stage, v.savePath.Focused())
		}
		return v
	}

	t.Run("esc", func(t *testing.T) {
		v := openPrompt(t)
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEsc})
		v = m.(RecoveryKitView)
		if v.stage != rkDone {
			t.Fatalf("stage = %v, want rkDone", v.stage)
		}
		if v.savePath.Focused() {
			t.Error("esc out of the save prompt must blur savePath")
		}
	})

	t.Run("successful write", func(t *testing.T) {
		v := openPrompt(t)
		v.savePath.SetValue(filepath.Join(t.TempDir(), "kit.md"))
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
		v = m.(RecoveryKitView)
		if v.stage != rkDone {
			t.Fatalf("stage = %v, want rkDone (saveErr=%q)", v.stage, v.saveErr)
		}
		if v.savePath.Focused() {
			t.Error("a successful write leaves the save prompt too — it must blur savePath")
		}
	})
}
