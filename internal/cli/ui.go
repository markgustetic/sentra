package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/setup"
	"github.com/markgustetic/sentra/internal/tui"
)

// UIDeps wires the side-effecting pieces of `sentra ui` so tests can
// drop in a memory store and a stub Run callback. Run isolates the
// "actually launch a Bubbletea program" step behind a function so
// unit tests don't need a real terminal.
type UIDeps struct {
	RepoDeps

	// Provider is the LLM provider for the agent view. May be nil
	// when no API key is configured — the agent view shows a
	// placeholder pointing at ANTHROPIC_API_KEY in that case.
	Provider llm.Provider

	// ProviderForConfig builds the LLM provider from the loaded
	// command config. When set, it takes precedence over Provider.
	ProviderForConfig func(cfg *config.Config) llm.Provider

	// Run is the actual TUI launcher. Production wires it to a
	// closure that constructs and runs a tea.Program; tests inject
	// a stub that captures the constructed App and returns nil.
	Run func(app tui.App) error

	// Actions is the agent action registry the TUI's agent-apply flow
	// executes confirmed recommendations through. Same registry the
	// `agent` command builds. May be nil (agent-apply then reports no
	// registry configured).
	Actions *action.Registry

	// SavePassphrase re-saves a rotated passphrase to the OS keyring
	// after the TUI's password flow changes it. Same hook the `passwd`
	// command uses. May be nil when no keyring is wired.
	SavePassphrase func(cfg *config.Config, passphrase []byte) error

	// SetupEffects overrides the setup engine's side-effecting seam. Nil
	// means runUI constructs the production setup.DefaultEffects(); tests
	// inject a fake to keep AWS/keyring calls out of the process.
	SetupEffects setup.Effects

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
			return runUI(cmd, deps, cfgPath)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	return cmd
}

// runUI is the body of `sentra ui`. Pulled out for grep-ability and
// to keep the cobra closure shallow.
func runUI(cmd *cobra.Command, deps UIDeps, cfgPath string) error {
	cmd.SilenceUsage = true

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

	// First run (no config) and configured-but-locked both launch the TUI
	// WITHOUT opening a repo — the wizard / unlock view own the interactive
	// path so huh never fires here. Repo is nil; the unlock view swaps a live
	// repo in via repoReadyMsg once the user provides the passphrase.
	if !st.ConfigExists || !st.PassphraseAvailable {
		initial := "setup"
		if st.ConfigExists {
			initial = "unlock"
		}
		repoName := st.Config.Repo.S3.Bucket
		app := tui.NewApp(tui.Deps{
			Provider:              providerForLaunch(deps, st.Config),
			RepoName:              repoName,
			Config:                st.Config,
			Ctx:                   cmd.Context(),
			ConfigPath:            absCfgPath,
			NewStore:              deps.NewStore,
			Actions:               deps.Actions,
			SaveKeyringPassphrase: deps.SavePassphrase,
			SetupEffects:          setupEffectsForLaunch(deps),
			InitialView:           initial,
		})
		if deps.Run == nil {
			return fmt.Errorf("ui: no Run hook configured")
		}
		return deps.Run(app)
	}

	r, pass, cfg, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
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

	provider := deps.Provider
	if deps.ProviderForConfig != nil {
		provider = deps.ProviderForConfig(cfg)
	}

	app := tui.NewApp(tui.Deps{
		Repo:     r,
		Provider: provider,
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
		ConfigPath:            absCfgPath,
		NewStore:              deps.NewStore,
		Actions:               deps.Actions,
		SaveKeyringPassphrase: deps.SavePassphrase,
		SetupEffects:          setupEffectsForLaunch(deps),
	})

	if deps.Run == nil {
		return fmt.Errorf("ui: no Run hook configured")
	}
	return deps.Run(app)
}

// setupEffectsForLaunch returns the UIDeps override or the production default.
func setupEffectsForLaunch(deps UIDeps) setup.Effects {
	if deps.SetupEffects != nil {
		return deps.SetupEffects
	}
	return setup.DefaultEffects()
}

// providerForLaunch builds the agent provider for the launch-path Apps (first
// run / locked), where no repo is open yet. It mirrors the dashboard path's
// provider selection: ProviderForConfig wins when set, else the static
// Provider.
func providerForLaunch(deps UIDeps, cfg *config.Config) llm.Provider {
	if deps.ProviderForConfig != nil {
		return deps.ProviderForConfig(cfg)
	}
	return deps.Provider
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
func DefaultUIRunner(app tui.App) error {
	if !term.IsTerminal(os.Stdout.Fd()) {
		return errors.New("sentra ui requires a terminal; pipe to a subcommand like `sentra snapshots --json` for non-interactive use")
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
