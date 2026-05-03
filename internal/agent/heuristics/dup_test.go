package heuristics

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/markgustetic/sentra/internal/walker"
)

// stageBytes writes content to dir/relpath and returns a walker.Entry
// describing it. Used by the dup_paths tests to build small trees with
// known-duplicate (or known-distinct) files.
func stageBytes(t *testing.T, dir, rel string, content []byte) walker.Entry {
	t.Helper()
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, content, 0o600); err != nil { //nolint:gosec // path under t.TempDir()
		t.Fatalf("write: %v", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return walker.Entry{
		AbsPath: abs,
		RelPath: rel,
		Size:    st.Size(),
		Mode:    st.Mode(),
		MTime:   st.ModTime(),
	}
}

// TestDupPaths_FlagsIdenticalContent: a/x and b/x have the same
// bytes; c/y is unique. The heuristic emits ONE finding listing the
// two duplicate paths together.
func TestDupPaths_FlagsIdenticalContent(t *testing.T) {
	dir := t.TempDir()
	dup := []byte("hello world from sentra")
	a := stageBytes(t, dir, "a/x.txt", dup)
	b := stageBytes(t, dir, "b/x.txt", dup)
	c := stageBytes(t, dir, "c/y.txt", []byte("a different file"))

	h := NewDupPaths()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{a, b, c}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Category != "dup_paths" || f.Severity != SeverityInfo {
		t.Errorf("category/severity: got %s/%s", f.Category, f.Severity)
	}
	paths, _ := f.Details["paths"].([]string)
	sort.Strings(paths)
	want := []string{a.AbsPath, b.AbsPath}
	sort.Strings(want)
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("paths: got %v, want %v", paths, want)
	}
}

// TestDupPaths_NoFindingWhenSizesMatchButContentDiffers: three files
// of the same size but different content produce no finding. This
// also exercises the size-bucketing optimization: the heuristic
// hashes only when 2+ files share a size, but it must NOT report a
// match when the hashes diverge.
func TestDupPaths_NoFindingWhenSizesMatchButContentDiffers(t *testing.T) {
	dir := t.TempDir()
	a := stageBytes(t, dir, "a.bin", []byte("aaaaaaaa"))
	b := stageBytes(t, dir, "b.bin", []byte("bbbbbbbb"))
	c := stageBytes(t, dir, "c.bin", []byte("cccccccc"))

	h := NewDupPaths()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{a, b, c}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %+v", got)
	}
}

// TestDupPaths_DifferentSizesAreNeverDups: two files with different
// sizes are skipped in the size-bucket pass before any hashing. This
// asserts the optimization doesn't accidentally hash too much.
func TestDupPaths_DifferentSizesAreNeverDups(t *testing.T) {
	dir := t.TempDir()
	a := stageBytes(t, dir, "a.bin", []byte("short"))
	b := stageBytes(t, dir, "b.bin", []byte("considerably longer content"))

	h := NewDupPaths()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{a, b}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %+v", got)
	}
}

// TestDupPaths_TripleDup: three identical files produce ONE finding
// listing all three paths.
func TestDupPaths_TripleDup(t *testing.T) {
	dir := t.TempDir()
	body := []byte("triply duplicated content")
	a := stageBytes(t, dir, "x/a.txt", body)
	b := stageBytes(t, dir, "y/b.txt", body)
	c := stageBytes(t, dir, "z/c.txt", body)

	h := NewDupPaths()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{a, b, c}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	paths, _ := got[0].Details["paths"].([]string)
	if len(paths) != 3 {
		t.Errorf("paths: got %d, want 3 (%v)", len(paths), paths)
	}
}

// TestDupPaths_ZeroByteFilesNotReported: zero-byte files are
// trivially "duplicates" of each other but reporting them is noise —
// users have many empty files (.gitkeep, placeholder configs) and
// they're not actionable. The heuristic skips size=0 buckets.
func TestDupPaths_ZeroByteFilesNotReported(t *testing.T) {
	dir := t.TempDir()
	a := stageBytes(t, dir, ".gitkeep", []byte{})
	b := stageBytes(t, dir, "sub/.gitkeep", []byte{})

	h := NewDupPaths()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{a, b}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings on empty files, got %+v", got)
	}
}

// TestDupPaths_Name: heuristic name is "dup_paths".
func TestDupPaths_Name(t *testing.T) {
	if got, want := NewDupPaths().Name(), "dup_paths"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}
