package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
)

// TestExpandPath_RefusesTildeWithoutHome pins the rule every persisting
// site relies on: with no home to expand against, "~" and "~/..." are an
// error naming the path — never a cwd-relative guess (NormalizePath alone
// turns "~/docs" + "" into <cwd>/docs). Paths that need no home resolve
// as before.
func TestExpandPath_RefusesTildeWithoutHome(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Chdir(cwd)
	cases := []struct {
		p, home, want string
		refuse        bool
	}{
		{p: "~", home: "", refuse: true},
		{p: "~/docs", home: "", refuse: true},
		{p: "~/docs", home: home, want: filepath.Join(home, "docs")},
		{p: "~", home: home, want: home},
		{p: "rel/dir", home: "", want: filepath.Join(cwd, "rel", "dir")},
		{p: "/abs/dir", home: "", want: "/abs/dir"},
	}
	for _, c := range cases {
		got, err := expandPath(c.p, c.home)
		if c.refuse {
			if !errors.Is(err, errNoHome) || !strings.Contains(err.Error(), c.p) {
				t.Errorf("expandPath(%q, %q) = %q, %v; want errNoHome naming the path", c.p, c.home, got, err)
			}
			if got != "" {
				t.Errorf("expandPath(%q, %q) returned %q alongside the error; a refused path must yield nothing to persist", c.p, c.home, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("expandPath(%q, %q) = %q, %v; want %q", c.p, c.home, got, err, c.want)
		}
	}
}

// TestJobs_FormSaveRefusesTildeWithoutHome: with an unreadable home the
// form save must refuse "~/docs" inline rather than persisting <cwd>/docs
// — a wrong path in sentra.yaml is worse than the raw tilde that at
// least failed loudly under the timer.
func TestJobs_FormSaveRefusesTildeWithoutHome(t *testing.T) {
	deps, path := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.homeOverride = ""
	v.homeDir = func() (string, error) { return "", errors.New("$HOME is not defined") }
	cwd := t.TempDir()
	t.Chdir(cwd)
	v, _ = pressJobsKey(v, 'a')
	v.form.name.SetValue("gamma")
	v.form.path.SetValue("~/docs")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, cmd := m.(JobsView).Update(confirmedMsg{id: jobAddConfirmID})
	v = m.(JobsView)
	if cmd != nil {
		t.Fatal("a refused save must never reach the guard")
	}
	if v.stage != jobsForm || !strings.Contains(v.form.err, "~/docs") {
		t.Fatalf("stage=%v form.err=%q; want the form with an error naming ~/docs", v.stage, v.form.err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := cfg.Policies["gamma"]; ok {
		t.Fatalf("refused save persisted %v", got.Paths)
	}
}

// TestBackupWizard_InstallRepeatRefusesTildeWithoutHome: the schedule
// install is the wizard's persisting site; the same rule applies, and the
// error must reach the operator on Confirm.
func TestBackupWizard_InstallRepeatRefusesTildeWithoutHome(t *testing.T) {
	v, cfgPath, _ := repeatFixture(t)
	t.Setenv("HOME", "")
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	sched := config.PolicySchedule{Cadence: "daily", At: "02:00"}
	err := v.installRepeat(context.Background(), "~/docs", "docs", sched, "")
	if !errors.Is(err, errNoHome) || !strings.Contains(err.Error(), "~/docs") {
		t.Fatalf("installRepeat(~/docs) = %v; want errNoHome naming the path", err)
	}
	onDisk, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := onDisk.Policies["docs"]; ok {
		t.Fatalf("refused install persisted %v", got.Paths)
	}
	// Through the guarded start the refusal returns to Confirm as a
	// visible error, not a silent no-op.
	v = atDailyConfirm(t, v, cwd)
	m, cmd := v.startRepeatInstall("~/docs", "docs", sched, "")
	m, _ = runGuardedOp(t, m, cmd)
	got := m.(BackupView)
	if got.stage != backupConfirm || !strings.Contains(got.pathErr, "~/docs") {
		t.Fatalf("stage=%v pathErr=%q; want Confirm with an error naming ~/docs", got.stage, got.pathErr)
	}
}

// TestBackupWizard_ChatDirRefusesTildeWithoutHome: a cwd holding a
// "docs" directory is exactly the case where the guess would have passed
// checkDir and aimed the backup at the wrong tree.
func TestBackupWizard_ChatDirRefusesTildeWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	v := NewBackupView(Deps{Repo: newFlowRepo(t)})
	m, _ := v.Update(chatBackupMsg{dir: "~/docs", tag: "x"})
	got := m.(BackupView)
	if got.stage != backupLocation || got.pending != "" {
		t.Fatalf("stage=%v pending=%q; want Location with nothing pending", got.stage, got.pending)
	}
	if !strings.Contains(got.pathErr, "~/docs") || !strings.Contains(got.View(), "~/docs") {
		t.Fatalf("pathErr=%q; the refusal must name ~/docs and be visible", got.pathErr)
	}
}

// TestJobRun_RefusesTildeWithoutHome: the run-time resolution of a stored
// "~/docs" follows the same rule — with no home the run fails naming the
// path instead of snapshotting <cwd>/docs under a policy tag.
func TestJobRun_RefusesTildeWithoutHome(t *testing.T) {
	r := newFlowRepo(t)
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	cfg := config.Defaults()
	p := config.PolicyConfig{Paths: []string{"~/docs"}, Schedule: config.PolicySchedule{Cadence: "manual"}}
	op := buildPolicyRunOp(Deps{Repo: r, Config: &cfg}, "job-run", "job", p, newOpReporter(), "")
	done := op.run(context.Background()).(policyRunDoneMsg)
	if !errors.Is(done.err, errNoHome) || !strings.Contains(done.err.Error(), "~/docs") {
		t.Fatalf("run err = %v; want errNoHome naming ~/docs", done.err)
	}
	if snaps, err := r.ListSnapshots(context.Background()); err != nil || len(snaps) != 0 {
		t.Fatalf("a refused run must create nothing; got %d snapshots (%v)", len(snaps), err)
	}
}
