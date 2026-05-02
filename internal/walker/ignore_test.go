package walker

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIgnore_MatchesGlobs covers the three pattern shapes the design
// promises: a leaf glob (*.log), a recursive ** glob (node_modules/**),
// and a directory-only suffix (build/). Negative cases ensure that
// unrelated paths in adjacent directories are not accidentally caught.
func TestIgnore_MatchesGlobs(t *testing.T) {
	patterns := []string{"*.log", "node_modules/**", "build/"}
	m := NewMatcher(patterns)
	cases := map[string]bool{
		"foo.log":             true,
		"node_modules/x/y.js": true,
		"build/out":           true,
		"src/foo.go":          false,
		"logs/keep.txt":       false,
	}
	for path, want := range cases {
		if got := m.Match(path); got != want {
			t.Errorf("Match(%q)=%v, want %v", path, got, want)
		}
	}
}

// TestIgnore_NegationOverridesMatch verifies the gitignore rule that
// a later "!pattern" line re-includes a path that an earlier line
// would have excluded. Without this, users can't poke holes in
// broad globs (e.g. "ignore all logs but keep this one").
func TestIgnore_NegationOverridesMatch(t *testing.T) {
	m := NewMatcher([]string{"*.log", "!keep.log"})
	if m.Match("keep.log") {
		t.Error("Match(keep.log)=true, want false (negation should override)")
	}
	if !m.Match("other.log") {
		t.Error("Match(other.log)=false, want true (still ignored)")
	}
}

// TestIgnore_LoadFileMissing: the common case where a project has no
// .sentraignore at all should silently produce a usable Matcher that
// matches nothing. Returning an error here would force every caller
// to special-case "not found", which is noise.
func TestIgnore_LoadFileMissing(t *testing.T) {
	m, err := LoadIgnoreFile(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if m == nil {
		t.Fatal("missing file should still return a non-nil matcher")
	}
	if m.Match("anything.log") {
		t.Error("empty matcher should not match anything")
	}
}

// TestIgnore_LoadFileSkipsCommentsAndBlanks: gitignore syntax allows
// leading "#" comments and blank lines. The underlying library handles
// this, but we lock the behavior in case we ever swap the impl.
func TestIgnore_LoadFileSkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".sentraignore")
	content := "# this is a comment\n" +
		"\n" +
		"*.log\n" +
		"# another comment\n" +
		"\n" +
		"build/\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadIgnoreFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("foo.log") {
		t.Error("Match(foo.log)=false, want true")
	}
	if !m.Match("build/out") {
		t.Error("Match(build/out)=false, want true")
	}
	if m.Match("# this is a comment") {
		t.Error("comment line was treated as a pattern")
	}
}

// TestIgnore_HandlesWindowsLineEndings: a file authored on Windows
// will have CRLF separators. Without explicit handling, the trailing
// "\r" on each line would become part of the pattern and break
// matching ("*.log\r" doesn't match "foo.log").
func TestIgnore_HandlesWindowsLineEndings(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".sentraignore")
	if err := os.WriteFile(p, []byte("*.log\r\nbuild/\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := LoadIgnoreFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("foo.log") {
		t.Error("Match(foo.log)=false, want true (CRLF should be stripped)")
	}
	if !m.Match("build/x") {
		t.Error("Match(build/x)=false, want true (CRLF should be stripped)")
	}
}

// TestIgnore_NilMatcherSafe: a zero-value or nil Matcher should be
// safe to call. This lets callers pass *Matcher around without
// nil-checking at every site.
func TestIgnore_NilMatcherSafe(t *testing.T) {
	var m *Matcher
	if m.Match("foo.log") {
		t.Error("nil matcher should match nothing")
	}
}

// TestIgnore_EmptyPatterns: NewMatcher with no patterns should produce
// a Matcher that matches nothing. Used by LoadIgnoreFile when the
// target file is missing.
func TestIgnore_EmptyPatterns(t *testing.T) {
	m := NewMatcher(nil)
	if m == nil {
		t.Fatal("NewMatcher(nil) returned nil")
	}
	if m.Match("anything") {
		t.Error("empty matcher should not match anything")
	}
}
