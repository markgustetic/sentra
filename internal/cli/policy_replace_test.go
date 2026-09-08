package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/scheduler"
)

// replaceFixture writes a daily@03:00 policy "home" with its darwin
// timer installed under a private home, returning the plist path, the
// fake runner, and deps whose Runner is that fake.
func replaceFixture(t *testing.T) (deps PolicyDeps, plist string, runner *fakeSchedRunner, out *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	chDir(t, dir)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["home"] = config.PolicyConfig{
		Paths:    []string{"/data"},
		Schedule: config.PolicySchedule{Cadence: "daily", At: "03:00"},
	}
	cfgPath := writePolicyConfigFile(t, dir, &cfg)
	home := filepath.Join(dir, "home")
	paths, err := scheduler.PathsFor("darwin", home, "home")
	if err != nil {
		t.Fatal(err)
	}
	files, err := scheduler.Render(paths, "/usr/local/bin/sentra", cfgPath, "home", cfg.Policies["home"].Schedule)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Install(files); err != nil {
		t.Fatal(err)
	}
	runner = &fakeSchedRunner{}
	out = &bytes.Buffer{}
	deps = PolicyDeps{
		RepoDeps:   RepoDeps{Stdout: out},
		OS:         "darwin",
		HomeDir:    func() (string, error) { return home, nil },
		Executable: func() (string, error) { return "/usr/local/bin/sentra", nil },
		Runner:     runner.run,
	}
	return deps, paths.Files[0], runner, out
}

func runPolicyAddReplace(t *testing.T, deps PolicyDeps, out *bytes.Buffer, args ...string) {
	t.Helper()
	cmd := NewPolicy(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"add", "home", "--path", "/data", "--replace"}, args...))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

// TestPolicyAddReplace_ResyncsInstalledTimer: a --replace that changes
// the schedule must re-render and re-bootstrap the installed timer, or
// launchd keeps the old calendar while `schedule status` shows the new
// one.
func TestPolicyAddReplace_ResyncsInstalledTimer(t *testing.T) {
	deps, plist, runner, out := replaceFixture(t)
	runPolicyAddReplace(t, deps, out, "--schedule", "hourly")

	raw, err := os.ReadFile(plist) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatalf("plist gone after resync: %v", err)
	}
	if strings.Contains(string(raw), "<key>Hour</key>") {
		t.Errorf("plist still carries the daily calendar:\n%s", raw)
	}
	if !runner.ran("launchctl bootstrap") {
		t.Errorf("timer not re-bootstrapped; calls: %v", runner.calls)
	}
	if !strings.Contains(out.String(), "timer reinstalled") {
		t.Errorf("output must say the timer was reinstalled:\n%s", out.String())
	}
}

// TestPolicyAddReplace_ManualUninstallsTimer: editing an installed
// policy to manual must unload and remove its timer — a manual policy
// firing on the old cadence is worse than a stale one.
func TestPolicyAddReplace_ManualUninstallsTimer(t *testing.T) {
	deps, plist, runner, out := replaceFixture(t)
	runPolicyAddReplace(t, deps, out, "--schedule", "manual")

	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Errorf("plist must be removed for a manual schedule (stat err=%v)", err)
	}
	if !runner.ran("launchctl bootout") {
		t.Errorf("timer not unloaded; calls: %v", runner.calls)
	}
	if !strings.Contains(out.String(), "timer uninstalled") {
		t.Errorf("output must say the timer was uninstalled:\n%s", out.String())
	}
}

// TestPolicyAddReplace_UnchangedScheduleLeavesTimerAlone: a --replace
// that only edits paths or tags must not shell out to launchctl.
func TestPolicyAddReplace_UnchangedScheduleLeavesTimerAlone(t *testing.T) {
	deps, plist, runner, out := replaceFixture(t)
	before, err := os.ReadFile(plist) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	runPolicyAddReplace(t, deps, out, "--schedule", "daily@03:00", "--tag", "extra")

	after, err := os.ReadFile(plist) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("plist rewritten though the schedule did not change")
	}
	if len(runner.calls) != 0 {
		t.Errorf("launchctl invoked for an unchanged schedule: %v", runner.calls)
	}
}
