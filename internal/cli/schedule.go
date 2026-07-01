package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
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
		"path to sentra.yaml (defaults to ./sentra.yaml)")
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
	p, absConfig, err := loadScheduledPolicy(cfgPath, name)
	if err != nil {
		return err
	}
	if policycfg.NormalizeSchedule(p.Schedule).Cadence == policycfg.CadenceManual {
		return fmt.Errorf("policy %q has manual schedule; use `sentra policy add --schedule ... --replace` before installing", name)
	}
	paths, err := schedulerPaths(deps, name)
	if err != nil {
		return err
	}
	exe, err := scheduleExecutable(deps)
	if err != nil {
		return err
	}
	files, err := renderScheduleFiles(paths, exe, absConfig, name, p.Schedule)
	if err != nil {
		return err
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create scheduler dir %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return fmt.Errorf("write scheduler file %s: %w", path, err)
		}
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
	if _, _, err := loadScheduledPolicy(cfgPath, name); err != nil {
		return err
	}
	paths, err := schedulerPaths(deps, name)
	if err != nil {
		return err
	}
	installed := true
	for _, path := range paths.Files {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				installed = false
				continue
			}
			return fmt.Errorf("stat scheduler file %s: %w", path, err)
		}
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
	if _, _, err := loadScheduledPolicy(cfgPath, name); err != nil {
		return err
	}
	paths, err := schedulerPaths(deps, name)
	if err != nil {
		return err
	}
	for _, path := range paths.Files {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove scheduler file %s: %w", path, err)
		}
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

type schedulePaths struct {
	OS    string
	Files []string
	Home  string
}

func schedulerPaths(deps ScheduleDeps, name string) (schedulePaths, error) {
	if err := policycfg.ValidateName(name); err != nil {
		return schedulePaths{}, err
	}
	goos := deps.OS
	if goos == "" {
		goos = runtime.GOOS
	}
	homeFn := deps.HomeDir
	if homeFn == nil {
		homeFn = os.UserHomeDir
	}
	home, err := homeFn()
	if err != nil {
		return schedulePaths{}, fmt.Errorf("locate home dir: %w", err)
	}
	switch goos {
	case "darwin":
		return schedulePaths{
			OS:    goos,
			Home:  home,
			Files: []string{filepath.Join(home, "Library", "LaunchAgents", "com.sentra."+name+".plist")},
		}, nil
	case "linux":
		dir := filepath.Join(home, ".config", "systemd", "user")
		return schedulePaths{
			OS:   goos,
			Home: home,
			Files: []string{
				filepath.Join(dir, "sentra-"+name+".service"),
				filepath.Join(dir, "sentra-"+name+".timer"),
			},
		}, nil
	default:
		return schedulePaths{}, fmt.Errorf("unsupported scheduler OS %q; supported: darwin, linux", goos)
	}
}

func scheduleExecutable(deps ScheduleDeps) (string, error) {
	exeFn := deps.Executable
	if exeFn == nil {
		exeFn = os.Executable
	}
	exe, err := exeFn()
	if err != nil {
		return "", fmt.Errorf("locate sentra executable: %w", err)
	}
	if !filepath.IsAbs(exe) {
		exe, err = filepath.Abs(exe)
		if err != nil {
			return "", fmt.Errorf("resolve sentra executable: %w", err)
		}
	}
	return filepath.Clean(exe), nil
}

func scheduleStdout(cmd *cobra.Command, deps ScheduleDeps) io.Writer {
	if deps.Stdout != nil {
		return deps.Stdout
	}
	return cmd.OutOrStdout()
}
