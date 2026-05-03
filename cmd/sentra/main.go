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

func main() {
	root := cli.NewRoot(version, commit, date)

	// Wire production-mode deps for each subcommand. Tests construct
	// the same commands with stubbed deps; main is the only place
	// that touches real S3 / huh / the OS keyring.
	initDeps := cli.InitDeps{
		NewStore:   newS3Store,
		Passphrase: promptInitPassphrase,
		Stdout:     os.Stdout,
	}
	root.AddCommand(cli.NewInit(initDeps))

	backupDeps := cli.BackupDeps{
		NewStore:   newS3Store,
		Passphrase: promptOpenPassphrase,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	}
	root.AddCommand(cli.NewBackup(backupDeps))

	snapshotsDeps := cli.SnapshotsDeps{
		NewStore:   newS3Store,
		Passphrase: promptOpenPassphrase,
		Stdout:     os.Stdout,
	}
	root.AddCommand(cli.NewSnapshots(snapshotsDeps))

	restoreDeps := cli.RestoreDeps{
		NewStore:   newS3Store,
		Passphrase: promptOpenPassphrase,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	}
	root.AddCommand(cli.NewRestore(restoreDeps))

	if err := root.Execute(); err != nil {
		// cobra prints the error itself when SilenceErrors is false;
		// we just need to propagate the non-zero exit so scripts can
		// detect failure.
		os.Exit(1)
	}
}

// newS3Store is the production blobstore factory. Reads the merged
// config and constructs a real S3 client. The init command passes
// Defaults() (an empty bucket) — that's a deliberate "must edit
// sentra.yaml first" signal until the user puts real values in.
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

// promptInitPassphrase wires init's passphrase callback to the huh
// confirm-on-entry flow. Init runs once per repo, so the small extra
// friction of a confirm prompt is the right call.
func promptInitPassphrase() ([]byte, error) {
	return ui.PromptPassphraseWithConfirm("Set repository passphrase", minPassphraseLen)
}

// promptOpenPassphrase is the passphrase callback used by every
// post-init command (backup, snapshots, restore, diff). It does NOT
// re-prompt for confirmation — that's only useful when *setting* a
// passphrase. A typo just means the repo won't open.
func promptOpenPassphrase() ([]byte, error) {
	return ui.PromptPassphrase("Repository passphrase", 0)
}
