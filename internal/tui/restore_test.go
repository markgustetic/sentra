package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
)

// seedSnapshotReal backs up a one-file directory and returns the
// snapshot ID plus the original file's content for byte-compare after
// restore. Mirrors backup_test.go's use of the real in-memory repo.
func seedSnapshotReal(t *testing.T, r *repo.Repo) (string, string) {
	t.Helper()
	src := t.TempDir()
	content := "restore-me-" + t.Name()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{})
	if err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	return info.ID, content
}

func TestRestoreFlow_FullPath(t *testing.T) {
	r := newFlowRepo(t)
	snapID, content := seedSnapshotReal(t, r)

	v := NewRestoreView(Deps{Repo: r})
	// The view loads snapshots on Init (synchronous Phase 1-style hydrate).
	if len(v.snaps) != 1 {
		t.Fatalf("snaps loaded = %d, want 1", len(v.snaps))
	}

	// Stage 1: pick the snapshot.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RestoreView)
	if v.stage != restoreDest {
		t.Fatalf("stage = %v, want restoreDest", v.stage)
	}

	// Stage 2: type an empty destination dir.
	dest := filepath.Join(t.TempDir(), "out")
	for _, r := range dest {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(RestoreView)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RestoreView)
	if v.stage != restoreConfirm {
		t.Fatalf("stage = %v, want restoreConfirm (plan preview)", v.stage)
	}
	if !strings.Contains(v.View(), "1 file") && !strings.Contains(v.View(), "files") {
		t.Errorf("plan preview should show file count:\n%s", v.View())
	}

	// Stage 3: confirm starts the op. The command batches the startOpMsg
	// with the seeded first opTickMsg — both must be present (the tick
	// seeds the progress-repaint self-loop; its absence is the regression
	// this guards against).
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RestoreView)
	if cmd == nil {
		t.Fatal("confirm must emit a command")
	}
	msgs := execCmds(t, cmd)
	var start startOpMsg
	var foundStart, foundTick bool
	for _, msg := range msgs {
		switch mm := msg.(type) {
		case startOpMsg:
			start, foundStart = mm, true
		case opTickMsg:
			foundTick = true
		}
	}
	if !foundStart || start.name != "restore" {
		t.Fatalf("expected startOpMsg{restore} in the batch, got %#v", msgs)
	}
	if !foundTick {
		t.Fatalf("expected a seeded opTickMsg in the batch (progress repaint seed), got %#v", msgs)
	}
	res := start.run(context.Background())
	done, ok := res.(restoreDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("restore result: %#v", res)
	}

	// Bytes actually landed.
	got, err := os.ReadFile(filepath.Join(dest, "f.txt"))
	if err != nil || string(got) != content {
		t.Fatalf("restored content = %q (%v), want %q", got, err, content)
	}

	m, _ = v.Update(res)
	v = m.(RestoreView)
	if v.stage != restoreDone {
		t.Fatalf("stage after result = %v", v.stage)
	}
	_ = snapID
}

func TestRestoreFlow_NonEmptyDestSurfacedBeforeStart(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)
	v := NewRestoreView(Deps{Repo: r})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // pick
	v = m.(RestoreView)

	dest := t.TempDir() // non-empty? make it so:
	if err := os.WriteFile(filepath.Join(dest, "existing.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, r := range dest {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(RestoreView)
	}
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RestoreView)
	if v.stage == restoreConfirm && cmd != nil {
		t.Fatal("non-empty destination must not reach a startable confirm")
	}
	if !strings.Contains(strings.ToLower(v.View()), "empty") {
		t.Errorf("view should explain the non-empty destination:\n%s", v.View())
	}
}

// TestRestoreFlow_ScopedRestore: the dest stage carries an optional
// scope field (tab to reach it) that narrows the restore to the named
// paths — the TUI face of `restore <snap> <dest> [path...]`.
func TestRestoreFlow_ScopedRestore(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "skip.txt"), []byte("skip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}

	v := NewRestoreView(Deps{Repo: r})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // pick
	v = m.(RestoreView)

	dest := filepath.Join(t.TempDir(), "out")
	for _, ch := range dest {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		v = m.(RestoreView)
	}
	// tab → scope field, type the selector.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(RestoreView)
	for _, ch := range "keep.txt" {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		v = m.(RestoreView)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // plan
	v = m.(RestoreView)
	if v.stage != restoreConfirm {
		t.Fatalf("stage = %v, want restoreConfirm (destErr=%q)", v.stage, v.destErr)
	}

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // confirm
	v = m.(RestoreView)
	var start startOpMsg
	found := false
	for _, msg := range execCmds(t, cmd) {
		if s, ok := msg.(startOpMsg); ok {
			start = s
			found = true
		}
	}
	if !found {
		t.Fatal("confirm must emit the restore op")
	}
	if res := start.run(context.Background()); res == nil {
		t.Fatal("op returned nil")
	} else if done, ok := res.(restoreDoneMsg); !ok || done.err != nil {
		t.Fatalf("restore op: %+v", res)
	}

	if _, err := os.Stat(filepath.Join(dest, "keep.txt")); err != nil {
		t.Errorf("scoped file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "sub")); !os.IsNotExist(err) {
		t.Errorf("out-of-scope subtree restored (err=%v)", err)
	}
}

// TestRestoreFlow_ScopeFocusResetOnReentry: tab to the scope field,
// esc back to the picker, re-enter the dest stage — typing must land
// in the DEST field again. The stale focusScope flag routed keys into
// the blurred scope field.
func TestRestoreFlow_ScopeFocusResetOnReentry(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)

	v := NewRestoreView(Deps{Repo: r})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // pick → dest
	v = m.(RestoreView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}) // → scope
	v = m.(RestoreView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEsc}) // back to pick
	v = m.(RestoreView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // pick again → dest
	v = m.(RestoreView)

	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	v = m.(RestoreView)
	if v.dest.Value() != "x" {
		t.Errorf("typed rune should land in dest (got dest=%q scope=%q)", v.dest.Value(), v.scope.Value())
	}
	if v.focusScope {
		t.Error("focusScope must reset when re-entering the dest stage")
	}
}

// TestRestore_ExactlyOneBoxAndItFollowsFocus mirrors the brief's canonical
// shape: exactly one box, and it follows focus across tab. The pick->dest
// transition (restore.go:197) focuses dest for the first time, so its
// returned cmd must also schedule the blink.
func TestRestore_ExactlyOneBoxAndItFollowsFocus(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)
	v := NewRestoreView(Deps{Repo: r})
	// dest/scope already exist on v (just unfocused) before either
	// transition below fires, so their BlinkSpeed can be dropped up front —
	// both handlers' cmds are the REAL ones Focus() produces, and executing
	// them (assertBlinkCmd does) would otherwise block for the default
	// ~530ms each.
	v.dest.Cursor.BlinkSpeed = time.Millisecond
	v.scope.Cursor.BlinkSpeed = time.Millisecond

	m, entryCmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // pick -> dest
	v = m.(RestoreView)
	if v.stage != restoreDest {
		t.Fatalf("stage = %v, want restoreDest", v.stage)
	}
	assertBlinkCmd(t, entryCmd)

	base := v
	base.dest.Blur()
	base.scope.Blur()
	n := boxCount(base.View())

	if got := boxCount(v.View()); got != n+1 {
		t.Fatalf("dest focused: boxCount = %d, want %d (+1 over blurred)", got, n+1)
	}

	tabbed, cmd := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // dest -> scope
	tv := tabbed.(RestoreView)
	if got := boxCount(tv.View()); got != n+1 {
		t.Fatalf("box count changed on tab (got %d, want %d) — box must follow focus, one at a time", got, n+1)
	}
	assertBlinkCmd(t, cmd)

	tv.scope.Cursor.BlinkSpeed = time.Millisecond
	tick := tv.scope.Cursor.BlinkCmd()
	if _, tickCmd := tv.Update(tick()); tickCmd == nil {
		t.Fatal("blink tick not routed to the newly focused scope field")
	}
}

// TestRestore_RoutesBlinkTicksToDestField exercises the switch's other arm:
// a tick reaches dest while it (not scope) holds focus.
func TestRestore_RoutesBlinkTicksToDestField(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)
	v := NewRestoreView(Deps{Repo: r})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // pick -> dest
	v = m.(RestoreView)

	v.dest.Cursor.BlinkSpeed = time.Millisecond
	tick := v.dest.Cursor.BlinkCmd()
	if _, cmd := v.Update(tick()); cmd == nil {
		t.Fatal("blink tick not routed to the focused dest field")
	}
}

// restoreAtDestOnScope drives a fresh view to the dest stage with tab having
// moved focus onto scope, so a stage exit that blurs only dest would still
// be caught.
func restoreAtDestOnScope(t *testing.T) RestoreView {
	t.Helper()
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)
	v := NewRestoreView(Deps{Repo: r})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // pick → dest
	v = m.(RestoreView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}) // dest → scope
	v = m.(RestoreView)
	if v.stage != restoreDest || !v.scope.Focused() {
		t.Fatalf("precondition: want the dest stage with scope focused (stage=%v scope=%v)", v.stage, v.scope.Focused())
	}
	return v
}

// TestRestore_LeavingTheDestStageBlursBothFields covers both exits from the
// dest stage — esc back to the picker and enter on to the plan — because
// the rule is "leaving the stage blurs its fields", not "esc does".
func TestRestore_LeavingTheDestStageBlursBothFields(t *testing.T) {
	t.Run("esc back to the picker", func(t *testing.T) {
		v := restoreAtDestOnScope(t)
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEsc})
		v = m.(RestoreView)
		if v.stage != restorePick {
			t.Fatalf("stage = %v, want restorePick", v.stage)
		}
		if v.dest.Focused() || v.scope.Focused() {
			t.Errorf("esc out of the dest stage must blur both fields (dest=%v scope=%v)", v.dest.Focused(), v.scope.Focused())
		}
	})
	t.Run("enter on to the plan", func(t *testing.T) {
		v := restoreAtDestOnScope(t)
		v.dest.SetValue(filepath.Join(t.TempDir(), "out"))
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
		v = m.(RestoreView)
		if v.stage != restoreConfirm {
			t.Fatalf("stage = %v, want restoreConfirm (destErr=%q)", v.stage, v.destErr)
		}
		if v.dest.Focused() || v.scope.Focused() {
			t.Errorf("planning leaves the dest stage — it must blur both fields (dest=%v scope=%v)", v.dest.Focused(), v.scope.Focused())
		}
	})
}

// TestRestore_BackFromConfirmRefocusesDest: esc on the plan returns to the
// dest stage, which now has to re-focus its field and restart the blink —
// the blur on the way out ended the previous chain. Before that blur
// existed this worked only by accident: dest had simply never been blurred.
func TestRestore_BackFromConfirmRefocusesDest(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)
	v := NewRestoreView(Deps{Repo: r})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // pick → dest
	v = m.(RestoreView)
	v.dest.SetValue(filepath.Join(t.TempDir(), "out"))
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // dest → confirm
	v = m.(RestoreView)
	if v.stage != restoreConfirm {
		t.Fatalf("precondition: stage = %v, want restoreConfirm (destErr=%q)", v.stage, v.destErr)
	}

	v.dest.Cursor.BlinkSpeed = time.Millisecond
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	v = m.(RestoreView)
	if v.stage != restoreDest {
		t.Fatalf("stage = %v, want restoreDest", v.stage)
	}
	if !v.dest.Focused() || v.scope.Focused() {
		t.Fatalf("back on the dest stage, dest alone must be focused (dest=%v scope=%v)", v.dest.Focused(), v.scope.Focused())
	}
	assertBlinkCmd(t, cmd)
}
