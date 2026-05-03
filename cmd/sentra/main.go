package main

import (
	"context"
	"fmt"
	"os"

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

	if err := root.Execute(); err != nil {
		// cobra prints the error itself when SilenceErrors is false;
		// we just need to propagate the non-zero exit so scripts can
		// detect failure.
		os.Exit(1)
	}
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
