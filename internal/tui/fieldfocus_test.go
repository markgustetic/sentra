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
// yields a cursor blink signal. It recognizes three forms: the real
// cursor.BlinkMsg (what a genuine Focus()/BlinkCmd() round-trip produces —
// the canonical production form), the unexported bootstrap sentinel from
// textinput.Blink by value equality (kept for any legacy/batched
// textinput.Blink a test constructs directly — see
// TestAssertBlinkCmd_RecognizesAllBlinkForms), and batches containing
// either.
func yieldsBlink(msg tea.Msg) bool {
	// textinput.Blink yields cursor's unexported bootstrap sentinel; the
	// real cursor.BlinkMsg is what Focus()/BlinkCmd() actually produces.
	// Recognize the bootstrap by value equality so a test CAN still assert
	// on a bare textinput.Blink if one shows up, even though production
	// code no longer emits it.
	if msg == cursor.Blink() {
		return true
	}

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

// TestAssertBlinkCmd_RecognizesAllBlinkForms verifies that assertBlinkCmd
// recognizes every message shape it might see: a bare textinput.Blink
// command and a batched textinput.Blink (the legacy bootstrap-sentinel
// forms, kept recognizable even though production code no longer emits
// them — see yieldsBlink's doc comment), and a raw cursor.BlinkMsg (what a
// real Focus()/BlinkCmd() round-trip actually produces).
func TestAssertBlinkCmd_RecognizesAllBlinkForms(t *testing.T) {
	assertBlinkCmd(t, textinput.Blink)
	assertBlinkCmd(t, tea.Batch(func() tea.Msg { return nil }, textinput.Blink))
	assertBlinkCmd(t, func() tea.Msg { return cursor.BlinkMsg{} })
}
