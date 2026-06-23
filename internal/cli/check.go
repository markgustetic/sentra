package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// ErrCheckFailed is returned after a check report is written when the
// repository has integrity or operational health failures.
var ErrCheckFailed = errors.New("repository check failed")

// CheckDeps wires the side-effecting pieces of `sentra check`.
type CheckDeps struct {
	NewStore             func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	Passphrase           func() ([]byte, error)
	PassphraseWithConfig func(cfg *config.Config) ([]byte, error)
	Stdout               io.Writer
}

// NewCheck returns the cobra command for `sentra check`.
func NewCheck(deps CheckDeps) *cobra.Command {
	var (
		asJSON         bool
		staleLockAfter time.Duration
		cfgPath        string
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Audit repository integrity and operational health",
		Long: "Load every snapshot manifest, verify that referenced chunks " +
			"exist, report unreferenced data blobs, and flag stale advisory locks.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheck(cmd, deps, asJSON, staleLockAfter, cfgPath)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a text report")
	cmd.Flags().DurationVar(&staleLockAfter, "stale-lock-after", 24*time.Hour,
		"age after which meta/lock is treated as stale")
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	return cmd
}

func runCheck(
	cmd *cobra.Command,
	deps CheckDeps,
	asJSON bool,
	staleLockAfter time.Duration,
	cfgPath string,
) error {
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

	report, err := r.Check(cmd.Context(), repo.CheckOptions{StaleLockAfter: staleLockAfter})
	if err != nil {
		return fmt.Errorf("check repo: %w", err)
	}

	out := deps.Stdout
	if out == nil {
		out = cmd.OutOrStdout()
	}
	if asJSON {
		err = writeCheckJSON(out, report)
	} else {
		err = writeCheckText(out, report)
	}
	if err != nil {
		return err
	}
	if !report.Healthy() {
		return fmt.Errorf("%w: missing=%d manifests=%d lock_stale=%t",
			ErrCheckFailed,
			len(report.MissingBlobs),
			len(report.ManifestIssues),
			report.Lock != nil && (report.Lock.Stale || report.Lock.Unreadable),
		)
	}
	return nil
}

type checkJSONReport struct {
	Status string `json:"status"`
	repo.CheckReport
}

func writeCheckJSON(w io.Writer, report repo.CheckReport) error {
	status := "healthy"
	if !report.Healthy() {
		status = "failed"
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(checkJSONReport{Status: status, CheckReport: report}); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

func writeCheckText(w io.Writer, report repo.CheckReport) error {
	if report.Healthy() {
		fmt.Fprintln(w, ui.Success.Render("Repository check: healthy"))
	} else {
		fmt.Fprintln(w, ui.Warn.Render("Repository check: failed"))
	}
	fmt.Fprintf(w, "  snapshots:        %d\n", report.Snapshots)
	fmt.Fprintf(w, "  files:            %d\n", report.Files)
	fmt.Fprintf(w, "  plaintext bytes:  %s (%d)\n", ui.FormatBytes(report.Bytes), report.Bytes)
	fmt.Fprintf(w, "  referenced blobs: %d\n", report.ReferencedBlobs)
	fmt.Fprintf(w, "  data blobs:       %d (%s)\n", report.DataBlobs, ui.FormatBytes(report.DataBytes))

	if len(report.OrphanBlobs) > 0 {
		fmt.Fprintf(w, "  orphan blobs:     %d (%s)\n", len(report.OrphanBlobs), ui.FormatBytes(report.OrphanBytes))
		for _, blob := range report.OrphanBlobs {
			fmt.Fprintf(w, "    - %s  %s\n", blob.Key, ui.FormatBytes(blob.Size))
		}
	}
	if len(report.ManifestIssues) > 0 {
		fmt.Fprintf(w, "  manifest issues:  %d\n", len(report.ManifestIssues))
		for _, issue := range report.ManifestIssues {
			fmt.Fprintf(w, "    - %s  %s\n", issue.Key, issue.Error)
		}
	}
	if len(report.MissingBlobs) > 0 {
		fmt.Fprintf(w, "  missing blobs:    %d\n", len(report.MissingBlobs))
		for _, missing := range report.MissingBlobs {
			fmt.Fprintf(w, "    - %s  snapshot=%s path=%s\n",
				missing.Key, missing.SnapshotID, missing.Path)
		}
	}
	if report.Lock != nil {
		state := "active"
		if report.Lock.Stale {
			state = "stale"
		}
		if report.Lock.Unreadable {
			state = "unreadable"
		}
		fmt.Fprintf(w, "  lock:             %s op=%s host=%s pid=%d age=%s\n",
			state,
			emptyDash(report.Lock.Operation),
			emptyDash(report.Lock.Host),
			report.Lock.PID,
			report.Lock.Age.Round(time.Second),
		)
	}
	return nil
}
