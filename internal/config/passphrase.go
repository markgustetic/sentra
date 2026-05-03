package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"

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

	// Prompt is the interactive callback. Typically wired to
	// ui.PromptPassphrase or ui.PromptPassphraseWithConfirm. Nil
	// disables the prompt branch (useful in tests / scripts).
	Prompt func() ([]byte, error)
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
		service := opts.KeyringService
		if service == "" {
			service = "sentra"
		}
		user := opts.KeyringUser
		if user == "" {
			user = "default"
		}
		val, err := keyringLookupFn(service, user)
		if err == nil {
			return val, nil
		}
		if !errors.Is(err, ErrKeyringEntryNotFound) {
			return nil, fmt.Errorf("config: keyring lookup: %w", err)
		}
		// Fall through to the prompt on a clean miss.
	}
	if opts.Prompt != nil {
		return opts.Prompt()
	}
	return nil, ErrNoPassphraseSource
}

// readPassphraseFile reads the passphrase from path, stripping a
// single trailing newline (and stripping a leading UTF-8 BOM if
// present). Editors love to add a trailing \n; users typing the
// passphrase into an unrelated tool wouldn't include it.
func readPassphraseFile(path string) ([]byte, error) {
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
