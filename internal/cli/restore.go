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
	"github.com/markgustetic/sentra/internal/ui"
)

// RestoreDeps wires the side-effecting pieces of `sentra restore`.
type RestoreDeps struct {
	NewStore   func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	Passphrase func() ([]byte, error)
	Stdout     io.Writer
	Stderr     io.Writer
}

// NewRestore returns the cobra command for `sentra restore <snap-id> <dest-dir>`.
// The dest-dir must either not exist (then it's created) or be empty;
// a non-empty dest is rejected by repo.Restore to prevent silent
// merging on top of stale content.
//
// Flags:
//   - --config  override the default sentra.yaml location
//
// Progress: a ui.ByteProgress reporter is wired into RestoreOptions
// and a goroutine repaints the bar to stderr on the same cadence as
// the backup command.
func NewRestore(deps RestoreDeps) *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "restore <snap-id> <dest-dir>",
		Short: "Restore a snapshot into a destination directory",
		Long: "Decrypt and reassemble every file in the named snapshot, " +
			"writing them under dest-dir. The destination must be empty (or " +
			"not yet exist) — restore refuses to merge over a populated tree.",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestore(cmd, deps, args[0], args[1], cfgPath)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	return cmd
}

// runRestore is the body of `sentra restore`.
func runRestore(cmd *cobra.Command, deps RestoreDeps, snapID, destDir, cfgPath string) error {
	cmd.SilenceUsage = true

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	store, err := deps.NewStore(cmd.Context(), cfg)
	if err != nil {
		return fmt.Errorf("open blobstore: %w", err)
	}
	pass, err := deps.Passphrase()
	if err != nil {
		return fmt.Errorf("resolve passphrase: %w", err)
	}
	defer crypto.Zeroize(pass)

	r, err := repo.Open(cmd.Context(), store, pass)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}
	defer r.Close()

	stderr := deps.Stderr
	if stderr == nil {
		stderr = cmd.ErrOrStderr()
	}
	stdout := deps.Stdout
	if stdout == nil {
		stdout = cmd.OutOrStdout()
	}

	progress := ui.NewByteProgress(0)
	stop := startProgressPainter(stderr, progress)

	// Manifest peek so we can show file/byte counts in the summary.
	// Cheap (one extra Get + decompress) and folds neatly into the
	// existing Restore call thanks to LoadSnapshot's cache-friendly
	// behavior on the in-memory store. For S3 it's one extra round
	// trip on a few-KB blob — fine.
	m, err := r.LoadSnapshot(cmd.Context(), snapID)
	if err != nil {
		stop()
		return fmt.Errorf("load snapshot: %w", err)
	}

	if err := r.Restore(cmd.Context(), snapID, destDir, repo.RestoreOptions{Progress: progress}); err != nil {
		stop()
		return fmt.Errorf("restore: %w", err)
	}
	stop()

	fmt.Fprintln(stdout, ui.Success.Render("Restore complete"))
	fmt.Fprintf(stdout, "  snapshot:  %s\n", snapID)
	fmt.Fprintf(stdout, "  dest:      %s\n", destDir)
	fmt.Fprintf(stdout, "  files:     %d\n", m.Stats.Files)
	fmt.Fprintf(stdout, "  bytes:     %s (%d)\n", ui.FormatBytes(m.Stats.Bytes), m.Stats.Bytes)
	return nil
}
