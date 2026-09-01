package tui

import (
	"strings"
	"testing"
	"time"

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

// TestPalette_CursorStaysVisibleAndActivates guards the scroll window:
// with more matches than paletteMaxResults, walking the cursor below the
// initial window must scroll so the highlighted row stays on screen, and
// Enter must activate exactly that (visible) command — never one the
// window scrolled out of view. Before the fix the cursor could walk past
// the rendered window and Enter fired an invisible command.
func TestPalette_CursorStaysVisibleAndActivates(t *testing.T) {
	r := NewRegistry()
	titles := []string{
		"Alpha", "Bravo", "Charlie", "Delta", "Echo",
		"Foxtrot", "Golf", "Hotel", "India", "Juliet",
	}
	for _, title := range titles {
		r.Add(Command{ID: title, Title: title, Category: "Test"})
	}
	if len(titles) <= paletteMaxResults {
		t.Fatalf("test needs > %d commands to exceed the window", paletteMaxResults)
	}

	p := NewPalette(r, 60, 40)
	// Empty query → all 10 match. Press down 9 times to land on the last
	// command ("Juliet"), which starts outside the initial 8-row window.
	for i := 0; i < 9; i++ {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	}

	wantID := titles[9] // "Juliet"
	// The highlighted command must be rendered — i.e. inside the window.
	if out := p.View(); !strings.Contains(out, wantID) {
		t.Errorf("cursor command %q not visible after scrolling:\n%s", wantID, out)
	}
	// Enter must activate that exact command, not a scrolled-out one.
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter must produce an activate command")
	}
	act, ok := cmd().(activateMsg)
	if !ok || act.id != wantID {
		t.Fatalf("got %v, want activateMsg{%s}", cmd(), wantID)
	}
}

// No box test: the palette's input is chrome inside the shared ModalBox
// frame, not the sole affordance for a field the operator picked out of
// several — the FieldBox distinction doesn't apply here.

// TestPalette_InitSchedulesBlink: the search field is constructed already
// focused (NewPalette) and never blurred, so there is no later Focus()
// transition to hang the blink cmd on — Init is where it starts, mirroring
// UnlockView.Init for the same "focused from birth" shape. Init's cmd is
// the REAL one Focus() produced at construction (see Palette.initBlink);
// executing it for real would block for the field's Cursor.BlinkSpeed
// (~530ms), and there is no field handle to preset that BEFORE
// construction runs Focus() internally, so this only checks the cmd
// exists. TestBlinkChain_ClosesEndToEnd (snapshots_test.go) proves the
// real round-trip once, on a key-triggered site where BlinkSpeed can be
// dropped first.
func TestPalette_InitSchedulesBlink(t *testing.T) {
	p := NewPalette(testRegistry(), 60, 20)
	if p.Init() == nil {
		t.Fatal("expected a blink command, got nil")
	}
}

// TestPalette_RoutesBlinkTicks: blink ticks must reach the search field so
// the cursor keeps blinking for as long as the palette stays open. A bare
// cursor.BlinkMsg{} won't do: bubbles/cursor tags each scheduled tick and
// rejects one whose tag doesn't match its current count (stale-tick guard),
// and Focus() at construction already advanced that counter past zero — so
// the test captures a genuinely tag-matched tick from the field's own
// cursor instead of a zero-value literal. BlinkSpeed is dropped to make
// capturing one instant rather than a real ~530ms wait.
func TestPalette_RoutesBlinkTicks(t *testing.T) {
	p := NewPalette(testRegistry(), 60, 20)
	p.input.Cursor.BlinkSpeed = time.Millisecond
	tick := p.input.Cursor.BlinkCmd()
	_, cmd := p.Update(tick())
	if cmd == nil {
		t.Fatal("blink tick was not routed to the palette's search field")
	}
}
