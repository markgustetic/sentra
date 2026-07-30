package cli

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
	"github.com/markgustetic/sentra/internal/walker"
)

// PolicyDeps wires side effects for `sentra policy`.
type PolicyDeps struct {
	RepoDeps
	Stderr io.Writer
}

type policyAddFlags struct {
	paths      []string
	tags       []string
	schedule   string
	check      bool
	prune      string
	replace    bool
	configPath *string
}

// NewPolicy returns the command group for named backup policies.
func NewPolicy(deps PolicyDeps) *cobra.Command {
	cfgPath := configFileName
	cmd := &cobra.Command{
		Use:           "policy",
		Short:         "Manage named backup policies",
		Long:          "Manage non-secret named backup policies in sentra.yaml and run them on demand.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	cmd.PersistentFlags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	cmd.AddCommand(newPolicyAdd(deps, &cfgPath))
	cmd.AddCommand(newPolicyList(deps, &cfgPath))
	cmd.AddCommand(newPolicyShow(deps, &cfgPath))
	cmd.AddCommand(newPolicyRemove(deps, &cfgPath))
	cmd.AddCommand(newPolicyRun(deps, &cfgPath))
	return cmd
}

func newPolicyAdd(deps PolicyDeps, cfgPath *string) *cobra.Command {
	flags := &policyAddFlags{schedule: policycfg.CadenceManual, prune: policycfg.PruneOff, configPath: cfgPath}
	cmd := &cobra.Command{
		Use:           "add <name>",
		Short:         "Add or replace a named backup policy",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyAdd(cmd, deps, args[0], flags)
		},
	}
	cmd.Flags().StringArrayVar(&flags.paths, "path", nil,
		"path to back up; may be passed more than once")
	cmd.Flags().StringArrayVar(&flags.tags, "tag", nil,
		"tag to add to snapshots created by this policy; may be passed more than once")
	cmd.Flags().StringVar(&flags.schedule, "schedule", policycfg.CadenceManual,
		"schedule shorthand: manual, hourly, daily@HH:MM, weekly@mon:HH:MM, monthly@HH:MM")
	cmd.Flags().BoolVar(&flags.check, "check", false,
		"run sentra check after successful backups")
	cmd.Flags().StringVar(&flags.prune, "prune", policycfg.PruneOff,
		"post-backup prune mode: off, dry-run, apply")
	cmd.Flags().BoolVar(&flags.replace, "replace", false,
		"replace an existing policy with the same name")
	return cmd
}

func newPolicyList(deps PolicyDeps, cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:           "list",
		Short:         "List configured backup policies",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPolicyList(cmd, deps, *cfgPath)
		},
	}
}

func newPolicyShow(deps PolicyDeps, cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:           "show <name>",
		Short:         "Show one configured backup policy",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyShow(cmd, deps, *cfgPath, args[0])
		},
	}
}

func newPolicyRemove(deps PolicyDeps, cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:           "remove <name>",
		Short:         "Remove a configured backup policy",
		Aliases:       []string{"rm"},
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyRemove(cmd, deps, *cfgPath, args[0])
		},
	}
}

func newPolicyRun(deps PolicyDeps, cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:           "run <name>",
		Short:         "Run a named backup policy now",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicy(cmd, deps, *cfgPath, args[0])
		},
	}
}

func runPolicyAdd(cmd *cobra.Command, deps PolicyDeps, name string, flags *policyAddFlags) error {
	schedule, err := policycfg.ParseScheduleSpec(flags.schedule)
	if err != nil {
		return fmt.Errorf("parse schedule: %w", err)
	}
	p := config.PolicyConfig{
		Paths:    append([]string(nil), flags.paths...),
		Tags:     append([]string(nil), flags.tags...),
		Schedule: schedule,
		AfterBackup: config.PolicyAfterBackup{
			Check: flags.check,
			Prune: strings.ToLower(strings.TrimSpace(flags.prune)),
		},
	}
	if err := policycfg.Validate(name, p); err != nil {
		return err
	}

	// config.Update rewrites against sentra.yaml as it exists on disk, so
	// editing the policies map can't persist this process's SENTRA_*
	// overrides into repo.s3. The duplicate-name check runs inside the
	// mutation, against the same on-disk map we're about to write back.
	err = config.Update(*flags.configPath, func(cfg *config.Config) error {
		if cfg.Policies == nil {
			cfg.Policies = map[string]config.PolicyConfig{}
		}
		if existing, exists := cfg.Policies[name]; exists {
			if !flags.replace {
				return fmt.Errorf("policy %q already exists; pass --replace to overwrite", name)
			}
			// Hooks are config-authored — no flag manages them — so a
			// replace carries them forward rather than silently wiping
			// a hand-written notifier because someone added a path.
			p.Hooks = existing.Hooks
		}
		cfg.Policies[name] = p
		return nil
	})
	if err != nil {
		return err
	}

	out := policyStdout(cmd, deps)
	fmt.Fprintln(out, ui.Success.Render("Policy added"))
	fmt.Fprintf(out, "  name:      %s\n", name)
	fmt.Fprintf(out, "  paths:     %d\n", len(p.Paths))
	fmt.Fprintf(out, "  schedule:  %s\n", policycfg.FormatScheduleSpec(p.Schedule))
	return nil
}

func runPolicyList(cmd *cobra.Command, deps PolicyDeps, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	out := policyStdout(cmd, deps)
	if len(cfg.Policies) == 0 {
		fmt.Fprintln(out, ui.Subtle.Render("No policies configured."))
		return nil
	}
	for _, name := range sortedPolicyNames(cfg.Policies) {
		p := cfg.Policies[name]
		if err := policycfg.Validate(name, p); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s  %s  paths=%d  tags=%s\n",
			name,
			policycfg.FormatScheduleSpec(p.Schedule),
			len(p.Paths),
			emptyDash(strings.Join(p.Tags, ",")),
		)
	}
	return nil
}

func runPolicyShow(cmd *cobra.Command, deps PolicyDeps, cfgPath, name string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	p, ok := cfg.Policies[name]
	if !ok {
		return fmt.Errorf("policy %q not found", name)
	}
	if err := policycfg.Validate(name, p); err != nil {
		return err
	}
	out := policyStdout(cmd, deps)
	fmt.Fprintln(out, ui.Primary.Render("Policy"))
	fmt.Fprintf(out, "  name: %s\n", name)
	fmt.Fprintln(out, "  paths:")
	for _, path := range p.Paths {
		fmt.Fprintf(out, "    - %s\n", path)
	}
	fmt.Fprintf(out, "  tags: %s\n", emptyDash(strings.Join(p.Tags, ", ")))
	fmt.Fprintf(out, "  schedule: %s\n", policycfg.FormatScheduleSpec(p.Schedule))
	fmt.Fprintf(out, "  check: %t\n", p.AfterBackup.Check)
	fmt.Fprintf(out, "  prune: %s\n", policyPruneMode(p.AfterBackup.Prune))
	return nil
}

func runPolicyRemove(cmd *cobra.Command, deps PolicyDeps, cfgPath, name string) error {
	// On-disk base, as in runPolicyAdd: dropping a policy must not rewrite
	// repo.s3 with whatever SENTRA_* said for this invocation.
	err := config.Update(cfgPath, func(cfg *config.Config) error {
		if _, ok := cfg.Policies[name]; !ok {
			return fmt.Errorf("policy %q not found", name)
		}
		delete(cfg.Policies, name)
		return nil
	})
	if err != nil {
		return err
	}
	out := policyStdout(cmd, deps)
	fmt.Fprintln(out, ui.Success.Render("Policy removed"))
	fmt.Fprintf(out, "  name: %s\n", name)
	return nil
}

func runPolicy(cmd *cobra.Command, deps PolicyDeps, cfgPath, name string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	p, ok := cfg.Policies[name]
	if !ok {
		return fmt.Errorf("policy %q not found", name)
	}
	if err := policycfg.Validate(name, p); err != nil {
		return err
	}

	// Hooks wrap the run: before → stages → after, with on_failure
	// (command and/or webhook) firing when ANY of them fails.
	// Config-shape errors above don't count as a run failure — the
	// run never started.
	runErr := func() error {
		if p.Hooks.Before != "" {
			if err := runPolicyHook(cmd, deps, "before", p.Hooks.Before); err != nil {
				return err
			}
		}
		if err := runPolicyStages(cmd, deps, cfg, name, p); err != nil {
			return err
		}
		if p.Hooks.After != "" {
			if err := runPolicyHook(cmd, deps, "after", p.Hooks.After); err != nil {
				return err
			}
		}
		return nil
	}()
	if runErr != nil {
		firePolicyFailureHooks(cmd, deps, name, p.Hooks, runErr)
	}
	return runErr
}

func runPolicyStages(cmd *cobra.Command, deps PolicyDeps, cfg *config.Config, name string, p config.PolicyConfig) error {
	if deps.NewStore == nil {
		return errors.New("policy run: blobstore factory is not configured")
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

	out := policyStdout(cmd, deps)
	walkerOpts := walker.Options{
		IgnoreFile:    cfg.Backup.IgnoreFile,
		ExcludeCaches: cfg.Backup.ExcludeCaches,
		Concurrency:   cfg.Backup.Concurrency,
	}
	normalizeBackupWalkerOptions(&walkerOpts)

	snapshots := make([]repo.SnapshotInfo, 0, len(p.Paths))
	tag := policySnapshotTag(name, p.Tags)
	for _, path := range p.Paths {
		snap, err := r.CreateSnapshot(cmd.Context(), path, repo.SnapshotOptions{
			Tag:    tag,
			Walker: walkerOpts,
		})
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", path, err)
		}
		snapshots = append(snapshots, snap)
	}

	fmt.Fprintln(out, ui.Success.Render("Policy run complete"))
	fmt.Fprintf(out, "  policy:    %s\n", name)
	fmt.Fprintf(out, "  snapshots: %d\n", len(snapshots))
	for _, snap := range snapshots {
		fmt.Fprintf(out, "    - %s  %s  %d files\n", snap.ID, snap.Tag, snap.Stats.Files)
	}
	if p.AfterBackup.Check {
		if err := runPolicyCheck(cmd, out, r); err != nil {
			return err
		}
	}
	if err := runPolicyPrune(cmd, out, r, cfg, policyPruneMode(p.AfterBackup.Prune)); err != nil {
		return err
	}
	return nil
}

// runPolicyHook and firePolicyFailureHooks delegate to internal/policy
// so a policy run behaves identically from the CLI and the TUI — hook
// execution lives below both surfaces.
func runPolicyHook(cmd *cobra.Command, deps PolicyDeps, label, script string) error {
	return policycfg.RunHook(cmd.Context(), policyStdout(cmd, deps), label, script)
}

func firePolicyFailureHooks(cmd *cobra.Command, deps PolicyDeps, name string, hooks config.PolicyHooks, cause error) {
	policycfg.FireFailureHooks(cmd.Context(), policyStdout(cmd, deps), name, hooks, cause)
}

func runPolicyCheck(cmd *cobra.Command, out io.Writer, r *repo.Repo) error {
	report, err := r.Check(cmd.Context(), repo.CheckOptions{StaleLockAfter: 24 * time.Hour})
	if err != nil {
		return fmt.Errorf("check repo: %w", err)
	}
	if report.Healthy() {
		fmt.Fprintln(out, "  check: healthy")
		return nil
	}
	fmt.Fprintln(out, "  check: failed")
	return checkFailedError(report)
}

func runPolicyPrune(cmd *cobra.Command, out io.Writer, r *repo.Repo, cfg *config.Config, mode string) error {
	if mode == policycfg.PruneOff {
		return nil
	}
	snaps, err := r.ListSnapshots(cmd.Context())
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	policy := repo.RetentionPolicy{
		KeepLast:    cfg.Retention.KeepLast,
		KeepDaily:   cfg.Retention.KeepDaily,
		KeepWeekly:  cfg.Retention.KeepWeekly,
		KeepMonthly: cfg.Retention.KeepMonthly,
	}
	decisions := repo.PlanRetentionExplain(snaps, policy)
	keep, drop := splitRetentionDecisions(decisions)
	if mode == policycfg.PruneDryRun {
		fmt.Fprintf(out, "  prune: dry-run keep=%d drop=%d\n", len(keep), len(drop))
		return nil
	}
	if len(drop) == 0 {
		fmt.Fprintln(out, "  prune: nothing to delete")
		return nil
	}
	if len(keep) == 0 {
		return errors.New("policy prune would drop every snapshot; refusing automatic apply")
	}
	for _, id := range drop {
		if err := r.DeleteSnapshot(cmd.Context(), id); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
			return fmt.Errorf("delete snapshot %s: %w", id, err)
		}
	}
	keepIDs := make(map[string]bool, len(keep))
	for _, id := range keep {
		keepIDs[id] = true
	}
	stats, err := r.GC(cmd.Context(), keepIDs)
	if err != nil {
		return fmt.Errorf("gc: %w", err)
	}
	fmt.Fprintf(out, "  prune: applied deleted=%d reclaimed=%s\n", len(drop), ui.FormatBytes(stats.DeletedBytes))
	return nil
}

func policyStdout(cmd *cobra.Command, deps PolicyDeps) io.Writer {
	if deps.Stdout != nil {
		return deps.Stdout
	}
	return cmd.OutOrStdout()
}

func sortedPolicyNames(policies map[string]config.PolicyConfig) []string {
	names := make([]string, 0, len(policies))
	for name := range policies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func policySnapshotTag(name string, tags []string) string {
	parts := []string{"policy:" + name}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			parts = append(parts, tag)
		}
	}
	return strings.Join(parts, " ")
}

func policyPruneMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return policycfg.PruneOff
	}
	return mode
}
