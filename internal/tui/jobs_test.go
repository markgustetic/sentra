package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/scheduler"
)

var jobsNow = time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC)

func snapAt(id, root, tag string, age time.Duration) repo.SnapshotInfo {
	return repo.SnapshotInfo{ID: id, Root: root, Tag: tag, CreatedAt: jobsNow.Add(-age)}
}

func TestNormalizeJobPath(t *testing.T) {
	home := t.TempDir()
	if got := normalizeJobPath("~/docs", home); got != filepath.Join(home, "docs") {
		t.Fatalf("tilde: got %q", got)
	}
	if got := normalizeJobPath("~", home); got != home {
		t.Fatalf("bare tilde: got %q", got)
	}
	if got := normalizeJobPath("/data//x/", home); got != "/data/x" {
		t.Fatalf("clean: got %q", got)
	}
	abs, _ := filepath.Abs("rel")
	if got := normalizeJobPath("rel", home); got != abs {
		t.Fatalf("relative: got %q want %q", got, abs)
	}
}

func TestHasPolicyTag(t *testing.T) {
	if !hasPolicyTag("policy:home nightly", "home") {
		t.Fatal("token match must hit")
	}
	if hasPolicyTag("policy:home-old nightly", "home") {
		t.Fatal("prefix of a longer token must not hit")
	}
	if hasPolicyTag("my-policy:home", "home") {
		t.Fatal("substring inside another token must not hit")
	}
}

func TestLastJobRun_TagWinsOverRootFallback(t *testing.T) {
	snaps := []repo.SnapshotInfo{
		snapAt("s-root-newer", "/data/a", "manual-tag", 1*time.Hour),
		snapAt("s-tagged-older", "/data/b", "policy:home", 5*time.Hour),
	}
	got, ok := lastJobRun("home", []string{"/data/a"}, snaps)
	if !ok || got.ID != "s-tagged-older" {
		t.Fatalf("tag match must win even when older: got %+v ok=%t", got, ok)
	}
}

func TestLastJobRun_RootFallbackWhenNoTag(t *testing.T) {
	snaps := []repo.SnapshotInfo{
		snapAt("s-old", "/data/a", "x", 5*time.Hour),
		snapAt("s-new", "/data/a", "y", 1*time.Hour),
		snapAt("s-other", "/data/z", "z", time.Minute),
	}
	got, ok := lastJobRun("home", []string{"/data/a"}, snaps)
	if !ok || got.ID != "s-new" {
		t.Fatalf("want newest root match, got %+v ok=%t", got, ok)
	}
	if _, ok := lastJobRun("home", []string{"/nope"}, snaps); ok {
		t.Fatal("no match must report ok=false")
	}
}

func TestNewestJobSnapshot_PrefersTaggedWithinRoot(t *testing.T) {
	snaps := []repo.SnapshotInfo{
		snapAt("s-untagged-new", "/data/a", "adhoc", 1*time.Hour),
		snapAt("s-tagged-old", "/data/a", "policy:home", 6*time.Hour),
		snapAt("s-tagged-new", "/data/a", "policy:home extra", 2*time.Hour),
		snapAt("s-wrong-root", "/data/b", "policy:home", time.Minute),
	}
	got, ok := newestJobSnapshot("home", "/data/a", snaps)
	if !ok || got.ID != "s-tagged-new" {
		t.Fatalf("want newest TAGGED at root, got %+v ok=%t", got, ok)
	}
	got, ok = newestJobSnapshot("home", "/data/a", snaps[:1])
	if !ok || got.ID != "s-untagged-new" {
		t.Fatalf("untagged fallback within root: got %+v ok=%t", got, ok)
	}
}

func TestRelAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{49 * time.Hour, "2d ago"},
	}
	for _, tc := range cases {
		if got := relAge(jobsNow.Add(-tc.d), jobsNow); got != tc.want {
			t.Fatalf("relAge(-%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// jobsDeps writes a sentra.yaml with two policies (one daily, one
// manual) and returns Deps pointing at it. homeOverride steers the
// scheduler stat into an empty temp dir, so "not installed" is
// deterministic.
func jobsDeps(t *testing.T) (Deps, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.yaml")
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "b"
	cfg.Policies["alpha"] = config.PolicyConfig{
		Paths:    []string{"/data/alpha"},
		Schedule: config.PolicySchedule{Cadence: "daily", At: "03:00"},
	}
	cfg.Policies["beta"] = config.PolicyConfig{
		Paths:    []string{"/data/beta"},
		Schedule: config.PolicySchedule{Cadence: "manual"},
	}
	if err := config.Write(path, &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return Deps{Config: &cfg, ConfigPath: path}, path
}

func newJobsForTest(t *testing.T, deps Deps) JobsView {
	t.Helper()
	v := NewJobsView(deps)
	v.homeOverride = t.TempDir() // no scheduler files -> not installed
	v.now = func() time.Time { return jobsNow }
	v.reload()
	return v
}

func TestJobs_ListShowsScheduleTimerAndNextRun(t *testing.T) {
	deps, _ := jobsDeps(t)
	v := newJobsForTest(t, deps)
	sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	out := sized.(JobsView).View()
	for _, want := range []string{"alpha", "beta", "daily@03:00", "manual", "not installed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list missing %q:\n%s", want, out)
		}
	}
	// alpha's timer is NOT installed, and beta is manual: neither may
	// promise a next run.
	if strings.Contains(out, "Mar 11") {
		t.Fatalf("uninstalled/manual rows must not show a next run:\n%s", out)
	}
}

func TestJobs_InstalledRowComputesNextRun(t *testing.T) {
	deps, _ := jobsDeps(t)
	v := newJobsForTest(t, deps)
	// Fake an installed timer: create the launchd plist path for alpha.
	paths, err := scheduler.PathsFor("darwin", v.homeOverride, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	v.osOverride = "darwin"
	if err := scheduler.Install(map[string]string{paths.Files[0]: "x"}); err != nil {
		t.Fatal(err)
	}
	v.reload()
	sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	out := sized.(JobsView).View()
	// jobsNow is Mar 10 14:30; daily@03:00 -> next run Mar 11 03:00.
	if !strings.Contains(out, "installed") || !strings.Contains(out, "Mar 11 03:00") {
		t.Fatalf("installed daily job must show next run:\n%s", out)
	}
}

func TestJobs_LastRunColumnFromPreload(t *testing.T) {
	deps, _ := jobsDeps(t)
	deps.preload = &snapshotPreload{snaps: []repo.SnapshotInfo{
		snapAt("s1", "/data/alpha", "policy:alpha", 2*time.Hour),
	}}
	v := newJobsForTest(t, deps)
	sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	out := sized.(JobsView).View()
	if !strings.Contains(out, "2h ago") {
		t.Fatalf("last-run column missing:\n%s", out)
	}
}

// pressKey drives one rune key through the view.
func pressJobsKey(v JobsView, r rune) (JobsView, tea.Cmd) {
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return m.(JobsView), cmd
}

func TestJobs_InstallThenUninstallTimer(t *testing.T) {
	deps, _ := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	v.exeOverride = "/usr/local/bin/sentra"
	v.tbl.SetCursor(0) // alpha (daily)

	v2, cmd := pressJobsKey(v, 'i')
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatal("i must push the install confirm modal")
	}
	_ = push
	m, cmd := v2.Update(confirmedMsg{id: jobInstallConfirmID})
	res := cmd() // filesystem-only tea.Cmd, runs inline
	m2, _ := m.(JobsView).Update(res)
	v3 := m2.(JobsView)
	paths, _ := scheduler.PathsFor("darwin", v3.homeOverride, "alpha")
	if installed, _ := scheduler.Installed(paths); !installed {
		t.Fatal("confirm must write the timer files")
	}
	if !v3.rows[0].installed {
		t.Fatal("reload after install must show installed")
	}

	v4, cmd := pressJobsKey(v3, 'u')
	if _, ok := cmd().(pushModalMsg); !ok {
		t.Fatal("u must push the uninstall confirm modal")
	}
	m, cmd = v4.Update(confirmedMsg{id: jobUninstallConfirmID})
	m2, _ = m.(JobsView).Update(cmd())
	if installed, _ := scheduler.Installed(paths); installed {
		t.Fatal("confirm must remove the timer files")
	}
	if m2.(JobsView).rows[0].installed {
		t.Fatal("reload after uninstall must show not installed")
	}
}

func TestJobs_InstallRejectsManual(t *testing.T) {
	deps, _ := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.tbl.SetCursor(1) // beta (manual)
	m, cmd := v.Update(confirmedMsg{id: jobInstallConfirmID})
	res := cmd()
	m2, _ := m.(JobsView).Update(res)
	if !strings.Contains(m2.(JobsView).notice, "manual") {
		t.Fatalf("manual install must be refused with a notice, got %q", m2.(JobsView).notice)
	}
}

// Run-now raises the typed confirm only for prune:apply — the same gate
// contract PoliciesView carried (validate first, so a corrupt mode can
// never hide behind the simple confirm).
func TestJobs_RunNowConfirmVariants(t *testing.T) {
	deps, path := jobsDeps(t)
	if err := config.Update(path, func(cfg *config.Config) error {
		p := cfg.Policies["alpha"]
		p.AfterBackup.Prune = "apply"
		cfg.Policies["alpha"] = p
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	v := newJobsForTest(t, deps)
	v.tbl.SetCursor(0)
	_, cmd := pressJobsKey(v, 'r')
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatal("r must push a confirm modal")
	}
	if _, typed := push.modal.(TypedConfirmModal); !typed {
		t.Fatal("prune:apply must get the TYPED confirm")
	}
}

func TestJobs_EditPrefillsAndSavesWithTimerReinstall(t *testing.T) {
	deps, path := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	v.exeOverride = "/usr/local/bin/sentra"
	// Install alpha's timer so the edit has something to re-render.
	paths, _ := scheduler.PathsFor("darwin", v.homeOverride, "alpha")
	files, err := scheduler.Render(paths, v.exeOverride, path, "alpha",
		config.PolicySchedule{Cadence: "daily", At: "03:00"})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Install(files); err != nil {
		t.Fatal(err)
	}
	v.reload()
	v.tbl.SetCursor(0)

	v2, _ := pressJobsKey(v, 'e')
	if v2.stage != jobsForm || v2.editName != "alpha" {
		t.Fatalf("e must open the edit form: stage=%v editName=%q", v2.stage, v2.editName)
	}
	if got := v2.form.schedule.Value(); got != "daily@03:00" {
		t.Fatalf("schedule not prefilled: %q", got)
	}
	if v2.form.focus == 0 {
		t.Fatal("edit mode must not focus the read-only name field")
	}

	v2.form.schedule.SetValue("daily@09:00")
	m, cmd := v2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := cmd().(pushModalMsg); !ok {
		t.Fatal("enter must push the edit confirm")
	}
	m2, cmd := m.(JobsView).Update(confirmedMsg{id: jobEditConfirmID})
	if cmd != nil {
		if res := cmd(); res != nil {
			m2m, _ := m2.(JobsView).Update(res)
			m2 = m2m
		}
	}
	v3 := m2.(JobsView)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Policies["alpha"].Schedule.At != "09:00" {
		t.Fatalf("edit must persist: %+v", cfg.Policies["alpha"].Schedule)
	}
	body, err := os.ReadFile(paths.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "9") {
		t.Fatalf("installed timer must be re-rendered for 09:00:\n%s", body)
	}
	_ = v3
}

func TestJobs_EditToManualUninstallsTimer(t *testing.T) {
	deps, path := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	v.exeOverride = "/usr/local/bin/sentra"
	paths, _ := scheduler.PathsFor("darwin", v.homeOverride, "alpha")
	files, _ := scheduler.Render(paths, v.exeOverride, path, "alpha",
		config.PolicySchedule{Cadence: "daily", At: "03:00"})
	if err := scheduler.Install(files); err != nil {
		t.Fatal(err)
	}
	v.reload()
	v.tbl.SetCursor(0)

	v2, _ := pressJobsKey(v, 'e')
	v2.form.schedule.SetValue("manual")
	m, _ := v2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	// saveForm runs synchronously (config write + timer sync) and, for an
	// edit confirm, always returns a nil cmd — there is nothing async to
	// chain here. The assertion below reads disk state, not the returned
	// view, so both results are discarded.
	m.(JobsView).Update(confirmedMsg{id: jobEditConfirmID})
	if installed, _ := scheduler.Installed(paths); installed {
		t.Fatal("editing an installed job to manual must uninstall its timer")
	}
}

func TestJobs_AddFormStillWorks(t *testing.T) {
	deps, path := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v2, _ := pressJobsKey(v, 'a')
	if v2.stage != jobsForm || v2.editName != "" {
		t.Fatalf("a must open a blank add form: stage=%v editName=%q", v2.stage, v2.editName)
	}
	v2.form.name.SetValue("gamma")
	v2.form.path.SetValue("/data/gamma")
	v2.form.schedule.SetValue("weekly@mon:04:00")
	m, _ := v2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2, _ := m.(JobsView).Update(confirmedMsg{id: jobAddConfirmID})
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Policies["gamma"]; !ok {
		t.Fatal("add must persist the new policy")
	}
	_ = m2
}

func TestJobs_RegisteredHiddenAndRoutable(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	for _, c := range app.registry.Commands() {
		if c.ID == "jobs" {
			t.Fatal("jobs must be hidden from the rail")
		}
	}
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ := sized.(App).Update(activateMsg{id: "jobs"})
	if got := m.(App).views[m.(App).active].id; got != "jobs" {
		t.Fatalf("activateMsg must route to jobs, got %q", got)
	}
}

func TestJobs_DeleteRemovesPolicyAndTimer(t *testing.T) {
	deps, path := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	paths, _ := scheduler.PathsFor("darwin", v.homeOverride, "alpha")
	if err := scheduler.Install(map[string]string{paths.Files[0]: "x"}); err != nil {
		t.Fatal(err)
	}
	v.reload()
	v.tbl.SetCursor(0)

	v2, cmd := pressJobsKey(v, 'd')
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatal("d must push the delete confirm")
	}
	_ = push
	m, cmd := v2.Update(confirmedMsg{id: jobDeleteConfirmID})
	m2, _ := m.(JobsView).Update(cmd())
	v3 := m2.(JobsView)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := cfg.Policies["alpha"]; still {
		t.Fatal("delete must remove the policy from sentra.yaml")
	}
	if installed, _ := scheduler.Installed(paths); installed {
		t.Fatal("delete must uninstall the timer files")
	}
	if len(v3.rows) != 1 || v3.rows[0].name != "beta" {
		t.Fatalf("rows after delete = %+v", v3.rows)
	}
}

// The confirm body must state the full effect — and that snapshots
// survive.
func TestJobs_DeleteConfirmBodyMentionsTimerAndSnapshots(t *testing.T) {
	deps, _ := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.tbl.SetCursor(0)
	_, cmd := pressJobsKey(v, 'd')
	push := cmd().(pushModalMsg)
	body := push.modal.View()
	for _, want := range []string{"timer", "napshot"} {
		if !strings.Contains(body, want) {
			t.Fatalf("delete confirm must mention %q:\n%s", want, body)
		}
	}
}

// jobsDepsWithRepo returns deps whose config points a policy at a real
// backed-up directory in the in-memory repo, so drill-in has a manifest.
// newFlowRepo alone hands back an EMPTY repo (other tests rely on that
// zero-snapshot starting point), so this helper takes the one extra step
// of actually creating a snapshot before pointing a policy at its root.
func jobsDepsWithRepo(t *testing.T) (Deps, string, string) {
	t.Helper()
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil || len(snaps) == 0 {
		t.Fatalf("flow repo must carry snapshots: %v", err)
	}
	root := snaps[0].Root
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.yaml")
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "b"
	cfg.Policies["alpha"] = config.PolicyConfig{
		Paths:    []string{root},
		Schedule: config.PolicySchedule{Cadence: "daily", At: "03:00"},
	}
	if err := config.Write(path, &cfg); err != nil {
		t.Fatal(err)
	}
	return Deps{Repo: r, Config: &cfg, ConfigPath: path}, path, root
}

func TestJobs_DrillInShowsNewestSnapshotTree(t *testing.T) {
	deps, _, _ := jobsDepsWithRepo(t)
	v := newJobsForTest(t, deps)
	sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = sized.(JobsView)
	v.tbl.SetCursor(0)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v2 := m.(JobsView)
	if v2.stage != jobsDetail || !v2.detailLoading {
		t.Fatalf("enter must open detail loading: stage=%v loading=%t", v2.stage, v2.detailLoading)
	}
	m2, _ := v2.Update(cmd()) // run the load cmd inline
	v3 := m2.(JobsView)
	out := v3.View()
	if v3.detailErr != nil {
		t.Fatalf("detail load failed: %v", v3.detailErr)
	}
	// Summary block + a tree line from the manifest.
	for _, want := range []string{"alpha", "daily@03:00", "snap-"} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail missing %q:\n%s", want, out)
		}
	}
	// esc returns to the list.
	m3, _ := v3.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m3.(JobsView).stage != jobsList {
		t.Fatal("esc must return to the list")
	}
}

func TestJobs_DrillInPathWithoutSnapshotShowsPlaceholder(t *testing.T) {
	deps, _ := jobsDeps(t) // policies point at /data/alpha — no repo, no snapshots
	v := newJobsForTest(t, deps)
	sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = sized.(JobsView)
	v.tbl.SetCursor(0)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	out := m.(JobsView).View()
	if !strings.Contains(out, "not backed up yet") {
		t.Fatalf("no-snapshot path must show the placeholder:\n%s", out)
	}
}

// TestJobs_DrillInCyclesPaths locks the left/right/tab path-cycling
// contract: detailPathIdx advances modulo len(Paths) and wraps both
// directions, and the summary's "path N/M" line tracks it.
func TestJobs_DrillInCyclesPaths(t *testing.T) {
	deps, path := jobsDeps(t)
	if err := config.Update(path, func(cfg *config.Config) error {
		p := cfg.Policies["alpha"]
		p.Paths = []string{"/data/alpha", "/data/alpha2"}
		cfg.Policies["alpha"] = p
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	v := newJobsForTest(t, deps)
	sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = sized.(JobsView)
	v.tbl.SetCursor(0)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v2 := m.(JobsView)
	if v2.detailPathIdx != 0 {
		t.Fatalf("enter must start at path 0, got %d", v2.detailPathIdx)
	}

	m2, _ := v2.Update(tea.KeyMsg{Type: tea.KeyRight})
	v3 := m2.(JobsView)
	if v3.detailPathIdx != 1 {
		t.Fatalf("right must advance to path 1, got %d", v3.detailPathIdx)
	}
	if out := v3.View(); !strings.Contains(out, "path 2/2") {
		t.Fatalf("detail must show path 2/2:\n%s", out)
	}

	m3, _ := v3.Update(tea.KeyMsg{Type: tea.KeyTab})
	v4 := m3.(JobsView)
	if v4.detailPathIdx != 0 {
		t.Fatalf("tab must wrap modulo len(paths), got %d", v4.detailPathIdx)
	}

	m4, _ := v4.Update(tea.KeyMsg{Type: tea.KeyLeft})
	v5 := m4.(JobsView)
	if v5.detailPathIdx != 1 {
		t.Fatalf("left must wrap backward, got %d", v5.detailPathIdx)
	}
}

// TestJobs_DrillInDeleteOperatesOnDetailJob pins the requirement that
// e/d/r pressed on the detail stage act on the job actually shown in
// detail, not whatever row the list cursor was last left on — a drift
// that could otherwise happen if something moved the table cursor
// while detail stayed open.
func TestJobs_DrillInDeleteOperatesOnDetailJob(t *testing.T) {
	deps, _ := jobsDeps(t) // alpha, beta
	v := newJobsForTest(t, deps)
	sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = sized.(JobsView)
	v.tbl.SetCursor(1) // beta
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v2 := m.(JobsView)
	if v2.detailName != "beta" {
		t.Fatalf("enter must open detail for the row under cursor: got %q", v2.detailName)
	}
	// Simulate the cursor drifting away from the detail job.
	v2.tbl.SetCursor(0)

	_, cmd := pressJobsKey(v2, 'd')
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatal("d in detail must push the delete confirm")
	}
	body := push.modal.View()
	if !strings.Contains(body, "beta") {
		t.Fatalf("delete confirm must target the DETAIL job (beta), not the drifted cursor row:\n%s", body)
	}
	if strings.Contains(body, `"alpha"`) {
		t.Fatalf("delete confirm must not target the drifted cursor row (alpha):\n%s", body)
	}
}

// TestJobs_DrillInDeleteFromDetailReturnsToList is the regression test for
// review Finding 1: enter -> d -> confirm -> the delete's jobTimerMsg must
// land the view back on jobsList (with detail state cleared) when the job
// it just deleted is the one that was on screen in detail — before the
// fix, stage stayed jobsDetail and viewDetail rendered a ghost page (a
// zero-value summary over the last-cached manifest) that only esc could
// escape, since left/right/tab short-circuit on len(Paths)==0.
func TestJobs_DrillInDeleteFromDetailReturnsToList(t *testing.T) {
	deps, path := jobsDeps(t) // alpha, beta
	v := newJobsForTest(t, deps)
	sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = sized.(JobsView)
	v.tbl.SetCursor(0) // alpha
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v2 := m.(JobsView)
	if v2.stage != jobsDetail || v2.detailName != "alpha" {
		t.Fatalf("enter must open detail for alpha: stage=%v name=%q", v2.stage, v2.detailName)
	}

	v3, cmd := pressJobsKey(v2, 'd')
	if _, ok := cmd().(pushModalMsg); !ok {
		t.Fatal("d in detail must push the delete confirm")
	}
	m2, cmd := v3.Update(confirmedMsg{id: jobDeleteConfirmID})
	v4 := m2.(JobsView)
	res := cmd() // the delete op's tea.Cmd — filesystem-only, runs inline
	m3, _ := v4.Update(res)
	v5 := m3.(JobsView)

	if v5.stage != jobsList {
		t.Fatalf("deleting the detail job must return to the list, got stage=%v", v5.stage)
	}
	if v5.detailName != "" || v5.detailSnapID != "" || v5.detailErr != nil || v5.detailLoading {
		t.Fatalf("detail state must be cleared after the delete: %+v", v5)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := cfg.Policies["alpha"]; still {
		t.Fatal("delete must remove the policy from sentra.yaml")
	}
}

// TestJobs_DrillInDropsResultForSupersededSnapID is the regression test
// for review Finding 2: the stale-result guard must key on snapID, not
// just name+pathIdx — a load kicked off for an older snapshot at the
// same path must not clobber a newer load already in flight for the
// same name+pathIdx (mirrors snapshots.go's detailID-keyed guard).
func TestJobs_DrillInDropsResultForSupersededSnapID(t *testing.T) {
	deps, _ := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.stage = jobsDetail
	v.detailName = "alpha"
	v.detailPathIdx = 0
	v.detailSnapID = "B"
	v.detailLoading = true
	v.detailMan = repo.Manifest{}

	stale := jobDetailMsg{
		name:    "alpha",
		pathIdx: 0,
		snapID:  "A", // superseded — the view is now waiting on "B"
		man:     repo.Manifest{ID: "A-manifest"},
	}
	m, _ := v.Update(stale)
	v2 := m.(JobsView)

	if !v2.detailLoading {
		t.Fatal("a superseded-snapID result must not clear detailLoading")
	}
	if v2.detailMan.ID != "" {
		t.Fatalf("a superseded-snapID result must not overwrite detailMan, got %+v", v2.detailMan)
	}
	if v2.detailSnapID != "B" {
		t.Fatalf("detailSnapID must remain the one actually being waited on, got %q", v2.detailSnapID)
	}
}

// --- Ports from the deleted PoliciesView/ScheduleView test suites ---
//
// The tests below port behaviors from policies_test.go (and one from
// schedule_test.go) that were the only proof of machinery shared with
// JobsView — the run/form machinery (buildPolicyRunOp, armRun, saveForm's
// replace guard, runPolicyRetentionPrune's fail-closed guard, hook
// execution through the run path) and the nil-config placeholder — once
// PoliciesView/ScheduleView were deleted. Every one of them is GREEN ON
// ARRIVAL: JobsView's machinery (jobs.go, jobs_run.go, jobs_form.go)
// already implements the behavior being ported — these tests were written
// and run against that existing code, not used to drive new
// implementation, so there is no RED phase to show here.
//
// schedule_test.go's install/uninstall/manual-refusal coverage is already
// subsumed by TestJobs_InstallThenUninstallTimer and
// TestJobs_InstallRejectsManual; TestScheduleView_NilConfigPlaceholder is
// ported below as TestJobs_NilConfigPlaceholder. schedule.go's
// reload-cursor-preservation logic is identical code copied verbatim into
// JobsView.reload (jobs.go) with no dedicated test on either side to port.

// TestJobs_RunOffModeUsesSimpleConfirm is the port of the deleted
// TestPoliciesView_RunOffModeUsesSimpleConfirm: a job whose prune mode is
// off (or unset, which normalizes to off) must gate run-now behind the
// SIMPLE confirm — TYPED is reserved for prune:apply (see
// TestJobs_RunNowConfirmVariants, which covers that side of the gate).
func TestJobs_RunOffModeUsesSimpleConfirm(t *testing.T) {
	deps, _ := jobsDeps(t) // alpha has no AfterBackup.Prune set -> "off"
	v := newJobsForTest(t, deps)
	v.tbl.SetCursor(0) // alpha
	_, cmd := pressJobsKey(v, 'r')
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("r must push a confirm modal, got %#v", cmd())
	}
	if _, ok := push.modal.(ConfirmModal); !ok {
		t.Fatalf("prune=off must use the SIMPLE ConfirmModal, got %T", push.modal)
	}
}

// TestJobs_RunRefusesInvalidPolicy is the port of the deleted
// TestPoliciesView_RunRefusesInvalidPolicy: a corrupt on-disk prune mode
// (a typo like "aply") must not slip past armRun's validation and arm the
// simple confirm — policyPruneModeOrOff only lowercases/trims, so armRun
// must validate first (mirroring the CLI's runPolicy, which calls
// policycfg.Validate) and refuse via notice instead of pushing a modal.
func TestJobs_RunRefusesInvalidPolicy(t *testing.T) {
	deps, path := jobsDeps(t)
	if err := config.Update(path, func(cfg *config.Config) error {
		p := cfg.Policies["alpha"]
		p.AfterBackup.Prune = "aply"
		cfg.Policies["alpha"] = p
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	v := newJobsForTest(t, deps)
	v.tbl.SetCursor(0)
	v2, cmd := pressJobsKey(v, 'r')
	if cmd != nil {
		if push, ok := cmd().(pushModalMsg); ok {
			t.Fatalf("invalid prune mode must not arm a RUN modal, got %T", push.modal)
		}
	}
	if v2.stage == jobsRunning {
		t.Fatal("invalid policy must not enter the running stage")
	}
	if v2.notice == "" {
		t.Fatal("invalid policy must surface a notice explaining the refusal")
	}
}

// TestJobs_RunConfirmedTakesOpGuardAndSnapshots is the port of the deleted
// TestPoliciesView_RunConfirmedTakesOpGuardAndSnapshots: confirming RUN
// emits a startOpMsg (name "job-run") whose run creates a real snapshot
// under the one-op guard. jobsDepsWithRepo seeds one snapshot for the
// drill-in tests, so the post-run store carries two.
func TestJobs_RunConfirmedTakesOpGuardAndSnapshots(t *testing.T) {
	deps, _, _ := jobsDepsWithRepo(t) // alpha, prune unset -> off
	v := newJobsForTest(t, deps)
	v.tbl.SetCursor(0) // alpha
	v2, _ := pressJobsKey(v, 'r')
	m, cmd := v2.Update(confirmedMsg{id: jobRunConfirmID})
	v3 := m.(JobsView)
	if v3.stage != jobsRunning {
		t.Fatalf("stage = %v, want jobsRunning", v3.stage)
	}
	msgs := execCmds(t, cmd)
	var start startOpMsg
	var foundStart bool
	for _, msg := range msgs {
		if s, ok := msg.(startOpMsg); ok {
			start, foundStart = s, true
		}
	}
	if !foundStart {
		t.Fatalf("confirmed run must emit a startOpMsg, got %#v", msgs)
	}
	if start.name != "job-run" {
		t.Fatalf("op name = %q, want job-run", start.name)
	}
	// Run the op synchronously; it must create a snapshot and report done.
	res := start.run(context.Background())
	done, ok := res.(policyRunDoneMsg)
	if !ok {
		t.Fatalf("expected policyRunDoneMsg, got %#v", res)
	}
	if done.err != nil {
		t.Fatalf("run failed: %v", done.err)
	}
	if done.snapshots != 1 {
		t.Fatalf("snapshots = %d, want 1", done.snapshots)
	}
	snaps, err := deps.Repo.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 2 {
		t.Fatalf("ListSnapshots = %v, %v, want 2 (jobsDepsWithRepo's seed + this run)", snaps, err)
	}
	// Delivering the result moves to the done stage.
	m2, _ := v3.Update(res)
	v4 := m2.(JobsView)
	if v4.stage != jobsRunDone {
		t.Fatalf("stage after result = %v, want jobsRunDone", v4.stage)
	}
}

// TestJobs_RunRejectedResetsToList is the port of the deleted
// TestPoliciesView_RunRejectedResetsToList: if the op guard rejects the
// start (another op running), the view must leave the running stage and
// surface a notice.
func TestJobs_RunRejectedResetsToList(t *testing.T) {
	deps, _ := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.tbl.SetCursor(0)
	v2, _ := pressJobsKey(v, 'r')
	m, _ := v2.Update(confirmedMsg{id: jobRunConfirmID})
	v3 := m.(JobsView)
	m2, _ := v3.Update(opRejectedMsg{name: "job-run"})
	v4 := m2.(JobsView)
	if v4.stage != jobsList {
		t.Fatalf("stage after rejection = %v, want jobsList", v4.stage)
	}
	if v4.notice == "" {
		t.Fatal("rejection must set a notice banner")
	}
}

// TestJobs_FormReplaceGuardPreservesHooks is the port of the deleted
// TestPoliciesForm_ReplaceGuardPreservesHooks: adding a job whose name
// already exists must NOT silently overwrite — it pushes a replace
// confirm, and confirming preserves the existing policy's config-authored
// hooks, matching `policy add --replace`.
func TestJobs_FormReplaceGuardPreservesHooks(t *testing.T) {
	deps, path := jobsDeps(t)
	// Give alpha a hand-authored hook the form can't express.
	if err := config.Update(path, func(cfg *config.Config) error {
		p := cfg.Policies["alpha"]
		p.Hooks = config.PolicyHooks{OnFailureWebhookEnv: "SENTRA_ALERT_URL"}
		cfg.Policies["alpha"] = p
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	v := newJobsForTest(t, deps)
	v2, _ := pressJobsKey(v, 'a')
	v2.form.name.SetValue("alpha")
	v2.form.path.SetValue("/data/alpha-new")
	m, _ := v2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v3 := m.(JobsView)
	m2, _ := v3.Update(confirmedMsg{id: jobAddConfirmID})
	v4 := m2.(JobsView)

	// The write must NOT have happened yet — a replace confirm is up.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Policies["alpha"].Paths[0] == "/data/alpha-new" {
		t.Fatal("existing policy overwritten without the replace confirm")
	}

	// The replace confirm's side effect is the config write; the returned
	// view is never inspected again.
	v4.Update(confirmedMsg{id: jobReplaceConfirmID})
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Policies["alpha"]
	if len(p.Paths) != 1 || p.Paths[0] != "/data/alpha-new" {
		t.Fatalf("replace did not apply: %+v", p.Paths)
	}
	if p.Hooks.OnFailureWebhookEnv != "SENTRA_ALERT_URL" {
		t.Errorf("replace dropped the config-authored hooks: %+v", p.Hooks)
	}
}

// TestJobs_NilConfigPlaceholder is the port of the deleted
// TestScheduleView_NilConfigPlaceholder (mirroring PoliciesView's own
// analogous test): an empty Deps — no ConfigPath, no Config — must not
// panic; it must set loadErr and View() must render the placeholder
// reload() actually writes ("no config file configured"), not some other
// text.
func TestJobs_NilConfigPlaceholder(t *testing.T) {
	v := NewJobsView(Deps{})
	if v.loadErr == "" {
		t.Fatal("empty deps must set a load error")
	}
	if !strings.Contains(v.View(), "no config file configured") {
		t.Errorf("view must surface the missing-config placeholder:\n%s", v.View())
	}
}

// TestRunPolicyRetentionPrune_UnknownModeIsFailClosed is the port of the
// deleted policies_test.go test of the same name — runPolicyRetentionPrune
// itself moved to jobs_run.go verbatim, so this test needed no adaptation
// beyond its new home. Even if an unrecognized mode reaches
// runPolicyRetentionPrune (defense in depth — armRun/policycfg.Validate
// should already have refused it upstream), it must be a no-op — never
// fall through to DeleteSnapshot+GC. With KeepLast=1 and two snapshots, an
// "apply" would drop one; an unknown mode must delete nothing.
func TestRunPolicyRetentionPrune_UnknownModeIsFailClosed(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	policy := repo.RetentionPolicy{KeepLast: 1}
	if err := runPolicyRetentionPrune(context.Background(), r, policy, "aply"); err != nil {
		t.Fatalf("unknown mode should be a no-op, got error: %v", err)
	}
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("unknown prune mode deleted snapshots: have %d, want 2 (fail-closed)", len(snaps))
	}
}

// TestJobs_RunExecutesHooks is the port of the deleted
// TestPoliciesRun_ExecutesHooks: a TUI job run executes the same hooks the
// CLI run does — a before hook lands its output in the snapshot, and a
// failing before hook aborts the run and fires on_failure. Skipping hooks
// would make TUI runs back up different data than CLI runs of the same
// policy. Hooks are config-authored only — the JobsView form has no field
// for them (see TestJobs_FormReplaceGuardPreservesHooks, which relies on
// the same fact) — so this test writes them straight into sentra.yaml
// rather than through the form, exactly like the deleted original did.
func TestJobs_RunExecutesHooks(t *testing.T) {
	r := newFlowRepo(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "failed.marker")

	path := filepath.Join(dir, "sentra.yaml")
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "b"
	cfg.Policies["hooked"] = config.PolicyConfig{
		Paths:    []string{src},
		Schedule: config.PolicySchedule{Cadence: "manual"},
		Hooks: config.PolicyHooks{
			Before: "echo dumped > " + filepath.Join(src, "dump.txt"),
		},
	}
	cfg.Policies["failing"] = config.PolicyConfig{
		Paths:    []string{src},
		Schedule: config.PolicySchedule{Cadence: "manual"},
		Hooks: config.PolicyHooks{
			Before:    "exit 7",
			OnFailure: "touch " + marker,
		},
	}
	if err := config.Write(path, &cfg); err != nil {
		t.Fatal(err)
	}
	deps := Deps{Repo: r, Config: &cfg, ConfigPath: path}

	runJobByName := func(name string) policyRunDoneMsg {
		t.Helper()
		v := newJobsForTest(t, deps)
		for i, n := range v.names {
			if n == name {
				v.tbl.SetCursor(i)
			}
		}
		m, cmd := v.startRun()
		_ = m
		var start startOpMsg
		for _, msg := range execCmds(t, cmd) {
			if s, ok := msg.(startOpMsg); ok {
				start = s
			}
		}
		if start.run == nil {
			t.Fatal("startRun emitted no op")
		}
		done, ok := start.run(context.Background()).(policyRunDoneMsg)
		if !ok {
			t.Fatal("op did not return policyRunDoneMsg")
		}
		return done
	}

	if done := runJobByName("hooked"); done.err != nil {
		t.Fatalf("hooked run: %v", done.err)
	}
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots: %v err=%v", snaps, err)
	}
	man, err := r.LoadSnapshot(context.Background(), snaps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, fe := range man.Tree {
		if fe.Path == "dump.txt" {
			found = true
		}
	}
	if !found {
		t.Error("before-hook output missing from the TUI-run snapshot")
	}

	if done := runJobByName("failing"); done.err == nil {
		t.Fatal("failing before hook must fail the TUI run")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("on_failure hook did not run from the TUI path: %v", err)
	}
	snaps, _ = r.ListSnapshots(context.Background())
	if len(snaps) != 1 {
		t.Errorf("aborted run must not snapshot; got %d", len(snaps))
	}
}
