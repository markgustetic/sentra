package tui

import (
	"strings"
	"testing"
	"time"

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
// with an accessor over every text input the view has.
//
// `fields` is what lets a test separate the two things a box or a blink
// could be driven from — the stage flag and the field's own Focused() —
// because it can force-blur, inspect, or speed up every field while leaving
// the stage exactly where it is. Tests below iterate this table so each rule
// is asserted for the CLASS of views, not re-derived per view (see "test the
// rule, not the case" in the repo's memory notes).
type fieldOwner struct {
	name string
	// focused builds the view driven to the stage that owns its field, with
	// that field focused.
	focused func(t *testing.T) tea.Model
	// fields applies do to every text input of a copy of m and returns the
	// copy, so a test can mutate (Blur, BlinkSpeed) or inspect (Focused)
	// the fields of a value-typed model in one place.
	fields func(m tea.Model, do func(f *textinput.Model)) tea.Model
}

func fieldOwners() []fieldOwner {
	return []fieldOwner{
		{
			name:    "unlock",
			focused: func(t *testing.T) tea.Model { return shown(t, NewUnlockView(unlockDeps(t, "hunter2"))) },
			fields: func(m tea.Model, do func(*textinput.Model)) tea.Model {
				v := m.(UnlockView)
				do(&v.input)
				return v
			},
		},
		{
			name:    "password",
			focused: func(t *testing.T) tea.Model { return shown(t, NewPasswordView(Deps{Repo: newFlowRepo(t)})) },
			fields: func(m tea.Model, do func(*textinput.Model)) tea.Model {
				v := m.(PasswordView)
				do(&v.newPass)
				do(&v.confirmPass)
				return v
			},
		},
		{
			name:    "sync",
			focused: func(t *testing.T) tea.Model { return shown(t, NewSyncView(Deps{Repo: newFlowRepo(t)})) },
			fields: func(m tea.Model, do func(*textinput.Model)) tea.Model {
				v := m.(SyncView)
				do(&v.dstPath)
				do(&v.snapRefs)
				return v
			},
		},
		{
			name: "backup",
			focused: func(t *testing.T) tea.Model {
				m, _ := backupAt(t, tempTree(t)).Update(tea.KeyMsg{Type: tea.KeyTab})
				return m
			},
			fields: func(m tea.Model, do func(*textinput.Model)) tea.Model {
				v := m.(BackupView)
				do(&v.tag)
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
			fields: func(m tea.Model, do func(*textinput.Model)) tea.Model {
				s := m.(Snapshots)
				do(&s.filter)
				return s
			},
		},
		{
			name: "recovery-kit",
			focused: func(t *testing.T) tea.Model {
				m, _ := recoveryKitAtDone(t).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
				return m
			},
			fields: func(m tea.Model, do func(*textinput.Model)) tea.Model {
				v := m.(RecoveryKitView)
				do(&v.savePath)
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
			fields: func(m tea.Model, do func(*textinput.Model)) tea.Model {
				v := m.(RestoreView)
				do(&v.dest)
				do(&v.scope)
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
			fields: func(m tea.Model, do func(*textinput.Model)) tea.Model {
				v := m.(JobsView)
				do(&v.form.name)
				do(&v.form.path)
				do(&v.form.tags)
				do(&v.form.schedule)
				return v
			},
		},
	}
}

// blurAll force-blurs every field of m without touching its stage.
func (fo fieldOwner) blurAll(m tea.Model) tea.Model {
	return fo.fields(m, func(f *textinput.Model) { f.Blur() })
}

// quick drops every field's BlinkSpeed so a Focus() cmd minted afterwards
// yields its tick at once instead of after the default ~530ms.
func (fo fieldOwner) quick(m tea.Model) tea.Model {
	return fo.fields(m, func(f *textinput.Model) { f.Cursor.BlinkSpeed = time.Millisecond })
}

// focusedCount reports how many of m's fields are focused.
func (fo fieldOwner) focusedCount(m tea.Model) int {
	n := 0
	fo.fields(m, func(f *textinput.Model) {
		if f.Focused() {
			n++
		}
	})
	return n
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

// TestFieldFocus_HiddenBlursAndShownRefocuses pins the shell-facing half of
// the focus contract for the class of field-owning views: viewHiddenMsg
// blurs every field the view has, so a view that is off screen never owns
// a focused field (its next blink tick finds nothing to reschedule), and
// viewShownMsg re-focuses whatever the current stage owns, returning
// Focus()'s REAL blink cmd so the chain restarts. The stage itself is not
// touched by either — hide and show are about the screen, not the flow.
func TestFieldFocus_HiddenBlursAndShownRefocuses(t *testing.T) {
	for _, fo := range fieldOwners() {
		t.Run(fo.name, func(t *testing.T) {
			v := fo.focused(t)
			if n := fo.focusedCount(v); n != 1 {
				t.Fatalf("precondition: focused view owns %d focused fields, want 1", n)
			}

			h, hideCmd := v.Update(viewHiddenMsg{})
			if hideCmd != nil {
				t.Error("hiding a view must not schedule anything")
			}
			if n := fo.focusedCount(h); n != 0 {
				t.Fatalf("hidden view still owns %d focused field(s) — viewHiddenMsg must blur them all", n)
			}
			if got := boxCount(h.View()); got != 0 {
				t.Fatalf("hidden view renders %d box(es), want 0:\n%s", got, h.View())
			}

			h = fo.quick(h)
			s, showCmd := h.Update(viewShownMsg{})
			if n := fo.focusedCount(s); n != 1 {
				t.Fatalf("shown view owns %d focused fields, want exactly 1 — viewShownMsg must re-focus the stage's field", n)
			}
			assertBlinkCmd(t, showCmd)
		})
	}
}

// TestFieldFocus_ShownFocusesNothingOutsideAFieldStage is the other half of
// the show rule: on a stage that owns no text field, viewShownMsg must
// focus nothing and schedule nothing. Otherwise every rail scroll past a
// view would light up a field the operator cannot type into.
func TestFieldFocus_ShownFocusesNothingOutsideAFieldStage(t *testing.T) {
	cases := []struct {
		name string
		view func(t *testing.T) tea.Model
	}{
		{"snapshots, not filtering", func(t *testing.T) tea.Model {
			return NewSnapshots(Deps{}).SetSnapshots(sampleSnaps())
		}},
		{"backup, keyboard on the picker", func(t *testing.T) tea.Model {
			return backupAt(t, tempTree(t))
		}},
		{"restore, on the picker", func(t *testing.T) tea.Model {
			r := newFlowRepo(t)
			seedSnapshotReal(t, r)
			return NewRestoreView(Deps{Repo: r})
		}},
		{"recovery kit, before save", func(t *testing.T) tea.Model {
			return recoveryKitAtDone(t)
		}},
		{"password, rotation done", func(t *testing.T) tea.Model {
			m, _ := passwordAtRunning(t).Update(passwordDoneMsg{})
			return m
		}},
		{"sync, run done", func(t *testing.T) tea.Model {
			m, _ := syncRunningFromPathField(t).Update(syncDoneMsg{})
			return m
		}},
		{"unlock, opening", func(t *testing.T) tea.Model {
			v := typeIntoUnlock(shown(t, NewUnlockView(unlockDeps(t, "hunter2"))), "hunter2")
			m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
			return m
		}},
		{"jobs, list", func(t *testing.T) tea.Model {
			deps, _ := jobsDeps(t)
			return newJobsForTest(t, deps)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, cmd := tc.view(t).Update(viewShownMsg{})
			if cmd != nil {
				t.Error("viewShownMsg on a fieldless stage must schedule nothing")
			}
			if got := boxCount(s.View()); got != 0 {
				t.Fatalf("viewShownMsg on a fieldless stage focused a field (%d box(es)):\n%s", got, s.View())
			}
		})
	}
}

// shown delivers viewShownMsg to v the way the shell does the moment it puts
// a view on screen, and returns the model. Tests that drive a view directly
// (no App) use it in place of the shell, because construction alone focuses
// nothing — see TestFieldFocus_ConstructionFocusesNothing.
func shown[V tea.Model](t *testing.T, v V) V {
	t.Helper()
	m, _ := v.Update(viewShownMsg{})
	out, ok := m.(V)
	if !ok {
		t.Fatalf("viewShownMsg changed the model type from %T to %T", v, m)
	}
	return out
}

// TestFieldFocus_ConstructionFocusesNothing pins where focus comes FROM. A
// constructor builds a view that is not yet on screen, so it must focus no
// field and its Init must schedule no blink: the shell's viewShownMsg is the
// only source of focus. Otherwise a view the operator never opens sits with
// a focused field (Focused() lying off screen) and, when App.Init batched
// every view's Init, ran a perpetual blink chain for it from launch. Only
// the three views that used to focus at construction are listed — the rest
// never did.
func TestFieldFocus_ConstructionFocusesNothing(t *testing.T) {
	built := map[string]func(t *testing.T) tea.Model{
		"unlock":   func(t *testing.T) tea.Model { return NewUnlockView(unlockDeps(t, "hunter2")) },
		"password": func(t *testing.T) tea.Model { return NewPasswordView(Deps{Repo: newFlowRepo(t)}) },
		"sync":     func(t *testing.T) tea.Model { return NewSyncView(Deps{Repo: newFlowRepo(t)}) },
	}
	for _, fo := range fieldOwners() {
		build, ok := built[fo.name]
		if !ok {
			continue
		}
		t.Run(fo.name, func(t *testing.T) {
			v := build(t)
			if n := fo.focusedCount(v); n != 0 {
				t.Errorf("freshly constructed view owns %d focused field(s), want 0 — only viewShownMsg focuses", n)
			}
			if v.Init() != nil {
				t.Error("Init must not schedule a blink — the chain starts on viewShownMsg, so an unopened view never runs one")
			}
		})
	}
}
