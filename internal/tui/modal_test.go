package tui

import (
	"errors"
	"strings"
	"testing"

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
