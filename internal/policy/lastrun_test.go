package policy

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/repo"
)

var lastRunNow = time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC)

func snapAt(id, root, tag string, age time.Duration) repo.SnapshotInfo {
	return repo.SnapshotInfo{ID: id, Root: root, Tag: tag, CreatedAt: lastRunNow.Add(-age)}
}

func TestNormalizePath(t *testing.T) {
	home := t.TempDir()
	if got := NormalizePath("~/docs", home); got != filepath.Join(home, "docs") {
		t.Fatalf("tilde: got %q", got)
	}
	if got := NormalizePath("~", home); got != home {
		t.Fatalf("bare tilde: got %q", got)
	}
	if got := NormalizePath("/data//x/", home); got != "/data/x" {
		t.Fatalf("clean: got %q", got)
	}
	abs, _ := filepath.Abs("rel")
	if got := NormalizePath("rel", home); got != abs {
		t.Fatalf("relative: got %q want %q", got, abs)
	}
}

func TestHasPolicyTag(t *testing.T) {
	if !HasPolicyTag("policy:home nightly", "home") {
		t.Fatal("token match must hit")
	}
	if HasPolicyTag("policy:home-old nightly", "home") {
		t.Fatal("prefix of a longer token must not hit")
	}
	if HasPolicyTag("my-policy:home", "home") {
		t.Fatal("substring inside another token must not hit")
	}
}

func TestLastRun_TagWinsOverRootFallback(t *testing.T) {
	snaps := []repo.SnapshotInfo{
		snapAt("s-root-newer", "/data/a", "manual-tag", 1*time.Hour),
		snapAt("s-tagged-older", "/data/b", "policy:home", 5*time.Hour),
	}
	got, ok := LastRun("home", []string{"/data/a"}, snaps)
	if !ok || got.ID != "s-tagged-older" {
		t.Fatalf("tag match must win even when older: got %+v ok=%t", got, ok)
	}
}

func TestLastRun_NewestTaggedWins(t *testing.T) {
	snaps := []repo.SnapshotInfo{
		snapAt("s-old", "/data/a", "policy:home", 5*time.Hour),
		snapAt("s-new", "/data/a", "policy:home", 1*time.Hour),
		snapAt("s-mid", "/data/a", "policy:home", 3*time.Hour),
	}
	got, ok := LastRun("home", nil, snaps)
	if !ok || got.ID != "s-new" {
		t.Fatalf("want newest tagged, got %+v ok=%t", got, ok)
	}
}

func TestLastRun_RootFallbackWhenNoTag(t *testing.T) {
	snaps := []repo.SnapshotInfo{
		snapAt("s-old", "/data/a", "x", 5*time.Hour),
		snapAt("s-new", "/data/a", "y", 1*time.Hour),
		snapAt("s-other", "/data/z", "z", time.Minute),
	}
	got, ok := LastRun("home", []string{"/data/a"}, snaps)
	if !ok || got.ID != "s-new" {
		t.Fatalf("want newest root match, got %+v ok=%t", got, ok)
	}
	if _, ok := LastRun("home", []string{"/nope"}, snaps); ok {
		t.Fatal("no match must report ok=false")
	}
}
