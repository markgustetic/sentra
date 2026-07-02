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
