package main

import (
	"context"
	"fmt"
	"os"

	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/cli"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/ui"
)

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

func main() {
	rootFlags := &cli.RootFlags{}
	root := cli.NewRootWithFlags(version, commit, date, rootFlags)

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

	agentDeps := cli.AgentDeps{
		NewStore:   newS3Store,
		Passphrase: promptOpenPassphrase(rootFlags),
		Stdout:     os.Stdout,
		Provider:   newAgentProvider(),
		Heuristics: defaultHeuristics(),
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
	cfg, _ := config.Load("sentra.yaml")
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
// config and constructs a real S3 client.
func newS3Store(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
	if cfg.Repo.S3.Bucket == "" {
		return nil, fmt.Errorf("repo.s3.bucket not set in sentra.yaml — edit the file and re-run")
	}
	return blobstore.NewS3(ctx, blobstore.S3Config{
		Bucket:      cfg.Repo.S3.Bucket,
		Prefix:      cfg.Repo.S3.Prefix,
		Region:      cfg.Repo.S3.Region,
		Profile:     cfg.Repo.S3.Profile,
		EndpointURL: cfg.Repo.S3.EndpointURL,
	})
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
		cfg, _ := config.Load("sentra.yaml")
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
		// works (file/env/prompt cover it). Any *real* config error
		// would surface in the subcommand's own config.Load anyway.
		cfg, _ := config.Load("sentra.yaml")
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
