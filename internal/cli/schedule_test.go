package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func scheduleConfigWithPolicy(t *testing.T, dir string, policyName string, schedule config.PolicySchedule) string {
	t.Helper()
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies[policyName] = config.PolicyConfig{
		Paths:    []string{"~/Documents"},
		Schedule: schedule,
	}
	return writePolicyConfigFile(t, dir, &cfg)
}

func TestScheduleInstall_DarwinWritesLaunchAgent(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfgPath := scheduleConfigWithPolicy(t, dir, "home", config.PolicySchedule{Cadence: "daily", At: "03:00"})
	home := filepath.Join(dir, "home")
	out := &bytes.Buffer{}

	cmd := NewSchedule(ScheduleDeps{
		OS:         "darwin",
		HomeDir:    func() (string, error) { return home, nil },
		Executable: func() (string, error) { return "/usr/local/bin/sentra", nil },
		Stdout:     out,
	})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"install", "home", "--config", cfgPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.sentra.home.plist")
	raw, err := os.ReadFile(plistPath) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"com.sentra.home",
		"/usr/local/bin/sentra",
		"policy",
		"run",
		"home",
		"--config",
		cfgPath,
		"<key>Hour</key>",
		"<integer>3</integer>",
		"<key>Minute</key>",
		"<integer>0</integer>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("plist missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(out.String(), "Schedule installed") {
		t.Fatalf("output missing install summary: %q", out.String())
	}
}

func TestScheduleInstall_LinuxWritesSystemdUserFiles(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfgPath := scheduleConfigWithPolicy(t, dir, "home", config.PolicySchedule{Cadence: "weekly", Weekday: "mon", At: "04:30"})
	home := filepath.Join(dir, "home")
	out := &bytes.Buffer{}

	cmd := NewSchedule(ScheduleDeps{
		OS:         "linux",
		HomeDir:    func() (string, error) { return home, nil },
		Executable: func() (string, error) { return "/usr/bin/sentra", nil },
		Stdout:     out,
	})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"install", "home", "--config", cfgPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	unitDir := filepath.Join(home, ".config", "systemd", "user")
	serviceRaw, err := os.ReadFile(filepath.Join(unitDir, "sentra-home.service")) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatalf("read service: %v", err)
	}
	timerRaw, err := os.ReadFile(filepath.Join(unitDir, "sentra-home.timer")) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatalf("read timer: %v", err)
	}
	service := string(serviceRaw)
	timer := string(timerRaw)
	for _, want := range []string{"/usr/bin/sentra", "policy run home", "--config", cfgPath, "--log-level info"} {
		if !strings.Contains(service, want) {
			t.Fatalf("service missing %q:\n%s", want, service)
		}
	}
	if !strings.Contains(timer, "OnCalendar=Mon *-*-* 04:30:00") {
		t.Fatalf("timer missing weekly calendar:\n%s", timer)
	}
}

func TestScheduleStatusAndUninstall(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfgPath := scheduleConfigWithPolicy(t, dir, "home", config.PolicySchedule{Cadence: "hourly"})
	home := filepath.Join(dir, "home")
	out := &bytes.Buffer{}
	deps := ScheduleDeps{
		OS:         "linux",
		HomeDir:    func() (string, error) { return home, nil },
		Executable: func() (string, error) { return "/usr/bin/sentra", nil },
		Stdout:     out,
	}

	cmd := NewSchedule(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"install", "home", "--config", cfgPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("install execute: %v", err)
	}

	out.Reset()
	cmd = NewSchedule(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"status", "home", "--config", cfgPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status execute: %v", err)
	}
	if !strings.Contains(out.String(), "installed") {
		t.Fatalf("status output: %q", out.String())
	}

	out.Reset()
	cmd = NewSchedule(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"uninstall", "home", "--config", cfgPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("uninstall execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "sentra-home.timer")); !os.IsNotExist(err) {
		t.Fatalf("timer should be removed, stat err=%v", err)
	}
	if !strings.Contains(out.String(), "Schedule removed") {
		t.Fatalf("uninstall output: %q", out.String())
	}
}

func TestScheduleInstall_RejectsManualPolicy(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfgPath := scheduleConfigWithPolicy(t, dir, "home", config.PolicySchedule{Cadence: "manual"})

	cmd := NewSchedule(ScheduleDeps{
		OS:         "linux",
		HomeDir:    func() (string, error) { return filepath.Join(dir, "home"), nil },
		Executable: func() (string, error) { return "/usr/bin/sentra", nil },
		Stdout:     io.Discard,
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"install", "home", "--config", cfgPath})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "manual") {
		t.Fatalf("error: got %v, want manual schedule error", err)
	}
}

func TestScheduleInstall_RejectsUnsupportedOS(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfgPath := scheduleConfigWithPolicy(t, dir, "home", config.PolicySchedule{Cadence: "daily", At: "03:00"})

	cmd := NewSchedule(ScheduleDeps{
		OS:         "plan9",
		HomeDir:    func() (string, error) { return filepath.Join(dir, "home"), nil },
		Executable: func() (string, error) { return "/bin/sentra", nil },
		Stdout:     io.Discard,
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"install", "home", "--config", cfgPath})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error: got %v, want unsupported OS error", err)
	}
}

// The cron/launchd artifact must embed the discovered ABSOLUTE config
// path: cron's cwd is arbitrary, so a relative or undiscovered path would
// break every scheduled run.
func TestScheduleInstall_DiscoversHomeConfigAndEmbedsAbsolutePath(t *testing.T) {
	xdg := t.TempDir()
	chDir(t, t.TempDir()) // empty cwd: no ./sentra.yaml
	t.Setenv("XDG_CONFIG_HOME", xdg)

	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["home"] = config.PolicyConfig{
		Paths:    []string{"~/Documents"},
		Schedule: config.PolicySchedule{Cadence: "daily", At: "03:00"},
	}
	cfgPath := filepath.Join(xdg, "sentra", "sentra.yaml")
	if err := config.Write(cfgPath, &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	home := filepath.Join(t.TempDir(), "home")
	out := &bytes.Buffer{}
	cmd := NewSchedule(ScheduleDeps{
		OS:         "darwin",
		HomeDir:    func() (string, error) { return home, nil },
		Executable: func() (string, error) { return "/usr/local/bin/sentra", nil },
		Stdout:     out,
	})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"install", "home"}) // no --config: discovery must find the XDG path
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "com.sentra.home.plist"))
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	if !strings.Contains(string(raw), cfgPath) {
		t.Errorf("launch agent does not embed the discovered absolute config path %q:\n%s", cfgPath, raw)
	}
}
