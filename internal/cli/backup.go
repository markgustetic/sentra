package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// BackupDeps wires the side-effecting pieces of `sentra backup`.
// Production fills these with real implementations from main.go;
// tests inject a memory store and static passphrase.
type BackupDeps struct {
	RepoDeps
	Stderr  io.Writer
	Confirm func(prompt string) (bool, error)
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
		rescan  bool
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "backup <path>",
		Short: "Snapshot a directory into the configured repository",
		Long: "Walk the given path, chunk and encrypt new content, and write a " +
			"sealed manifest. Files whose size and mtime match the previous snapshot " +
			"reuse its chunks without being read; re-runs of a quiet tree read and " +
			"upload almost nothing. --rescan forces every file to be re-read.",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackup(cmd, deps, args[0], tag, cfgPath, rescan, asJSON)
		},
	}
	cmd.Flags().StringVar(&tag, "tag", "", "optional human-readable tag for the snapshot")
	cmd.Flags().BoolVar(&rescan, "rescan", false,
		"read and re-chunk every file even when size+mtime match the previous snapshot")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"emit the snapshot summary as JSON instead of the styled text")
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
	cmd.AddCommand(newBackupPlan(deps))
	cmd.AddCommand(newBackupApply(deps))
	return cmd
}

func newBackupPlan(deps BackupDeps) *cobra.Command {
	var (
		tag     string
		cfgPath string
		outPath string
	)
	cmd := &cobra.Command{
		Use:   "plan <path>",
		Short: "Write a reviewable backup plan file",
		Long: "Walk the given path with the configured backup filters and write " +
			"a JSON plan containing the exact file set and metadata to review before apply.",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackupPlan(cmd, deps, args[0], tag, cfgPath, outPath)
		},
	}
	cmd.Flags().StringVar(&tag, "tag", "", "optional human-readable tag persisted on apply")
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
	cmd.Flags().StringVar(&outPath, "out", "sentra-backup-plan.json",
		"path to write the reviewable JSON plan")
	return cmd
}

func newBackupApply(deps BackupDeps) *cobra.Command {
	var (
		cfgPath string
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "apply <plan-file>",
		Short: "Create a snapshot from a reviewed backup plan",
		Long: "Read a JSON plan from `sentra backup plan`, validate the current " +
			"tree still matches it, then chunk/encrypt/upload the reviewed file set.",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackupApply(cmd, deps, args[0], cfgPath, yes)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
	cmd.Flags().BoolVar(&yes, "yes", false,
		"skip the interactive confirmation prompt")
	return cmd
}

// runBackup is the body of `sentra backup`, factored out so it's
// independently testable and easy to grep.
func runBackup(cmd *cobra.Command, deps BackupDeps, path, tag, cfgPath string, rescan, asJSON bool) error {
	cmd.SilenceUsage = true
	cfgPath, err := resolveConfigPath(cmd, cfgPath)
	if err != nil {
		return err
	}

	r, pass, cfg, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	stderr := cmdStderr(cmd, deps.Stderr)
	stdout := cmdStdout(cmd, deps.Stdout)

	// Wire a ByteProgress + a goroutine that repaints to stderr on
	// a fixed cadence. The reporter is updated synchronously by
	// repo.CreateSnapshot; the goroutine just renders the latest
	// state. Stop+drain on completion so the final newline lands
	// after the bar's last frame, not in the middle of it.
	progress := ui.NewByteProgress(0)
	painter := startProgressPainter(stderr, progress)

	walkerOpts := policycfg.BackupWalkerOptions(cfg)

	// Skipped folders are told as they happen, on stderr beside the
	// bar — under --json too, since stdout must stay one JSON
	// document — and counted again in the summary.
	snap, snapErr := r.CreateSnapshot(cmd.Context(), path, repo.SnapshotOptions{
		Tag:         tag,
		Progress:    progress,
		Walker:      walkerOpts,
		ForceRescan: rescan,
		OnSkip:      func(p string, err error) { painter.note(skipLine(p, err)) },
	})
	painter.stop()
	if snapErr != nil {
		return fmt.Errorf("snapshot: %w", snapErr)
	}

	// Final summary on stdout — parseable, no animation chars.
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(snapshotJSONRow{
			ID:        snap.ID,
			CreatedAt: snap.CreatedAt,
			Tag:       snap.Tag,
			Files:     snap.Stats.Files,
			Bytes:     snap.Stats.Bytes,
			NewBytes:  snap.Stats.NewBytes,
			Skipped:   snap.Stats.Skipped,
		}); err != nil {
			return fmt.Errorf("encode json: %w", err)
		}
		return nil
	}
	fmt.Fprintln(stdout, ui.Success.Render("Snapshot created"))
	fmt.Fprintf(stdout, "  id:        %s\n", snap.ID)
	fmt.Fprintf(stdout, "  tag:       %s\n", emptyDash(snap.Tag))
	fmt.Fprintf(stdout, "  files:     %d\n", snap.Stats.Files)
	fmt.Fprintf(stdout, "  bytes:     %s (%d)\n", ui.FormatBytes(snap.Stats.Bytes), snap.Stats.Bytes)
	fmt.Fprintf(stdout, "  uploaded:  %s (%d new)\n", ui.FormatBytes(snap.Stats.NewBytes), snap.Stats.NewBytes)
	writeSkippedLine(stdout, snap.Stats.Skipped)
	return nil
}

// writeSkippedLine appends the summary's skip count. It is a warning,
// so it appears only when there is one: a "skipped: 0" on every clean
// run would train the eye to ignore the line that matters.
func writeSkippedLine(w io.Writer, skipped int) {
	if skipped > 0 {
		fmt.Fprintf(w, "  skipped:   %d\n", skipped)
	}
}

func runBackupPlan(cmd *cobra.Command, deps BackupDeps, path, tag, cfgPath, outPath string) error {
	cmd.SilenceUsage = true
	cfgPath, err := resolveConfigPath(cmd, cfgPath)
	if err != nil {
		return err
	}
	if outPath == "" {
		return errors.New("backup plan: --out must not be empty")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	walkerOpts := policycfg.BackupWalkerOptions(cfg)

	// No progress bar here, so skip lines go straight to stderr; the
	// reviewer must learn a folder is missing from the plan before
	// approving it.
	stderr := cmdStderr(cmd, deps.Stderr)
	plan, err := repo.PlanSnapshot(cmd.Context(), path, repo.SnapshotOptions{
		Tag:    tag,
		Walker: walkerOpts,
		OnSkip: func(p string, err error) { fmt.Fprintln(stderr, skipLine(p, err)) },
	})
	if err != nil {
		return fmt.Errorf("plan backup: %w", err)
	}
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal backup plan: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(outPath, raw, 0o600); err != nil {
		return fmt.Errorf("write backup plan: %w", err)
	}

	stdout := cmdStdout(cmd, deps.Stdout)
	fmt.Fprintln(stdout, ui.Success.Render("Plan written"))
	fmt.Fprintf(stdout, "  file:   %s\n", outPath)
	fmt.Fprintf(stdout, "  root:   %s\n", plan.Root)
	fmt.Fprintf(stdout, "  tag:    %s\n", emptyDash(plan.Tag))
	fmt.Fprintf(stdout, "  files:  %d\n", plan.Stats.Files)
	fmt.Fprintf(stdout, "  bytes:  %s (%d)\n", ui.FormatBytes(plan.Stats.Bytes), plan.Stats.Bytes)
	return nil
}

func runBackupApply(cmd *cobra.Command, deps BackupDeps, planPath, cfgPath string, yes bool) error {
	cmd.SilenceUsage = true
	cfgPath, err := resolveConfigPath(cmd, cfgPath)
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(planPath) //nolint:gosec // user-provided plan path is the command argument.
	if err != nil {
		return fmt.Errorf("read backup plan: %w", err)
	}
	var plan repo.BackupPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return fmt.Errorf("parse backup plan: %w", err)
	}

	r, pass, _, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	stdout := cmdStdout(cmd, deps.Stdout)
	stderr := cmdStderr(cmd, deps.Stderr)

	if !yes {
		if deps.Confirm == nil {
			return errors.New("backup apply: confirmation callback is not configured; pass --yes for non-interactive apply")
		}
		ok, err := deps.Confirm(fmt.Sprintf("Create snapshot from plan %q with %d files?", planPath, plan.Stats.Files))
		if err != nil {
			return fmt.Errorf("confirm: %w", err)
		}
		if !ok {
			fmt.Fprintln(stdout, ui.Subtle.Render("Aborted by user."))
			return nil
		}
	}

	progress := ui.NewByteProgress(0)
	painter := startProgressPainter(stderr, progress)
	snap, snapErr := r.CreateSnapshotFromPlan(cmd.Context(), plan, repo.SnapshotOptions{
		Progress: progress,
		OnSkip:   func(p string, err error) { painter.note(skipLine(p, err)) },
	})
	painter.stop()
	if snapErr != nil {
		return fmt.Errorf("apply backup plan: %w", snapErr)
	}

	fmt.Fprintln(stdout, ui.Success.Render("Snapshot created from plan"))
	fmt.Fprintf(stdout, "  id:        %s\n", snap.ID)
	fmt.Fprintf(stdout, "  plan:      %s\n", planPath)
	fmt.Fprintf(stdout, "  root:      %s\n", plan.Root)
	fmt.Fprintf(stdout, "  tag:       %s\n", emptyDash(snap.Tag))
	fmt.Fprintf(stdout, "  files:     %d\n", snap.Stats.Files)
	fmt.Fprintf(stdout, "  bytes:     %s (%d)\n", ui.FormatBytes(snap.Stats.Bytes), snap.Stats.Bytes)
	fmt.Fprintf(stdout, "  uploaded:  %s (%d new)\n", ui.FormatBytes(snap.Stats.NewBytes), snap.Stats.NewBytes)
	writeSkippedLine(stdout, snap.Stats.Skipped)
	return nil
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
func startProgressPainter(w io.Writer, p *ui.ByteProgress) *progressPainter {
	pp := &progressPainter{w: w, p: p, stopCh: make(chan struct{})}
	pp.wg.Add(1)
	go func() {
		defer pp.wg.Done()
		ticker := time.NewTicker(progressTickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pp.stopCh:
				return
			case <-ticker.C:
				// \r returns the cursor to the start of the line; the
				// next frame overwrites the previous one. We don't
				// clear-to-EOL because the rendered string includes
				// the entire line.
				pp.mu.Lock()
				fmt.Fprintf(w, "\r%s", p.Render())
				pp.mu.Unlock()
			}
		}
	}()
	return pp
}

// progressPainter owns the stderr line the bar repaints in place. Every
// write to that stream goes through its mutex: the walk reports skipped
// folders from its own goroutine while the ticker paints, and two
// unsynchronised writers would interleave a half-drawn frame with the
// message (and race on a test's bytes.Buffer).
type progressPainter struct {
	w      io.Writer
	p      *ui.ByteProgress
	mu     sync.Mutex
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// note prints one line above the bar. \r moves to the start of the
// bar's line so the message overwrites it, and the newline leaves the
// cursor on a fresh line for the next frame — the bar re-appears
// under the message on the next tick, and messages stack in order.
func (pp *progressPainter) note(line string) {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	// The bar never clears to end of line because every frame has the
	// same width; a note is usually narrower than the frame it replaces,
	// so it must erase the tail itself or the bar's remainder survives to
	// the right of the message.
	fmt.Fprintf(pp.w, "\r\x1b[K%s\n", line)
}

// stop ends the repaint loop and paints one final frame so completed
// runs end at 100% rather than at whatever the last tick caught. The
// trailing newline terminates the in-place rewrite cleanly.
func (pp *progressPainter) stop() {
	close(pp.stopCh)
	pp.wg.Wait()
	pp.mu.Lock()
	defer pp.mu.Unlock()
	fmt.Fprintf(pp.w, "\r%s\n", pp.p.Render())
}

// skipLine is the one spelling of a dropped subtree across `backup`,
// `backup plan/apply`, and `policy run`, so an operator grepping a
// timer's log and one watching a terminal look for the same words.
// The walker only skips on a denied listing today; the fallback keeps
// the line truthful should that ever widen.
func skipLine(path string, err error) string {
	reason := "permission denied"
	switch {
	case err == nil:
		reason = "skipped"
	case !errors.Is(err, fs.ErrPermission):
		reason = err.Error()
	}
	return "skipped " + path + ": " + reason
}
