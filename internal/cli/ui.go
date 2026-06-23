package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/tui"
)

// UIDeps wires the side-effecting pieces of `sentra ui` so tests can
// drop in a memory store and a stub Run callback. Run isolates the
// "actually launch a Bubbletea program" step behind a function so
// unit tests don't need a real terminal.
type UIDeps struct {
	// NewStore opens the blobstore. Same pattern as every other
	// command's deps.
	NewStore func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)

	// Passphrase resolves the repo passphrase. May be skipped when
	// the user has SENTRA_PASSPHRASE set or a keyring entry; under
	// the hood that's all hidden by the existing resolver chain.
	Passphrase func() ([]byte, error)

	// PassphraseWithConfig is the config-aware production resolver.
	// When set, it takes precedence over Passphrase.
	PassphraseWithConfig func(cfg *config.Config) ([]byte, error)

	// Provider is the LLM provider for the agent view. May be nil
	// when no API key is configured — the agent view shows a
	// placeholder pointing at ANTHROPIC_API_KEY in that case.
	Provider llm.Provider

	// ProviderForConfig builds the LLM provider from the loaded
	// command config. When set, it takes precedence over Provider.
	ProviderForConfig func(cfg *config.Config) llm.Provider

	// Stdout receives any pre-launch messages (e.g. "press q to
	// quit"). The TUI itself writes directly to the terminal via
	// tea.Program; this is purely for the wrapper's own output.
	Stdout io.Writer

	// Run is the actual TUI launcher. Production wires it to a
	// closure that constructs and runs a tea.Program; tests inject
	// a stub that captures the constructed App and returns nil.
	Run func(app tui.App) error
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

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	store, err := deps.NewStore(cmd.Context(), cfg)
	if err != nil {
		return fmt.Errorf("open blobstore: %w", err)
	}
	pass, err := resolvePassphrase(deps.Passphrase, deps.PassphraseWithConfig, cfg)
	if err != nil {
		return fmt.Errorf("resolve passphrase: %w", err)
	}
	defer crypto.Zeroize(pass)

	r, err := repo.Open(cmd.Context(), store, pass)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}
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
		// Pass the cobra command's context so:
		//   1. Signals (Ctrl+C wired by cobra) cancel TUI work.
		//   2. The TUI's App.cleanup() can cancel the same context
		//      tree on a 'q' quit, terminating in-flight blobstore
		//      calls instead of letting them drain to per-call timeouts.
		Ctx: cmd.Context(),
	})

	if deps.Run == nil {
		return fmt.Errorf("ui: no Run hook configured")
	}
	return deps.Run(app)
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
