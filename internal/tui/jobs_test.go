package tui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/repo"
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
