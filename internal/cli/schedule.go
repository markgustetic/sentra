package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/scheduler"
	"github.com/markgustetic/sentra/internal/ui"
)

// ScheduleDeps wires filesystem and platform details for `sentra schedule`.
type ScheduleDeps struct {
	OS         string
	HomeDir    func() (string, error)
	Executable func() (string, error)
	Stdout     io.Writer

	// Now feeds the next-run computation; nil means time.Now. A seam so
	// status output is testable against a pinned clock.
	Now func() time.Time

	// Runner executes launchctl/systemctl for activation, deactivation and
	// the status query; nil means scheduler.ExecRunner. Tests MUST inject
	// a fake: the real one loads a job on the developer's machine.
	Runner scheduler.Runner
}

// NewSchedule returns the command group for installing policy schedules.
func NewSchedule(deps ScheduleDeps) *cobra.Command {
	cfgPath := configFileName
	cmd := &cobra.Command{
		Use:           "schedule",
		Short:         "Install OS scheduler entries for policies",
		Long:          "Install, inspect, and remove user-level OS scheduler entries that run `sentra policy run`. Install loads the timer into launchd/systemd right away; uninstall unloads it before removing the files.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	cmd.PersistentFlags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
	cmd.AddCommand(newScheduleInstall(deps, &cfgPath))
	cmd.AddCommand(newScheduleStatus(deps, &cfgPath))
	cmd.AddCommand(newScheduleUninstall(deps, &cfgPath))
	return cmd
}

func newScheduleInstall(deps ScheduleDeps, cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:           "install <policy>",
		Short:         "Install and activate a policy schedule",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScheduleInstall(cmd, deps, *cfgPath, args[0])
		},
	}
}

func newScheduleStatus(deps ScheduleDeps, cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:           "status <policy>",
		Short:         "Show whether a policy schedule is installed and active",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScheduleStatus(cmd, deps, *cfgPath, args[0])
		},
	}
}

func newScheduleUninstall(deps ScheduleDeps, cfgPath *string) *cobra.Command {
	return &cobra.Command{
		Use:           "uninstall <policy>",
		Short:         "Deactivate and remove an installed policy schedule",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScheduleUninstall(cmd, deps, *cfgPath, args[0])
		},
	}
}

func runScheduleInstall(cmd *cobra.Command, deps ScheduleDeps, cfgPath, name string) error {
	cfgPath, err := resolveConfigPath(cmd, cfgPath)
	if err != nil {
		return err
	}
	p, absConfig, err := loadScheduledPolicy(cfgPath, name)
	if err != nil {
		return err
	}
	if policycfg.NormalizeSchedule(p.Schedule).Cadence == policycfg.CadenceManual {
		return fmt.Errorf("policy %q has manual schedule; use `sentra policy add --schedule ... --replace` before installing", name)
	}
	home, err := scheduleHome(deps)
	if err != nil {
		return err
	}
	paths, err := scheduler.PathsFor(scheduleOS(deps), home, name)
	if err != nil {
		return err
	}
	exe, err := scheduler.Executable(scheduleExe(deps))
	if err != nil {
		return err
	}
	files, err := scheduler.Render(paths, exe, absConfig, name, p.Schedule)
	if err != nil {
		return err
	}
	if err := scheduler.Install(files); err != nil {
		return err
	}
	// Files alone do nothing until launchd/systemd loads them (at the next
	// login, or never for a systemd timer that was not enabled). Activate
	// now; on failure the files stay and the error names the command.
	actErr := scheduler.Activate(cmd.Context(), paths, deps.Runner)

	out := scheduleStdout(cmd, deps)
	fmt.Fprintln(out, ui.Success.Render("Schedule installed"))
	fmt.Fprintf(out, "  policy:   %s\n", name)
	fmt.Fprintf(out, "  schedule: %s\n", policycfg.FormatScheduleSpec(p.Schedule))
	for _, path := range paths.Files {
		fmt.Fprintf(out, "  file:     %s\n", path)
	}
	if actErr != nil {
		fmt.Fprintln(out, "  timer:    not active")
		return actErr
	}
	fmt.Fprintln(out, "  timer:    active")
	return nil
}

func runScheduleStatus(cmd *cobra.Command, deps ScheduleDeps, cfgPath, name string) error {
	cfgPath, err := resolveConfigPath(cmd, cfgPath)
	if err != nil {
		return err
	}
	p, _, err := loadScheduledPolicy(cfgPath, name)
	if err != nil {
		return err
	}
	home, err := scheduleHome(deps)
	if err != nil {
		return err
	}
	paths, err := scheduler.PathsFor(scheduleOS(deps), home, name)
	if err != nil {
		return err
	}
	installed, err := scheduler.Installed(paths)
	if err != nil {
		return err
	}
	out := scheduleStdout(cmd, deps)
	if installed {
		fmt.Fprintln(out, ui.Success.Render("Schedule installed"))
	} else {
		fmt.Fprintln(out, ui.Subtle.Render("Schedule not installed"))
	}
	fmt.Fprintf(out, "  policy: %s\n", name)
	for _, path := range paths.Files {
		fmt.Fprintf(out, "  file:   %s\n", path)
	}
	if !installed {
		return nil
	}
	// Installed files are only half the story: report whether the OS has
	// the timer loaded, and hand over the activation command when it does
	// not. A next run is promised only when the timer can actually fire —
	// or when we could not ask (no user bus), which is not a "no".
	active, actErr := scheduler.Active(cmd.Context(), paths, deps.Runner)
	switch {
	case actErr != nil:
		fmt.Fprintf(out, "  timer:  unknown (%v)\n", actErr)
	case active:
		fmt.Fprintln(out, "  timer:  active")
	default:
		fmt.Fprintf(out, "  timer:  not active — run: %s\n", scheduler.ActivateCommand(paths))
		return nil
	}
	nowFn := deps.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	if next, ok := policycfg.NextRun(p.Schedule, nowFn()); ok {
		fmt.Fprintf(out, "  next run: %s\n", next.Format("2006-01-02 15:04"))
	}
	return nil
}

func runScheduleUninstall(cmd *cobra.Command, deps ScheduleDeps, cfgPath, name string) error {
	cfgPath, err := resolveConfigPath(cmd, cfgPath)
	if err != nil {
		return err
	}
	if _, _, err := loadScheduledPolicy(cfgPath, name); err != nil {
		return err
	}
	home, err := scheduleHome(deps)
	if err != nil {
		return err
	}
	paths, err := scheduler.PathsFor(scheduleOS(deps), home, name)
	if err != nil {
		return err
	}
	// Deactivate first — systemd resolves `disable` from the unit on disk —
	// then remove the files regardless: a headless session that cannot
	// reach the OS scheduler must still be able to clean up, and the
	// returned error names the command to finish the job with.
	deactErr := scheduler.Deactivate(cmd.Context(), paths, deps.Runner)
	if err := scheduler.Uninstall(paths); err != nil {
		return err
	}
	out := scheduleStdout(cmd, deps)
	fmt.Fprintln(out, ui.Success.Render("Schedule removed"))
	fmt.Fprintf(out, "  policy: %s\n", name)
	if deactErr != nil {
		fmt.Fprintln(out, "  timer:  may still be loaded")
		return deactErr
	}
	fmt.Fprintln(out, "  timer:  stopped")
	return nil
}

func loadScheduledPolicy(cfgPath, name string) (config.PolicyConfig, string, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return config.PolicyConfig{}, "", fmt.Errorf("load config: %w", err)
	}
	p, ok := cfg.Policies[name]
	if !ok {
		return config.PolicyConfig{}, "", fmt.Errorf("policy %q not found", name)
	}
	if err := policycfg.Validate(name, p); err != nil {
		return config.PolicyConfig{}, "", err
	}
	absConfig, err := filepath.Abs(cfgPath)
	if err != nil {
		return config.PolicyConfig{}, "", fmt.Errorf("resolve config path: %w", err)
	}
	return p, filepath.Clean(absConfig), nil
}

// scheduleOS returns the target GOOS, honoring the deps override used by
// tests; "" lets scheduler.PathsFor fall back to runtime.GOOS.
func scheduleOS(deps ScheduleDeps) string { return deps.OS }

// scheduleHome resolves the home dir via the deps hook (tests inject a temp
// dir) or the OS default. It resolves here rather than passing "" to
// scheduler.PathsFor so the deps.HomeDir override is honored.
func scheduleHome(deps ScheduleDeps) (string, error) {
	if deps.HomeDir == nil {
		return "", nil // let scheduler.PathsFor default to os.UserHomeDir
	}
	home, err := deps.HomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return home, nil
}

// scheduleExe returns the executable path from the deps hook (tests inject a
// fixed path); "" lets scheduler.Executable fall back to os.Executable.
func scheduleExe(deps ScheduleDeps) string {
	if deps.Executable == nil {
		return ""
	}
	exe, err := deps.Executable()
	if err != nil {
		return ""
	}
	return exe
}

func scheduleStdout(cmd *cobra.Command, deps ScheduleDeps) io.Writer {
	if deps.Stdout != nil {
		return deps.Stdout
	}
	return cmd.OutOrStdout()
}
