package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/setup"
	"github.com/markgustetic/sentra/internal/tui"
)

// UIDeps wires the side-effecting pieces of `sentra ui` so tests can
// drop in a memory store and a stub Run callback. Run isolates the
// "actually launch a Bubbletea program" step behind a function so
// unit tests don't need a real terminal.
type UIDeps struct {
	RepoDeps

	// Run is the actual TUI launcher. Production wires it to a
	// closure that constructs and runs a tea.Program; tests inject
	// a stub that captures the constructed App and returns nil.
	Run func(app tui.App) error

	// Version and Commit identify the build; they reach the TUI's welcome
	// splash. Plain display data, threaded from cmd/sentra. Commit may be the
	// goreleaser placeholder "none".
	Version string
	Commit  string

	// SavePassphrase re-saves a rotated passphrase to the OS keyring
	// after the TUI's password flow changes it. Same hook the `passwd`
	// command uses. May be nil when no keyring is wired.
	SavePassphrase func(cfg *config.Config, passphrase []byte) error

	// DeletePassphrase removes the OS keyring entry for the configured
	// repo — the Settings view's "forget keyring passphrase" action.
	DeletePassphrase func(cfg *config.Config) (bool, error)

	// SetupEffects overrides the setup engine's side-effecting seam. Nil
	// means runUI constructs the production setup.DefaultEffects(); tests
	// inject a fake to keep AWS/keyring calls out of the process.
	SetupEffects setup.Effects

	// SetupSeedConfig pre-fills the first-run setup wizard. When non-nil AND the
	// launch lands on the first-run path (no config file present), runUI hands
	// this config to the wizard as its starting point so the S3 detail fields
	// (bucket/prefix/region/endpoint) come up populated — the wizard reads them
	// via deps.Config → config0 → setup.DefaultPlan. It is NOT written to disk:
	// the launch stays first-run and the wizard persists the config only on
	// completion. `sentra local` sets this to MinIO coordinates; every other
	// caller (NewUI) leaves it nil for a blank wizard. Non-secret S3 coordinates
	// only — never a passphrase or credentials.
	//
	// `sentra setup` forces the wizard with a config present, so the seed's
	// first-run precondition is now an explicit !ConfigExists term in runUI
	// rather than a property of the branch.
	SetupSeedConfig *config.Config

	// PassphraseFile resolves the --passphrase-file path (the root persistent
	// flag) at run time. The launch-routing probe (probeLaunchState) must
	// honor it so a non-interactive file source resolves the same way it does
	// on every command's read path — otherwise `sentra ui --passphrase-file X`
	// against a keyring-off repo misroutes to the unlock gate, which cannot
	// read the file. It is a func, not a plain string, because production wires
	// the command deps BEFORE cobra parses argv: a value snapshot would always
	// be empty, so the probe must read the live cli.RootFlags.PassphraseFile at
	// run time. Nil (or a func returning "") means "no file source". Returns a
	// path, never a secret.
	PassphraseFile func() string
}

// NewUI returns the cobra command for `sentra ui`. The command
// loads sentra.yaml, opens the repo, builds the App via the deps,
// and hands it to deps.Run. Any error from Run propagates as the
// command's exit code.
//
// Flags:
//   - --config <path>   sentra.yaml path (default ./sentra.yaml)
//
// Future iterations might add a --readonly mode that skips the
// repo open and shows only static file-cached state.
func NewUI(deps UIDeps) *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:           "ui",
		Short:         "Launch the full-screen TUI dashboard",
		Long:          "Open the Bubbletea dashboard. Use 'd', 's', 'D', 'a' to switch views; 'q' or Ctrl+C to quit.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUI(cmd, deps, cfgPath, false)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
	return cmd
}

// runUI is the body of `sentra ui`. Pulled out for grep-ability and
// to keep the cobra closure shallow.
func runUI(cmd *cobra.Command, deps UIDeps, cfgPath string, forceSetup bool) error {
	cmd.SilenceUsage = true

	// An explicit `--config ""` means the default file, not the current
	// directory. Without this, filepath.Abs("") below resolves to the cwd and
	// every config-writing flow the TUI hosts (setup, policy, schedule) is handed
	// a DIRECTORY to write to. The deleted runSetup normalized this on entry;
	// doing it here covers `sentra ui` as well as `sentra setup`.
	if cfgPath == "" {
		cfgPath = configFileName
	}

	// Discovery: the untouched default falls back to the user-level
	// config when the cwd has none, so bare `sentra` opens the
	// configured repo from anywhere. Explicit --config (and `sentra
	// local`'s programmatic path) pass through untouched.
	cfgPath = resolveConfigPath(cmd, cfgPath)

	passphraseFile := ""
	if deps.PassphraseFile != nil {
		passphraseFile = deps.PassphraseFile()
	}
	st, err := probeLaunchState(cmd, cfgPath, passphraseFile)
	if err != nil {
		return err
	}

	// Resolve the config path to an absolute location so config-writing
	// flows (setup, policy, schedule) write back to the file the user
	// actually launched against, regardless of the process cwd. Abs only
	// fails when the cwd is unreadable; fall back to the raw path then.
	absCfgPath := cfgPath
	if p, err := filepath.Abs(cfgPath); err == nil {
		absCfgPath = p
	}

	// The welcome splash is on unless the operator persisted the opt-out.
	// probeLaunchState already loaded the config on both paths, and
	// launchState.Config is non-nil on a nil error, so no extra load is needed.
	showSplash := true
	if st.ConfigExists {
		showSplash = !st.Config.UI.HideSplash
	}

	// First run (no config), configured-but-locked, and an explicit
	// `sentra setup` all launch the TUI WITHOUT opening a repo — the wizard /
	// unlock view own the interactive path so huh never fires here. Repo is
	// nil; the unlock view swaps a live repo in via repoReadyMsg once the user
	// provides the passphrase. forceSetup outranks the lock gate: reconfiguring
	// must not demand the passphrase for a repo the operator may be replacing.
	if forceSetup || !st.ConfigExists || !st.PassphraseAvailable {
		initial := "setup"
		if st.ConfigExists && !forceSetup {
			initial = "unlock"
		}
		// Pick what pre-fills the wizard, highest priority first:
		//
		//	1. a real on-disk config       (st.Config, when st.ConfigExists)
		//	2. the setup draft             (a previous run that never finished)
		//	3. deps.SetupSeedConfig        (`sentra local`'s MinIO coordinates)
		//	4. the blank/default config
		//
		// The !ConfigExists guard on both 2 and 3 is load bearing now that
		// forceSetup can reach initial=="setup" WITH a config present: a real
		// on-disk config must always outrank anything reconstructed or supplied.
		// Nothing is written to disk here — the wizard persists on completion.
		launchCfg := st.Config
		if initial == "setup" && !st.ConfigExists {
			if draft := loadSetupDraft(cfgPath); draft != nil {
				launchCfg = draft
			} else if deps.SetupSeedConfig != nil {
				launchCfg = deps.SetupSeedConfig
			}
		}
		repoName := launchCfg.Repo.S3.Bucket
		app := tui.NewApp(tui.Deps{
			RepoName:                repoName,
			Config:                  launchCfg,
			Ctx:                     cmd.Context(),
			ConfigPath:              absCfgPath,
			NewStore:                deps.NewStore,
			SaveKeyringPassphrase:   deps.SavePassphrase,
			DeleteKeyringPassphrase: deps.DeletePassphrase,
			SetupEffects:            setupEffectsForLaunch(deps),
			PassphraseFile:          passphraseFile,
			InitialView:             initial,
			Reconfigure:             forceSetup && st.ConfigExists,
			ShowSplash:              showSplash,
			Version:                 deps.Version,
			Commit:                  deps.Commit,
		})
		if deps.Run == nil {
			return fmt.Errorf("ui: no Run hook configured")
		}
		return deps.Run(app)
	}

	r, pass, cfg, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		// Configured, passphrase available, but the repo would not OPEN
		// (expired AWS credentials, unreachable bucket, network down).
		// The TUI owns the other launch states — first-run lands on the
		// wizard, locked on unlock — so this one lands on the connect
		// gate with the fix a keypress away, instead of dying to stderr.
		// Config-load/probe failures never reach here: probeLaunchState
		// already returned those above.
		return launchConnectGate(cmd, deps, cfgPath, absCfgPath, st, showSplash, passphraseFile, err)
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	// Pick a friendly repo name for the top bar. The bucket name is
	// usually the most meaningful single label; falls back to the
	// repo's own UUID-style ID if the bucket is unset (in-memory test
	// runs or unconfigured installs).
	repoName := cfg.Repo.S3.Bucket
	if repoName == "" {
		repoName = r.Config().ID
	}

	app := tui.NewApp(tui.Deps{
		Repo:     r,
		RepoName: repoName,
		Config:   cfg,
		// Pass the cobra command's context so:
		//   1. Signals (Ctrl+C wired by cobra) cancel TUI work.
		//   2. The TUI's App.cleanup() can cancel the same context
		//      tree on a 'q' quit, terminating in-flight blobstore
		//      calls instead of letting them drain to per-call timeouts.
		Ctx: cmd.Context(),

		// Unit-1 plumbing: call-time hooks + plain data the ported
		// operation flows consume. None hold resolved secrets.
		ConfigPath:              absCfgPath,
		NewStore:                deps.NewStore,
		SaveKeyringPassphrase:   deps.SavePassphrase,
		DeleteKeyringPassphrase: deps.DeletePassphrase,
		SetupEffects:            setupEffectsForLaunch(deps),
		// Reconfiguring from Settings reaches the same wizard as `sentra setup`,
		// so the flag has to follow the dashboard launch too.
		PassphraseFile: passphraseFile,
		ShowSplash:     showSplash,
		Version:        deps.Version,
		Commit:         deps.Commit,
	})

	if deps.Run == nil {
		return fmt.Errorf("ui: no Run hook configured")
	}
	return deps.Run(app)
}

// launchConnectGate builds the App for the connect gate: no repo, the
// open failure for the gate to render, and a retry closure that re-runs
// the launch open end-to-end. The closure re-resolves the passphrase
// chain NON-INTERACTIVELY (env / file / keyring only, never the prompt) on
// every call and zeroizes the secret before returning — the repo's derived
// keys are what the TUI needs, the passphrase never crosses the seam.
// Non-interactive is load-bearing here, not just tidy: this closure runs
// inside a live tea.Program, and a source that answered on the original
// launch (e.g. a keyring entry) can vanish before the operator retries: if
// that pushed the closure into an interactive prompt, huh would fight
// Bubbletea for stdin and wedge the terminal (CLAUDE.md: "huh cannot run
// inside a live tea.Program"). See openRepoForConfigNonInteractive. Closure
// injection keeps the tui→cli import direction clean, same as Deps.NewStore.
func launchConnectGate(cmd *cobra.Command, deps UIDeps, cfgPath, absCfgPath string, st launchState, showSplash bool, passphraseFile string, openErr error) error {
	app := tui.NewApp(tui.Deps{
		RepoName:                st.Config.Repo.S3.Bucket,
		Config:                  st.Config,
		Ctx:                     cmd.Context(),
		ConfigPath:              absCfgPath,
		NewStore:                deps.NewStore,
		SaveKeyringPassphrase:   deps.SavePassphrase,
		DeleteKeyringPassphrase: deps.DeletePassphrase,
		SetupEffects:            setupEffectsForLaunch(deps),
		PassphraseFile:          passphraseFile,
		InitialView:             "connect",
		ShowSplash:              showSplash,
		Version:                 deps.Version,
		Commit:                  deps.Commit,
		ConnectError:            openErr,
		OpenRepo: func(_ context.Context) (*repo.Repo, *config.Config, error) {
			// openRepoForConfigNonInteractive reads the command's context
			// itself; the parameter exists for the seam's shape, and the
			// command's context is the same cancellation tree the TUI runs
			// under. Non-interactive on purpose — see the doc comment above.
			r, pass, cfg, err := openRepoForConfigNonInteractive(cmd, cfgPath, passphraseFile, deps.NewStore)
			if err != nil {
				return nil, nil, err
			}
			crypto.Zeroize(pass)
			return r, cfg, nil
		},
	})
	if deps.Run == nil {
		return fmt.Errorf("ui: no Run hook configured")
	}
	return deps.Run(app)
}

// loadSetupDraft reads the setup draft beside cfgPath, or returns nil when
// there isn't a usable one. It is what makes an interrupted `sentra setup`
// resumable: the wizard writes the draft before provisioning and removes it
// only on success, so a draft on disk means a previous run got as far as the
// review gate and then failed. Without a reader the draft would be litter.
//
// Every failure degrades to nil rather than propagating. A corrupt or
// unreadable draft is a stale convenience artifact, and refusing to launch the
// wizard over one would strand the operator with no in-product way to clear it.
func loadSetupDraft(cfgPath string) *config.Config {
	draftPath := setup.NewEngine(nil).DraftPath(cfgPath)
	if info, err := os.Stat(draftPath); err != nil || info.IsDir() {
		return nil
	}
	cfg, err := config.Load(draftPath)
	if err != nil {
		return nil
	}
	return cfg
}

// setupEffectsForLaunch returns the UIDeps override or the production default.
func setupEffectsForLaunch(deps UIDeps) setup.Effects {
	if deps.SetupEffects != nil {
		return deps.SetupEffects
	}
	return setup.DefaultEffects()
}

// DefaultUIRunner is the production launcher: wraps the App in a
// tea.Program with alt-screen and runs it. Wiring this up here
// keeps the cmd/sentra main.go terse.
//
// Refuses to launch when stdout isn't a TTY: alt-screen escape codes
// would otherwise be written into a pipe, file, or CI log, polluting
// the consumer's stdout with no useful output. Scripts piping
// `sentra` should call a JSON-emitting subcommand (`snapshots --json`,
// `agent scan --json`) instead.
//
// The message names both non-interactive routes because `sentra setup` reaches
// this same refusal — setup is a launcher for this TUI. Telling someone who
// typed `setup` to run `sentra ui` restates the thing that just refused;
// `sentra init` is the flow that configures a repository without a terminal.
func DefaultUIRunner(app tui.App) error {
	if !term.IsTerminal(os.Stdout.Fd()) {
		return errors.New("sentra requires a terminal for the TUI; run `sentra init` to configure a repository without one, or call a JSON-emitting subcommand like `sentra snapshots --json` from scripts")
	}
	p := tea.NewProgram(app, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// SetUIAsDefault rewires the root command so that bare `sentra` (no
// subcommand and no args) falls through to `ui`. Cobra's default
// behavior is to print help in that case; we want the TUI to be the
// default landing experience as documented in the design doc.
//
// We do this by setting the root command's RunE: cobra invokes RunE
// when there is no subcommand to dispatch to. We delegate straight
// to the ui command's RunE so the loading / error-propagation logic
// stays in one place.
//
// Important: cobra ignores RunE if the user passed an unknown
// subcommand — it'll print "unknown command" and exit. That's the
// right behavior; we only want the bare-invocation path.
func SetUIAsDefault(root *cobra.Command, deps UIDeps) {
	uiCmd := NewUI(deps)
	root.RunE = func(cmd *cobra.Command, args []string) error {
		// The executed command on this path is root, so root's
		// SilenceUsage governs error output: without this, any launch
		// failure (expired AWS credentials, unreachable bucket) gets
		// buried under the full command list. `sentra ui` already
		// silences usage; the bare dispatch must fail the same way.
		cmd.SilenceUsage = true
		// When the user types `sentra --help`, args is empty but
		// cobra prints help before reaching here. So this body fires
		// only on bare `sentra` (or `sentra` with global flags).
		// Forward to the ui command's body. We share the same flag
		// pointer so --config still works.
		uiCmd.SetContext(cmd.Context())
		uiCmd.SetOut(cmd.OutOrStdout())
		uiCmd.SetErr(cmd.ErrOrStderr())
		return uiCmd.RunE(uiCmd, args)
	}
}
