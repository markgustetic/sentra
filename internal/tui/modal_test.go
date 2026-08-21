package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestErrorModal_RendersMessageAndAdvice(t *testing.T) {
	m := NewErrorModal(errors.New("open repo: boom"), "Check the bucket configuration.", 80, 24)
	out := m.View()
	for _, want := range []string{"open repo: boom", "Check the bucket"} {
		if !strings.Contains(out, want) {
			t.Errorf("error modal missing %q:\n%s", want, out)
		}
	}
}

func TestErrorModal_AnyKeyDismisses(t *testing.T) {
	m := NewErrorModal(errors.New("x"), "", 80, 24)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected dismiss command")
	}
	if _, ok := cmd().(dismissModalMsg); !ok {
		t.Fatalf("expected dismissModalMsg, got %T", cmd())
	}
}

func TestConfirmModal_EscCancelsEnterConfirms(t *testing.T) {
	m := NewConfirmModal("Quit during operation?", "The running backup will be canceled.", "confirm-quit", 80, 24)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, ok := cmd().(dismissModalMsg); !ok {
		t.Fatalf("esc: expected dismissModalMsg, got %T", cmd())
	}

	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	res, ok := cmd().(confirmedMsg)
	if !ok || res.id != "confirm-quit" {
		t.Fatalf("enter: expected confirmedMsg{confirm-quit}, got %v", cmd())
	}
}

func TestTypedConfirmModal_RequiresExactWord(t *testing.T) {
	m := Modal(NewTypedConfirmModal("Confirm prune", "Deletes 9 snapshots.", "prune", "prune-apply", 80, 24))

	// Enter with the wrong word: no confirmation, modal stays.
	for _, r := range "prun" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("wrong word must not confirm")
	}

	// Complete the word: enter confirms with the modal's id.
	m2 := Modal(NewTypedConfirmModal("Confirm prune", "b", "prune", "prune-apply", 80, 24))
	for _, r := range "prune" {
		m2, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd = m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("exact word must confirm")
	}
	res, ok := cmd().(confirmedMsg)
	if !ok || res.id != "prune-apply" {
		t.Fatalf("got %#v, want confirmedMsg{prune-apply}", cmd())
	}
}

func TestTypedConfirmModal_EscCancelsAndQTypes(t *testing.T) {
	m := Modal(NewTypedConfirmModal("t", "b", "prune", "id", 80, 24))
	// 'q' must be typed into the input, not treated as quit/dismiss.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !strings.Contains(m.View(), "q") {
		t.Fatal("'q' should appear in the typed input")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, ok := cmd().(dismissModalMsg); !ok {
		t.Fatalf("esc must dismiss, got %#v", cmd())
	}
}

// No box test: the typed field is chrome inside the shared ModalBox frame,
// not the sole affordance distinguishing a focused field from its
// surroundings — the FieldBox distinction doesn't apply here.

// TestTypedConfirmModal_InitSchedulesBlink: the typed field is constructed
// already focused (NewTypedConfirmModal) and never blurred while the modal
// is up, so there is no later Focus() transition to hang the blink cmd on —
// Init is where it starts, mirroring UnlockView.Init for the same
// "focused from birth" shape.
func TestTypedConfirmModal_InitSchedulesBlink(t *testing.T) {
	m := NewTypedConfirmModal("Confirm prune", "b", "prune", "id", 80, 24)
	assertBlinkCmd(t, m.Init())
}

// TestTypedConfirmModal_RoutesBlinkTicks: blink ticks must reach the typed
// field so the cursor keeps blinking for as long as the modal is up. A bare
// cursor.BlinkMsg{} won't do: bubbles/cursor tags each scheduled tick and
// rejects one whose tag doesn't match its current count (stale-tick guard),
// and Focus() at construction already advanced that counter past zero — so
// the test captures a genuinely tag-matched tick from the field's own
// cursor instead of a zero-value literal. BlinkSpeed is dropped to make
// capturing one instant rather than a real ~530ms wait.
func TestTypedConfirmModal_RoutesBlinkTicks(t *testing.T) {
	tc := NewTypedConfirmModal("t", "b", "prune", "id", 80, 24)
	tc.input.Cursor.BlinkSpeed = time.Millisecond
	tick := tc.input.Cursor.BlinkCmd()
	_, cmd := Modal(tc).Update(tick())
	if cmd == nil {
		t.Fatal("blink tick was not routed to the modal's typed field")
	}
}
