package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/zalando/go-keyring"
)

// envPassphrase is the env var sentra reads for a non-prompted
// passphrase. Documented in the design doc and surfaced here so the
// constant has one definition.
//
//nolint:gosec // G101: constant is the *name* of an env var, not a credential.
const envPassphrase = "SENTRA_PASSPHRASE"

// ErrNoPassphraseSource is returned by Resolve when none of the
// configured sources (file, env, keyring, prompt) can supply a
// passphrase. Common cause: a non-interactive run with no env var
// set and no Prompt callback.
var ErrNoPassphraseSource = errors.New("config: no passphrase source available (set SENTRA_PASSPHRASE, --passphrase-file, or run on a TTY)")

// ErrKeyringEntryNotFound is the canonical "no entry yet" error from
// the keyring lookup. The resolver treats this as a soft miss and
// falls through to the prompt path so the user can enter the
// passphrase once and (later) opt to save it to the keyring.
var ErrKeyringEntryNotFound = errors.New("config: keyring entry not found")

// ResolveOptions configures how the resolver sources the passphrase.
//
// The documented priority is:
//  1. PassphraseFile (--passphrase-file flag)
//  2. SENTRA_PASSPHRASE env var
//  3. OS keyring (when UseKeyring is set)
//  4. Prompt callback (typically ui.PromptPassphrase)
//
// Each source short-circuits the rest. A keyring miss falls through
// to the next source so a clean install (no entry yet) doesn't fail
// hard.
type ResolveOptions struct {
	// PassphraseFile is the optional path passed via --passphrase-file.
	// Empty disables this branch.
	PassphraseFile string

	// UseKeyring enables the OS-keyring lookup branch (sourced from
	// sentra.yaml's passphrase.use_keyring or a future flag).
	UseKeyring bool

	// KeyringService is the service name passed to the keyring lib.
	// Defaults to "sentra" when empty.
	KeyringService string

	// KeyringUser is the per-repo identifier passed to the keyring.
	// Defaults to "default" when empty — fine for single-repo users.
	KeyringUser string

	// KeyringFallbackUsers are legacy per-repo identifiers to try after
	// KeyringUser misses. Non-empty values are tried in order with duplicates
	// removed. Other keyring errors still fail closed instead of falling back.
	KeyringFallbackUsers []string

	// Prompt is the interactive callback. Typically wired to
	// ui.PromptPassphrase or ui.PromptPassphraseWithConfirm. Nil
	// disables the prompt branch (useful in tests / scripts).
	Prompt func() ([]byte, error)
}

// StoreKeyringOptions configures where a passphrase should be saved in the
// OS keyring.
type StoreKeyringOptions struct {
	// KeyringService is the service name passed to the keyring lib.
	// Defaults to "sentra" when empty.
	KeyringService string

	// KeyringUser is the per-repo identifier passed to the keyring.
	// Defaults to "default" when empty.
	KeyringUser string
}

// Resolve looks up the passphrase per the documented priority and
// returns the bytes. The caller is responsible for zeroizing the
// returned slice after deriving keys from it.
//
// On a keyring lookup miss (ErrKeyringEntryNotFound), Resolve falls
// through to the prompt branch — a clean install hasn't stored the
// passphrase in the keyring yet. Other keyring errors surface as-is.
func Resolve(opts ResolveOptions) ([]byte, error) {
	if opts.PassphraseFile != "" {
		return readPassphraseFile(opts.PassphraseFile)
	}
	if v := os.Getenv(envPassphrase); v != "" {
		// Defensive copy so the env-var storage isn't aliased into
		// the returned slice; we want callers to be able to zeroize
		// without wondering whether the runtime keeps the env around.
		out := make([]byte, len(v))
		copy(out, v)
		return out, nil
	}
	if opts.UseKeyring {
		service, user := normalizeKeyringTarget(opts.KeyringService, opts.KeyringUser)
		for _, candidate := range keyringLookupUsers(user, opts.KeyringFallbackUsers) {
			val, err := keyringLookupFn(service, candidate)
			if err == nil {
				return val, nil
			}
			if !errors.Is(err, ErrKeyringEntryNotFound) {
				return nil, fmt.Errorf("config: keyring lookup for %q: %w", candidate, err)
			}
		}
		// Fall through to the prompt on a clean miss.
	}
	if opts.Prompt != nil {
		return opts.Prompt()
	}
	return nil, ErrNoPassphraseSource
}

// StoreKeyringPassphrase saves passphrase in the OS keyring. It never writes
// the secret to sentra.yaml; callers should store only non-secret keyring
// lookup settings in config.
func StoreKeyringPassphrase(opts StoreKeyringOptions, passphrase []byte) error {
	if len(passphrase) == 0 {
		return fmt.Errorf("config: cannot store empty passphrase in keyring")
	}
	service, user := normalizeKeyringTarget(opts.KeyringService, opts.KeyringUser)
	if err := keyringSetFn(service, user, passphrase); err != nil {
		return fmt.Errorf("config: keyring store: %w", err)
	}
	return nil
}

// DeleteKeyringPassphrase removes the configured passphrase from the OS
// keyring. It returns false without error when no entry exists.
func DeleteKeyringPassphrase(opts StoreKeyringOptions) (bool, error) {
	service, user := normalizeKeyringTarget(opts.KeyringService, opts.KeyringUser)
	if err := keyringDeleteFn(service, user); err != nil {
		if errors.Is(err, ErrKeyringEntryNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("config: keyring delete: %w", err)
	}
	return true, nil
}

func normalizeKeyringTarget(service, user string) (string, string) {
	if service == "" {
		service = "sentra"
	}
	if user == "" {
		user = "default"
	}
	return service, user
}

func keyringLookupUsers(primary string, fallbacks []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 1+len(fallbacks))
	for _, user := range append([]string{primary}, fallbacks...) {
		if user == "" {
			user = "default"
		}
		if seen[user] {
			continue
		}
		seen[user] = true
		out = append(out, user)
	}
	return out
}

// readPassphraseFile reads the passphrase from path, stripping a
// single trailing newline (and stripping a leading UTF-8 BOM if
// present). Editors love to add a trailing \n; users typing the
// passphrase into an unrelated tool wouldn't include it.
//
// On Unix, the file's mode is checked first: any group- or world-
// readable bits cause Resolve to fail closed with a clear error
// rather than silently use a passphrase that other accounts on the
// system can read. Windows permissions don't map onto Unix bits, so
// the check is skipped there (the threat model already documents
// that the OS keyring/file trust assumptions are platform-specific).
func readPassphraseFile(path string) ([]byte, error) {
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("config: stat passphrase file: %w", err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			return nil, fmt.Errorf(
				"config: passphrase file %s has insecure permissions %#o (group or world bits set); run `chmod 600 %s`",
				path, mode, path,
			)
		}
	}
	raw, err := os.ReadFile(path) //nolint:gosec // path comes from the user via a flag, not from observed content
	if err != nil {
		return nil, fmt.Errorf("config: read passphrase file: %w", err)
	}
	// Strip leading UTF-8 BOM (some Windows editors emit it).
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	// Strip exactly one trailing CRLF or LF — but NOT all trailing
	// whitespace, since spaces in a passphrase are legal and
	// significant.
	switch {
	case bytes.HasSuffix(raw, []byte("\r\n")):
		raw = raw[:len(raw)-2]
	case bytes.HasSuffix(raw, []byte("\n")):
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("config: passphrase file %s is empty", path)
	}
	return raw, nil
}

// keyringLookupFn is the bridge to the OS keyring. Pulled out as a
// package-level variable so tests can swap it for a deterministic
// stub. The default reaches into github.com/zalando/go-keyring.
var keyringLookupFn = func(service, user string) ([]byte, error) {
	v, err := keyring.Get(service, user)
	if err != nil {
		// Map the upstream "not found" error to our sentinel so the
		// resolver can fall through cleanly.
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, ErrKeyringEntryNotFound
		}
		return nil, err
	}
	return []byte(v), nil
}

var keyringSetFn = func(service, user string, value []byte) error {
	return keyring.Set(service, user, string(value)) //nolint:gosec // go-keyring accepts string values; the secret is handed directly to the OS keyring.
}

var keyringDeleteFn = func(service, user string) error {
	if err := keyring.Delete(service, user); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return ErrKeyringEntryNotFound
		}
		return err
	}
	return nil
}
