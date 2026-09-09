package tui

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/repo"
)

// oldestSnapshotID returns the ID of the earliest snapshot in r.
func oldestSnapshotID(t *testing.T, r *repo.Repo) string {
	t.Helper()
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil || len(snaps) == 0 {
		t.Fatalf("ListSnapshots: %v (%d)", err, len(snaps))
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].CreatedAt.Before(snaps[j].CreatedAt) })
	return snaps[0].ID
}

// TestJobRun_PrunePlansAroundPins mirrors the CLI bug in the TUI's job
// run: retention was built without the repo's pin set, so a pinned
// snapshot beyond keep_last was planned for deletion, DeleteSnapshot
// refused it at the choke point, and every run of the job failed. The
// prune view already loads pins; the job run must too.
func TestJobRun_PrunePlansAroundPins(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	pinned := oldestSnapshotID(t, r)
	if err := r.Pin(context.Background(), pinned); err != nil {
		t.Fatal(err)
	}
	src := realTempDir(t)
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := pruneDeps(r) // keep_last 1, everything else 0
	p := config.PolicyConfig{
		Paths:       []string{src},
		Schedule:    config.PolicySchedule{Cadence: "manual"},
		AfterBackup: config.PolicyAfterBackup{Prune: policycfg.PruneApply},
	}
	op := buildPolicyRunOp(deps, "job-run", "job", p, newOpReporter())
	done := op.run(context.Background()).(policyRunDoneMsg)
	if done.err != nil {
		t.Fatalf("a pinned snapshot beyond keep_last must not fail the run: %v", done.err)
	}
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, s := range snaps {
		kept = append(kept, s.ID)
	}
	if !containsStr(kept, pinned) {
		t.Fatalf("pinned snapshot %s was pruned; kept %v", pinned, kept)
	}
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// TestRunPolicyRetentionPrune_ToleratesPinRefusal is the defense in
// depth: a pin placed between planning and deleting reaches
// DeleteSnapshot's ErrSnapshotPinned. The step skips that snapshot the
// way it skips one already gone, rather than failing the whole run.
func TestRunPolicyRetentionPrune_ToleratesPinRefusal(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	pinned := oldestSnapshotID(t, r)
	if err := r.Pin(context.Background(), pinned); err != nil {
		t.Fatal(err)
	}
	// No Pinned on the policy: the plan drops the older snapshot.
	policy := repo.RetentionPolicy{KeepLast: 1}
	if err := runPolicyRetentionPrune(context.Background(), r, policy, policycfg.PruneApply); err != nil {
		t.Fatalf("a pin refusal must be skipped, not fatal: %v", err)
	}
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("pinned snapshot must survive: %d snapshots left", len(snaps))
	}
}

// TestJobRun_ResolvesPolicyPathsAtRunTime: a stored "~/docs" (or a
// relative dir) reached CreateSnapshot raw and failed under the timer,
// whose cwd and HOME are not the operator's shell. The run resolves
// every path through policycfg.ResolvePathFrom, whose tilde form reads
// the process's home.
func TestJobRun_ResolvesPolicyPathsAtRunTime(t *testing.T) {
	r := newFlowRepo(t)
	home := realTempDir(t)
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "docs", "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	p := config.PolicyConfig{Paths: []string{"~/docs"}, Schedule: config.PolicySchedule{Cadence: "manual"}}
	op := buildPolicyRunOp(Deps{Repo: r, Config: &cfg}, "job-run", "job", p, newOpReporter())
	done := op.run(context.Background()).(policyRunDoneMsg)
	if done.err != nil {
		t.Fatalf("run: %v", done.err)
	}
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("want one snapshot, got %d (%v)", len(snaps), err)
	}
	if want := filepath.Join(home, "docs"); snaps[0].Root != want {
		t.Fatalf("snapshot root = %q, want %q", snaps[0].Root, want)
	}
}

// TestJobRun_RelativePathAnchorsToConfigDir: the TUI run resolves a
// stored relative path exactly as the CLI's `policy run` does — against
// the directory of the sentra.yaml the policy came from, never the
// process cwd. A hand-written `paths: [src]` must mean the src next to
// the file whichever directory the operator launched `sentra` in.
func TestJobRun_RelativePathAnchorsToConfigDir(t *testing.T) {
	r := newFlowRepo(t)
	cfgDir := realTempDir(t)
	if err := os.MkdirAll(filepath.Join(cfgDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "src", "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	elsewhere := realTempDir(t)
	if err := os.MkdirAll(filepath.Join(elsewhere, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(elsewhere)
	cfg := config.Defaults()
	p := config.PolicyConfig{Paths: []string{"src"}, Schedule: config.PolicySchedule{Cadence: "manual"}}
	deps := Deps{Repo: r, Config: &cfg, ConfigPath: filepath.Join(cfgDir, "sentra.yaml")}
	op := buildPolicyRunOp(deps, "job-run", "job", p, newOpReporter())
	if done := op.run(context.Background()).(policyRunDoneMsg); done.err != nil {
		t.Fatalf("run: %v", done.err)
	}
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("want one snapshot, got %d (%v)", len(snaps), err)
	}
	if want := filepath.Join(cfgDir, "src"); snaps[0].Root != want {
		t.Fatalf("snapshot root = %q, want %q (anchored to the config dir, not the cwd)", snaps[0].Root, want)
	}
}

// TestJobs_FormSaveStoresAbsolutePaths: the add/edit form persisted the
// paths exactly as typed, so "~/docs" and "rel" landed in sentra.yaml
// and failed under the timer. Saving resolves them first.
func TestJobs_FormSaveStoresAbsolutePaths(t *testing.T) {
	deps, path := jobsDeps(t)
	v := newJobsForTest(t, deps)
	home := realTempDir(t)
	t.Setenv("HOME", home) // the persisting resolver reads the process home
	cwd := realTempDir(t)
	t.Chdir(cwd)
	v, _ = pressJobsKey(v, 'a')
	v.form.name.SetValue("gamma")
	v.form.path.SetValue("~/docs, rel/dir")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, cmd := m.(JobsView).Update(confirmedMsg{id: jobAddConfirmID})
	runGuardedOp(t, m, cmd)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Policies["gamma"].Paths
	want := []string{filepath.Join(home, "docs"), filepath.Join(cwd, "rel", "dir")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("stored paths = %v, want %v", got, want)
	}
}

// TestBackupWizard_InstallRepeatStoresAbsoluteRoot: the wizard's schedule
// install persists whatever root it is handed; a tilde or relative dir
// (the chat's start_backup intent) must be resolved before it becomes a
// policy the timer will run.
func TestBackupWizard_InstallRepeatStoresAbsoluteRoot(t *testing.T) {
	v, cfgPath, _ := repeatFixture(t)
	home := realTempDir(t)
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := v.installRepeat(context.Background(), "~/docs", "docs", config.PolicySchedule{Cadence: "daily", At: "02:00"}, ""); err != nil {
		t.Fatal(err)
	}
	onDisk, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := onDisk.Policies["docs"].Paths; len(got) != 1 || got[0] != filepath.Join(home, "docs") {
		t.Fatalf("stored root = %v, want %s", got, filepath.Join(home, "docs"))
	}
}

// TestBackupWizard_ChatDirIsResolvedBeforePending: the chat's
// start_backup intent may carry "~/x" or a relative dir; the wizard
// resolves it before it becomes pending, so the confirm summary, the
// policy, and the snapshot root all name the same absolute directory.
func TestBackupWizard_ChatDirIsResolvedBeforePending(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := realTempDir(t)
	if err := os.MkdirAll(filepath.Join(cwd, "rel"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	for dir, want := range map[string]string{
		"~/src": filepath.Join(home, "src"),
		"rel":   filepath.Join(cwd, "rel"),
	} {
		v := NewBackupView(Deps{Repo: newFlowRepo(t)})
		m, _ := v.Update(chatBackupMsg{dir: dir, tag: "x"})
		got := m.(BackupView)
		if got.stage != backupConfirm || got.pending != want {
			t.Fatalf("chatBackupMsg{dir:%q}: stage=%v pending=%q, want Confirm with %q", dir, got.stage, got.pending, want)
		}
	}
}

// An empty chat dir must still be refused: resolving "" would yield the
// cwd and quietly aim the backup at wherever sentra was launched.
func TestBackupWizard_EmptyChatDirStaysRefused(t *testing.T) {
	v := NewBackupView(Deps{Repo: newFlowRepo(t)})
	m, _ := v.Update(chatBackupMsg{dir: "  ", tag: "x"})
	if got := m.(BackupView); got.stage != backupLocation || got.pending != "" {
		t.Fatalf("empty dir: stage=%v pending=%q, want Location with nothing pending", got.stage, got.pending)
	}
}
