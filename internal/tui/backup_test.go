package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/repo"
)

// newFlowRepo creates a real in-memory repo for flow tests. It takes a
// testing.TB so a fuzz target can build ONE repo for the whole run (repo.Init
// runs Argon2id; per-iteration would starve the fuzzer).
func newFlowRepo(tb testing.TB) *repo.Repo {
	tb.Helper()
	r, err := repo.Init(context.Background(), blobstore.NewMemory(), []byte("flow-test-pass"))
	if err != nil {
		tb.Fatalf("repo.Init: %v", err)
	}
	tb.Cleanup(func() { r.Close() })
	return r
}

// backupAtRepo returns a Location-stage BackupView on repo r, its folder picker
// browsing dir so the button chooses dir.
func backupAtRepo(t *testing.T, r *repo.Repo, dir string) BackupView {
	t.Helper()
	v := NewBackupView(Deps{Repo: r})
	v.picker = newDirPicker(dir)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m.(BackupView)
}

// backupAt returns a Location-stage BackupView rooted at a temp tree.
func backupAt(t *testing.T, root string) BackupView {
	t.Helper()
	v := NewBackupView(Deps{Repo: newFlowRepo(t)})
	v.picker = newDirPicker(root)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m.(BackupView)
}

// onStartButton puts the picker's cursor on the choose button — the top,
// default position (cursor 0) from which enter leaves the Location step. A
// fresh picker already opens here; this is explicit for tests that navigate
// first.
func onStartButton(v BackupView) BackupView {
	v.picker.cursor = 0
	return v
}

// toSchedule walks a Location-stage view to the Schedule step by pressing
// enter on the button.
func toSchedule(t *testing.T, v BackupView) BackupView {
	t.Helper()
	m, _ := onStartButton(v).Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(BackupView)
	if got.stage != backupSchedule {
		t.Fatalf("enter on the button: stage = %v, want backupSchedule (pathErr=%q)", got.stage, got.pathErr)
	}
	return got
}

// toConfirm walks on to the Confirm step (one-shot unless the caller moved
// the cadence first).
func toConfirm(t *testing.T, v BackupView) (BackupView, tea.Cmd) {
	t.Helper()
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(BackupView)
	if got.stage != backupConfirm {
		t.Fatalf("enter on the schedule step: stage = %v, want backupConfirm (err=%q)", got.stage, got.sched.err)
	}
	return got, cmd
}

// TestBackupWizard_StepsForwardAndBack: enter advances Location → Schedule →
// Confirm; esc steps back; Location's esc belongs to the shell.
func TestBackupWizard_StepsForwardAndBack(t *testing.T) {
	v := backupAt(t, tempTree(t))
	if v.stage != backupLocation || v.ConsumesEscape() {
		t.Fatal("a fresh view is on Location and leaves esc to the shell")
	}
	v = toSchedule(t, v)
	if !v.ConsumesEscape() || !v.ConsumesTab() || !v.ConsumesArrows() || v.CapturesText() {
		t.Fatal("Schedule with the list focused: esc/tab/arrows are ours, text is not captured")
	}
	if v.pending == "" {
		t.Fatal("leaving Location must record the chosen directory")
	}
	v, _ = toConfirm(t, v)
	if !v.ConsumesEscape() || !v.ConsumesTab() || v.ConsumesArrows() || !v.CapturesText() {
		t.Fatal("Confirm with the tag focused: esc/tab ours, text captured, arrows to the shell")
	}
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	v = m.(BackupView)
	if v.stage != backupSchedule || v.confirm.tag.Focused() {
		t.Fatalf("esc on Confirm → Schedule with the tag blurred; stage=%v", v.stage)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	v = m.(BackupView)
	if v.stage != backupLocation || v.sched.name.Focused() || v.sched.at.Focused() {
		t.Fatalf("esc on Schedule → Location with its fields blurred; stage=%v", v.stage)
	}
}

// Enter on a folder row navigates; only the button advances.
func TestBackupWizard_EnterOnFolderRowDoesNotAdvance(t *testing.T) {
	v := backupAt(t, tempTree(t))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown}) // onto ".."
	m, _ = m.(BackupView).Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.(BackupView).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.(BackupView); got.stage != backupLocation {
		t.Fatalf("enter on a folder row must navigate, not advance; stage=%v", got.stage)
	}
}

// The header names the step so the operator always knows where they are.
func TestBackupWizard_HeaderNamesTheStep(t *testing.T) {
	v := backupAt(t, tempTree(t))
	for _, want := range []string{"New backup", "Step 1 of 3", "Location"} {
		if !strings.Contains(v.View(), want) {
			t.Errorf("Location view lacks %q:\n%s", want, v.View())
		}
	}
	v = toSchedule(t, v)
	if !strings.Contains(v.View(), "Step 2 of 3") || !strings.Contains(v.View(), "Schedule") {
		t.Errorf("Schedule view lacks its header:\n%s", v.View())
	}
	v, _ = toConfirm(t, v)
	if !strings.Contains(v.View(), "Step 3 of 3") || !strings.Contains(v.View(), "Confirm") {
		t.Errorf("Confirm view lacks its header:\n%s", v.View())
	}
}

// Confirm's summary names the directory and the schedule; one-shot installs
// nothing and enter starts the op through the one-op guard with the seeded
// first tick.
func TestBackupWizard_OneShotConfirmStartsTheBackup(t *testing.T) {
	root := tempTree(t)
	// A wide terminal: the summary clips a path that does not fit its row
	// (TestBackupWizard_ConfirmFitsTheMinWidthPanel covers that), and a
	// temp path is long enough to hit the clip at the default 100.
	m0, _ := backupAt(t, root).Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	v, _ := toConfirm(t, toSchedule(t, m0.(BackupView)))
	if !strings.Contains(v.View(), root) || !strings.Contains(v.View(), "one-shot") {
		t.Fatalf("confirm summary must name the directory and one-shot:\n%s", v.View())
	}
	v.confirm.tag.SetValue("nightly")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	if v.stage != backupRunning {
		t.Fatalf("stage = %v, want backupRunning (pathErr=%q)", v.stage, v.pathErr)
	}
	if v.confirm.tag.Focused() {
		t.Error("starting must blur the tag field")
	}
	var foundStart, foundTick bool
	for _, msg := range execCmds(t, cmd) {
		switch mm := msg.(type) {
		case startOpMsg:
			foundStart = true
			if mm.name != "backup" {
				t.Errorf("op name = %q, want backup", mm.name)
			}
		case opTickMsg:
			foundTick = true
		}
	}
	if !foundStart || !foundTick {
		t.Errorf("start=%v tick=%v; both must be batched", foundStart, foundTick)
	}
}

// The rescan toggle and the tag reach SnapshotOptions: prove it end to end by
// running the op and reading the snapshot back. Delivering the result moves
// the wizard to its done screen, which names the snapshot.
func TestBackupWizard_TagReachesTheSnapshot(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, _ := toConfirm(t, toSchedule(t, backupAtRepo(t, r, src)))
	v.confirm.tag.SetValue("nightly")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	for _, msg := range execCmds(t, cmd) {
		if start, ok := msg.(startOpMsg); ok {
			res := start.run(context.Background())
			done := res.(backupDoneMsg)
			if done.err != nil {
				t.Fatalf("backup failed: %v", done.err)
			}
			if done.info.Tag != "nightly" {
				t.Fatalf("snapshot tag = %q, want nightly", done.info.Tag)
			}
			m, _ = v.Update(res)
			v = m.(BackupView)
			if v.stage != backupDone {
				t.Fatalf("stage after result = %v, want backupDone", v.stage)
			}
			if out := v.View(); !strings.Contains(out, done.info.ID) {
				t.Errorf("done screen should show the snapshot ID:\n%s", out)
			}
			return
		}
	}
	t.Fatal("no startOpMsg emitted")
}

// The toggle and the [x] glyph only prove the UI's own state; nothing
// upstream of this test pinned that arming it actually reaches
// SnapshotOptions.ForceRescan on the real backup. SnapshotInfo carries no
// ForceRescan field to read back, and an unchanged file's stats (Files,
// Bytes, NewBytes) come out identical whether the incremental scan reused
// its parent's chunks or a forced rescan re-hashed them to the same
// content — so nothing in the result distinguishes the two paths for a
// quiet file. What does distinguish them, and what
// TestCreateSnapshot_ReusesUnchangedParentEntries (internal/repo) already
// uses to prove the repo layer's half of this: whether the file gets
// OPENED. chmod 000 blocks the open; the incremental scan never opens an
// unchanged (matching size+mtime) file, so it would silently succeed by
// reusing the parent's chunk list, while ForceRescan must open every file
// and so must fail on this one. Running the wizard's own startOpMsg (the
// same route TestBackupWizard_TagReachesTheSnapshot uses) against a real
// repo and asserting the run FAILS is a direct behavioral proof that the
// armed toggle reached CreateSnapshot's ForceRescan option.
func TestBackupWizard_RescanToggleReachesOptions(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("chmod 000 does not block reads for root")
	}
	r := newFlowRepo(t)
	src := t.TempDir()
	path := filepath.Join(src, "a.txt")
	if err := os.WriteFile(path, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Parent snapshot so the incremental scan below has a size+mtime match
	// to reuse instead of opening the file.
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
		t.Fatalf("parent snapshot: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	v, _ := toConfirm(t, toSchedule(t, backupAtRepo(t, r, src)))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // rescan row
	m, _ = m.(BackupView).Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	v = m.(BackupView)
	if !v.confirm.rescan || !strings.Contains(v.View(), "[x]") {
		t.Fatalf("space on the rescan row must arm it:\n%s", v.View())
	}
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // enter from the rescan row confirms too
	if got := m.(BackupView); got.stage != backupRunning {
		t.Fatalf("enter on the rescan row must confirm; stage=%v", got.stage)
	}

	var found bool
	for _, msg := range execCmds(t, cmd) {
		if start, ok := msg.(startOpMsg); ok {
			found = true
			res := start.run(context.Background())
			done := res.(backupDoneMsg)
			if done.err == nil {
				t.Fatal("ForceRescan must open every file and fail on the unreadable one; " +
					"the run succeeded instead, which means the armed toggle never reached ForceRescan " +
					"(the incremental scan silently reused the parent's chunks)")
			}
		}
	}
	if !found {
		t.Fatal("no startOpMsg emitted")
	}
}

// Focus seams on Schedule: tab onto the name field captures text, blinks,
// boxes; viewHiddenMsg blurs; viewShownMsg re-focuses the stage's field.
func TestBackupWizard_ScheduleFieldFocusSeams(t *testing.T) {
	v := toSchedule(t, backupAt(t, tempTree(t)))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown}) // hourly: name appears
	v = m.(BackupView)
	v.sched.name.Cursor.BlinkSpeed = time.Millisecond
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(BackupView)
	assertBlinkCmd(t, cmd)
	if !v.CapturesText() || v.ConsumesArrows() || boxCount(v.View()) != 1 {
		t.Fatalf("name focused: captures=%v arrows=%v boxes=%d", v.CapturesText(), v.ConsumesArrows(), boxCount(v.View()))
	}
	m, _ = v.Update(viewHiddenMsg{})
	if got := m.(BackupView); got.sched.name.Focused() {
		t.Fatal("viewHiddenMsg must blur the schedule field")
	}
	m, showCmd := m.(BackupView).Update(viewShownMsg{})
	if got := m.(BackupView); !got.sched.name.Focused() {
		t.Fatal("viewShownMsg must re-focus the field the stage owns")
	}
	assertBlinkCmd(t, showCmd)
}

// Blink ticks must reach whichever field the current stage focuses, or the
// chain stops the moment it starts. A bare cursor.BlinkMsg{} won't do:
// bubbles/cursor rejects a tick whose tag doesn't match the field's own
// counter, so the tick is minted from the field's cursor.
func TestBackupWizard_RoutesBlinkTicksToTheFocusedField(t *testing.T) {
	v, _ := toConfirm(t, toSchedule(t, backupAt(t, tempTree(t))))
	if !v.confirm.tag.Focused() {
		t.Fatal("precondition: Confirm focuses the tag")
	}
	v.confirm.tag.Cursor.BlinkSpeed = time.Millisecond
	tick := v.confirm.tag.Cursor.BlinkCmd()
	if _, cmd := v.Update(tick()); cmd == nil {
		t.Fatal("blink tick was not routed to the focused tag field")
	}
}

// A chat intent lands on Confirm with the directory and tag seeded, one-shot,
// and the tag focused — the confirm screen is the human gate the chat needs.
func TestBackupWizard_ChatIntentLandsOnConfirm(t *testing.T) {
	src := tempTree(t)
	v := NewBackupView(Deps{Repo: newFlowRepo(t)})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, cmd := m.(BackupView).Update(chatBackupMsg{dir: src, tag: "from-chat"})
	v = m.(BackupView)
	if v.stage != backupConfirm || v.pending != src || v.confirm.tag.Value() != "from-chat" || !v.sched.oneShot() {
		t.Fatalf("chat seed: stage=%v pending=%q tag=%q oneShot=%v", v.stage, v.pending, v.confirm.tag.Value(), v.sched.oneShot())
	}
	if !v.confirm.tag.Focused() {
		t.Fatal("landing on Confirm focuses the tag")
	}
	assertBlinkCmd(t, cmd)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.(BackupView); got.stage != backupRunning {
		t.Fatalf("enter on the seeded Confirm starts; stage=%v", got.stage)
	}
}

// A chat intent naming a directory that is gone drops the wizard back to
// Location with the error — and must leave no field focused behind it. The
// operator can be anywhere when the intent arrives (ctrl+a works on every
// step), and a field focused on a stage that never renders it blinks
// forever while Focused() lies to every guard.
func TestBackupWizard_ChatIntentWithAMissingDirBlursTheStep(t *testing.T) {
	v, _ := toConfirm(t, toSchedule(t, backupAt(t, tempTree(t))))
	if !v.confirm.tag.Focused() {
		t.Fatal("precondition: Confirm focuses the tag")
	}
	m, cmd := v.Update(chatBackupMsg{dir: filepath.Join(t.TempDir(), "gone"), tag: "x"})
	v = m.(BackupView)
	if v.stage != backupLocation {
		t.Fatalf("a missing directory must drop back to Location; stage=%v", v.stage)
	}
	if cmd != nil {
		t.Error("a refused intent must schedule nothing")
	}
	if v.confirm.tag.Focused() || v.sched.name.Focused() || v.sched.at.Focused() {
		t.Errorf("no field may stay focused: tag=%v name=%v at=%v",
			v.confirm.tag.Focused(), v.sched.name.Focused(), v.sched.at.Focused())
	}
	out := v.View()
	if n := boxCount(out); n != 0 {
		t.Errorf("Location renders %d field box(es), want 0:\n%s", n, out)
	}
	if !strings.Contains(out, "not found") {
		t.Errorf("the refused intent's error must be visible:\n%s", out)
	}
}

// The chat's start_backup intent rebuilds the picker for the seeded
// directory (enterSchedule keeps v.picker as-is, but the earlier code
// path replaced it outright): a fresh newDirPicker starts at width 0
// (unbounded), so the column width the last resize computed must be
// carried onto it — otherwise two escs back to Location render folder
// rows unclipped until the next resize.
func TestBackupWizard_ChatIntentPreservesPickerWidth(t *testing.T) {
	src := tempTree(t)
	v := NewBackupView(Deps{Repo: newFlowRepo(t)})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = m.(BackupView)
	wantWidth := v.picker.width
	if wantWidth == 0 {
		t.Fatal("precondition: a sized view must have a nonzero picker width")
	}
	m, _ = v.Update(chatBackupMsg{dir: src, tag: "from-chat"})
	v = m.(BackupView)
	if v.stage != backupConfirm {
		t.Fatalf("precondition: chat intent lands on Confirm, got %v", v.stage)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEsc})              // Confirm -> Schedule
	m, _ = m.(BackupView).Update(tea.KeyMsg{Type: tea.KeyEsc}) // Schedule -> Location
	v = m.(BackupView)
	if v.stage != backupLocation {
		t.Fatalf("two escs from Confirm must land on Location, got %v", v.stage)
	}
	if v.picker.width != wantWidth {
		t.Errorf("picker.width = %d after the chat seed, want %d preserved from the last resize", v.picker.width, wantWidth)
	}
}

// A refused start (another op running) returns to Confirm with the tag
// re-focused, so the operator can retry without re-walking the wizard.
func TestBackupWizard_RejectedStartReturnsToConfirm(t *testing.T) {
	v, _ := toConfirm(t, toSchedule(t, backupAt(t, tempTree(t))))
	v.confirm.tag.Cursor.BlinkSpeed = time.Millisecond
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, cmd := m.(BackupView).Update(opRejectedMsg{name: "backup"})
	v = m.(BackupView)
	if v.stage != backupConfirm || !v.confirm.tag.Focused() || v.notice == "" {
		t.Fatalf("rejection: stage=%v tagFocused=%v notice=%q", v.stage, v.confirm.tag.Focused(), v.notice)
	}
	assertBlinkCmd(t, cmd)
}

// The picker only ever offers real directories, so the wizard's stat guard
// exists for one case: the browsed folder disappears before enter. Pin it.
func TestBackupFlow_VanishedFolderRefusesToStart(t *testing.T) {
	dir := t.TempDir()
	v := onStartButton(backupAtRepo(t, newFlowRepo(t), dir))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // enter on the choose button
	v = m.(BackupView)
	if cmd != nil {
		t.Fatal("a folder that no longer exists must not advance the wizard")
	}
	if v.stage != backupLocation {
		t.Fatal("the wizard must stay on Location when the folder is gone")
	}
	if !strings.Contains(v.View(), "not found") {
		t.Errorf("view should surface the error:\n%s", v.View())
	}
}

func TestBackupFlow_EscDuringRunEmitsCancel(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, _ := toConfirm(t, toSchedule(t, backupAtRepo(t, r, src)))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // confirm → running
	v = m.(BackupView)
	if v.stage != backupRunning {
		t.Fatalf("confirming must start the backup; stage = %v", v.stage)
	}
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc while running must emit a command")
	}
	if _, ok := cmd().(cancelOpMsg); !ok {
		t.Fatalf("expected cancelOpMsg, got %#v", cmd())
	}
}

// TestBackupFlow_RunAnotherKeepsSizing: "run another" must not reset the
// progress-bar width to its default — bubbletea won't re-emit a
// WindowSizeMsg after a model swap, so the fresh view must carry the last
// known size forward.
func TestBackupFlow_RunAnotherKeepsSizing(t *testing.T) {
	v := NewBackupView(Deps{Repo: newFlowRepo(t)})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = m.(BackupView)
	v.stage = backupDone // simulate a finished backup
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	fresh := m.(BackupView)
	if got, want := fresh.bar.Width, min(100-8, 60); got != want {
		t.Errorf("bar width after 'run another' = %d, want %d (sizing lost on reset)", got, want)
	}
}

// Done after a scheduled run names the policy and next run, and `s` jumps
// to the Scheduled backups tab.
func TestBackupWizard_DoneOffersScheduledBackups(t *testing.T) {
	v := backupAt(t, tempTree(t))
	v.installedName = "docs"
	v.installedNext = time.Date(2026, 9, 3, 2, 0, 0, 0, time.UTC)
	v.installedNextOK = true
	m, _ := v.Update(backupDoneMsg{info: repo.SnapshotInfo{ID: "abc"}})
	v = m.(BackupView)
	for _, want := range []string{`policy "docs" installed`, "next run Thu 2026-09-03 02:00", "s scheduled backups"} {
		if !strings.Contains(v.View(), want) {
			t.Errorf("done view lacks %q:\n%s", want, v.View())
		}
	}
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	found := false
	for _, msg := range execCmds(t, cmd) {
		if a, ok := msg.(activateMsg); ok && a.id == "jobs" {
			found = true
		}
	}
	if !found {
		t.Fatal("'s' on Done must emit activateMsg{jobs}")
	}
}

// Keys through the whole wizard at App level: enter (Location) → enter
// (Schedule, one-shot) → enter (Confirm) → running, with no modal raised.
func TestApp_BackupWizardEndToEnd(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp(Deps{Repo: r, RepoName: "x"})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = m.(App)
	bi := -1
	for i, v := range app.views {
		if v.id == "backup" {
			bi = i
		}
	}
	app.active, app.focus = bi, focusContent
	bv := app.views[bi].model.(BackupView)
	bv.picker = newDirPicker(src)
	app.views[bi].model = bv

	press := func(k tea.KeyMsg) {
		m, cmd := app.Update(k)
		app = m.(App)
		for _, msg := range execCmds(t, cmd) {
			if _, blink := msg.(cursor.BlinkMsg); blink {
				continue
			}
			m, _ = app.Update(msg)
			app = m.(App)
		}
	}
	stage := func() backupStage { return app.views[bi].model.(BackupView).stage }

	press(tea.KeyMsg{Type: tea.KeyEnter})
	if stage() != backupSchedule {
		t.Fatalf("after enter on Location: stage = %v", stage())
	}
	press(tea.KeyMsg{Type: tea.KeyEnter})
	if stage() != backupConfirm {
		t.Fatalf("after enter on Schedule: stage = %v", stage())
	}
	if len(app.modals) != 0 {
		t.Fatalf("the wizard must raise no modal, got %d", len(app.modals))
	}
	press(tea.KeyMsg{Type: tea.KeyEsc})
	if stage() != backupSchedule {
		t.Fatalf("esc on Confirm must step back, stage = %v", stage())
	}
	press(tea.KeyMsg{Type: tea.KeyEnter})
	press(tea.KeyMsg{Type: tea.KeyEnter})
	if stage() != backupRunning {
		t.Fatalf("after enter on Confirm: stage = %v", stage())
	}
}

// Down moves the highlight; enter on a folder row descends (does not advance).
func TestBackupPickerNavigatesWithoutStarting(t *testing.T) {
	root := tempTree(t)
	v := backupAt(t, root)

	// The picker opens on the top choose button; step down past ".." onto the
	// first folder (alpha).
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown}) // button -> ".."
	v = m.(BackupView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown}) // ".." -> alpha
	v = m.(BackupView)
	if got := v.picker.rows[v.picker.cursor-1].label; got != "alpha" {
		t.Fatalf("cursor on %q, want alpha", got)
	}

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // descend into alpha
	v = m.(BackupView)
	if cmd != nil {
		t.Error("enter on a folder row must navigate, not advance the wizard")
	}
	if filepath.Base(v.picker.cwd) != "alpha" {
		t.Fatalf("enter on a child must descend, cwd = %q", v.picker.cwd)
	}
	if v.stage != backupLocation {
		t.Error("navigating must not leave the Location step")
	}
}

// Backspace climbs out of a directory.
func TestBackupPickerBackspaceGoesUp(t *testing.T) {
	root := tempTree(t)
	v := backupAt(t, filepath.Join(root, "beta"))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	v = m.(BackupView)
	if v.picker.cwd != root {
		t.Fatalf("cwd after backspace = %q, want %q", v.picker.cwd, root)
	}
}

// On the choose button the footer must promise choosing the browsed folder —
// a footer that says "open" while the cursor rests on the button would be lying.
func TestBackupStartButtonFooterSaysChoose(t *testing.T) {
	root := tempTree(t)
	v := onStartButton(backupAt(t, root))
	want := "choose " + filepath.Base(root)
	if v.picker.enterVerb() != want {
		t.Errorf("button verb = %q, want %q", v.picker.enterVerb(), want)
	}
	if !strings.Contains(v.View(), "Press enter to "+want) {
		t.Errorf("footer must say %q:\n%s", "Press enter to "+want, v.View())
	}
}

// pickerAt points the view's picker at a hermetic tree so preview
// assertions don't depend on the process cwd. It keeps the width the
// resize already derived — a fresh picker starts at 0 (unbounded), and
// losing the bound would let a long temp path wrap inside the column and
// mask the very defect the width field exists to prevent.
func pickerAt(v BackupView, dir string) BackupView {
	w := v.picker.width
	v.picker = newDirPicker(dir)
	v.picker.width = w
	return v
}

// At the 80-col minimum (the App forwards 59) the pane renders beside the
// picker, previewing the current directory, and NO line exceeds the
// panel's text region — the rule the two-column join must uphold.
func TestBackupView_PreviewPaneAtMinSize(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := NewBackupView(Deps{})
	const forwarded = 59 // contentW the App forwards at an 80-col terminal
	m, _ := v.Update(tea.WindowSizeMsg{Width: forwarded, Height: 16})
	v = pickerAt(m.(BackupView), root)

	// Arm the notice banner: it must clip to the panel just like the
	// picker's own rows do (a review finding — the banner could overflow
	// the interior on its own before the fit fix).
	v.notice = "another operation is in progress — try again when it finishes"

	out := v.View()
	if want := "in " + filepath.Base(root) + string(filepath.Separator); !strings.Contains(out, want) {
		t.Errorf("pane must preview the cwd (missing %q):\n%s", want, out)
	}
	if !strings.Contains(out, "hello.txt") {
		t.Errorf("pane must list the cwd's files:\n%s", out)
	}
	region := pickerContentWidth(forwarded)
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > region {
			t.Errorf("line %d exceeds content region (%d > %d): %q", i, w, region, line)
		}
	}
	// The footer must stay actionable at min size.
	if !strings.Contains(out, "esc leaves") {
		t.Errorf("footer must keep its hints at min size:\n%s", out)
	}
}

// The Confirm step must respect the same panel bound, with the worst case
// armed: a deep directory path, a long tag, and the tag FOCUSED — focus
// wraps it in ui.FieldBox, which costs 4 cells the interior has to have
// room for. The unfocused pass cannot catch that; the frame only exists
// while the field owns the keyboard, i.e. exactly when someone is typing.
func TestBackupWizard_ConfirmFitsTheMinWidthPanel(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a-fairly-deeply-nested", "documents-and-settings", "archive")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	v := NewBackupView(Deps{Repo: newFlowRepo(t)})
	const forwarded = 59
	m, _ := v.Update(tea.WindowSizeMsg{Width: forwarded, Height: 16})
	v = pickerAt(m.(BackupView), root)
	v, _ = toConfirm(t, toSchedule(t, v))
	v.confirm.tag.SetValue(strings.Repeat("tag-", 30))
	v.notice = "another operation is in progress — try again when it finishes"

	out := v.View()
	if !strings.Contains(out, "╭") {
		t.Fatalf("precondition: Confirm frames the focused tag field:\n%s", out)
	}
	region := pickerContentWidth(forwarded)
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > region {
			t.Errorf("line %d exceeds content region (%d > %d): %q", i, w, region, line)
		}
	}
}

// The pane follows the picker cursor through real key routing, not just
// the model: two ↓ from the choose button rest on the first child dir.
func TestBackupView_PreviewFollowsCursorThroughKeys(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "docs")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "inner.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := NewBackupView(Deps{})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 59, Height: 16})
	v = pickerAt(m.(BackupView), root)

	for range 2 { // choose button → ".." → docs
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown})
		v = m.(BackupView)
	}
	out := v.View()
	if !strings.Contains(out, "in docs"+string(filepath.Separator)) {
		t.Errorf("pane must preview the hovered folder:\n%s", out)
	}
	if !strings.Contains(out, "inner.txt") {
		t.Errorf("pane must list the hovered folder's files:\n%s", out)
	}
}

// Below the width threshold the pane hides entirely and the picker keeps
// the full interior — the degraded layout IS today's layout.
func TestBackupView_PreviewPaneHidesWhenNarrow(t *testing.T) {
	root := t.TempDir()
	v := NewBackupView(Deps{})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 50, Height: 16}) // interior 48 → pane would get 14 < 20
	v = pickerAt(m.(BackupView), root)

	if out := v.View(); strings.Contains(out, "in "+filepath.Base(root)+string(filepath.Separator)) {
		t.Errorf("pane must hide below the threshold:\n%s", out)
	}
}

// The threshold rule itself: pane width is interior minus the fixed
// picker column and gap, floored to hidden below previewMinWidth and
// capped at previewMaxWidth — on a very wide terminal an uncapped pane
// pushes the right-aligned sizes far from their names.
func TestPreviewPaneWidth(t *testing.T) {
	cases := []struct{ interior, want int }{
		{57, 23},  // 80-col terminal: 57 - 32 - 2
		{54, 20},  // exactly the floor
		{53, 0},   // one below → hidden
		{0, 0},    // no size yet (fresh view before WindowSizeMsg)
		{-2, 0},   // pickerContentWidth(0)
		{82, 48},  // exactly the cap
		{196, 48}, // ~200-col terminal → capped, not 162
	}
	for _, c := range cases {
		if got := previewPaneWidth(c.interior); got != c.want {
			t.Errorf("previewPaneWidth(%d) = %d, want %d", c.interior, got, c.want)
		}
	}
}

// Confirm's rescan row is a toggle with no vertical motion of its own, but
// reporting "arrows to the shell" there sends ↑/↓ to the nav rail and the
// live preview swaps the view out from under the wizard. The row owns them
// and drops them; the tag field, one tab away, still leaves them to the shell.
func TestBackupWizard_RescanRowOwnsVerticalArrows(t *testing.T) {
	v, _ := toConfirm(t, toSchedule(t, backupAt(t, tempTree(t))))
	if v.ConsumesArrows() {
		t.Fatal("Confirm with the tag focused leaves ↑/↓ to the shell")
	}
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // rescan row
	v = m.(BackupView)
	if v.confirm.focus != confirmRescan || !v.ConsumesArrows() {
		t.Fatalf("rescan row: focus=%v consumesArrows=%v, want rescan/true", v.confirm.focus, v.ConsumesArrows())
	}
	for _, k := range []tea.KeyMsg{{Type: tea.KeyDown}, {Type: tea.KeyUp}} {
		m, _ = v.Update(k)
		got := m.(BackupView)
		if got.stage != backupConfirm || got.confirm.focus != confirmRescan || got.confirm.rescan || got.confirm.tag.Focused() {
			t.Fatalf("%s on the rescan row changed the step: stage=%v focus=%v rescan=%v tagFocused=%v",
				k, got.stage, got.confirm.focus, got.confirm.rescan, got.confirm.tag.Focused())
		}
	}
}
