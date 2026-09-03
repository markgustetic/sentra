package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/scheduler"
)

// fakeExitErr stands in for *exec.ExitError so the scheduler helpers read
// it as "the OS answered no", not "the command never ran".
type fakeExitErr int

func (e fakeExitErr) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e fakeExitErr) ExitCode() int { return int(e) }

// fakeSchedRunner records every launchctl/systemctl line and fails the
// ones listed in fail with that output. It is the only Runner any CLI test
// may use: the real one would load a job on the developer's machine.
type fakeSchedRunner struct {
	calls []string
	fail  map[string]string
}

func (f *fakeSchedRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	if out, ok := f.fail[line]; ok {
		return []byte(out), fakeExitErr(1)
	}
	return nil, nil
}

func (f *fakeSchedRunner) ran(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func guiDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func runSchedule(t *testing.T, deps ScheduleDeps, args ...string) (string, error) {
	t.Helper()
	out := &bytes.Buffer{}
	deps.Stdout = out
	cmd := NewSchedule(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func linuxScheduleDeps(home string, f *fakeSchedRunner) ScheduleDeps {
	return ScheduleDeps{
		OS:         "linux",
		HomeDir:    func() (string, error) { return home, nil },
		Executable: func() (string, error) { return "/usr/bin/sentra", nil },
		Runner:     f.run,
	}
}

// Writing the unit files is not enough: nothing runs them until systemd
// (or launchd) is told. Install must enable the timer right away.
func TestScheduleInstall_ActivatesTheTimer(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfgPath := scheduleConfigWithPolicy(t, dir, "home", config.PolicySchedule{Cadence: "daily", At: "03:00"})
	home := filepath.Join(dir, "home")
	f := &fakeSchedRunner{}

	out, err := runSchedule(t, linuxScheduleDeps(home, f), "install", "home", "--config", cfgPath)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now sentra-home.timer",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands = %q, want %q", f.calls, want)
	}
	if !strings.Contains(out, "timer:    active") {
		t.Fatalf("install output must report the timer as active:\n%s", out)
	}
}

func TestScheduleInstall_DarwinBootstrapsTheLaunchAgent(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfgPath := scheduleConfigWithPolicy(t, dir, "home", config.PolicySchedule{Cadence: "daily", At: "03:00"})
	home := filepath.Join(dir, "home")
	f := &fakeSchedRunner{}
	deps := ScheduleDeps{
		OS:         "darwin",
		HomeDir:    func() (string, error) { return home, nil },
		Executable: func() (string, error) { return "/usr/local/bin/sentra", nil },
		Runner:     f.run,
	}
	if _, err := runSchedule(t, deps, "install", "home", "--config", cfgPath); err != nil {
		t.Fatalf("install: %v", err)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.sentra.home.plist")
	if !f.ran("launchctl bootstrap " + guiDomain() + " " + plist) {
		t.Fatalf("install must bootstrap the plist into the gui domain, ran %q", f.calls)
	}
}

// Headless (no user bus): the files must survive, the command must fail so
// scripts notice, and the message must hand over the exact command.
func TestScheduleInstall_ActivationFailureKeepsFilesAndNamesCommand(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfgPath := scheduleConfigWithPolicy(t, dir, "home", config.PolicySchedule{Cadence: "daily", At: "03:00"})
	home := filepath.Join(dir, "home")
	f := &fakeSchedRunner{fail: map[string]string{
		"systemctl --user enable --now sentra-home.timer": "Failed to connect to bus: No medium found",
	}}

	out, err := runSchedule(t, linuxScheduleDeps(home, f), "install", "home", "--config", cfgPath)
	var aerr *scheduler.ActivationError
	if !errors.As(err, &aerr) {
		t.Fatalf("install err = %v, want *scheduler.ActivationError", err)
	}
	for _, want := range []string{
		"systemctl --user daemon-reload && systemctl --user enable --now sentra-home.timer",
		"No medium found",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
	timer := filepath.Join(home, ".config", "systemd", "user", "sentra-home.timer")
	if _, statErr := os.Stat(timer); statErr != nil {
		t.Fatalf("activation failure must leave the files in place: %v", statErr)
	}
	if !strings.Contains(out, "Schedule installed") || !strings.Contains(out, "timer:    not active") {
		t.Fatalf("output must still summarize the install and say the timer is not active:\n%s", out)
	}
}

func TestScheduleStatus_ReportsTimerActivity(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfgPath := scheduleConfigWithPolicy(t, dir, "home", config.PolicySchedule{Cadence: "daily", At: "03:00"})
	home := filepath.Join(dir, "home")
	f := &fakeSchedRunner{}
	if _, err := runSchedule(t, linuxScheduleDeps(home, f), "install", "home", "--config", cfgPath); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Active: systemctl is-active exits 0.
	f = &fakeSchedRunner{}
	out, err := runSchedule(t, linuxScheduleDeps(home, f), "status", "home", "--config", cfgPath)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !f.ran("systemctl --user is-active sentra-home.timer") {
		t.Fatalf("status must ask systemd, ran %q", f.calls)
	}
	if !strings.Contains(out, "timer:  active") || !strings.Contains(out, "next run:") {
		t.Fatalf("active status output:\n%s", out)
	}

	// Installed but not loaded: say so, hand over the command, and do not
	// promise a next run the OS will never deliver.
	f = &fakeSchedRunner{fail: map[string]string{"systemctl --user is-active sentra-home.timer": "inactive"}}
	out, err = runSchedule(t, linuxScheduleDeps(home, f), "status", "home", "--config", cfgPath)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "timer:  not active") ||
		!strings.Contains(out, "systemctl --user daemon-reload && systemctl --user enable --now sentra-home.timer") {
		t.Fatalf("inactive status must name the activation command:\n%s", out)
	}
	if strings.Contains(out, "next run:") {
		t.Fatalf("inactive status must not print a next run:\n%s", out)
	}

	// Could not ask (no bus): unknown, not "inactive" — and status itself
	// still succeeds, since the files are what it reports on.
	f = &fakeSchedRunner{fail: map[string]string{"systemctl --user is-active sentra-home.timer": "Failed to connect to bus: No medium found"}}
	out, err = runSchedule(t, linuxScheduleDeps(home, f), "status", "home", "--config", cfgPath)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "timer:  unknown") || !strings.Contains(out, "No medium found") {
		t.Fatalf("unqueryable status output:\n%s", out)
	}
}

// Not installed: no files, so nothing to ask the OS about.
func TestScheduleStatus_NotInstalledDoesNotQueryOS(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfgPath := scheduleConfigWithPolicy(t, dir, "home", config.PolicySchedule{Cadence: "daily", At: "03:00"})
	f := &fakeSchedRunner{}
	out, err := runSchedule(t, linuxScheduleDeps(filepath.Join(dir, "home"), f), "status", "home", "--config", cfgPath)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("not-installed status must not shell out, ran %q", f.calls)
	}
	if !strings.Contains(out, "not installed") {
		t.Fatalf("status output:\n%s", out)
	}
}

func TestScheduleUninstall_DeactivatesBeforeRemovingFiles(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfgPath := scheduleConfigWithPolicy(t, dir, "home", config.PolicySchedule{Cadence: "daily", At: "03:00"})
	home := filepath.Join(dir, "home")
	if _, err := runSchedule(t, linuxScheduleDeps(home, &fakeSchedRunner{}), "install", "home", "--config", cfgPath); err != nil {
		t.Fatalf("install: %v", err)
	}
	timer := filepath.Join(home, ".config", "systemd", "user", "sentra-home.timer")

	var sawTimerFile bool
	f := &fakeSchedRunner{}
	deps := linuxScheduleDeps(home, f)
	deps.Runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		// systemd resolves disable from the unit on disk: the file must
		// still exist when we ask.
		if _, err := os.Stat(timer); err == nil {
			sawTimerFile = true
		}
		return f.run(ctx, name, args...)
	}
	out, err := runSchedule(t, deps, "uninstall", "home", "--config", cfgPath)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if !f.ran("systemctl --user disable --now sentra-home.timer") {
		t.Fatalf("uninstall must disable the timer, ran %q", f.calls)
	}
	if !sawTimerFile {
		t.Fatal("uninstall must deactivate BEFORE removing the unit files")
	}
	if _, statErr := os.Stat(timer); !os.IsNotExist(statErr) {
		t.Fatalf("timer file should be gone, stat err=%v", statErr)
	}
	if !strings.Contains(out, "Schedule removed") || !strings.Contains(out, "timer:  stopped") {
		t.Fatalf("uninstall output:\n%s", out)
	}
}

func TestScheduleUninstall_DeactivationFailureStillRemovesFiles(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfgPath := scheduleConfigWithPolicy(t, dir, "home", config.PolicySchedule{Cadence: "daily", At: "03:00"})
	home := filepath.Join(dir, "home")
	if _, err := runSchedule(t, linuxScheduleDeps(home, &fakeSchedRunner{}), "install", "home", "--config", cfgPath); err != nil {
		t.Fatalf("install: %v", err)
	}
	f := &fakeSchedRunner{fail: map[string]string{
		"systemctl --user disable --now sentra-home.timer": "Failed to connect to bus: No medium found",
	}}
	out, err := runSchedule(t, linuxScheduleDeps(home, f), "uninstall", "home", "--config", cfgPath)
	var aerr *scheduler.ActivationError
	if !errors.As(err, &aerr) {
		t.Fatalf("uninstall err = %v, want *scheduler.ActivationError", err)
	}
	if !strings.Contains(err.Error(), "systemctl --user disable --now sentra-home.timer") {
		t.Fatalf("error must name the command: %v", err)
	}
	timer := filepath.Join(home, ".config", "systemd", "user", "sentra-home.timer")
	if _, statErr := os.Stat(timer); !os.IsNotExist(statErr) {
		t.Fatalf("files must be removed even when the OS could not be told, stat err=%v", statErr)
	}
	if !strings.Contains(out, "Schedule removed") {
		t.Fatalf("uninstall output:\n%s", out)
	}
}

// policy remove's timer cleanup must stop the live job too, not just
// delete the files it was loaded from.
func TestPolicyRemove_DeactivatesTimer(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["home"] = config.PolicyConfig{
		Paths:    []string{"."},
		Schedule: config.PolicySchedule{Cadence: "daily", At: "03:00"},
	}
	writePolicyConfigFile(t, dir, &cfg)
	home := filepath.Join(dir, "home")
	paths, err := scheduler.PathsFor("darwin", home, "home")
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Install(map[string]string{paths.Files[0]: "timer"}); err != nil {
		t.Fatal(err)
	}

	f := &fakeSchedRunner{}
	out := &bytes.Buffer{}
	cmd := NewPolicy(PolicyDeps{
		RepoDeps: RepoDeps{Stdout: out},
		OS:       "darwin",
		HomeDir:  func() (string, error) { return home, nil },
		Runner:   f.run,
	})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"remove", "home"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !f.ran("launchctl bootout " + guiDomain() + "/com.sentra.home") {
		t.Fatalf("remove must boot the job out, ran %q", f.calls)
	}
	if installed, _ := scheduler.Installed(paths); installed {
		t.Fatal("remove must still uninstall the files")
	}
}
