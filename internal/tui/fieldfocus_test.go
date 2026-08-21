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
// yields a cursor blink signal. It recognizes three forms: the unexported
// bootstrap sentinel from textinput.Blink by value equality (the idiomatic
// Init() form), the real cursor.BlinkMsg (the live Update form), and batches
// containing either.
func yieldsBlink(msg tea.Msg) bool {
	// textinput.Blink yields cursor's unexported bootstrap sentinel; the
	// real cursor.BlinkMsg only appears after a live Update() round-trip.
	// Recognize the bootstrap by value equality so tests can assert on a
	// bare textinput.Blink (the idiomatic Init() form).
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
// correctly recognizes all three forms later tasks will produce: a bare
// textinput.Blink command, a batched textinput.Blink, and a cursor.BlinkMsg.
func TestAssertBlinkCmd_RecognizesAllBlinkForms(t *testing.T) {
	assertBlinkCmd(t, textinput.Blink)
	assertBlinkCmd(t, tea.Batch(func() tea.Msg { return nil }, textinput.Blink))
	assertBlinkCmd(t, func() tea.Msg { return cursor.BlinkMsg{} })
}
