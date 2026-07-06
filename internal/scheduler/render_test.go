package scheduler

import (
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
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
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("plist missing %q:\n%s", want, body)
		}
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
	for _, want := range []string{"/usr/bin/sentra", "policy run home", "--config", "/etc/sentra.yaml", "--log-level info"} {
		if !strings.Contains(service, want) {
			t.Fatalf("service missing %q:\n%s", want, service)
		}
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
