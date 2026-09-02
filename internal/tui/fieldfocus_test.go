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
