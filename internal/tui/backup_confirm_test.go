package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestConfirmControls_TabCyclesTagAndRescan(t *testing.T) {
	c := newConfirmControls()
	if c.tag.Focused() {
		t.Fatal("constructor must focus nothing")
	}
	cmd := c.refocus()
	if !c.tag.Focused() || cmd == nil || !c.capturesText() {
		t.Fatal("refocus on the tag control must focus the field and return its blink cmd")
	}
	c, _ = c.update(tea.KeyMsg{Type: tea.KeyTab})
	if c.focus != confirmRescan || c.tag.Focused() || c.capturesText() {
		t.Fatalf("tab must move to the rescan row and blur the tag: focus=%v", c.focus)
	}
	c, _ = c.update(tea.KeyMsg{Type: tea.KeyTab})
	if c.focus != confirmTag || !c.tag.Focused() {
		t.Fatal("tab must wrap back to the tag field, focused")
	}
}

func TestConfirmControls_SpaceTogglesRescanOnlyOnItsRow(t *testing.T) {
	c := newConfirmControls()
	c.refocus()
	// Space keypresses must include Runes; bubbletea's key parser emits
	// KeyMsg{Type: KeySpace, Runes: []rune{' '}} from the terminal.
	c, _ = c.update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if c.rescan {
		t.Fatal("space in the tag field is a character, not a toggle")
	}
	if c.tag.Value() != " " {
		t.Fatalf("space must type into the tag: %q", c.tag.Value())
	}
	c, _ = c.update(tea.KeyMsg{Type: tea.KeyTab})
	c, _ = c.update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if !c.rescan {
		t.Fatal("space on the rescan row must arm it")
	}
	c, _ = c.update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if c.rescan {
		t.Fatal("second space must disarm it")
	}
}

func TestConfirmControls_ViewBoxesOnlyFocusedTagAndMarksRescan(t *testing.T) {
	c := newConfirmControls()
	if n := boxCount(c.view()); n != 0 {
		t.Fatalf("nothing focused: boxCount = %d", n)
	}
	if !strings.Contains(c.view(), "[ ] force a full rescan") {
		t.Fatalf("rescan row must render unchecked:\n%s", c.view())
	}
	c.refocus()
	if n := boxCount(c.view()); n != 1 {
		t.Fatalf("tag focused: boxCount = %d, want 1", n)
	}
	c, _ = c.update(tea.KeyMsg{Type: tea.KeyTab})
	c, _ = c.update(tea.KeyMsg{Type: tea.KeySpace})
	if !strings.Contains(c.view(), "[x] force a full rescan") || !strings.Contains(c.view(), "▍") {
		t.Fatalf("armed rescan row must render checked and glyph-selected:\n%s", c.view())
	}
}
