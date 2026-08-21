package cli

import (
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

// RepoDeps is the dependency set shared by every read-path command: the
// blobstore factory, the two passphrase resolvers, and the stdout sink.
// Commands embed it and add their own extra fields (Stderr, Confirm, ...).
// Exported so cmd/sentra can construct it when wiring each command.
type RepoDeps struct {
	NewStore             func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	Passphrase           func() ([]byte, error)
	PassphraseWithConfig func(cfg *config.Config) ([]byte, error)
	Stdout               io.Writer
}

// openRepoForConfig runs the shared load-config -> open-store -> resolve-
// passphrase -> open-repo sequence. On success it returns the opened repo,
// the passphrase bytes (caller owns `defer crypto.Zeroize(pass)` and
// `defer r.Close()`), and the loaded config. On any error it cleans up the
// passphrase itself and returns it nil. Error strings are identical to the
// per-command blocks this replaces.
func openRepoForConfig(cmd *cobra.Command, cfgPath string, deps RepoDeps) (*repo.Repo, []byte, *config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load config: %w", err)
	}
	store, err := deps.NewStore(cmd.Context(), cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open blobstore: %w", err)
	}
	pass, err := resolvePassphrase(deps.Passphrase, deps.PassphraseWithConfig, cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve passphrase: %w", err)
	}
	r, err := repo.Open(cmd.Context(), store, pass)
	if err != nil {
		crypto.Zeroize(pass)
		return nil, nil, nil, fmt.Errorf("open repo: %w", err)
	}
	return r, pass, cfg, nil
}

// openRepoForConfigNonInteractive mirrors openRepoForConfig's load -> open-
// store -> resolve-passphrase -> open-repo sequence, but resolves the
// passphrase the same way probeLaunchState does: env / --passphrase-file /
// keyring only, with the interactive Prompt disabled. It exists for callers
// that run inside a live tea.Program — the connect gate's retry closure —
// where reaching config.Resolve's interactive prompt would hand stdin to huh
// while Bubbletea also owns it and wedge the terminal (CLAUDE.md: "huh
// cannot run inside a live tea.Program"). Unlike openRepoForConfig, this
// helper does not take RepoDeps: it never calls the CLI's prompt-capable
// Passphrase / PassphraseWithConfig resolvers, only newStore.
//
// A source that answered on the ORIGINAL launch (populating
// probeLaunchState.PassphraseAvailable) can still vanish before a retry —
// e.g. a keyring entry deleted while the operator sits on the gate — so this
// helper cannot assume availability. ErrNoPassphraseSource is mapped to a
// message that says what actually happened, instead of surfacing
// config.Resolve's generic wording, which mentions a TTY prompt this path
// deliberately never reaches.
//
// On any error the passphrase (if resolved at all) is zeroized before
// returning; on success the caller owns zeroizing it, same as
// openRepoForConfig.
func openRepoForConfigNonInteractive(cmd *cobra.Command, cfgPath, passphraseFile string, newStore func(context.Context, *config.Config) (blobstore.Store, error)) (*repo.Repo, []byte, *config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load config: %w", err)
	}
	store, err := newStore(cmd.Context(), cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open blobstore: %w", err)
	}
	pass, err := config.Resolve(config.ResolveOptions{
		PassphraseFile:       passphraseFile,
		UseKeyring:           cfg.Passphrase.UseKeyring,
		KeyringService:       config.KeyringService,
		KeyringUser:          config.KeyringUserForConfig(cfg),
		KeyringFallbackUsers: config.LegacyKeyringUsersForConfig(cfg),
		Prompt:               nil, // this path must never prompt; see doc comment
	})
	if err != nil {
		if errors.Is(err, config.ErrNoPassphraseSource) {
			return nil, nil, nil, errors.New("passphrase source no longer available — quit and relaunch to unlock")
		}
		return nil, nil, nil, fmt.Errorf("resolve passphrase: %w", err)
	}
	r, err := repo.Open(cmd.Context(), store, pass)
	if err != nil {
		crypto.Zeroize(pass)
		return nil, nil, nil, fmt.Errorf("open repo: %w", err)
	}
	return r, pass, cfg, nil
}

// launchState classifies what `sentra ui` should show at startup without ever
// prompting: whether a config file exists, and whether a passphrase can be
// resolved non-interactively (keyring / env / file). It never opens the repo
// and never calls an interactive resolver — the TUI's unlock/setup views own
// the interactive path so huh never fires on the launch path.
type launchState struct {
	// ConfigExists reports whether cfgPath is present on disk. Absent means
	// first run: show the setup wizard.
	ConfigExists bool
	// PassphraseAvailable reports whether a non-interactive source supplied
	// the passphrase. False with ConfigExists true means show the unlock view.
	PassphraseAvailable bool
	// Config is the loaded (or default) config, always non-nil on nil error.
	Config *config.Config
}

// probeLaunchState loads the config and attempts a NON-INTERACTIVE passphrase
// resolution. passphraseFile is the --passphrase-file path (empty when unset);
// it MUST be honored here so a file source routes the launch identically to the
// normal read path — otherwise `sentra ui --passphrase-file X` against a
// keyring-off repo misroutes to the unlock gate, which cannot read the file.
// The launch path must not run an interactive resolver (it would prompt), so
// this helper resolves through config.Resolve with a nil Prompt and the same
// file + keyring settings the read path would use, and treats
// ErrNoPassphraseSource as "not available" rather than an error.
func probeLaunchState(_ *cobra.Command, cfgPath, passphraseFile string) (launchState, error) {
	exists := false
	if info, err := os.Stat(cfgPath); err == nil && !info.IsDir() {
		exists = true
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return launchState{}, fmt.Errorf("load config: %w", err)
	}
	st := launchState{ConfigExists: exists, Config: cfg}
	if !exists {
		return st, nil // first run: no passphrase needed, wizard handles it
	}
	pass, err := config.Resolve(config.ResolveOptions{
		PassphraseFile:       passphraseFile, // --passphrase-file: highest priority, same as the read path
		UseKeyring:           cfg.Passphrase.UseKeyring,
		KeyringService:       config.KeyringService,
		KeyringUser:          config.KeyringUserForConfig(cfg),
		KeyringFallbackUsers: config.LegacyKeyringUsersForConfig(cfg),
		Prompt:               nil, // launch path never prompts
	})
	if err != nil {
		if errors.Is(err, config.ErrNoPassphraseSource) {
			return st, nil // locked: unlock view will collect it
		}
		return launchState{}, fmt.Errorf("resolve passphrase: %w", err)
	}
	// A source supplied the passphrase; wipe it — the read path re-resolves it.
	crypto.Zeroize(pass)
	st.PassphraseAvailable = true
	return st, nil
}

// cmdStdout returns w, or the command's default stdout when w is nil.
func cmdStdout(cmd *cobra.Command, w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return cmd.OutOrStdout()
}

// cmdStderr returns w, or the command's default stderr when w is nil.
func cmdStderr(cmd *cobra.Command, w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return cmd.ErrOrStderr()
}
