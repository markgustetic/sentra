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
// transition to hang the blink cmd on — Init, called on every open, is
// where it starts. (Views differ: they focus on viewShownMsg and their
// Init schedules nothing, because App.Init batches every view's Init.)
//
// Init calls Focus() at CALL time (not at construction), so BlinkSpeed can
// be dropped first and the cmd genuinely executed — this asserts the cmd
// yields a blink, not merely that it is non-nil.
func TestPalette_InitSchedulesBlink(t *testing.T) {
	p := NewPalette(testRegistry(), 60, 20)
	p.input.Cursor.BlinkSpeed = time.Millisecond
	assertBlinkCmd(t, p.Init())
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

// TestApp_PaletteReopenReArmsBlink is the regression guard for a
// SINGLE-USE cached cmd. Palette is constructed once in NewApp and
// reopened on every ctrl+p, so Init must produce a cmd that works EVERY
// time — not replay one captured at construction.
//
// cursor.BlinkCmd (bubbles v1.0.0, cursor.go:176) bakes
// BlinkMsg{id, tag} into its closure at call time and guards on a
// one-shot context deadline. Replaying that closure yields the SAME
// message, but the live field's blinkTag has advanced past that tag by
// then, so cursor.Update's stale-tick guard (cursor.go:122) drops it and
// returns nil: the chain never restarts, and the cursor sits solid from
// open until the first keystroke. A nil check on Init's cmd cannot see
// this — the cached cmd is non-nil and yields a real cursor.BlinkMsg; it
// is simply addressed to a tag nobody is waiting for any more.
//
// So this drives the REAL App through a full open → tick → close →
// reopen cycle and asserts the second open's tick is still ACCEPTED.
func TestApp_PaletteReopenReArmsBlink(t *testing.T) {
	app := newTestApp(t)
	app.palette.input.Cursor.BlinkSpeed = time.Millisecond

	// First open: consume one tick so the field's blinkTag advances past
	// whatever a construction-time cmd would have captured.
	m, first := app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	app = m.(App)
	if first == nil {
		t.Fatal("first open must schedule a blink")
	}
	m, cont := app.Update(first())
	app = m.(App)
	if cont == nil {
		t.Fatal("the first open's tick must be accepted and reschedule")
	}

	// Close, then reopen — the same construction-time Palette value.
	app.paletteOpen = false
	m, second := app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	app = m.(App)
	if second == nil {
		t.Fatal("reopen must schedule a blink")
	}
	if _, cmd := app.Update(second()); cmd == nil {
		t.Fatal("the palette's blink chain did not re-arm on reopen — Init replayed a stale, single-use cmd")
	}
}
