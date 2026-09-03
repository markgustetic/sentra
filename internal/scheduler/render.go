// Package scheduler renders and installs user-level OS scheduler entries
// (launchd plists on darwin, systemd user units on linux) that run
// `sentra policy run`. It is a pure/filesystem-only helper: it never opens
// a repository, takes the repo lock, or touches the bucket. Imports are
// limited to internal/config and internal/policy so the TUI and CLI can both
// depend on it without pulling in cobra or the repo layer.
package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
)

// Paths locates the OS scheduler files for one policy.
type Paths struct {
	OS    string
	Home  string
	Name  string
	Files []string
}

// PathsFor validates name and returns the per-OS scheduler file paths.
// goos "" defaults to runtime.GOOS; home "" defaults to os.UserHomeDir().
func PathsFor(goos, home, name string) (Paths, error) {
	if err := policycfg.ValidateName(name); err != nil {
		return Paths{}, err
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("locate home dir: %w", err)
		}
		home = h
	}
	switch goos {
	case "darwin":
		return Paths{
			OS:    goos,
			Home:  home,
			Name:  name,
			Files: []string{filepath.Join(home, "Library", "LaunchAgents", "com.sentra."+name+".plist")},
		}, nil
	case "linux":
		dir := filepath.Join(home, ".config", "systemd", "user")
		return Paths{
			OS:   goos,
			Home: home,
			Name: name,
			Files: []string{
				filepath.Join(dir, "sentra-"+name+".service"),
				filepath.Join(dir, "sentra-"+name+".timer"),
			},
		}, nil
	default:
		return Paths{}, fmt.Errorf("unsupported scheduler OS %q; supported: darwin, linux", goos)
	}
}

// Executable normalizes exe to a cleaned absolute path. exe "" defaults to
// os.Executable().
func Executable(exe string) (string, error) {
	if exe == "" {
		e, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("locate sentra executable: %w", err)
		}
		exe = e
	}
	if !filepath.IsAbs(exe) {
		abs, err := filepath.Abs(exe)
		if err != nil {
			return "", fmt.Errorf("resolve sentra executable: %w", err)
		}
		exe = abs
	}
	return filepath.Clean(exe), nil
}

// Render returns path->file-body for every file the policy installs.
func Render(paths Paths, exe, cfgPath, name string, schedule config.PolicySchedule) (map[string]string, error) {
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
	args := policyRunArgs(exe, cfgPath, name)
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
	// RunAtLoad: launchd skips a StartCalendarInterval slot that passes
	// while the machine is shut down (it only catches up after sleep),
	// so the agent also runs when it loads — at login — and --if-due
	// decides whether that run is owed. --startup-delay lets the
	// network and keyring settle first.
	b.WriteString(`  </array>
  <key>RunAtLoad</key>
  <true/>
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

// policyRunArgs is the one command both platforms install:
// `sentra policy run <name> --if-due --startup-delay 1m --config <path>
// --log-level info`. --if-due makes a login-time launch a no-op when
// the last run already covers the schedule's most recent slot, so the
// catch-up never doubles a backup that fired on time.
func policyRunArgs(exe, cfgPath, name string) []string {
	return []string{
		exe, "policy", "run", name,
		"--if-due",
		"--startup-delay", "1m",
		"--config", cfgPath,
		"--log-level", "info",
	}
}

type launchdCalendarEntry struct {
	Key   string
	Value int
}

func launchdCalendar(schedule config.PolicySchedule) ([]launchdCalendarEntry, error) {
	s := policycfg.NormalizeSchedule(schedule)
	// Hourly carries no clock, so it must branch before scheduleClock:
	// a Minute-only StartCalendarInterval matches every hour at :00, the
	// same instant policy.NextRun computes. Adding an Hour key would pin
	// it to one firing a day.
	if s.Cadence == policycfg.CadenceHourly {
		return []launchdCalendarEntry{{Key: "Minute", Value: 0}}, nil
	}
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
	// systemd already catches up a missed slot via Persistent=true, so
	// --if-due is redundant here; it is passed anyway so both platforms
	// install one command shape.
	args := policyRunArgs(exe, cfgPath, name)
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = systemdQuoteArg(arg)
	}
	execStart := strings.Join(quoted, " ")
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
		hh, mm, err := scheduleClock(s)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s *-*-* %02d:%02d:00", systemdWeekday(s.Weekday), hh, mm), nil
	case policycfg.CadenceMonthly:
		hh, mm, err := scheduleClock(s)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("*-*-01 %02d:%02d:00", hh, mm), nil
	default:
		return "", fmt.Errorf("unsupported systemd cadence %q", s.Cadence)
	}
}

func systemdDailyCalendar(s config.PolicySchedule) (string, error) {
	// Render from the parsed hour/minute rather than interpolating s.At
	// verbatim, so a malformed clock can never reach the OnCalendar spec.
	hh, mm, err := scheduleClock(s)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("*-*-* %02d:%02d:00", hh, mm), nil
}

func scheduleClock(schedule config.PolicySchedule) (int, int, error) {
	hourText, minuteText, ok := strings.Cut(schedule.At, ":")
	if !ok {
		return 0, 0, fmt.Errorf("schedule requires HH:MM")
	}
	// Require exactly two ASCII digits per field. strconv.Atoi accepts a
	// leading sign, so "+9" would parse to 9 and (on the systemd path)
	// render a malformed OnCalendar spec that never fires. Reject it here
	// too so the render path is robust independent of upstream validation.
	if !isTwoASCIIDigits(hourText) || !isTwoASCIIDigits(minuteText) {
		return 0, 0, fmt.Errorf("schedule requires HH:MM with two digits per field, got %q", schedule.At)
	}
	hour, err := strconv.Atoi(hourText)
	if err != nil || hour > 23 {
		return 0, 0, fmt.Errorf("schedule hour out of range: %q", hourText)
	}
	minute, err := strconv.Atoi(minuteText)
	if err != nil || minute > 59 {
		return 0, 0, fmt.Errorf("schedule minute out of range: %q", minuteText)
	}
	return hour, minute, nil
}

// isTwoASCIIDigits reports whether s is exactly two ASCII digits.
func isTwoASCIIDigits(s string) bool {
	return len(s) == 2 && s[0] >= '0' && s[0] <= '9' && s[1] >= '0' && s[1] <= '9'
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
