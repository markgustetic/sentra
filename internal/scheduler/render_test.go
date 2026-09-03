package scheduler

import (
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
)

func TestRender_DarwinLaunchAgent(t *testing.T) {
	paths, err := PathsFor("darwin", "/home/u", "home")
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	files, err := Render(paths, "/usr/local/bin/sentra", "/etc/sentra.yaml", "home",
		config.PolicySchedule{Cadence: "daily", At: "03:00"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	body := files[paths.Files[0]]
	for _, want := range []string{
		"com.sentra.home", "/usr/local/bin/sentra", "policy", "run", "home",
		"--config", "/etc/sentra.yaml",
		"<key>Hour</key>", "<integer>3</integer>",
		"<key>Minute</key>", "<integer>0</integer>",
		// Catch-up: launchd skips a StartCalendarInterval slot that
		// passes while the machine is off, so the agent also runs at
		// load (login) and lets --if-due decide whether that run is owed.
		"<key>RunAtLoad</key>\n  <true/>",
		"<string>--if-due</string>",
		"<string>--startup-delay</string>\n    <string>1m</string>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("plist missing %q:\n%s", want, body)
		}
	}
	wantArgs := "<string>/usr/local/bin/sentra</string>\n" +
		"    <string>policy</string>\n" +
		"    <string>run</string>\n" +
		"    <string>home</string>\n" +
		"    <string>--if-due</string>\n" +
		"    <string>--startup-delay</string>\n" +
		"    <string>1m</string>\n" +
		"    <string>--config</string>\n" +
		"    <string>/etc/sentra.yaml</string>\n" +
		"    <string>--log-level</string>\n" +
		"    <string>info</string>\n"
	if !strings.Contains(body, wantArgs) {
		t.Fatalf("plist ProgramArguments differ from the one command shape:\n%s", body)
	}
}

func TestRender_LinuxSystemdUnits(t *testing.T) {
	paths, err := PathsFor("linux", "/home/u", "home")
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	files, err := Render(paths, "/usr/bin/sentra", "/etc/sentra.yaml", "home",
		config.PolicySchedule{Cadence: "weekly", Weekday: "mon", At: "04:30"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	service := files[paths.Files[0]]
	timer := files[paths.Files[1]]
	// One command shape on both platforms: --if-due is redundant with
	// Persistent=true here but keeps the units in step with launchd.
	wantExec := "ExecStart=/usr/bin/sentra policy run home --if-due --startup-delay 1m --config /etc/sentra.yaml --log-level info\n"
	if !strings.Contains(service, wantExec) {
		t.Fatalf("service missing %q:\n%s", wantExec, service)
	}
	if !strings.Contains(timer, "OnCalendar=Mon *-*-* 04:30:00") {
		t.Fatalf("timer missing weekly calendar:\n%s", timer)
	}
}

func TestRender_RejectsManualCadence(t *testing.T) {
	paths, _ := PathsFor("linux", "/home/u", "home")
	if _, err := Render(paths, "/usr/bin/sentra", "/etc/sentra.yaml", "home",
		config.PolicySchedule{Cadence: "manual"}); err == nil {
		t.Fatal("manual cadence must be rejected by Render")
	}
}

func TestRender_RejectsUnsupportedOS(t *testing.T) {
	if _, err := PathsFor("plan9", "/home/u", "home"); err == nil {
		t.Fatal("PathsFor(plan9) must error")
	}
	if _, err := Render(Paths{OS: "plan9", Files: []string{"x"}}, "/bin/sentra", "/c.yaml", "home",
		config.PolicySchedule{Cadence: "daily", At: "03:00"}); err == nil {
		t.Fatal("Render with unsupported OS must error")
	}
}

// TestRender_RejectsSignedClock: the systemd calendar renderer must reject a
// signed clock rather than emitting a malformed "*-*-* +9:00:00" OnCalendar
// spec that systemd silently refuses to schedule.
func TestRender_RejectsSignedClock(t *testing.T) {
	for _, cad := range []string{"daily", "weekly", "monthly"} {
		if _, err := systemdOnCalendar(config.PolicySchedule{Cadence: cad, At: "+9:00", Weekday: "mon"}); err == nil {
			t.Errorf("systemdOnCalendar(%s, +9:00) = nil error, want a rejection", cad)
		}
	}
	got, err := systemdOnCalendar(config.PolicySchedule{Cadence: "daily", At: "09:00"})
	if err != nil {
		t.Fatalf("valid clock rejected: %v", err)
	}
	if !strings.Contains(got, "09:00:00") {
		t.Errorf("daily 09:00 rendered as %q, want it to contain 09:00:00", got)
	}
}

// TestRender_EveryCadenceRendersOnEveryOS is a rule test: every cadence the
// policy package can install must render on every scheduler OS. The TUI
// wizard offers the cadence list regardless of platform, so a cadence one
// renderer accepts and the other rejects surfaces only at timer install —
// exactly what happened when launchd demanded HH:MM from an hourly schedule.
func TestRender_EveryCadenceRendersOnEveryOS(t *testing.T) {
	schedules := []config.PolicySchedule{
		{Cadence: policycfg.CadenceHourly},
		{Cadence: policycfg.CadenceDaily, At: "03:00"},
		{Cadence: policycfg.CadenceWeekly, Weekday: "mon", At: "04:30"},
		{Cadence: policycfg.CadenceMonthly, At: "05:15"},
	}
	for _, goos := range []string{"darwin", "linux"} {
		for _, s := range schedules {
			t.Run(goos+"/"+s.Cadence, func(t *testing.T) {
				paths, err := PathsFor(goos, "/home/u", "home")
				if err != nil {
					t.Fatalf("PathsFor: %v", err)
				}
				if _, err := Render(paths, "/usr/bin/sentra", "/etc/sentra.yaml", "home", s); err != nil {
					t.Fatalf("Render(%s, %+v): %v", goos, s, err)
				}
			})
		}
	}
}

// An hourly launch agent fires at the top of every hour: a
// StartCalendarInterval dict carrying Minute 0 and nothing else, matching
// policy.NextRun's "hourly at minute 0". An Hour key would pin it to one
// hour a day; a Weekday or Day key would pin it further still.
func TestRender_DarwinHourlyFiresAtTopOfEveryHour(t *testing.T) {
	cal, err := launchdCalendar(config.PolicySchedule{Cadence: policycfg.CadenceHourly})
	if err != nil {
		t.Fatalf("launchdCalendar: %v", err)
	}
	if len(cal) != 1 || cal[0].Key != "Minute" || cal[0].Value != 0 {
		t.Fatalf("want [{Minute 0}], got %+v", cal)
	}
}
