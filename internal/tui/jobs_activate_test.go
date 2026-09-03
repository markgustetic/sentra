package tui

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/scheduler"
)

// fakeExitErr stands in for *exec.ExitError: the scheduler helpers read an
// ExitCode() as "the OS answered no", not "the command never ran".
type fakeExitErr int

func (e fakeExitErr) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e fakeExitErr) ExitCode() int { return int(e) }

// fakeSchedRunner records every launchctl/systemctl line and fails the
// ones listed in fail with that output. Every TUI test that can reach a
// timer install, uninstall, or status query goes through one of these:
// the production runner would load a real job on the developer's Mac.
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

func launchdGUIDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// installAlphaFiles writes alpha's plist into the view's temp home without
// touching the OS — the "files present" precondition every activation
// test starts from.
func installAlphaFiles(t *testing.T, v JobsView, path string) scheduler.Paths {
	t.Helper()
	paths, err := scheduler.PathsFor("darwin", v.homeOverride, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	files, err := scheduler.Render(paths, "/usr/local/bin/sentra", path, "alpha",
		config.PolicySchedule{Cadence: "daily", At: "03:00"})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Install(files); err != nil {
		t.Fatal(err)
	}
	return paths
}

// confirmTimerOp runs the confirm for id through the view and feeds the
// resulting jobTimerMsg back, returning the reloaded view.
func confirmTimerOp(t *testing.T, v JobsView, id string) JobsView {
	t.Helper()
	m, cmd := v.Update(confirmedMsg{id: id})
	if cmd == nil {
		t.Fatalf("confirm %s must return the timer cmd", id)
	}
	m2, _ := m.(JobsView).Update(cmd())
	return m2.(JobsView)
}

func TestJobs_InstallActivatesTheTimer(t *testing.T) {
	deps, _ := jobsDeps(t)
	f := &fakeSchedRunner{}
	deps.SchedulerRunner = f.run
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	v.exeOverride = "/usr/local/bin/sentra"
	v.tbl.SetCursor(0) // alpha (daily)

	v = confirmTimerOp(t, v, jobInstallConfirmID)
	paths, _ := scheduler.PathsFor("darwin", v.homeOverride, "alpha")
	if !f.ran("launchctl bootstrap " + launchdGUIDomain() + " " + paths.Files[0]) {
		t.Fatalf("install must bootstrap the plist, ran %q", f.calls)
	}
	if !strings.Contains(v.notice, "active") {
		t.Fatalf("notice must say the timer is live: %q", v.notice)
	}
	if !v.rows[0].installed || !v.rows[0].active {
		t.Fatalf("row after install = %+v, want installed+active", v.rows[0])
	}
}

// Headless: the files stay, the row says so, and the notice carries the
// exact command — the operator should never have to guess it.
func TestJobs_InstallActivationFailureKeepsFilesAndNamesCommand(t *testing.T) {
	deps, _ := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	v.exeOverride = "/usr/local/bin/sentra"
	paths, _ := scheduler.PathsFor("darwin", v.homeOverride, "alpha")
	bootstrap := "launchctl bootstrap " + launchdGUIDomain() + " " + paths.Files[0]
	f := &fakeSchedRunner{fail: map[string]string{
		bootstrap: "Bootstrap failed: 125: Domain does not support specified action",
		"launchctl print " + launchdGUIDomain() + "/com.sentra.alpha": "Could not find service",
	}}
	v.deps.SchedulerRunner = f.run
	v.tbl.SetCursor(0)

	v = confirmTimerOp(t, v, jobInstallConfirmID)
	if installed, _ := scheduler.Installed(paths); !installed {
		t.Fatal("activation failure must leave the files in place")
	}
	if !strings.Contains(v.notice, bootstrap) || !strings.Contains(v.notice, "Domain does not support") {
		t.Fatalf("notice must name the command and the reason: %q", v.notice)
	}
	if !v.rows[0].installed || v.rows[0].active {
		t.Fatalf("row = %+v, want installed but not active", v.rows[0])
	}
}

func TestJobs_UninstallDeactivatesBeforeRemovingFiles(t *testing.T) {
	deps, path := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	paths := installAlphaFiles(t, v, path)
	var sawPlist bool
	f := &fakeSchedRunner{}
	v.deps.SchedulerRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if _, err := os.Stat(paths.Files[0]); err == nil {
			sawPlist = true
		}
		return f.run(ctx, name, args...)
	}
	v.reload()
	v.tbl.SetCursor(0)

	v = confirmTimerOp(t, v, jobUninstallConfirmID)
	if !f.ran("launchctl bootout " + launchdGUIDomain() + "/com.sentra.alpha") {
		t.Fatalf("uninstall must boot the job out, ran %q", f.calls)
	}
	if !sawPlist {
		t.Fatal("uninstall must deactivate BEFORE removing the plist")
	}
	if installed, _ := scheduler.Installed(paths); installed {
		t.Fatal("uninstall must remove the files")
	}
	if !strings.Contains(v.notice, "removed timer") {
		t.Fatalf("notice = %q", v.notice)
	}
}

func TestJobs_DeleteDeactivatesTimer(t *testing.T) {
	deps, path := jobsDeps(t)
	f := &fakeSchedRunner{}
	deps.SchedulerRunner = f.run
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	installAlphaFiles(t, v, path)
	v.reload()
	v.tbl.SetCursor(0)

	v = confirmTimerOp(t, v, jobDeleteConfirmID)
	if !f.ran("launchctl bootout " + launchdGUIDomain() + "/com.sentra.alpha") {
		t.Fatalf("delete must boot the job out, ran %q", f.calls)
	}
	if len(v.rows) != 1 || v.rows[0].name != "beta" {
		t.Fatalf("rows after delete = %+v", v.rows)
	}
}

func TestJobs_EditToManualDeactivatesTimer(t *testing.T) {
	deps, path := jobsDeps(t)
	f := &fakeSchedRunner{}
	deps.SchedulerRunner = f.run
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	v.exeOverride = "/usr/local/bin/sentra"
	installAlphaFiles(t, v, path)
	v.reload()
	v.tbl.SetCursor(0)

	v2, _ := pressJobsKey(v, 'e')
	v2.form.schedule.SetValue("manual")
	m, _ := v2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.(JobsView).Update(confirmedMsg{id: jobEditConfirmID})
	if !f.ran("launchctl bootout " + launchdGUIDomain() + "/com.sentra.alpha") {
		t.Fatalf("edit-to-manual must boot the job out, ran %q", f.calls)
	}
}

// An edit that changes the cadence re-renders the plist; launchd keeps
// running the OLD one until it is bootstrapped again.
func TestJobs_EditRescheduleReactivatesTimer(t *testing.T) {
	deps, path := jobsDeps(t)
	f := &fakeSchedRunner{}
	deps.SchedulerRunner = f.run
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	v.exeOverride = "/usr/local/bin/sentra"
	paths := installAlphaFiles(t, v, path)
	v.reload()
	v.tbl.SetCursor(0)

	v2, _ := pressJobsKey(v, 'e')
	v2.form.schedule.SetValue("daily@09:00")
	m, _ := v2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2, _ := m.(JobsView).Update(confirmedMsg{id: jobEditConfirmID})
	if !f.ran("launchctl bootstrap " + launchdGUIDomain() + " " + paths.Files[0]) {
		t.Fatalf("reschedule must bootstrap the new plist, ran %q", f.calls)
	}
	if notice := m2.(JobsView).notice; !strings.Contains(notice, "daily@09:00") {
		t.Fatalf("notice = %q", notice)
	}
}

// The Timer column reports what the OS says, not just what is on disk.
func TestJobs_TimerColumnReflectsOSState(t *testing.T) {
	deps, path := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	installAlphaFiles(t, v, path)
	print := "launchctl print " + launchdGUIDomain() + "/com.sentra.alpha"
	cases := []struct {
		name     string
		fail     map[string]string
		wantCell string
		wantNext bool
	}{
		{name: "loaded", wantCell: "active", wantNext: true},
		{name: "files only", fail: map[string]string{print: "Could not find service"}, wantCell: "inactive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSchedRunner{fail: tc.fail}
			v.deps.SchedulerRunner = f.run
			v.reload()
			if !f.ran(print) {
				t.Fatalf("reload must ask launchd, ran %q", f.calls)
			}
			sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			out := sized.(JobsView).View()
			if !strings.Contains(out, tc.wantCell) {
				t.Fatalf("timer column missing %q:\n%s", tc.wantCell, out)
			}
			if got := strings.Contains(out, "Mar 11 03:00"); got != tc.wantNext {
				t.Fatalf("next run shown = %v, want %v:\n%s", got, tc.wantNext, out)
			}
		})
	}
}

// A query that cannot run (no user bus) is "unknown", never "inactive":
// the row keeps its plain "installed" label and its next run.
func TestJobs_TimerColumnUnknownWhenOSCannotBeAsked(t *testing.T) {
	deps, path := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	installAlphaFiles(t, v, path)
	v.deps.SchedulerRunner = func(context.Context, string, ...string) ([]byte, error) {
		return nil, os.ErrNotExist // launchctl missing: not an exit status
	}
	v.reload()
	sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	out := sized.(JobsView).View()
	if strings.Contains(out, "inactive") || !strings.Contains(out, "installed") || !strings.Contains(out, "Mar 11 03:00") {
		t.Fatalf("unknown state must render as installed with a next run:\n%s", out)
	}
}

// Manual and not-installed rows have nothing to ask the OS about.
func TestJobs_ReloadDoesNotQueryOSWithoutFiles(t *testing.T) {
	deps, _ := jobsDeps(t)
	f := &fakeSchedRunner{}
	deps.SchedulerRunner = f.run
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	v.reload()
	if len(f.calls) != 0 {
		t.Fatalf("reload with no files must not shell out, ran %q", f.calls)
	}
}

// The Backup wizard's schedule step installs through the same path and
// must activate the timer too.
func TestBackupWizard_InstallRepeatActivatesTimer(t *testing.T) {
	v, _, _ := repeatFixture(t)
	f := &fakeSchedRunner{}
	v.deps.SchedulerRunner = f.run
	dir := filepath.Join(t.TempDir(), "docs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := v.installRepeat(dir, "docs", config.PolicySchedule{Cadence: "daily", At: "02:00"}, ""); err != nil {
		t.Fatalf("installRepeat: %v", err)
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable --now sentra-docs.timer"}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands = %q, want %q", f.calls, want)
	}
}

func TestBackupWizard_InstallRepeatActivationFailureNamesCommand(t *testing.T) {
	v, _, home := repeatFixture(t)
	f := &fakeSchedRunner{fail: map[string]string{
		"systemctl --user enable --now sentra-docs.timer": "Failed to connect to bus: No medium found",
	}}
	v.deps.SchedulerRunner = f.run
	err := v.installRepeat("/tmp/docs", "docs", config.PolicySchedule{Cadence: "daily", At: "02:00"}, "")
	if err == nil || !strings.Contains(err.Error(), "systemctl --user daemon-reload && systemctl --user enable --now sentra-docs.timer") {
		t.Fatalf("installRepeat err = %v, want the activation command", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".config", "systemd", "user", "sentra-docs.timer")); statErr != nil {
		t.Fatalf("files must stay written: %v", statErr)
	}
}
