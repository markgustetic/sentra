package tui

import (
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
