package cli

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// BackupDeps wires the side-effecting pieces of `sentra backup`.
// Production fills these with real implementations from main.go;
// tests inject a memory store and static passphrase.
type BackupDeps struct {
	NewStore   func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	Passphrase func() ([]byte, error)
	Stdout     io.Writer
	Stderr     io.Writer
}

// progressTickInterval is how often the inline progress UI repaints
// the bar to stderr. 250ms is the right balance for a CLI: fast
// enough that "still alive" feels real, slow enough to avoid
// flooding the terminal during fast small-file workloads.
const progressTickInterval = 250 * time.Millisecond

// NewBackup returns the cobra command for `sentra backup <path>`.
// Flags:
//   - --tag string  human-readable label persisted on the snapshot
//   - --config path overrides the default sentra.yaml location
//
// The command flow is:
//  1. Load sentra.yaml (env overlays applied)
//  2. Resolve passphrase (deps.Passphrase callback)
//  3. Open the repo via deps.NewStore + repo.Open
//  4. CreateSnapshot with a ui.ByteProgress reporter, repainted to
//     stderr every progressTickInterval until the call returns
//  5. Print the final summary (snapshot ID, files, bytes, new bytes)
func NewBackup(deps BackupDeps) *cobra.Command {
	var (
		tag     string
		cfgPath string
	)
	cmd := &cobra.Command{
		Use:   "backup <path>",
		Short: "Snapshot a directory into the configured repository",
		Long: "Walk the given path, chunk and encrypt new content, and write a " +
			"sealed manifest. Re-runs share unchanged chunks via content-addressed " +
			"deduplication, so the second backup of an unchanged tree uploads almost nothing.",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackup(cmd, deps, args[0], tag, cfgPath)
		},
	}
	cmd.Flags().StringVar(&tag, "tag", "", "optional human-readable tag for the snapshot")
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	return cmd
}

// runBackup is the body of `sentra backup`, factored out so it's
// independently testable and easy to grep.
func runBackup(cmd *cobra.Command, deps BackupDeps, path, tag, cfgPath string) error {
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
	defer zeroize(pass)

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

	// Wire a ByteProgress + a goroutine that repaints to stderr on
	// a fixed cadence. The reporter is updated synchronously by
	// repo.CreateSnapshot; the goroutine just renders the latest
	// state. Stop+drain on completion so the final newline lands
	// after the bar's last frame, not in the middle of it.
	progress := ui.NewByteProgress(0)
	stop := startProgressPainter(stderr, progress)

	snap, snapErr := r.CreateSnapshot(cmd.Context(), path, repo.SnapshotOptions{
		Tag:      tag,
		Progress: progress,
	})
	stop()
	if snapErr != nil {
		return fmt.Errorf("snapshot: %w", snapErr)
	}

	// Final summary on stdout — parseable, no animation chars.
	fmt.Fprintln(stdout, ui.Success.Render("Snapshot created"))
	fmt.Fprintf(stdout, "  id:        %s\n", snap.ID)
	fmt.Fprintf(stdout, "  tag:       %s\n", emptyDash(snap.Tag))
	fmt.Fprintf(stdout, "  files:     %d\n", snap.Stats.Files)
	fmt.Fprintf(stdout, "  bytes:     %s (%d)\n", ui.FormatBytes(snap.Stats.Bytes), snap.Stats.Bytes)
	fmt.Fprintf(stdout, "  uploaded:  %s (%d new)\n", ui.FormatBytes(snap.Stats.NewBytes), snap.Stats.NewBytes)
	return nil
}

// emptyDash renders an empty string as "-" for tabular display.
func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// startProgressPainter spins up a goroutine that periodically writes
// the rendered progress bar to w. Returns a stop function that
// signals the painter to exit, paints one final frame followed by a
// newline, and waits for the goroutine to finish.
//
// We paint to a single stderr line using \r so the bar overwrites
// itself in place. The terminal must support carriage returns —
// every supported sentra environment (xterm, mac Terminal, iTerm2,
// Windows Terminal) does.
func startProgressPainter(w io.Writer, p *ui.ByteProgress) func() {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(progressTickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				// \r returns the cursor to the start of the line; the
				// next frame overwrites the previous one. We don't
				// clear-to-EOL because the rendered string includes
				// the entire line.
				fmt.Fprintf(w, "\r%s", p.Render())
			}
		}
	}()
	return func() {
		close(stop)
		wg.Wait()
		// One final frame so completed runs end at 100% rather than
		// at whatever the last tick caught. The trailing newline
		// terminates the in-place rewrite cleanly.
		fmt.Fprintf(w, "\r%s\n", p.Render())
	}
}
