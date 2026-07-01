package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
)

// minPasswdNewPassphraseLen is the lower bound `password` enforces on
// the new passphrase. Mirrors the floor `init` applies — the
// passphrase machinery can technically handle shorter inputs, but
// 8 bytes is the operational floor across the codebase.
const minPasswdNewPassphraseLen = 8

// PasswdDeps wires the side-effecting pieces of `sentra password` so
// tests can inject deterministic callbacks. Production wires:
//
//   - NewStore: the standard S3-store factory (RetryStore-wrapped).
//   - Passphrase: the existing old-passphrase resolver chain
//     (--passphrase-file → SENTRA_PASSPHRASE → keyring → prompt).
//   - NewPassphrase: a constrained resolver that handles ONLY the
//     --new-passphrase-file flag and falls through to an interactive
//     confirm-on-entry prompt. The env var SENTRA_PASSPHRASE is
//     intentionally NOT a source for the new passphrase — env vars
//     persist in shell history / process listings, which is the
//     wrong default for the new secret.
//   - SavePassphrase: updates the OS keyring entry after a successful
//     rotation when passphrase.use_keyring is enabled.
//   - DeletePassphrase: removes the OS keyring entry for
//     `sentra password forget`.
//   - Stdout: where the success summary lands.
//
// The deps shape mirrors InitDeps + a NewPassphrase callback. The
// new callback takes the --new-passphrase-file flag value as a
// parameter so production wiring doesn't need to share state with
// the cobra command.
type PasswdDeps struct {
	NewStore             func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	Passphrase           func() ([]byte, error)
	PassphraseWithConfig func(cfg *config.Config) ([]byte, error)
	NewPassphrase        func(passphraseFile string) ([]byte, error)
	SavePassphrase       func(cfg *config.Config, passphrase []byte) error
	DeletePassphrase     func(cfg *config.Config) (bool, error)
	Stdout               io.Writer
}

// passwdFlags holds the values of `sentra password`'s flags. Bundled
// into a struct so the cobra wiring is compact and the runE body
// can be tested independently.
type passwdFlags struct {
	newPassphraseFile string
}

type passwordForgetFlags struct {
	configPath string
	bucket     string
}

// NewPasswd returns the cobra command for `sentra password`. The
// command flow:
//
//  1. Load sentra.yaml (config.Load).
//  2. Open the blobstore via deps.NewStore.
//  3. Read the OLD passphrase via deps.Passphrase.
//  4. repo.Open under the old passphrase. If this fails (wrong
//     passphrase or tampered config), the new-passphrase callback
//     is NEVER invoked — operators get a clean "wrong passphrase"
//     without being prompted for the new one (which would imply
//     they authenticated successfully).
//  5. Read the NEW passphrase via deps.NewPassphrase (file flag
//     or interactive confirm-on-entry prompt).
//  6. Validate: non-empty, length >= minPasswdNewPassphraseLen,
//     not equal to the old passphrase.
//  7. Call repo.Repo.Passwd which acquires the advisory lock,
//     rotates salt + wrap + MAC, and writes the new config blob.
//  8. Print a one-line summary.
//
// Refusal cases short-circuit before any S3 write.
func NewPasswd(deps PasswdDeps) *cobra.Command {
	flags := &passwdFlags{}
	cmd := &cobra.Command{
		Use:     "password",
		Aliases: []string{"passwd"},
		Short:   "Manage the repository passphrase",
		Long: "Rewrite the encrypted config blob so a new passphrase wraps the " +
			"(unchanged) repo key. Existing chunks, manifests, and the snapshot " +
			"index remain readable; only the passphrase that Opens the repo " +
			"changes. Holds the same advisory lock as backup and GC.\n\n" +
			"`sentra passwd` remains as a backwards-compatible alias.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPasswd(cmd, deps, flags)
		},
	}
	cmd.Flags().StringVar(&flags.newPassphraseFile, "new-passphrase-file", "",
		"path to a file containing the new passphrase (default: interactive prompt)")
	cmd.AddCommand(newPasswordForget(deps))
	return cmd
}

func newPasswordForget(deps PasswdDeps) *cobra.Command {
	flags := &passwordForgetFlags{configPath: configFileName}
	cmd := &cobra.Command{
		Use:   "forget",
		Short: "Remove the saved repository passphrase from the OS keyring",
		Long: "Remove Sentra's saved repository passphrase from the OS keyring " +
			"and disable passphrase.use_keyring in sentra.yaml when that file exists. " +
			"This does not change the repository passphrase or delete S3 data.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPasswordForget(cmd, deps, flags)
		},
	}
	cmd.Flags().StringVar(&flags.configPath, "config", configFileName,
		"path to sentra.yaml used to identify the keyring entry")
	cmd.Flags().StringVar(&flags.bucket, "bucket", "",
		"bucket/keyring user to forget when sentra.yaml is unavailable or should be overridden")
	return cmd
}

// runPasswd is the body of `sentra password`. Pulled out of the
// closure so it's grep-able and unit-testable independently of cobra.
func runPasswd(cmd *cobra.Command, deps PasswdDeps, flags *passwdFlags) error {
	cmd.SilenceUsage = true

	out := deps.Stdout
	if out == nil {
		out = cmd.OutOrStdout()
	}

	cfg, err := config.Load(configFileName)
	if err != nil {
		return fmt.Errorf("load %s: %w", configFileName, err)
	}

	store, err := deps.NewStore(cmd.Context(), cfg)
	if err != nil {
		return fmt.Errorf("open blobstore: %w", err)
	}

	// 1. Read OLD passphrase via the existing chain. We keep the
	// bytes around briefly to compare against the new one (so we
	// catch matching-passphrase before paying the cost of opening
	// the repo); zeroize on every exit.
	oldPass, err := resolvePassphrase(deps.Passphrase, deps.PassphraseWithConfig, cfg)
	if err != nil {
		return fmt.Errorf("resolve old passphrase: %w", err)
	}
	defer crypto.Zeroize(oldPass)

	// 2. Open the repo with the old passphrase. Failure here MUST
	// short-circuit before any new-passphrase prompt — operators
	// shouldn't be asked to re-type their new secret if their
	// old one didn't even authenticate.
	r, err := repo.Open(cmd.Context(), store, oldPass)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}
	defer r.Close()

	// 3. Read NEW passphrase. The deps callback handles the
	// file-or-prompt resolution; --new-passphrase-file passed
	// through.
	newPass, err := deps.NewPassphrase(flags.newPassphraseFile)
	if err != nil {
		return fmt.Errorf("resolve new passphrase: %w", err)
	}
	defer crypto.Zeroize(newPass)

	// 4. Validate the new passphrase before touching the repo.
	if len(newPass) < minPasswdNewPassphraseLen {
		return fmt.Errorf("new passphrase must be at least %d bytes long", minPasswdNewPassphraseLen)
	}
	// bytes.Equal is non-constant-time, which is fine here: the
	// comparison is between two secrets the operator just typed;
	// timing leakage discriminates "you typed the same thing
	// twice" from "you didn't," which the operator already knows.
	if bytes.Equal(oldPass, newPass) {
		return fmt.Errorf("new passphrase matches old; nothing to rotate")
	}

	if cfg.Passphrase.UseKeyring {
		// Validate the saver is wired up BEFORE rotating, so we fail
		// fast rather than rotate the repo and only then discover we
		// can't persist the new secret. We do NOT delete the old keyring
		// entry here: the entry is keyed by bucket+prefix (which rotation
		// never changes), so SavePassphrase below overwrites it in place.
		// A pre-delete would only open a window where a rotation failure
		// leaves the keyring empty while the repo stays on the old
		// passphrase — breaking non-interactive scheduled runs.
		if deps.SavePassphrase == nil {
			return fmt.Errorf("update keyring passphrase: missing keyring passphrase saver")
		}
	}

	// 5. Rotate. Repo.Passwd acquires the advisory lock, rotates
	// salt + wrap + MAC, and writes the new config atomically. On
	// failure the keyring is left untouched (we haven't touched it yet),
	// so the repo and keyring stay consistent on the old passphrase.
	if err := r.Passwd(cmd.Context(), newPass); err != nil {
		return fmt.Errorf("rotate passphrase: %w", err)
	}

	if cfg.Passphrase.UseKeyring {
		// Rotation succeeded; overwrite the keyring entry with the new
		// secret. Only now do we mutate the keyring, so a rotation
		// failure never leaves it stale.
		if err := deps.SavePassphrase(cfg, newPass); err != nil {
			return fmt.Errorf("repository passphrase was rotated, but update keyring passphrase failed: %w", err)
		}
		fmt.Fprintln(out, "OS keyring passphrase updated.")
	}

	fmt.Fprintln(out, "Passphrase rotated.")
	fmt.Fprintln(out, "Old passphrase is no longer accepted; the new passphrase is in effect for subsequent sentra commands.")
	return nil
}

func runPasswordForget(cmd *cobra.Command, deps PasswdDeps, flags *passwordForgetFlags) error {
	cmd.SilenceUsage = true
	if deps.DeletePassphrase == nil {
		return fmt.Errorf("forget keyring passphrase: missing keyring passphrase deleter")
	}

	out := deps.Stdout
	if out == nil {
		out = cmd.OutOrStdout()
	}

	cfgPath := flags.configPath
	if cfgPath == "" {
		cfgPath = configFileName
	}
	yamlExists := false
	if _, err := os.Stat(cfgPath); err == nil {
		yamlExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", cfgPath, err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", cfgPath, err)
	}
	deleteCfg := *cfg
	if flags.bucket != "" {
		deleteCfg.Repo.S3.Bucket = flags.bucket
	}
	deleted, err := deps.DeletePassphrase(&deleteCfg)
	if err != nil {
		return fmt.Errorf("forget keyring passphrase: %w", err)
	}
	if deleted {
		fmt.Fprintln(out, "OS keyring passphrase removed.")
	} else {
		fmt.Fprintln(out, "No OS keyring passphrase was stored.")
	}

	if yamlExists && cfg.Passphrase.UseKeyring {
		cfg.Passphrase.UseKeyring = false
		if err := os.WriteFile(cfgPath, []byte(renderConfigYAML(cfg)), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", cfgPath, err)
		}
		fmt.Fprintf(out, "%s updated to disable keyring lookup.\n", cfgPath)
	}
	return nil
}
