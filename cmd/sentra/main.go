package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/cli"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/ui"
)

// isUICommand reports whether cmd is the bare-sentra dispatch or the
// explicit `sentra ui` subcommand — both modes take over the terminal
// with Bubbletea's alt-screen, so slog needs to discard stderr writes
// during their lifetime. Detected via the cobra command's Use field
// rather than a direct pointer comparison so the check survives
// SetUIAsDefault rewiring the root's RunE without exposing internals.
func isUICommand(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	// `sentra` (no subcommand) — the parent root command falls
	// through to the TUI via SetUIAsDefault.
	if cmd.Parent() == nil {
		return true
	}
	return cmd.Use == "ui"
}

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// minPassphraseLen is the minimum passphrase length the init prompt
// enforces. 8 bytes is a permissive floor — meant to catch typos and
// accidental empty input, not to be a security policy.
const minPassphraseLen = 8

// keyringService is the service name passed to the OS keyring lookup.
// One name across the whole CLI so all commands hit the same entry.
const keyringService = "sentra"

// keyringDefaultUser is used when the loaded config has no bucket yet
// (the init path on a fresh machine, before --bucket has landed).
// A fixed string is fine for the most-common single-repo install;
// multi-repo users can disambiguate by setting different bucket
// names — that's what feeds the per-repo identity.
const keyringDefaultUser = "default"

// loadConfigBestEffort wraps config.Load with the convention used by
// startup-time helpers (newAgentProvider, the passphrase prompts):
// a missing file is fine and proceeds silently; a real parse or
// stat error is logged to stderr so the operator sees a clear
// signal rather than mysterious default-fallback behavior. Returns
// (nil, false) on error so callers can fall through to their own
// defaults without discriminating between missing-file and broken-
// file cases.
//
// `where` is a short description of the call site — it appears in
// the warning so an operator running multiple commands can tell which
// path tripped the warning.
func loadConfigBestEffort(path, where string) *config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		// Don't fail the whole CLI on a config that's only used for
		// optional features (model selection, keyring lookup), but DO
		// surface the error so the operator knows their YAML is broken.
		fmt.Fprintf(os.Stderr, "sentra: warning: %s: %v (using defaults)\n", where, err)
		return nil
	}
	return cfg
}

func main() {
	rootFlags := &cli.RootFlags{}
	root := cli.NewRootWithFlags(version, commit, date, rootFlags)

	// Wire slog.Default before subcommands run. We use a
	// PersistentPreRunE hook so flag parsing has completed by the
	// time we read the values out of rootFlags. tuiMode is true for
	// the bare `sentra` invocation and the explicit `sentra ui`
	// subcommand — both take over the terminal, so writing logs to
	// stderr would corrupt the alt-screen unless --log-file is set.
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		tuiMode := isUICommand(cmd)
		cleanup, err := cli.ConfigureSlog(rootFlags, tuiMode)
		if err != nil {
			// Fall back to stderr at warn level so the user sees
			// the failure without losing all log output.
			fmt.Fprintf(os.Stderr, "sentra: warning: log setup failed: %v (falling back to stderr)\n", err)
		}
		// Best-effort cleanup at process exit. cobra doesn't have a
		// PostRunE that fires for every subcommand path (errors can
		// short-circuit), so we register via runtime.SetFinalizer
		// avoidance by leaning on the OS to flush+close on exit. The
		// cleanup func is a no-op when --log-file is empty, so this
		// is only material when the operator opted in to a file.
		_ = cleanup
		return nil
	}

	// Wire production-mode deps for each subcommand. Tests construct
	// the same commands with stubbed deps; main is the only place
	// that touches real S3 / huh / the OS keyring.
	initDeps := cli.InitDeps{
		NewStore:   newS3Store,
		Passphrase: promptInitPassphrase(rootFlags),
		Stdout:     os.Stdout,
	}
	root.AddCommand(cli.NewInit(initDeps))

	backupDeps := cli.BackupDeps{
		NewStore:   newS3Store,
		Passphrase: promptOpenPassphrase(rootFlags),
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Confirm:    cli.HuhBackupApplyConfirm,
	}
	root.AddCommand(cli.NewBackup(backupDeps))

	snapshotsDeps := cli.SnapshotsDeps{
		NewStore:   newS3Store,
		Passphrase: promptOpenPassphrase(rootFlags),
		Stdout:     os.Stdout,
	}
	root.AddCommand(cli.NewSnapshots(snapshotsDeps))

	restoreDeps := cli.RestoreDeps{
		NewStore:   newS3Store,
		Passphrase: promptOpenPassphrase(rootFlags),
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	}
	root.AddCommand(cli.NewRestore(restoreDeps))

	diffDeps := cli.DiffDeps{
		NewStore:   newS3Store,
		Passphrase: promptOpenPassphrase(rootFlags),
		Stdout:     os.Stdout,
	}
	root.AddCommand(cli.NewDiff(diffDeps))

	pruneDeps := cli.PruneDeps{
		NewStore:   newS3Store,
		Passphrase: promptOpenPassphrase(rootFlags),
		Stdout:     os.Stdout,
		Confirm:    cli.HuhConfirm,
	}
	root.AddCommand(cli.NewPrune(pruneDeps))

	// `sentra passwd` rotates the wrapping passphrase. Old passphrase
	// uses the existing resolution chain (so --passphrase-file etc.
	// still work for the operator's CURRENT secret); the new
	// passphrase comes from --new-passphrase-file when set, or an
	// interactive confirm-on-entry prompt otherwise. SENTRA_PASSPHRASE
	// is deliberately not a source for the new passphrase per the
	// design doc — env vars persist in shell history / process
	// listings, which is the wrong default for a fresh secret.
	passwdDeps := cli.PasswdDeps{
		NewStore:      newS3Store,
		Passphrase:    promptOpenPassphrase(rootFlags),
		NewPassphrase: promptNewRepoPassphrase(),
		Stdout:        os.Stdout,
	}
	root.AddCommand(cli.NewPasswd(passwdDeps))

	agentDeps := cli.AgentDeps{
		NewStore:   newS3Store,
		Passphrase: promptOpenPassphrase(rootFlags),
		Stdout:     os.Stdout,
		Provider:   newAgentProvider(),
		Heuristics: defaultHeuristics(),
		Actions:    action.NewDefaultRegistry(),
		Confirm:    cli.HuhAgentConfirm,
	}
	root.AddCommand(cli.NewAgent(agentDeps))

	// `sentra ui` and the bare-sentra default. The deps share most
	// of their construction with the other open-passphrase commands;
	// the only ui-specific piece is the Run hook that actually starts
	// the Bubbletea program. Wiring SetUIAsDefault makes bare
	// `sentra` fall through to the TUI per the design doc.
	uiDeps := cli.UIDeps{
		NewStore:   newS3Store,
		Passphrase: promptOpenPassphrase(rootFlags),
		Stdout:     os.Stdout,
		Provider:   newAgentProvider(),
		Run:        cli.DefaultUIRunner,
	}
	root.AddCommand(cli.NewUI(uiDeps))
	cli.SetUIAsDefault(root, uiDeps)

	if err := root.Execute(); err != nil {
		// cobra prints the error itself when SilenceErrors is false;
		// we just need to propagate the non-zero exit so scripts can
		// detect failure.
		os.Exit(1)
	}
}

// defaultHeuristics returns the production set of heuristics the
// agent runs over the repo. The set mirrors Phase 9's enumeration:
// secrets, large files, cache dirs, stale paths, dup paths, orphan
// blobs, retention drift. Heuristics whose Input contracts the
// orchestrator doesn't currently populate (walker results, snapshot
// list, etc.) are still included — they're no-ops on missing input
// rather than errors, and Phase 12's TUI wiring will populate the
// fuller Input.
func defaultHeuristics() []heuristics.Heuristic {
	return []heuristics.Heuristic{
		heuristics.NewSecrets(),
		heuristics.NewLargeFiles(),
		heuristics.NewCacheDirs(),
		heuristics.NewStalePaths(),
		heuristics.NewDupPaths(),
		heuristics.NewOrphanBlobs(),
		heuristics.NewRetentionDrift(),
	}
}

// newAgentProvider returns the production LLM provider. Reads the
// config's agent.model field at call-time (each invocation gets the
// current sentra.yaml). If ANTHROPIC_API_KEY is missing or model is
// blank, NewAnthropic surfaces a clear error which `sentra agent
// scan` prints — better than a deferred 401 on first request.
func newAgentProvider() llm.Provider {
	cfg := loadConfigBestEffort("sentra.yaml", "agent provider config")
	model := "claude-sonnet-4-6"
	if cfg != nil && cfg.Agent.Model != "" {
		model = cfg.Agent.Model
	}
	p, err := llm.NewAnthropic(llm.AnthropicConfig{Model: model})
	if err != nil {
		// Defer the error to scan time so the user sees a clear
		// "missing key" message instead of `sentra` failing to start
		// for a feature they haven't used yet. The lazy provider
		// returns the same error from every Generate call.
		return &lazyErrProvider{err: err}
	}
	return p
}

// lazyErrProvider is a Provider that fails every call with the same
// error. Used at startup when llm.NewAnthropic fails (typically a
// missing API key) so unrelated commands still work — only `agent
// scan` surfaces the error, and only when actually invoked.
type lazyErrProvider struct{ err error }

func (p *lazyErrProvider) Generate(ctx context.Context, sys string, msgs []llm.Message, tools []llm.Tool, stream chan<- string) ([]llm.ToolCall, string, error) {
	return nil, "", p.err
}

// newS3Store is the production blobstore factory. Reads the merged
// config, constructs a real S3 client, and wraps it in a RetryStore
// so transient S3 errors (5xx, throttling, request-timeout) don't
// abort a long-running backup or restore. The AWS SDK already retries
// 3 times internally on transient failures; the RetryStore wraps that
// in a coarser outer loop that handles sustained throttling and any
// errors the SDK's per-request retry didn't catch.
func newS3Store(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
	if cfg.Repo.S3.Bucket == "" {
		return nil, fmt.Errorf("repo.s3.bucket not set in sentra.yaml — edit the file and re-run")
	}
	s3, err := blobstore.NewS3(ctx, blobstore.S3Config{
		Bucket:      cfg.Repo.S3.Bucket,
		Prefix:      cfg.Repo.S3.Prefix,
		Region:      cfg.Repo.S3.Region,
		Profile:     cfg.Repo.S3.Profile,
		EndpointURL: cfg.Repo.S3.EndpointURL,
	})
	if err != nil {
		return nil, err
	}
	return blobstore.NewRetryStore(s3, blobstore.DefaultRetryPolicy()), nil
}

// promptNewRepoPassphrase returns the new-passphrase callback for
// `sentra passwd`. The signature takes a passphraseFile argument so
// the cobra command can pass the --new-passphrase-file flag value
// through at run time without sharing state at construction time.
//
// Resolution is deliberately narrower than the old-passphrase chain:
//
//   - When passphraseFile is non-empty, read from that file.
//     Honors the same 0600 enforcement as the existing chain on
//     Unix (see internal/config/passphrase.go).
//   - Otherwise prompt interactively with confirm-on-entry. Same
//     UI helper init uses, with the same minPassphraseLen floor.
//
// Note: SENTRA_PASSPHRASE is NOT a source for the new passphrase.
// Env vars persist in shell history and process listings; sourcing
// the freshly-rotated secret from there by default would be the
// wrong UX. Operators who want non-interactive rotation use the
// --new-passphrase-file flag.
func promptNewRepoPassphrase() func(passphraseFile string) ([]byte, error) {
	return func(passphraseFile string) ([]byte, error) {
		if passphraseFile != "" {
			// Reuse config.Resolve with ONLY the file source set.
			// Resolve short-circuits to readPassphraseFile when
			// PassphraseFile is non-empty, so env / keyring / prompt
			// branches are never consulted.
			return config.Resolve(config.ResolveOptions{PassphraseFile: passphraseFile})
		}
		return ui.PromptPassphraseWithConfirm("Set new repository passphrase", minPassphraseLen)
	}
}

// promptInitPassphrase returns the passphrase callback for `sentra init`.
// Routes through config.Resolve so --passphrase-file and SENTRA_PASSPHRASE
// short-circuit the interactive prompt; falls through to the
// confirm-on-entry huh flow when nothing else is configured. Init
// runs once per repo, so the small extra friction of a confirm prompt
// when interactive is the right call.
func promptInitPassphrase(rootFlags *cli.RootFlags) func() ([]byte, error) {
	return func() ([]byte, error) {
		// On `init` we don't yet have a loaded config (the bucket may
		// be coming in via flag), so the keyring user defaults to
		// "default". A future enhancement could load any partial
		// sentra.yaml here to pick up the bucket if present.
		cfg := loadConfigBestEffort("sentra.yaml", "init passphrase prompt")
		opts := config.ResolveOptions{
			PassphraseFile: rootFlags.PassphraseFile,
			Prompt: func() ([]byte, error) {
				return ui.PromptPassphraseWithConfirm("Set repository passphrase", minPassphraseLen)
			},
		}
		if cfg != nil {
			opts.UseKeyring = cfg.Passphrase.UseKeyring
			opts.KeyringService = keyringService
			opts.KeyringUser = cfg.Repo.S3.Bucket
		}
		if opts.KeyringUser == "" {
			opts.KeyringUser = keyringDefaultUser
		}
		return config.Resolve(opts)
	}
}

// promptOpenPassphrase returns the passphrase callback used by every
// post-init command (backup, snapshots, restore, diff). It does NOT
// re-prompt for confirmation — that's only useful when *setting* a
// passphrase. A typo just means the repo won't open.
//
// Routes through config.Resolve so the documented priority chain
// (--passphrase-file → SENTRA_PASSPHRASE → keyring → prompt) is
// honored uniformly across commands.
func promptOpenPassphrase(rootFlags *cli.RootFlags) func() ([]byte, error) {
	return func() ([]byte, error) {
		// Best-effort load: if sentra.yaml is missing, Resolve still
		// works (file/env/prompt cover it). A real parse error is
		// logged via loadConfigBestEffort so the operator sees a
		// signal — the subcommand's own config.Load will then surface
		// the same error as a hard failure when the command actually
		// needs the config.
		cfg := loadConfigBestEffort("sentra.yaml", "open passphrase prompt")
		opts := config.ResolveOptions{
			PassphraseFile: rootFlags.PassphraseFile,
			Prompt: func() ([]byte, error) {
				return ui.PromptPassphrase("Repository passphrase", 0)
			},
		}
		if cfg != nil {
			opts.UseKeyring = cfg.Passphrase.UseKeyring
			opts.KeyringService = keyringService
			opts.KeyringUser = cfg.Repo.S3.Bucket
		}
		if opts.KeyringUser == "" {
			opts.KeyringUser = keyringDefaultUser
		}
		return config.Resolve(opts)
	}
}
