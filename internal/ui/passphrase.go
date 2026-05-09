package ui

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/x/term"
	"github.com/markgustetic/sentra/internal/crypto"
)

// ErrNotATTY is returned by the passphrase prompts when stdin is not
// a terminal. Callers should resolve the passphrase via the flag,
// SENTRA_PASSPHRASE env var, or OS keyring before falling back to a
// prompt.
var ErrNotATTY = errors.New("ui: passphrase prompt requires a TTY; pass the passphrase via --passphrase-file, SENTRA_PASSPHRASE, or the OS keyring")

// ErrPassphraseTooShort is returned when the user enters a passphrase
// shorter than the configured minimum.
var ErrPassphraseTooShort = errors.New("ui: passphrase is shorter than the configured minimum")

// ErrPassphraseMismatch is returned by PromptPassphraseWithConfirm
// when the two entries differ.
var ErrPassphraseMismatch = errors.New("ui: passphrase entries did not match")

// PromptPassphrase reads a passphrase from the terminal with input
// echoed as asterisks. Returns ErrNotATTY when stdin is not a
// terminal so scripted callers fail fast instead of hanging on a
// stalled prompt.
//
// minLen is the minimum acceptable passphrase length. Pass 0 to
// disable the check.
func PromptPassphrase(prompt string, minLen int) ([]byte, error) {
	if !stdinIsTTY() {
		return nil, ErrNotATTY
	}
	pw, err := runPasswordPromptFn(prompt)
	if err != nil {
		return nil, err
	}
	if minLen > 0 && len(pw) < minLen {
		crypto.Zeroize(pw)
		return nil, fmt.Errorf("%w: minimum %d bytes", ErrPassphraseTooShort, minLen)
	}
	return pw, nil
}

// PromptPassphraseWithConfirm prompts twice and verifies the two
// entries match before returning. Used during sentra init to catch
// typos before they bake into a wrapped repo key. Comparison is
// constant-time — overkill for typed input, but cheap and tidy.
func PromptPassphraseWithConfirm(prompt string, minLen int) ([]byte, error) {
	first, err := PromptPassphrase(prompt, minLen)
	if err != nil {
		return nil, err
	}
	second, err := PromptPassphrase("Confirm: ", 0)
	if err != nil {
		crypto.Zeroize(first)
		return nil, err
	}
	defer crypto.Zeroize(second)
	if subtle.ConstantTimeCompare(first, second) != 1 {
		crypto.Zeroize(first)
		return nil, ErrPassphraseMismatch
	}
	return first, nil
}

// runPasswordPromptFn is the bridge to huh. It's a var so tests can
// swap in a deterministic stub without coupling to huh's API or
// actually running an interactive form.
var runPasswordPromptFn = func(prompt string) ([]byte, error) {
	var entered string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title(prompt).
				EchoMode(huh.EchoModePassword).
				Value(&entered),
		),
	)
	if err := form.Run(); err != nil {
		return nil, fmt.Errorf("ui: passphrase prompt failed: %w", err)
	}
	// Take ownership of the entered string's bytes. Strings are
	// immutable in Go so we can't zero the form's copy, but at least
	// the byte-slice we hand back is the only mutable reference.
	return []byte(entered), nil
}

// stdinIsTTY reports whether os.Stdin is a terminal device. Pulled
// out so tests can override via testIsTTY.
var stdinIsTTY = func() bool {
	return term.IsTerminal(os.Stdin.Fd())
}
