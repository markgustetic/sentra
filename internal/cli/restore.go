package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// RestoreDeps wires the side-effecting pieces of `sentra restore`.
type RestoreDeps struct {
	RepoDeps
	Stderr io.Writer
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
	var (
		cfgPath     string
		dryRun      bool
		verify      bool
		asJSON      bool
		concurrency int
	)
	cmd := &cobra.Command{
		Use:   "restore <snap-id> <dest-dir> [path...]",
		Short: "Restore a snapshot into a destination directory",
		Long: "Decrypt and reassemble the named snapshot under dest-dir. " +
			"Extra path arguments scope the restore to those files or subtrees " +
			"(paths relative to the snapshot root; see `sentra ls`). The " +
			"destination must be empty (or not yet exist) — restore refuses to " +
			"merge over a populated tree. The snapshot may be a full ID, " +
			"\"latest\", a unique prefix, or the trailing hex from `sentra snapshots`.",
		Args:          cobra.MinimumNArgs(2),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestore(cmd, deps, args[0], args[1], args[2:], cfgPath, dryRun, verify, asJSON, concurrency)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"preview the restore without creating or writing the destination")
	cmd.Flags().BoolVar(&verify, "verify", false,
		"verify destination files against the snapshot after restore")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"emit the restore summary (or dry-run plan) as JSON")
	cmd.Flags().IntVar(&concurrency, "concurrency", 0,
		"files restored in parallel (0 = one per logical CPU)")
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
	return cmd
}

// runRestore is the body of `sentra restore`.
func runRestore(
	cmd *cobra.Command,
	deps RestoreDeps,
	snapID, destDir string,
	paths []string,
	cfgPath string,
	dryRun, verify, asJSON bool,
	concurrency int,
) error {
	cmd.SilenceUsage = true
	cfgPath, err := resolveConfigPath(cmd, cfgPath)
	if err != nil {
		return err
	}
	if dryRun && verify {
		return fmt.Errorf("restore: --dry-run and --verify cannot be combined")
	}

	r, pass, _, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	// Accept "latest", a unique prefix, or the trailing hex a listing
	// shows — not just the full ID.
	snapID, err = r.ResolveSnapshotID(cmd.Context(), snapID)
	if err != nil {
		return err
	}

	stderr := cmdStderr(cmd, deps.Stderr)
	stdout := cmdStdout(cmd, deps.Stdout)

	if dryRun {
		plan, err := r.PlanRestore(cmd.Context(), snapID, destDir, paths...)
		if err != nil {
			return fmt.Errorf("plan restore: %w", err)
		}
		if asJSON {
			return encodeJSON(stdout, restoreJSONRow{
				SnapshotID: plan.SnapshotID,
				Dest:       plan.DestDir,
				Files:      plan.Files,
				Bytes:      plan.Bytes,
				Scope:      paths,
				DryRun:     true,
			})
		}
		writeRestorePlan(stdout, plan)
		return nil
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

	if err := r.Restore(cmd.Context(), snapID, destDir, repo.RestoreOptions{
		Progress:    progress,
		Paths:       paths,
		Concurrency: concurrency,
	}); err != nil {
		stop()
		return fmt.Errorf("restore: %w", err)
	}
	stop()

	if asJSON {
		row := restoreJSONRow{
			SnapshotID: snapID,
			Dest:       destDir,
			Files:      m.Stats.Files,
			Bytes:      m.Stats.Bytes,
			Scope:      paths,
		}
		if len(paths) > 0 {
			// Scoped runs report the scoped counts — the same numbers
			// the dry-run form and text mode show — never the whole
			// manifest's stats.
			files, bytes, err := repo.ScopedTreeStats(m, paths)
			if err != nil {
				return err
			}
			row.Files, row.Bytes = files, bytes
		}
		if verify {
			report, err := r.VerifyRestore(cmd.Context(), snapID, destDir, paths...)
			if err != nil {
				return fmt.Errorf("verify restore: %w", err)
			}
			row.Verify = &restoreJSONVerify{
				VerifiedFiles: report.VerifiedFiles,
				Mismatches:    len(report.Mismatches),
			}
			if err := encodeJSON(stdout, row); err != nil {
				return err
			}
			if !report.OK() {
				return fmt.Errorf("restore verification failed: %d mismatches", len(report.Mismatches))
			}
			return nil
		}
		return encodeJSON(stdout, row)
	}

	fmt.Fprintln(stdout, ui.Success.Render("Restore complete"))
	fmt.Fprintf(stdout, "  snapshot:  %s\n", snapID)
	fmt.Fprintf(stdout, "  dest:      %s\n", destDir)
	if len(paths) > 0 {
		fmt.Fprintf(stdout, "  scope:     %s\n", strings.Join(paths, ", "))
	} else {
		fmt.Fprintf(stdout, "  files:     %d\n", m.Stats.Files)
		fmt.Fprintf(stdout, "  bytes:     %s (%d)\n", ui.FormatBytes(m.Stats.Bytes), m.Stats.Bytes)
	}
	if verify {
		report, err := r.VerifyRestore(cmd.Context(), snapID, destDir, paths...)
		if err != nil {
			return fmt.Errorf("verify restore: %w", err)
		}
		writeRestoreVerification(stdout, report)
		if !report.OK() {
			return fmt.Errorf("restore verification failed: %d mismatches", len(report.Mismatches))
		}
	}
	return nil
}

// restoreJSONRow is the stable machine-readable schema for
// `restore --json` (and its dry-run form).
type restoreJSONRow struct {
	SnapshotID string             `json:"snapshot_id"`
	Dest       string             `json:"dest"`
	Files      int                `json:"files"`
	Bytes      int64              `json:"bytes"`
	Scope      []string           `json:"scope,omitempty"`
	DryRun     bool               `json:"dry_run,omitempty"`
	Verify     *restoreJSONVerify `json:"verify,omitempty"`
}

type restoreJSONVerify struct {
	VerifiedFiles int `json:"verified_files"`
	Mismatches    int `json:"mismatches"`
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

func writeRestorePlan(w io.Writer, plan repo.RestorePlan) {
	fmt.Fprintln(w, ui.Primary.Render("Dry-run: restore preview"))
	fmt.Fprintf(w, "  snapshot:  %s\n", plan.SnapshotID)
	fmt.Fprintf(w, "  dest:      %s\n", plan.DestDir)
	fmt.Fprintf(w, "  files:     %d\n", plan.Files)
	fmt.Fprintf(w, "  bytes:     %s (%d)\n", ui.FormatBytes(plan.Bytes), plan.Bytes)
	if plan.DestExists {
		fmt.Fprintf(w, "  dest state: exists and empty\n")
	} else {
		fmt.Fprintf(w, "  dest state: will be created\n")
	}
	if len(plan.Paths) > 0 {
		fmt.Fprintln(w, "  paths:")
		limit := len(plan.Paths)
		if limit > 10 {
			limit = 10
		}
		for _, p := range plan.Paths[:limit] {
			fmt.Fprintf(w, "    - %s\n", p)
		}
		if len(plan.Paths) > limit {
			fmt.Fprintf(w, "    ... %d more\n", len(plan.Paths)-limit)
		}
	}
}

func writeRestoreVerification(w io.Writer, report repo.RestoreVerification) {
	if report.OK() {
		fmt.Fprintln(w, ui.Success.Render("Restore verify passed"))
	} else {
		fmt.Fprintln(w, ui.Warn.Render("Restore verify failed"))
	}
	fmt.Fprintf(w, "  verified files: %d/%d\n", report.VerifiedFiles, report.Files)
	if len(report.Mismatches) > 0 {
		fmt.Fprintf(w, "  mismatches:     %d\n", len(report.Mismatches))
		for _, mismatch := range report.Mismatches {
			fmt.Fprintf(w, "    - %s  %s\n", mismatch.Path, mismatch.Reason)
		}
	}
}
