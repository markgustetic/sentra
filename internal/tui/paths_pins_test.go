package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
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

// errPinsUnavailable stands in for a bucket that answers everything but
// the pin set — a transient S3 error on one key, which is exactly the
// case a plan built "without pins" would silently mis-handle.
var errPinsUnavailable = errors.New("simulated pins outage")

// pinsFailStore serves a memory store whose meta/pins read fails while
// fail is set. Every other key — manifests, chunks, the lock — behaves,
// so the only thing a caller cannot learn is which snapshots are pinned.
type pinsFailStore struct {
	blobstore.Store
	fail atomic.Bool
}

func (s *pinsFailStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == "meta/pins" && s.fail.Load() {
		return nil, errPinsUnavailable
	}
	return s.Store.Get(ctx, key)
}

// newPinsFailRepo returns a seeded two-snapshot repo whose pin set is
// unreadable from the returned store's fail flag onward. Seeding
// happens before the flag flips: the failure under test is the plan's,
// not the backup's.
func newPinsFailRepo(t *testing.T) (*repo.Repo, *pinsFailStore) {
	t.Helper()
	store := &pinsFailStore{Store: blobstore.NewMemory()}
	r, err := repo.Init(context.Background(), store, []byte("flow-test-pass"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	seedTwoSnapshots(t, r)
	store.fail.Store(true)
	return r, store
}

// TestJobRun_PinsLoadFailureFailsClosed is the RULE behind
// TestJobRun_PrunePlansAroundPins: a retention plan is never computed
// without the pin set. When meta/pins cannot be read, the job run fails
// there — named, before a single DeleteSnapshot — and the failure hooks
// fire as for any failed run. Planning around an empty set instead
// would drop a pinned snapshot on paper and then either fail at the
// choke point or, worse, tolerate the refusal (see the test below) and
// report a clean prune that silently skipped the operator's decision.
func TestJobRun_PinsLoadFailureFailsClosed(t *testing.T) {
	r, _ := newPinsFailRepo(t)
	src := realTempDir(t)
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "failed.marker")
	deps := pruneDeps(r) // keep_last 1: without pins the plan would drop two
	p := config.PolicyConfig{
		Paths:       []string{src},
		Schedule:    config.PolicySchedule{Cadence: "manual"},
		AfterBackup: config.PolicyAfterBackup{Prune: policycfg.PruneApply},
		Hooks:       config.PolicyHooks{OnFailure: "touch " + marker},
	}
	op := buildPolicyRunOp(deps, "job-run", "job", p, newOpReporter())
	done := op.run(context.Background()).(policyRunDoneMsg)
	if !errors.Is(done.err, errPinsUnavailable) {
		t.Fatalf("run err = %v, want the pins load failure", done.err)
	}
	if done.snapshots != 1 {
		t.Errorf("the backup itself must complete before the plan fails: snapshots = %d", done.snapshots)
	}
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 3 {
		t.Fatalf("a run that could not read pins must delete nothing: %d snapshots left, want 3", len(snaps))
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("on_failure hook did not fire for the failed run: %v", err)
	}
}

// TestPruneView_PinsLoadFailureIsLoadError: the prune view is the same
// rule on the interactive surface. An unreadable pin set is a load error
// the view reports; it never shows a drop list it cannot trust, so
// there is nothing for the typed confirm to apply.
func TestPruneView_PinsLoadFailureIsLoadError(t *testing.T) {
	r, _ := newPinsFailRepo(t)
	v := NewPruneView(pruneDeps(r))
	if !strings.Contains(v.loadErr, errPinsUnavailable.Error()) {
		t.Fatalf("loadErr = %q, want the pins load failure", v.loadErr)
	}
	if len(v.drop) != 0 || len(v.keep) != 0 || len(v.decisions) != 0 {
		t.Fatalf("a view that could not read pins must plan nothing: drop=%v keep=%v", v.drop, v.keep)
	}
	if out := v.View(); !strings.Contains(out, errPinsUnavailable.Error()) {
		t.Errorf("view must surface the load error:\n%s", out)
	}
	if _, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Errorf("enter on a failed load must not open the confirm: %#v", cmd())
	}
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

// TestBackupWizard_InstallRepeatMatchesExistingPathBySpelling: a policy
// stored by an older build carries the unresolved spelling of its root
// (/tmp/docs), while the wizard now resolves symlinks (/private/tmp/docs).
// The reuse check must compare the stored entry through the same
// resolver, or the operator is told the policy "already backs up" the very
// directory they picked.
func TestBackupWizard_InstallRepeatMatchesExistingPathBySpelling(t *testing.T) {
	v, cfgPath, _ := repeatFixture(t)
	real := filepath.Join(realTempDir(t), "docs")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(realTempDir(t), "docs-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	err := config.Update(cfgPath, func(cfg *config.Config) error {
		if cfg.Policies == nil {
			cfg.Policies = map[string]config.PolicyConfig{}
		}
		cfg.Policies["docs"] = config.PolicyConfig{Paths: []string{link}, Schedule: config.PolicySchedule{Cadence: "daily", At: "02:00"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.installRepeat(context.Background(), real, "docs", config.PolicySchedule{Cadence: "daily", At: "03:00"}, ""); err != nil {
		t.Fatalf("installRepeat refused the same directory under its resolved spelling: %v", err)
	}
	onDisk, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := onDisk.Policies["docs"].Paths; len(got) != 1 || got[0] != real {
		t.Fatalf("stored root = %v, want %s", got, real)
	}
}
