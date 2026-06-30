package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

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

func renderScheduleFiles(paths schedulePaths, exe, cfgPath, name string, schedule config.PolicySchedule) (map[string]string, error) {
	switch paths.OS {
	case "darwin":
		body, err := renderLaunchAgent(paths.Home, exe, cfgPath, name, schedule)
		if err != nil {
			return nil, err
		}
		return map[string]string{paths.Files[0]: body}, nil
	case "linux":
		service, timer, err := renderSystemdUserUnits(exe, cfgPath, name, schedule)
		if err != nil {
			return nil, err
		}
		return map[string]string{
			paths.Files[0]: service,
			paths.Files[1]: timer,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported scheduler OS %q", paths.OS)
	}
}

func renderLaunchAgent(home, exe, cfgPath, name string, schedule config.PolicySchedule) (string, error) {
	cal, err := launchdCalendar(schedule)
	if err != nil {
		return "", err
	}
	args := []string{exe, "policy", "run", name, "--config", cfgPath, "--log-level", "info"}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.sentra.` + xmlEscape(name) + `</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, arg := range args {
		fmt.Fprintf(&b, "    <string>%s</string>\n", xmlEscape(arg))
	}
	b.WriteString(`  </array>
  <key>StartCalendarInterval</key>
  <dict>
`)
	for _, entry := range cal {
		fmt.Fprintf(&b, "    <key>%s</key>\n", entry.Key)
		fmt.Fprintf(&b, "    <integer>%d</integer>\n", entry.Value)
	}
	logPath := filepath.Join(home, "Library", "Logs", "sentra-"+name+".log")
	fmt.Fprintf(&b, `  </dict>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, xmlEscape(logPath), xmlEscape(logPath))
	return b.String(), nil
}

type launchdCalendarEntry struct {
	Key   string
	Value int
}

func launchdCalendar(schedule config.PolicySchedule) ([]launchdCalendarEntry, error) {
	s := policycfg.NormalizeSchedule(schedule)
	hour, minute, err := scheduleClock(s)
	if err != nil {
		return nil, err
	}
	entries := []launchdCalendarEntry{
		{Key: "Hour", Value: hour},
		{Key: "Minute", Value: minute},
	}
	switch s.Cadence {
	case policycfg.CadenceDaily:
		return entries, nil
	case policycfg.CadenceWeekly:
		entries = append(entries, launchdCalendarEntry{Key: "Weekday", Value: launchdWeekday(s.Weekday)})
		return entries, nil
	case policycfg.CadenceMonthly:
		entries = append(entries, launchdCalendarEntry{Key: "Day", Value: 1})
		return entries, nil
	default:
		return nil, fmt.Errorf("unsupported launchd cadence %q", s.Cadence)
	}
}

func renderSystemdUserUnits(exe, cfgPath, name string, schedule config.PolicySchedule) (service, timer string, err error) {
	onCalendar, err := systemdOnCalendar(schedule)
	if err != nil {
		return "", "", err
	}
	execStart := strings.Join([]string{
		systemdQuoteArg(exe),
		"policy",
		"run",
		systemdQuoteArg(name),
		"--config",
		systemdQuoteArg(cfgPath),
		"--log-level",
		"info",
	}, " ")
	service = fmt.Sprintf(`[Unit]
Description=Sentra policy %s backup

[Service]
Type=oneshot
ExecStart=%s
`, name, execStart)
	timer = fmt.Sprintf(`[Unit]
Description=Sentra policy %s schedule

[Timer]
OnCalendar=%s
Persistent=true
Unit=sentra-%s.service

[Install]
WantedBy=timers.target
`, name, onCalendar, name)
	return service, timer, nil
}

func systemdOnCalendar(schedule config.PolicySchedule) (string, error) {
	s := policycfg.NormalizeSchedule(schedule)
	switch s.Cadence {
	case policycfg.CadenceHourly:
		return "hourly", nil
	case policycfg.CadenceDaily:
		return systemdDailyCalendar(s)
	case policycfg.CadenceWeekly:
		_, _, err := scheduleClock(s)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s *-*-* %s:00", systemdWeekday(s.Weekday), s.At), nil
	case policycfg.CadenceMonthly:
		_, _, err := scheduleClock(s)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("*-*-01 %s:00", s.At), nil
	default:
		return "", fmt.Errorf("unsupported systemd cadence %q", s.Cadence)
	}
}

func systemdDailyCalendar(s config.PolicySchedule) (string, error) {
	_, _, err := scheduleClock(s)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("*-*-* %s:00", s.At), nil
}

func scheduleClock(schedule config.PolicySchedule) (int, int, error) {
	hourText, minuteText, ok := strings.Cut(schedule.At, ":")
	if !ok {
		return 0, 0, fmt.Errorf("schedule requires HH:MM")
	}
	hour, err := strconv.Atoi(hourText)
	if err != nil {
		return 0, 0, fmt.Errorf("schedule hour: %w", err)
	}
	minute, err := strconv.Atoi(minuteText)
	if err != nil {
		return 0, 0, fmt.Errorf("schedule minute: %w", err)
	}
	return hour, minute, nil
}

func launchdWeekday(day string) int {
	switch strings.ToLower(day) {
	case "mon":
		return 1
	case "tue":
		return 2
	case "wed":
		return 3
	case "thu":
		return 4
	case "fri":
		return 5
	case "sat":
		return 6
	default:
		return 0
	}
}

func systemdWeekday(day string) string {
	switch strings.ToLower(day) {
	case "mon":
		return "Mon"
	case "tue":
		return "Tue"
	case "wed":
		return "Wed"
	case "thu":
		return "Thu"
	case "fri":
		return "Fri"
	case "sat":
		return "Sat"
	default:
		return "Sun"
	}
}

func scheduleStdout(cmd *cobra.Command, deps ScheduleDeps) io.Writer {
	if deps.Stdout != nil {
		return deps.Stdout
	}
	return cmd.OutOrStdout()
}

func xmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}

func systemdQuoteArg(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\n\"'\\") {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return `"` + s + `"`
	}
	return s
}
