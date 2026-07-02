package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func typeString(p Palette, s string) Palette {
	for _, r := range s {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return p
}

func TestPalette_TypingFiltersResults(t *testing.T) {
	p := NewPalette(testRegistry(), 60, 20)
	p = typeString(p, "snap")
	out := p.View()
	if !strings.Contains(out, "Snapshots") {
		t.Errorf("filtered result missing:\n%s", out)
	}
	if strings.Contains(out, "Dashboard") {
		t.Errorf("non-match still visible:\n%s", out)
	}
}

func TestPalette_EnterActivatesTopMatch(t *testing.T) {
	p := NewPalette(testRegistry(), 60, 20)
	p = typeString(p, "diff")
	p, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter must produce a command")
	}
	act, ok := cmd().(activateMsg)
	if !ok || act.id != "diff" {
		t.Fatalf("got %v, want activateMsg{diff}", cmd())
	}
	// Enter activates without mutating the input — clearing the query
	// is the App's job (Reset on the next open), not the palette's.
	if got := p.Query(); got != "diff" {
		t.Fatalf("enter clobbered the query: got %q, want diff", got)
	}
}

func TestPalette_EnterOnNoMatchesDoesNothing(t *testing.T) {
	p := NewPalette(testRegistry(), 60, 20)
	p = typeString(p, "zzzz")
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("enter with zero matches must not activate anything")
	}
}

// TestPalette_QIsTypedNotQuit guards the focus rule: while the palette
// is open, 'q' is input, not quit — the shell must not see it.
func TestPalette_QIsTypedNotQuit(t *testing.T) {
	p := NewPalette(testRegistry(), 60, 20)
	p = typeString(p, "q")
	if got := p.Query(); got != "q" {
		t.Fatalf("query = %q, want q", got)
	}
}
