package cli

import (
	"fmt"
	"io"
	"path/filepath"

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
}

// NewSchedule returns the command group for installing policy schedules.
func NewSchedule(deps ScheduleDeps) *cobra.Command {
	cfgPath := configFileName
	cmd := &cobra.Command{
		Use:           "schedule",
		Short:         "Install OS scheduler entries for policies",
		Long:          "Install, inspect, and remove user-level OS scheduler entries that run `sentra policy run`.",
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
		Short:         "Install a policy schedule",
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
		Short:         "Show whether a policy schedule is installed",
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
		Short:         "Remove an installed policy schedule",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScheduleUninstall(cmd, deps, *cfgPath, args[0])
		},
	}
}

func runScheduleInstall(cmd *cobra.Command, deps ScheduleDeps, cfgPath, name string) error {
	cfgPath = resolveConfigPath(cmd, cfgPath)
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

	out := scheduleStdout(cmd, deps)
	fmt.Fprintln(out, ui.Success.Render("Schedule installed"))
	fmt.Fprintf(out, "  policy:   %s\n", name)
	fmt.Fprintf(out, "  schedule: %s\n", policycfg.FormatScheduleSpec(p.Schedule))
	for _, path := range paths.Files {
		fmt.Fprintf(out, "  file:     %s\n", path)
	}
	return nil
}

func runScheduleStatus(cmd *cobra.Command, deps ScheduleDeps, cfgPath, name string) error {
	cfgPath = resolveConfigPath(cmd, cfgPath)
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
	return nil
}

func runScheduleUninstall(cmd *cobra.Command, deps ScheduleDeps, cfgPath, name string) error {
	cfgPath = resolveConfigPath(cmd, cfgPath)
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
	if err := scheduler.Uninstall(paths); err != nil {
		return err
	}
	out := scheduleStdout(cmd, deps)
	fmt.Fprintln(out, ui.Success.Render("Schedule removed"))
	fmt.Fprintf(out, "  policy: %s\n", name)
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
