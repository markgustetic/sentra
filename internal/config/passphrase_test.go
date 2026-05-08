package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writePassphraseFile(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "pass")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write passphrase file: %v", err)
	}
	return path
}

// TestResolve_FilePriority verifies the file beats env, keyring, and
// prompt. The env var is set, but the resolver must read the file
// because PassphraseFile takes precedence.
func TestResolve_FilePriority(t *testing.T) {
	t.Setenv("SENTRA_PASSPHRASE", "from-env")
	path := writePassphraseFile(t, t.TempDir(), "from-file")
	got, err := Resolve(ResolveOptions{
		PassphraseFile: path,
		Prompt: func() ([]byte, error) {
			t.Fatal("prompt should not be called when file is set")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(got) != "from-file" {
		t.Errorf("got %q, want from-file", got)
	}
}

// TestResolve_FileTrimsTrailingNewline catches a common foot-gun: an
// editor adds a trailing newline to a "secrets file" and the resulting
// passphrase is silently different from what the user typed. The
// resolver strips one trailing newline (and one leading BOM) so the
// stored passphrase matches what the user pastes into other tools.
func TestResolve_FileTrimsTrailingNewline(t *testing.T) {
	path := writePassphraseFile(t, t.TempDir(), "from-file\n")
	got, err := Resolve(ResolveOptions{PassphraseFile: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(got) != "from-file" {
		t.Errorf("got %q, want from-file (trailing newline should be stripped)", got)
	}
}

// TestResolve_EnvPriority verifies SENTRA_PASSPHRASE wins when no
// file is set, beating keyring and prompt.
func TestResolve_EnvPriority(t *testing.T) {
	t.Setenv("SENTRA_PASSPHRASE", "from-env")
	got, err := Resolve(ResolveOptions{
		Prompt: func() ([]byte, error) {
			t.Fatal("prompt should not be called when env is set")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(got) != "from-env" {
		t.Errorf("got %q, want from-env", got)
	}
}

// TestResolve_PromptFallback is the script-friendly path: no file, no
// env, no keyring → fall through to the prompt callback.
func TestResolve_PromptFallback(t *testing.T) {
	// Make sure nothing else is set.
	t.Setenv("SENTRA_PASSPHRASE", "")
	called := false
	got, err := Resolve(ResolveOptions{
		Prompt: func() ([]byte, error) {
			called = true
			return []byte("from-prompt"), nil
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !called {
		t.Fatal("prompt was not invoked")
	}
	if string(got) != "from-prompt" {
		t.Errorf("got %q, want from-prompt", got)
	}
}

// TestResolve_PromptError surfaces the prompt callback's error
// verbatim so the caller can distinguish "user cancelled" from
// "passphrase too short" etc.
func TestResolve_PromptError(t *testing.T) {
	t.Setenv("SENTRA_PASSPHRASE", "")
	want := errors.New("user cancelled")
	_, err := Resolve(ResolveOptions{
		Prompt: func() ([]byte, error) { return nil, want },
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}

// TestResolve_NoSourceConfigured returns ErrNoPassphraseSource when no
// resolution mechanism is available. We achieve that by:
//   - PassphraseFile empty
//   - SENTRA_PASSPHRASE empty
//   - UseKeyring false
//   - Prompt nil
func TestResolve_NoSourceConfigured(t *testing.T) {
	t.Setenv("SENTRA_PASSPHRASE", "")
	_, err := Resolve(ResolveOptions{})
	if !errors.Is(err, ErrNoPassphraseSource) {
		t.Fatalf("expected ErrNoPassphraseSource, got %v", err)
	}
}

// TestResolve_KeyringDeferred captures the implementation choice
// documented in the plan: if the OS keyring lookup hasn't been wired
// up yet, the keyring branch returns a clear error mentioning that
// it isn't implemented. If the OS keyring branch IS implemented, the
// test instead asserts that the documented sentinel still surfaces
// (e.g. for a missing-key error) — but the most common case in CI
// is a simple "not yet implemented".
//
// The test relies on swapping the keyring lookup function via a
// package-level hook so we don't depend on a real keyring being
// available in the test environment.
func TestResolve_KeyringHookCalled(t *testing.T) {
	t.Setenv("SENTRA_PASSPHRASE", "")
	called := false
	prev := keyringLookupFn
	t.Cleanup(func() { keyringLookupFn = prev })
	keyringLookupFn = func(service, user string) ([]byte, error) {
		called = true
		if service != "sentra" {
			t.Errorf("service: got %q, want sentra", service)
		}
		if user != "default" {
			t.Errorf("user: got %q, want default", user)
		}
		return []byte("from-keyring"), nil
	}
	got, err := Resolve(ResolveOptions{
		UseKeyring:     true,
		KeyringService: "sentra",
		KeyringUser:    "default",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !called {
		t.Fatal("keyring lookup was not invoked")
	}
	if string(got) != "from-keyring" {
		t.Errorf("got %q, want from-keyring", got)
	}
}

// TestResolve_FileRejectsGroupReadable refuses to read a passphrase
// file whose mode permits group or world access. The most common cause
// is a passphrase file accidentally committed into a Docker build
// context or copied with `cp` (which preserves source permissions, so
// a 644 source becomes a 644 destination). Failing closed gives the
// operator a clear signal rather than silently uploading ciphertext
// encrypted with a passphrase that anyone on the system can read.
func TestResolve_FileRejectsGroupReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-style permission bits don't map cleanly on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "pass")
	if err := os.WriteFile(path, []byte("from-file"), 0o644); err != nil {
		t.Fatalf("write passphrase file: %v", err)
	}
	_, err := Resolve(ResolveOptions{PassphraseFile: path})
	if err == nil {
		t.Fatal("expected error for group-readable passphrase file, got nil")
	}
	if !strings.Contains(err.Error(), "permissions") {
		t.Errorf("error %q should mention permissions", err.Error())
	}
}

// TestResolve_FileRejectsWorldReadable is the same check for the
// world-readable bit. Mode 0604 is unusual but still leaks the
// passphrase to anyone on the system.
func TestResolve_FileRejectsWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-style permission bits don't map cleanly on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "pass")
	if err := os.WriteFile(path, []byte("from-file"), 0o604); err != nil {
		t.Fatalf("write passphrase file: %v", err)
	}
	_, err := Resolve(ResolveOptions{PassphraseFile: path})
	if err == nil {
		t.Fatal("expected error for world-readable passphrase file, got nil")
	}
}

// TestResolve_FileAcceptsOwnerOnly is the happy path. 0600 is the
// canonical "owner read/write, no one else" mode.
func TestResolve_FileAcceptsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix-style permission bits don't map cleanly on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "pass")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatalf("write passphrase file: %v", err)
	}
	got, err := Resolve(ResolveOptions{PassphraseFile: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(got) != "from-file" {
		t.Errorf("got %q, want from-file", got)
	}
}

// TestResolve_KeyringFallsThroughOnMiss confirms that a clean
// "not-found" from the keyring proceeds to the prompt fallback rather
// than failing the whole resolution. The user might have configured
// a keyring but never stored the entry yet.
func TestResolve_KeyringFallsThroughOnMiss(t *testing.T) {
	t.Setenv("SENTRA_PASSPHRASE", "")
	prev := keyringLookupFn
	t.Cleanup(func() { keyringLookupFn = prev })
	keyringLookupFn = func(string, string) ([]byte, error) {
		return nil, ErrKeyringEntryNotFound
	}
	got, err := Resolve(ResolveOptions{
		UseKeyring:     true,
		KeyringService: "sentra",
		KeyringUser:    "default",
		Prompt:         func() ([]byte, error) { return []byte("fallback"), nil },
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(got) != "fallback" {
		t.Errorf("got %q, want fallback", got)
	}
}
