package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
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
