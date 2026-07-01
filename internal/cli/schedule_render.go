package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
)

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
