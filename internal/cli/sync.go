package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
)

// SyncDeps wires the side-effecting pieces of `sentra sync` so
// tests can inject in-memory stores and a deterministic passphrase
// callback. Production wires:
//
//   - NewStore: the standard S3-store factory (RetryStore-wrapped).
//     Called twice — once with the source's *config.Config, once
//     with the destination's. Same factory both times.
//   - Passphrase: the existing chain (--passphrase-file → env →
//     keyring → prompt). Called EXACTLY ONCE per sync run; the
//     resolved bytes open BOTH endpoints (clone semantic).
//   - Stdout: where the success summary lands.
//
// The deps shape mirrors PasswdDeps + a single store factory used
// for both ends.
type SyncDeps struct {
	NewStore             func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	Passphrase           func() ([]byte, error)
	PassphraseWithConfig func(cfg *config.Config) ([]byte, error)
	Stdout               io.Writer
}

// syncFlags holds the values of `sentra sync`'s flags. Bundled
// into a struct so the cobra wiring is compact and the runE body
// can be tested independently.
type syncFlags struct {
	dstConfig   string
	initDest    bool
	concurrency int
	dryRun      bool
}

// NewSync returns the cobra command for `sentra sync`. The
// command flow:
//
//  1. Validate --dst-config is set (refuse with a clear error).
//  2. Load the source's sentra.yaml from cwd via config.Load.
//  3. Load the destination's sentra.yaml from --dst-config.
//  4. Refuse if source and destination resolve to the same bucket
//     + prefix (a no-op at best, deadlock at worst).
//  5. Resolve the (single) passphrase via deps.Passphrase.
//  6. Open the source repo (verifies passphrase + config MAC).
//  7. Open the destination store (no Open() — the dest may be
//     empty in --init-dest mode and we'd fail before sync got
//     to bootstrap it).
//  8. Call repo.Repo.SyncTo with the appropriate options.
//  9. Print the stats summary.
//
// Refusal cases short-circuit before any S3 write.
func NewSync(deps SyncDeps) *cobra.Command {
	flags := &syncFlags{}
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Replicate this repository to a clone destination",
		Long: "Copy every snapshot, chunk, and config from the cwd's repository " +
			"to the destination repository specified by --dst-config. The " +
			"destination becomes a working clone with the same passphrase. " +
			"Subsequent syncs are incremental — only new blobs are transferred. " +
			"Holds the same advisory lock as backup and GC on the destination.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSync(cmd, deps, flags)
		},
	}
	cmd.Flags().StringVar(&flags.dstConfig, "dst-config", "",
		"path to the destination's sentra.yaml (required)")
	cmd.Flags().BoolVar(&flags.initDest, "init-dest", false,
		"bootstrap an empty destination by copying source's config")
	cmd.Flags().IntVar(&flags.concurrency, "concurrency", 0,
		"parallel transfers per phase (0 = GOMAXPROCS)")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false,
		"list what would be copied without writing to the destination")
	return cmd
}

// runSync is the body of `sentra sync`. Pulled out of the closure
// so it's grep-able and unit-testable independently of cobra.
func runSync(cmd *cobra.Command, deps SyncDeps, flags *syncFlags) error {
	cmd.SilenceUsage = true

	out := cmdStdout(cmd, deps.Stdout)

	if flags.dstConfig == "" {
		return fmt.Errorf("sentra sync: --dst-config is required")
	}

	// 1. Load source's sentra.yaml from cwd (same as every other
	// command).
	srcCfg, err := config.Load(configFileName)
	if err != nil {
		return fmt.Errorf("load %s: %w", configFileName, err)
	}

	// 2. Load destination's sentra.yaml from --dst-config.
	dstCfg, err := config.Load(flags.dstConfig)
	if err != nil {
		return fmt.Errorf("load %s: %w", flags.dstConfig, err)
	}

	// 3. Refuse same-source-and-destination configurations.
	if sameS3Location(srcCfg, dstCfg) {
		return fmt.Errorf("sentra sync: source and destination resolve to the same S3 location (bucket=%q prefix=%q)",
			srcCfg.Repo.S3.Bucket, srcCfg.Repo.S3.Prefix)
	}

	// 4. Open the source store.
	srcStore, err := deps.NewStore(cmd.Context(), srcCfg)
	if err != nil {
		return fmt.Errorf("open source blobstore: %w", err)
	}

	// 5. Resolve passphrase via the existing chain. ONE call total.
	passphrase, err := resolvePassphrase(deps.Passphrase, deps.PassphraseWithConfig, srcCfg)
	if err != nil {
		return fmt.Errorf("resolve passphrase: %w", err)
	}
	defer crypto.Zeroize(passphrase)

	// 6. Open the source repo. Failure here MUST short-circuit
	// before we touch the destination — operators with a wrong
	// passphrase shouldn't have their dest connection probed.
	src, err := repo.Open(cmd.Context(), srcStore, passphrase)
	if err != nil {
		return fmt.Errorf("open source repo: %w", err)
	}
	defer src.Close()

	// 7. Open the destination store (raw — no repo.Open, since dest
	// may be empty under --init-dest).
	dstStore, err := deps.NewStore(cmd.Context(), dstCfg)
	if err != nil {
		return fmt.Errorf("open destination blobstore: %w", err)
	}

	// 8. SyncTo.
	stats, err := src.SyncTo(cmd.Context(), dstStore, repo.SyncOptions{
		InitDest:    flags.initDest,
		DryRun:      flags.dryRun,
		Concurrency: flags.concurrency,
	})
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	// 9. Print summary.
	if flags.dryRun {
		fmt.Fprintln(out, "Dry-run plan (no writes performed):")
	} else {
		fmt.Fprintln(out, "Sync complete.")
	}
	if stats.Bootstrapped {
		fmt.Fprintln(out, "  bootstrap:  yes (destination's config was empty)")
	}
	fmt.Fprintf(out, "  copied:     %d blobs (%d bytes)\n", stats.CopiedBlobs, stats.CopiedBytes)
	fmt.Fprintf(out, "  skipped:    %d (already on destination)\n", stats.SkippedBlobs)
	fmt.Fprintf(out, "  elapsed:    %s\n", stats.Elapsed)
	return nil
}

// sameS3Location reports whether two configs resolve to the same
// bucket + prefix. We intentionally compare bucket+prefix rather
// than including region — a bucket name is globally unique within
// its provider, so a same-bucket-different-region configuration
// would still write to the same data.
//
// Edge cases:
//   - Empty buckets on both sides: probably a misconfiguration, but
//     we don't refuse on this path; the NewStore factory will
//     surface a "bucket not set" error.
//   - One config has prefix "" and the other has "/". We
//     intentionally don't normalize — operators with intentional
//     prefix differences should have those prefixes match the
//     factory's expectations exactly.
func sameS3Location(a, b *config.Config) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Repo.S3.Bucket == b.Repo.S3.Bucket &&
		a.Repo.S3.Prefix == b.Repo.S3.Prefix &&
		a.Repo.S3.Bucket != "" // empty buckets aren't "the same location"
}
