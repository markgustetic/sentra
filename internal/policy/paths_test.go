package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

// TestResolvePath pins the rule every stored policy path is held to:
// the result is absolute and clean, `~` means the home directory, and
// a bare relative path means the caller's cwd — the only moment a cwd
// is meaningful, since a timer-launched run has none worth trusting.
func TestResolvePath(t *testing.T) {
	home := t.TempDir()
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := t.TempDir()
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
