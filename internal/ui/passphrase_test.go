package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// stubTTY swaps the package-level stdinIsTTY for the duration of t.
// Tests run with stdin not a TTY by default; we make the override
// explicit so each test states what world it's in.
func stubTTY(t *testing.T, isTTY bool) {
	t.Helper()
	prev := stdinIsTTY
	stdinIsTTY = func() bool { return isTTY }
	t.Cleanup(func() { stdinIsTTY = prev })
}

// stubPrompt swaps runPasswordPrompt for tests so we can drive the
// prompt deterministically without an actual TTY interaction. The
// stub still goes through the IsTTY gate, so the no-TTY tests
// exercise the real fallback path.
func stubPrompt(t *testing.T, fn func(prompt string) ([]byte, error)) {
	t.Helper()
	prev := runPasswordPromptFn
	runPasswordPromptFn = fn
	t.Cleanup(func() { runPasswordPromptFn = prev })
}

func TestPromptPassphrase_NoTTY(t *testing.T) {
	stubTTY(t, false)
	got, err := PromptPassphrase("Passphrase: ", 0)
	if err == nil {
		t.Fatalf("expected ErrNotATTY, got nil error and bytes=%q", got)
	}
	if !errors.Is(err, ErrNotATTY) {
		t.Errorf("expected ErrNotATTY, got %v", err)
	}
	// Error message should clearly mention TTY/terminal/scripted use
	// so the user has a fighting chance of knowing what to do.
	msg := err.Error()
	if !strings.Contains(msg, "TTY") && !strings.Contains(msg, "terminal") {
		t.Errorf("error should mention TTY or terminal, got %q", msg)
	}
}

func TestPromptPassphraseWithConfirm_NoTTY(t *testing.T) {
	stubTTY(t, false)
	if _, err := PromptPassphraseWithConfirm("Passphrase: ", 0); !errors.Is(err, ErrNotATTY) {
		t.Errorf("expected ErrNotATTY, got %v", err)
	}
}

func TestPromptPassphrase_RespectsMinLen(t *testing.T) {
	stubTTY(t, true)
	stubPrompt(t, func(string) ([]byte, error) { return []byte("short"), nil })

	if _, err := PromptPassphrase("p: ", 10); !errors.Is(err, ErrPassphraseTooShort) {
		t.Errorf("expected ErrPassphraseTooShort, got %v", err)
	}
}

func TestPromptPassphrase_AcceptsLongEnough(t *testing.T) {
	stubTTY(t, true)
	stubPrompt(t, func(string) ([]byte, error) { return []byte("longenough"), nil })

	got, err := PromptPassphrase("p: ", 8)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("longenough")) {
		t.Errorf("got %q, want longenough", got)
	}
}

func TestPromptPassphraseWithConfirm_Match(t *testing.T) {
	stubTTY(t, true)
	calls := 0
	stubPrompt(t, func(string) ([]byte, error) {
		calls++
		return []byte("hunter2hunter2"), nil
	})

	got, err := PromptPassphraseWithConfirm("p: ", 0)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected 2 prompt calls, got %d", calls)
	}
	if !bytes.Equal(got, []byte("hunter2hunter2")) {
		t.Errorf("got %q", got)
	}
}

func TestPromptPassphraseWithConfirm_Mismatch(t *testing.T) {
	stubTTY(t, true)
	answers := [][]byte{[]byte("hunter2"), []byte("Hunter2")}
	stubPrompt(t, func(string) ([]byte, error) {
		a := answers[0]
		answers = answers[1:]
		return a, nil
	})

	if _, err := PromptPassphraseWithConfirm("p: ", 0); !errors.Is(err, ErrPassphraseMismatch) {
		t.Errorf("expected ErrPassphraseMismatch, got %v", err)
	}
}

func TestZeroize(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5}
	zeroize(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("byte %d: got %d, want 0", i, v)
		}
	}
}
