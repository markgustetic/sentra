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

// TestResolveNonInteractive covers the file-then-env half of the priority list
// on its own, for callers that must know whether a secret is available BEFORE
// they decide to prompt — the TUI setup wizard, which skips its passphrase
// stage entirely when a non-interactive source answers.
//
// The reported source is a label, never the secret: an operator has to be able
// to see which source setup is about to initialize the repository under, and a
// silent mismatch between that source and what they typed is the failure this
// whole path exists to prevent.
func TestResolveNonInteractive(t *testing.T) {
	t.Run("file wins over env", func(t *testing.T) {
		t.Setenv("SENTRA_PASSPHRASE", "from-env")
		path := writePassphraseFile(t, t.TempDir(), "from-file")
		got, source, err := ResolveNonInteractive(path)
		if err != nil {
			t.Fatalf("ResolveNonInteractive: %v", err)
		}
		if string(got) != "from-file" {
			t.Errorf("got %q, want from-file", got)
		}
		if source != PassphraseSourceFile {
			t.Errorf("source = %q, want %q", source, PassphraseSourceFile)
		}
	})

	t.Run("env when no file", func(t *testing.T) {
		t.Setenv("SENTRA_PASSPHRASE", "from-env")
		got, source, err := ResolveNonInteractive("")
		if err != nil {
			t.Fatalf("ResolveNonInteractive: %v", err)
		}
		if string(got) != "from-env" {
			t.Errorf("got %q, want from-env", got)
		}
		if source != PassphraseSourceEnv {
			t.Errorf("source = %q, want %q", source, PassphraseSourceEnv)
		}
	})

	// No source is not an error: the caller falls back to prompting. Returning
	// an error here would force every caller to branch on errors.Is just to
	// reach its normal interactive path.
	t.Run("no source is a clean miss", func(t *testing.T) {
		t.Setenv("SENTRA_PASSPHRASE", "")
		got, source, err := ResolveNonInteractive("")
		if err != nil {
			t.Fatalf("ResolveNonInteractive: %v", err)
		}
		if got != nil || source != "" {
			t.Errorf("got (%q, %q), want a clean miss", got, source)
		}
	})

	// A named-but-unusable file must fail loudly. Falling back to a prompt
	// would let an operator who pointed setup at a file initialize the repo
	// under a passphrase that file does not contain.
	t.Run("unreadable file is an error", func(t *testing.T) {
		got, source, err := ResolveNonInteractive(filepath.Join(t.TempDir(), "missing"))
		if err == nil {
			t.Fatalf("expected an error for a missing file, got (%q, %q)", got, source)
		}
		if got != nil {
			t.Errorf("no secret may be returned alongside an error, got %q", got)
		}
	})
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
	if err := os.WriteFile(path, []byte("from-file"), 0o644); err != nil { //nolint:gosec // intentional: testing rejection of group-readable file
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
	if err := os.WriteFile(path, []byte("from-file"), 0o604); err != nil { //nolint:gosec // intentional: testing rejection of world-readable file
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

func TestResolve_KeyringFallbackUserAfterPrimaryMiss(t *testing.T) {
	t.Setenv("SENTRA_PASSPHRASE", "")
	prev := keyringLookupFn
	t.Cleanup(func() { keyringLookupFn = prev })
	var calls []string
	keyringLookupFn = func(_ string, user string) ([]byte, error) {
		calls = append(calls, user)
		if user == "shared-bucket" {
			return []byte("legacy-entry"), nil
		}
		return nil, ErrKeyringEntryNotFound
	}

	got, err := Resolve(ResolveOptions{
		UseKeyring:           true,
		KeyringService:       "sentra",
		KeyringUser:          "shared-bucket/sentra-a/",
		KeyringFallbackUsers: []string{"shared-bucket"},
		Prompt: func() ([]byte, error) {
			t.Fatal("prompt should not run when a fallback keyring entry exists")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(got) != "legacy-entry" {
		t.Fatalf("got %q, want legacy-entry", got)
	}
	wantCalls := []string{"shared-bucket/sentra-a/", "shared-bucket"}
	if strings.Join(calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("keyring users: got %v, want %v", calls, wantCalls)
	}
}

func TestResolve_KeyringPrimaryErrorDoesNotTryFallback(t *testing.T) {
	t.Setenv("SENTRA_PASSPHRASE", "")
	prev := keyringLookupFn
	t.Cleanup(func() { keyringLookupFn = prev })
	wantErr := errors.New("keyring locked")
	var calls []string
	keyringLookupFn = func(_ string, user string) ([]byte, error) {
		calls = append(calls, user)
		return nil, wantErr
	}

	_, err := Resolve(ResolveOptions{
		UseKeyring:           true,
		KeyringUser:          "shared-bucket/sentra-a/",
		KeyringFallbackUsers: []string{"shared-bucket"},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected keyring error, got %v", err)
	}
	if len(calls) != 1 || calls[0] != "shared-bucket/sentra-a/" {
		t.Fatalf("keyring users: got %v, want primary only", calls)
	}
}

func TestStoreKeyringPassphrase_UsesConfiguredServiceAndUser(t *testing.T) {
	prev := keyringSetFn
	t.Cleanup(func() { keyringSetFn = prev })
	var gotService string
	var gotUser string
	var gotValue []byte
	keyringSetFn = func(service, user string, value []byte) error {
		gotService = service
		gotUser = user
		gotValue = append([]byte(nil), value...)
		return nil
	}

	err := StoreKeyringPassphrase(StoreKeyringOptions{
		KeyringService: "sentra",
		KeyringUser:    "bucket",
	}, []byte("from-prompt"))
	if err != nil {
		t.Fatalf("StoreKeyringPassphrase: %v", err)
	}
	if gotService != "sentra" {
		t.Errorf("service: got %q, want sentra", gotService)
	}
	if gotUser != "bucket" {
		t.Errorf("user: got %q, want bucket", gotUser)
	}
	if string(gotValue) != "from-prompt" {
		t.Errorf("value: got %q, want from-prompt", gotValue)
	}
}

func TestStoreKeyringPassphrase_DefaultsServiceAndUser(t *testing.T) {
	prev := keyringSetFn
	t.Cleanup(func() { keyringSetFn = prev })
	var gotService string
	var gotUser string
	keyringSetFn = func(service, user string, _ []byte) error {
		gotService = service
		gotUser = user
		return nil
	}

	if err := StoreKeyringPassphrase(StoreKeyringOptions{}, []byte("from-prompt")); err != nil {
		t.Fatalf("StoreKeyringPassphrase: %v", err)
	}
	if gotService != "sentra" {
		t.Errorf("service: got %q, want sentra", gotService)
	}
	if gotUser != "default" {
		t.Errorf("user: got %q, want default", gotUser)
	}
}

func TestStoreKeyringPassphrase_RejectsEmpty(t *testing.T) {
	prev := keyringSetFn
	t.Cleanup(func() { keyringSetFn = prev })
	keyringSetFn = func(string, string, []byte) error {
		t.Fatal("keyring set should not be called for empty passphrase")
		return nil
	}

	if err := StoreKeyringPassphrase(StoreKeyringOptions{}, nil); err == nil {
		t.Fatal("expected empty passphrase error, got nil")
	}
}

func TestDeleteKeyringPassphrase_UsesConfiguredServiceAndUser(t *testing.T) {
	prev := keyringDeleteFn
	t.Cleanup(func() { keyringDeleteFn = prev })
	var gotService string
	var gotUser string
	keyringDeleteFn = func(service, user string) error {
		gotService = service
		gotUser = user
		return nil
	}

	deleted, err := DeleteKeyringPassphrase(StoreKeyringOptions{
		KeyringService: "sentra",
		KeyringUser:    "bucket",
	})
	if err != nil {
		t.Fatalf("DeleteKeyringPassphrase: %v", err)
	}
	if !deleted {
		t.Fatal("deleted: got false, want true")
	}
	if gotService != "sentra" {
		t.Errorf("service: got %q, want sentra", gotService)
	}
	if gotUser != "bucket" {
		t.Errorf("user: got %q, want bucket", gotUser)
	}
}

func TestDeleteKeyringPassphrase_DefaultsServiceAndUser(t *testing.T) {
	prev := keyringDeleteFn
	t.Cleanup(func() { keyringDeleteFn = prev })
	var gotService string
	var gotUser string
	keyringDeleteFn = func(service, user string) error {
		gotService = service
		gotUser = user
		return nil
	}

	if _, err := DeleteKeyringPassphrase(StoreKeyringOptions{}); err != nil {
		t.Fatalf("DeleteKeyringPassphrase: %v", err)
	}
	if gotService != "sentra" {
		t.Errorf("service: got %q, want sentra", gotService)
	}
	if gotUser != "default" {
		t.Errorf("user: got %q, want default", gotUser)
	}
}

func TestDeleteKeyringPassphrase_NotFoundIsNotError(t *testing.T) {
	prev := keyringDeleteFn
	t.Cleanup(func() { keyringDeleteFn = prev })
	keyringDeleteFn = func(string, string) error {
		return ErrKeyringEntryNotFound
	}

	deleted, err := DeleteKeyringPassphrase(StoreKeyringOptions{})
	if err != nil {
		t.Fatalf("DeleteKeyringPassphrase: %v", err)
	}
	if deleted {
		t.Fatal("deleted: got true, want false")
	}
}
