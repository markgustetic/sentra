package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// PruneDeps wires the side-effecting pieces of `sentra prune`. The
// Confirm callback is what production replaces with the huh.NewConfirm
// flow; tests inject a deterministic "yes/no" function so the run is
// scriptable.
type PruneDeps struct {
	// NewStore opens the blobstore the repo lives in. Same shape as
	// every other command's deps so wiring is uniform.
	NewStore func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)

	// Passphrase returns the bytes used to unwrap the repo key.
	Passphrase func() ([]byte, error)

	// PassphraseWithConfig is the config-aware production resolver.
	// When set, it takes precedence over Passphrase.
	PassphraseWithConfig func(cfg *config.Config) ([]byte, error)

	// Stdout receives the user-facing summary. cobra.SetOut also goes
	// here when the caller wants stderr-style routing.
	Stdout io.Writer

	// Confirm prompts the user with a question and returns the
	// answer. Production wires this to huh.NewConfirm; tests inject a
	// stub. Skipped entirely when --yes is passed (the Confirm
	// callback is not invoked at all in that case, so test stubs that
	// panic-on-call still work correctly under --yes).
	Confirm func(prompt string) (bool, error)
}

// pruneFlags captures the --keep-* overrides plus the apply / yes
// toggles. We bind these to a struct so the cobra wiring stays compact
// and so the helper that overlays flags onto config has a single,
// explicit input shape.
//
// The "explicit" booleans (keepLastSet, ...) track whether the user
// passed the flag at all. Without that, we can't tell "user set
// --keep-last=0" (an explicit override that says "keep none") apart
// from "user didn't pass --keep-last" (use the config value). cobra's
// flag.Changed gives us the same information at the *flag set* level;
// we mirror it into the struct for grep-ability.
type pruneFlags struct {
	keepLast    int
	keepDaily   int
	keepWeekly  int
	keepMonthly int

	keepLastSet    bool
	keepDailySet   bool
	keepWeeklySet  bool
	keepMonthlySet bool

	apply   bool
	yes     bool
	all     bool
	explain bool
	cfgPath string
}

// NewPrune returns the cobra command for `sentra prune`. Without
// --apply the command is dry-run: it prints what would be deleted but
// does not modify the store. With --apply, the user is prompted for
// confirmation (huh.NewConfirm in production; PruneDeps.Confirm in
// tests) unless --yes is passed for scripting.
//
// The retention policy is the union of (config defaults < flag
// overrides). Each --keep-* flag layers on top of the corresponding
// retention key from sentra.yaml.
//
// Flags:
//   - --keep-last N      override retention.keep_last
//   - --keep-daily N     override retention.keep_daily
//   - --keep-weekly N    override retention.keep_weekly
//   - --keep-monthly N   override retention.keep_monthly
//   - --apply            actually delete (default: dry-run)
//   - --yes              skip the confirm prompt (apply-mode only)
//   - --explain          print why each snapshot is kept or dropped
//   - --config <path>    sentra.yaml path (defaults to ./sentra.yaml)
func NewPrune(deps PruneDeps) *cobra.Command {
	flags := &pruneFlags{}
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Apply retention policy and reclaim unreferenced storage",
		Long: "Compute the retention policy from --keep-* flags overlaying " +
			"the config, then either dry-run a report (default) or actually " +
			"delete the dropped snapshots and run GC. --yes skips the " +
			"interactive confirm so prune runs cleanly under cron.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Mirror cobra's "flag was set" knowledge into the struct
			// so the runner doesn't have to reach back into cmd.Flags().
			// Centralized so the lookups happen once, before the
			// runner has the chance to make its own mistakes.
			flags.keepLastSet = cmd.Flags().Changed("keep-last")
			flags.keepDailySet = cmd.Flags().Changed("keep-daily")
			flags.keepWeeklySet = cmd.Flags().Changed("keep-weekly")
			flags.keepMonthlySet = cmd.Flags().Changed("keep-monthly")
			return runPrune(cmd, deps, flags)
		},
	}
	cmd.Flags().IntVar(&flags.keepLast, "keep-last", 0,
		"keep the most recent N snapshots regardless of date (overrides retention.keep_last)")
	cmd.Flags().IntVar(&flags.keepDaily, "keep-daily", 0,
		"keep the newest snapshot per day for the last N days (overrides retention.keep_daily)")
	cmd.Flags().IntVar(&flags.keepWeekly, "keep-weekly", 0,
		"keep the newest snapshot per ISO week for the last N weeks (overrides retention.keep_weekly)")
	cmd.Flags().IntVar(&flags.keepMonthly, "keep-monthly", 0,
		"keep the newest snapshot per calendar month for the last N months (overrides retention.keep_monthly)")
	cmd.Flags().BoolVar(&flags.apply, "apply", false,
		"actually delete snapshots and reclaim storage (default: dry-run)")
	cmd.Flags().BoolVar(&flags.yes, "yes", false,
		"skip the interactive confirm prompt (use with --apply for scripts)")
	cmd.Flags().BoolVar(&flags.all, "all", false,
		"required when the policy would drop every snapshot (safety rail)")
	cmd.Flags().BoolVar(&flags.explain, "explain", false,
		"print the retention decision and reason for every snapshot")
	cmd.Flags().StringVar(&flags.cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	return cmd
}

// runPrune is the body of `sentra prune`. Pulled out of the closure
// for grep-ability and unit-testability.
func runPrune(cmd *cobra.Command, deps PruneDeps, flags *pruneFlags) error {
	cmd.SilenceUsage = true

	cfg, err := config.Load(flags.cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	policy := buildRetentionPolicy(cfg, flags)

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

	snaps, err := r.ListSnapshots(cmd.Context())
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}

	decisions := repo.PlanRetentionExplain(snaps, policy)
	keep, drop := splitRetentionDecisions(decisions)

	out := deps.Stdout
	if out == nil {
		out = cmd.OutOrStdout()
	}

	// Build a quick lookup for SnapshotInfo by ID so the printer can
	// surface CreatedAt and Stats.NewBytes for each drop candidate.
	infoByID := make(map[string]repo.SnapshotInfo, len(snaps))
	for _, s := range snaps {
		infoByID[s.ID] = s
	}

	// Estimate storage that would be freed: sum NewBytes across drops.
	// This is an upper bound — chunks shared between a dropped and a
	// kept snapshot are accounted to NewBytes of *whichever snapshot
	// uploaded them first*, so the actual freed bytes is "sum of
	// NewBytes for drops where the chunks are not also referenced by
	// any keep". Computing the real number requires loading every
	// manifest, which is O(N) blob fetches; the upper bound is fine
	// for a dry-run summary. The apply path's GC stats reports the
	// real number after the fact.
	var freedEstimate int64
	for _, id := range drop {
		freedEstimate += infoByID[id].Stats.NewBytes
	}

	// Print the dry-run / what-would-happen summary unconditionally.
	// In apply-mode this doubles as the prompt context.
	if len(drop) == 0 {
		fmt.Fprintln(out, ui.Subtle.Render("Nothing to delete; current snapshots match retention policy."))
		fmt.Fprintf(out, "  keep: %d\n", len(keep))
		if flags.explain {
			writeRetentionExplanation(out, decisions)
		}
		return nil
	}

	if !flags.apply {
		fmt.Fprintln(out, ui.Primary.Render("Dry-run: would prune snapshots"))
	} else {
		fmt.Fprintln(out, ui.Warn.Render("Will prune snapshots"))
	}
	fmt.Fprintf(out, "  keep:  %d snapshots\n", len(keep))
	fmt.Fprintf(out, "  drop:  %d snapshots (~%s freed estimate)\n",
		len(drop), ui.FormatBytes(freedEstimate))
	for _, id := range drop {
		info := infoByID[id]
		fmt.Fprintf(out, "    - %s  %s  %s\n",
			id,
			info.CreatedAt.UTC().Format("2006-01-02 15:04"),
			emptyDash(info.Tag),
		)
	}
	if flags.explain {
		writeRetentionExplanation(out, decisions)
	}

	if !flags.apply {
		fmt.Fprintln(out, ui.Subtle.Render("Re-run with --apply to delete; --yes to skip the confirm prompt."))
		return nil
	}

	// Safety rail: refuse to wipe the entire repo unless the user
	// explicitly opts in with --all. This catches the "all keep-*=0"
	// footgun where a user (or a bad cron config) would otherwise
	// silently delete every snapshot.
	if len(keep) == 0 && !flags.all {
		return errors.New("prune would drop every snapshot; pass --all to confirm")
	}

	// Apply path: confirm (unless --yes), then DeleteSnapshot each
	// drop ID, then GC with the kept IDs as the live set.
	if !flags.yes {
		ok, err := deps.Confirm(fmt.Sprintf("Delete %d snapshots and run GC?", len(drop)))
		if err != nil {
			return fmt.Errorf("confirm: %w", err)
		}
		if !ok {
			fmt.Fprintln(out, ui.Subtle.Render("Aborted by user."))
			return nil
		}
	}

	deletedCount := 0
	for _, id := range drop {
		if err := r.DeleteSnapshot(cmd.Context(), id); err != nil {
			// Continue past not-found (idempotent re-runs) but bail on
			// any other error — the manifest still being there means
			// GC will spare its chunks, which is the safe outcome.
			if errors.Is(err, blobstore.ErrNotFound) {
				continue
			}
			return fmt.Errorf("delete snapshot %s: %w", id, err)
		}
		deletedCount++
	}

	keepIDs := make(map[string]bool, len(keep))
	for _, id := range keep {
		keepIDs[id] = true
	}
	stats, err := r.GC(cmd.Context(), keepIDs)
	if err != nil {
		return fmt.Errorf("gc: %w", err)
	}

	fmt.Fprintln(out, ui.Success.Render("Prune complete"))
	fmt.Fprintf(out, "  deleted snapshots: %d\n", deletedCount)
	fmt.Fprintf(out, "  reclaimed blobs:   %d\n", stats.DeletedBlobs)
	fmt.Fprintf(out, "  reclaimed bytes:   %s (%d)\n",
		ui.FormatBytes(stats.DeletedBytes), stats.DeletedBytes)
	fmt.Fprintf(out, "  live blobs:        %d\n", stats.LiveBlobs)
	return nil
}

func splitRetentionDecisions(decisions []repo.RetentionDecision) (keep, drop []string) {
	for _, decision := range decisions {
		if decision.Keep {
			keep = append(keep, decision.Snapshot.ID)
		} else {
			drop = append(drop, decision.Snapshot.ID)
		}
	}
	return keep, drop
}

func writeRetentionExplanation(w io.Writer, decisions []repo.RetentionDecision) {
	fmt.Fprintln(w, ui.Subtle.Render("Retention decision details"))
	for _, decision := range decisions {
		action := "drop"
		if decision.Keep {
			action = "keep"
		}
		fmt.Fprintf(w, "    - %-4s %s  %s  %s\n",
			action,
			decision.Snapshot.ID,
			decision.Snapshot.CreatedAt.UTC().Format("2006-01-02 15:04"),
			strings.Join(decision.Reasons, "; "),
		)
	}
}

// buildRetentionPolicy starts from the config's retention block and
// overlays explicit flag values. A flag the user did NOT pass leaves
// the corresponding config value alone; an explicitly-passed flag
// (even one set to 0) overrides.
func buildRetentionPolicy(cfg *config.Config, flags *pruneFlags) repo.RetentionPolicy {
	policy := repo.RetentionPolicy{
		KeepLast:    cfg.Retention.KeepLast,
		KeepDaily:   cfg.Retention.KeepDaily,
		KeepWeekly:  cfg.Retention.KeepWeekly,
		KeepMonthly: cfg.Retention.KeepMonthly,
	}
	if flags.keepLastSet {
		policy.KeepLast = flags.keepLast
	}
	if flags.keepDailySet {
		policy.KeepDaily = flags.keepDaily
	}
	if flags.keepWeeklySet {
		policy.KeepWeekly = flags.keepWeekly
	}
	if flags.keepMonthlySet {
		policy.KeepMonthly = flags.keepMonthly
	}
	return policy
}

// (HuhConfirm now lives in confirm.go alongside the other two
// production confirm callbacks; their bodies were identical except
// for the affirmative/negative label pair.)
