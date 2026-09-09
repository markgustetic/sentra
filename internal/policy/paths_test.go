package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// TestResolvePath pins the rule every stored policy path is held to:
// the result is absolute and clean, `~` means the home directory, and
// a bare relative path means the caller's cwd — the only moment a cwd
// is meaningful, since a timer-launched run has none worth trusting.
func TestResolvePath(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"~/docs":     filepath.Join(home, "docs"),
		"~":          home,
		"/data//x/":  "/data/x",
		"rel":        filepath.Join(cwd, "rel"),
		".":          cwd,
		"./a/../b":   filepath.Join(cwd, "b"),
		"/abs/path/": "/abs/path",
	}
	for in, want := range cases {
		got, err := ResolvePath(in)
		if err != nil {
			t.Errorf("ResolvePath(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ResolvePath(%q) = %q, want %q", in, got, want)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("ResolvePath(%q) = %q is not absolute", in, got)
		}
	}
}

// TestResolvePath_Rejects: an empty path and a tilde with no home to
// expand against cannot become a real directory — Validate must refuse
// them instead of letting a timer discover the problem at 03:00.
func TestResolvePath_Rejects(t *testing.T) {
	t.Setenv("HOME", "")
	for _, in := range []string{"", "   ", "~/x", "~"} {
		if got, err := ResolvePath(in); err == nil {
			t.Errorf("ResolvePath(%q) = %q, want error", in, got)
		}
	}
}

// TestResolvePathFrom: at run time a relative path stored by hand in
// sentra.yaml anchors to base (the config's directory), never to the
// process cwd — launchd starts jobs in `/`, so a cwd-relative "." would
// back up the root filesystem.
func TestResolvePathFrom(t *testing.T) {
	home := realTempDir(t)
	t.Setenv("HOME", home)
	base := realTempDir(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		".":       base,
		"src":     filepath.Join(base, "src"),
		"../up":   filepath.Join(filepath.Dir(base), "up"),
		"~/docs":  filepath.Join(home, "docs"),
		"/abs/p/": "/abs/p",
	}
	for in, want := range cases {
		got, err := ResolvePathFrom(in, base)
		if err != nil {
			t.Errorf("ResolvePathFrom(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ResolvePathFrom(%q) = %q, want %q", in, got, want)
		}
		if strings.HasPrefix(got, cwd+string(filepath.Separator)) || got == cwd {
			t.Errorf("ResolvePathFrom(%q) = %q resolved against the process cwd", in, got)
		}
	}
	if _, err := ResolvePathFrom("src", ""); err == nil {
		t.Error("ResolvePathFrom with an empty base must fail rather than fall back to cwd")
	}
	if _, err := ResolvePathFrom("src", "relative/base"); err == nil {
		t.Error("ResolvePathFrom with a relative base must fail rather than fall back to cwd")
	}
}

// TestNormalizePath_MatchesResolve: the TUI's error-free variant must
// agree with ResolvePath wherever ResolvePath succeeds — one rule for
// what a policy path means, or Last-run lookups and snapshot roots drift.
func TestNormalizePath_MatchesResolve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, in := range []string{"~/docs", "~", "/data//x/", "rel", "."} {
		want, err := ResolvePath(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := NormalizePath(in, home); got != want {
			t.Errorf("NormalizePath(%q) = %q, ResolvePath = %q", in, got, want)
		}
	}
}

func TestValidate_RejectsUnresolvablePath(t *testing.T) {
	t.Setenv("HOME", "")
	p := config.PolicyConfig{Paths: []string{"~/Documents"}}
	err := Validate("home", p)
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("Validate error: got %v, want path error", err)
	}
}

// TestResolvePath_MatchesSnapshotRoot pins the rule that makes Last-run
// lookups work at all: CreateSnapshot records repo.ResolveRoot's form of
// the root (symlinks resolved), so a policy path that reaches the same
// directory through a link must resolve to the identical string, or the
// snapshot the run just wrote is invisible to the next --if-due and the
// timer backs up every fire. On macOS every t.TempDir is such a path:
// /var is a link to /private/var.
func TestResolvePath_MatchesSnapshotRoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	want, err := repo.ResolveRoot(link)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{link, link + "/", link + "/./"} {
		got, err := ResolvePath(in)
		if err != nil {
			t.Fatalf("ResolvePath(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ResolvePath(%q) = %q, repo.ResolveRoot = %q", in, got, want)
		}
		if got := NormalizePath(in, home); got != want {
			t.Errorf("NormalizePath(%q) = %q, repo.ResolveRoot = %q", in, got, want)
		}
	}
	got, err := ResolvePathFrom("link", base)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("ResolvePathFrom(%q, %q) = %q, repo.ResolveRoot = %q", "link", base, got, want)
	}
}

// TestResolvePath_NotYetExisting: a policy may name a directory before it
// exists (add now, mkdir later). The resolver must not fail; it resolves
// the longest existing prefix so the stored string is the one a later
// snapshot of that directory records, whether or not a link sits above it.
func TestResolvePath_NotYetExisting(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	realBase, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolvePath(filepath.Join(link, "later", "deeper"))
	if err != nil {
		t.Fatalf("ResolvePath of a not-yet-existing path: %v", err)
	}
	if want := filepath.Join(realBase, "later", "deeper"); got != want {
		t.Errorf("ResolvePath = %q, want %q (longest existing prefix resolved)", got, want)
	}
	// Nothing of it exists: the absolute, cleaned spelling stands.
	if got, err := ResolvePath("/no/such/sentra//dir/"); err != nil || got != "/no/such/sentra/dir" {
		t.Errorf("ResolvePath(/no/such/sentra//dir/) = %q, %v; want /no/such/sentra/dir", got, err)
	}
}

// TestResolvePath_RejectsFile: a path that exists but is not a directory
// can never be snapshotted (repo.ErrRootNotDir), so Validate refuses it
// at add time rather than letting the timer fail at 03:00. NormalizePath
// only compares and stays error-free.
func TestResolvePath_RejectsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolvePath(file); !errors.Is(err, repo.ErrRootNotDir) {
		t.Errorf("ResolvePath(file) = %q, %v; want ErrRootNotDir", got, err)
	}
	if err := Validate("f", config.PolicyConfig{Paths: []string{file}}); err == nil || !strings.Contains(err.Error(), "path") {
		t.Errorf("Validate error: got %v, want path error", err)
	}
	if got := NormalizePath(file, ""); got == "" || !filepath.IsAbs(got) {
		t.Errorf("NormalizePath(file) = %q, want an absolute path", got)
	}
}

// realTempDir is t.TempDir with symlinks resolved. The resolver answers
// in repo.ResolveRoot's form, and on macOS a /var TMPDIR is a link to
// /private/var, so an expectation built from the raw t.TempDir string
// can never match.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
