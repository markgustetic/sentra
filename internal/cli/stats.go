package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/ui"
)

// StatsDeps wires the side-effecting pieces of `sentra stats`.
type StatsDeps struct {
	RepoDeps
}

// NewStats returns the cobra command for `sentra stats`: dedup ratio,
// logical-vs-stored bytes, and each snapshot's unique footprint —
// the "what is this repo costing me and which snapshot owns it" view.
func NewStats(deps StatsDeps) *cobra.Command {
	var (
		asJSON  bool
		cfgPath string
	)
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Report storage usage and deduplication efficiency",
		Long: "Sum every snapshot's logical bytes against the sealed bytes " +
			"actually stored, report the dedup factor, and show each " +
			"snapshot's unique (unshared) footprint — what pruning it would " +
			"eventually reclaim.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStats(cmd, deps, asJSON, cfgPath)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of the text report")
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
	return cmd
}

func runStats(cmd *cobra.Command, deps StatsDeps, asJSON bool, cfgPath string) error {
	cmd.SilenceUsage = true
	cfgPath, err := resolveConfigPath(cmd, cfgPath)
	if err != nil {
		return err
	}

	r, pass, _, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	stats, err := r.Stats(cmd.Context())
	if err != nil {
		return fmt.Errorf("stats: %w", err)
	}

	out := cmdStdout(cmd, deps.Stdout)
	if asJSON {
		return encodeJSON(out, stats)
	}

	fmt.Fprintln(out, ui.Primary.Render("Repository storage"))
	fmt.Fprintf(out, "  snapshots:     %d\n", stats.Snapshots)
	fmt.Fprintf(out, "  logical:       %s (%d)\n", ui.FormatBytes(stats.LogicalBytes), stats.LogicalBytes)
	fmt.Fprintf(out, "  stored:        %s (%d sealed)\n", ui.FormatBytes(stats.StoredBytes), stats.StoredBytes)
	fmt.Fprintf(out, "  unique chunks: %d\n", stats.UniqueChunks)
	fmt.Fprintf(out, "  dedup factor:  %.2fx\n", stats.DedupFactor())
	if len(stats.PerSnapshot) > 0 {
		fmt.Fprintln(out, ui.Subtle.Render("  per snapshot (unique = reclaimable if pruned alone):"))
		for _, s := range stats.PerSnapshot {
			fmt.Fprintf(out, "    %s  %s  %d files  %s unique\n",
				s.ID, emptyDash(s.Tag), s.Files, ui.FormatBytes(s.UniqueBytes))
		}
	}
	return nil
}
