package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// assertBlinkCmd fails t unless cmd (possibly a batch) yields at least
// one cursor.BlinkMsg when executed. Blink TIMING is untestable; the
// contract under test is that a focus transition schedules the blink.
//
// The canonical production cmd is textinput.Model.Focus()'s own return
// value — the real, tag-matched cursor.BlinkCmd() output — NOT the
// textinput.Blink package var. textinput.Blink resolves to cursor's
// UNEXPORTED bootstrap message (initialBlinkMsg{}); no view's Update switch
// can name that type (only cursor.BlinkMsg is exported), so a cmd built
// from textinput.Blink is silently dropped by every view's routing case —
// the blink chain never starts in a live terminal even though such a cmd is
// non-nil and "looks" fine to a naive nil check. Every focus site in this
// package now returns Focus()'s own cmd for exactly this reason.
//
// yieldsBlink accepts ONLY the real cursor.BlinkMsg, never textinput.Blink's
// bootstrap sentinel — see its doc comment for why that rejection is what
// gives every *SchedulesBlink test in this package its teeth.
//
// A CAVEAT for callers: because Focus()'s cmd is real, EXECUTING it (as
// this function does) blocks until the field's Cursor.BlinkSpeed elapses
// (~530ms by default) before yielding cursor.BlinkMsg. For a focus
// transition triggered by a key/message the test sends, drop the target
// field's Cursor.BlinkSpeed to time.Millisecond BEFORE sending it, so the
// wait is negligible (the technique RoutesBlinkTicks tests already use for
// hand-minting a tag-matched tick applies equally well here). For a
// CONSTRUCTION-time focus (Focus() called inside a NewXxxView/NewPalette/
// NewTypedConfirmModal constructor, surfaced later via Init()), there is no
// field handle to adjust before that Focus() call runs — those call sites
// assert the cmd is non-nil directly instead of calling this helper, and
// TestBlinkChain_ClosesEndToEnd (in snapshots_test.go) is the one place
// that proves a REAL Focus()-cmd's execution round-trips end-to-end,
// using a key-triggered (presettable) site to keep it fast.
func assertBlinkCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a blink command, got nil")
	}
	if !yieldsBlink(cmd()) {
		t.Fatal("command did not yield cursor.BlinkMsg")
	}
}

// yieldsBlink recursively checks whether msg (possibly a batch of commands)
// yields a cursor blink signal. It accepts EXACTLY ONE form: the real
// cursor.BlinkMsg that a genuine Focus()/BlinkCmd() round-trip produces
// (and batches containing one).
//
// It deliberately does NOT accept cursor.Blink()'s unexported bootstrap
// sentinel, which is what textinput.Blink yields. Accepting it would make
// this helper blind to the precise regression the package was fixed for:
// with a `msg == cursor.Blink()` branch here, reintroducing textinput.Blink
// at any of the ~ten focus sites stayed green even though no view's Update
// switch can name that unexported type, so the blink chain never started in
// a live terminal. The helper must reject what production must never emit.
func yieldsBlink(msg tea.Msg) bool {
	switch m := msg.(type) {
	case cursor.BlinkMsg:
		return true
	case tea.BatchMsg:
		for _, c := range m {
			if c != nil && yieldsBlink(c()) {
				return true
			}
		}
	}
	return false
}

// boxCount counts FieldBox frames in a render via the top-left corner.
// The rule every view test asserts: focusing a field adds exactly one.
func boxCount(s string) int { return strings.Count(s, "╭") }

// TestAssertBlinkCmd_AcceptsOnlyTheRealBlink pins the helper's discrimination
// in both directions: it must accept a real cursor.BlinkMsg (bare or
// batched) and must REJECT textinput.Blink's bootstrap sentinel. The
// rejection half is the load-bearing one — it is what makes every
// *SchedulesBlink test in this package able to catch a regression back to
// the dead-end sentinel.
func TestAssertBlinkCmd_AcceptsOnlyTheRealBlink(t *testing.T) {
	real := func() tea.Msg { return cursor.BlinkMsg{} }
	if !yieldsBlink(real()) {
		t.Error("a bare cursor.BlinkMsg must be accepted")
	}
	if !yieldsBlink(tea.Batch(func() tea.Msg { return nil }, real)()) {
		t.Error("a batched cursor.BlinkMsg must be accepted")
	}
	if yieldsBlink(textinput.Blink()) {
		t.Error("textinput.Blink's bootstrap sentinel must be REJECTED: no view's Update switch can name that unexported type, so a cmd built from it never starts the blink chain")
	}
	if yieldsBlink(tea.Batch(textinput.Blink)()) {
		t.Error("a batched textinput.Blink must be REJECTED for the same reason")
	}
}

// fieldOwners enumerates every view that renders a boxed text field, each
// driven to the stage that owns its field so the field is focused, together
// with a blurAll that force-blurs every field the view has while leaving the
// stage exactly where it is.
//
// The pair is what lets a test separate the two things a box could be drawn
// from — the stage flag and the field's own Focused() — because a view that
// boxes on its stage keeps the frame after blurAll, and one that boxes on
// Focused() drops it. Tests below iterate this table so the rule is asserted
// for the CLASS of views, not re-derived per view (see "test the rule, not
// the case" in the repo's memory notes).
type fieldOwner struct {
	name    string
	focused func(t *testing.T) tea.Model
	blurAll func(m tea.Model) tea.Model
}

func fieldOwners() []fieldOwner {
	return []fieldOwner{
		{
			name:    "unlock",
			focused: func(t *testing.T) tea.Model { return NewUnlockView(unlockDeps(t, "hunter2")) },
			blurAll: func(m tea.Model) tea.Model {
				v := m.(UnlockView)
				v.input.Blur()
				return v
			},
		},
		{
			name:    "password",
			focused: func(t *testing.T) tea.Model { return NewPasswordView(Deps{Repo: newFlowRepo(t)}) },
			blurAll: func(m tea.Model) tea.Model {
				v := m.(PasswordView)
				v.newPass.Blur()
				v.confirmPass.Blur()
				return v
			},
		},
		{
			name:    "sync",
			focused: func(t *testing.T) tea.Model { return NewSyncView(Deps{Repo: newFlowRepo(t)}) },
			blurAll: func(m tea.Model) tea.Model {
				v := m.(SyncView)
				v.dstPath.Blur()
				v.snapRefs.Blur()
				return v
			},
		},
		{
			name: "backup",
			focused: func(t *testing.T) tea.Model {
				m, _ := backupAt(t, tempTree(t)).Update(tea.KeyMsg{Type: tea.KeyTab})
				return m
			},
			blurAll: func(m tea.Model) tea.Model {
				v := m.(BackupView)
				v.tag.Blur()
				return v
			},
		},
		{
			name: "snapshots",
			focused: func(t *testing.T) tea.Model {
				m, _ := NewSnapshots(Deps{}).SetSnapshots(sampleSnaps()).
					Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
				return m
			},
			blurAll: func(m tea.Model) tea.Model {
				s := m.(Snapshots)
				s.filter.Blur()
				return s
			},
		},
		{
			name: "recovery-kit",
			focused: func(t *testing.T) tea.Model {
				m, _ := recoveryKitAtDone(t).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
				return m
			},
			blurAll: func(m tea.Model) tea.Model {
				v := m.(RecoveryKitView)
				v.savePath.Blur()
				return v
			},
		},
		{
			name: "restore",
			focused: func(t *testing.T) tea.Model {
				r := newFlowRepo(t)
				seedSnapshotReal(t, r)
				m, _ := NewRestoreView(Deps{Repo: r}).Update(tea.KeyMsg{Type: tea.KeyEnter})
				return m
			},
			blurAll: func(m tea.Model) tea.Model {
				v := m.(RestoreView)
				v.dest.Blur()
				v.scope.Blur()
				return v
			},
		},
		{
			name: "jobs",
			focused: func(t *testing.T) tea.Model {
				deps, _ := jobsDeps(t)
				m, _ := newJobsForTest(t, deps).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
				return m
			},
			blurAll: func(m tea.Model) tea.Model {
				v := m.(JobsView)
				v.form.name.Blur()
				v.form.path.Blur()
				v.form.tags.Blur()
				v.form.schedule.Blur()
				return v
			},
		},
	}
}

// TestFieldBox_FollowsFocusedNotStage pins the one rule behind every box in
// the TUI: the frame is drawn from the field's Focused(), never from the
// stage flag that happens to show the field. With the field focused the
// view renders exactly one frame; force-blur every field without touching
// the stage and the frame must be gone.
//
// Boxing on the stage instead lets the two drift: a stage that forgot to
// blur on exit comes back framed around a field nobody focused, and a stage
// that blurred correctly still frames a dead field. Deriving the box from
// Focused() makes it impossible to draw a frame the keyboard doesn't back.
func TestFieldBox_FollowsFocusedNotStage(t *testing.T) {
	for _, fo := range fieldOwners() {
		t.Run(fo.name, func(t *testing.T) {
			focused := fo.focused(t)
			if got := boxCount(focused.View()); got != 1 {
				t.Fatalf("focused field: boxCount = %d, want exactly 1:\n%s", got, focused.View())
			}
			blurred := fo.blurAll(focused)
			if got := boxCount(blurred.View()); got != 0 {
				t.Fatalf("every field blurred, stage unchanged: boxCount = %d, want 0 — the box is being drawn from the stage, not from Focused():\n%s", got, blurred.View())
			}
		})
	}
}
