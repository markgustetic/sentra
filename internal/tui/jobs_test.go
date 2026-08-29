package tui

import (
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
	m2, cmd := m.(JobsView).Update(confirmedMsg{id: jobEditConfirmID})
	if cmd != nil {
		if res := cmd(); res != nil {
			m2m, _ := m2.(JobsView).Update(res)
			m2 = m2m
		}
	}
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
