package heuristics

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/markgustetic/sentra/internal/walker"
)

// makeSparseFile creates a sparse file at the given path with the
// requested logical size, using os.Truncate. The file's bytes are all
// zero on disk, but we lie to the test by recording size = wantSize
// in the walker.Entry, so the heuristic never has to actually read
// the body — it just looks at Entry.Size.
func makeSparseFile(t *testing.T, dir, name string, size int64) walker.Entry {
	t.Helper()
	abs := filepath.Join(dir, name)
	f, err := os.Create(abs) //nolint:gosec // path under t.TempDir()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return walker.Entry{
		AbsPath: abs,
		RelPath: name,
		Size:    st.Size(),
		Mode:    st.Mode(),
		MTime:   st.ModTime(),
	}
}

// TestLargeFiles_FlagsLargeFile: a 200 MiB file with the default
// 100 MiB threshold produces a warn finding.
func TestLargeFiles_FlagsLargeFile(t *testing.T) {
	dir := t.TempDir()
	entry := makeSparseFile(t, dir, "huge.bin", 200<<20)

	h := NewLargeFiles()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{entry}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Category != "large_files" || f.Severity != SeverityWarn {
		t.Errorf("category/severity: got %s/%s", f.Category, f.Severity)
	}
	if f.Target != entry.AbsPath {
		t.Errorf("target: got %s, want %s", f.Target, entry.AbsPath)
	}
	size, _ := f.Details["size"].(int64)
	if size != entry.Size {
		t.Errorf("details size: got %d, want %d", size, entry.Size)
	}
}

// TestLargeFiles_IgnoresSmallFile: a 50 MiB file is below the default
// threshold and produces no finding.
func TestLargeFiles_IgnoresSmallFile(t *testing.T) {
	dir := t.TempDir()
	entry := makeSparseFile(t, dir, "small.bin", 50<<20)

	h := NewLargeFiles()
	got, err := h.Run(context.Background(), Input{Walked: []walker.Entry{entry}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %+v", got)
	}
}

// TestLargeFiles_HonorsConfigOverride: setting LargeFileBytes=1 MiB
// turns a 2 MiB file into a finding (where the default 100 MiB would
// have ignored it).
func TestLargeFiles_HonorsConfigOverride(t *testing.T) {
	dir := t.TempDir()
	entry := makeSparseFile(t, dir, "two_mb.bin", 2<<20)

	h := NewLargeFiles()
	got, err := h.Run(context.Background(), Input{
		Walked: []walker.Entry{entry},
		Config: InputConfig{LargeFileBytes: 1 << 20},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
}

// TestLargeFiles_ExactBoundary: a file at exactly the threshold is
// NOT flagged — the predicate is "strictly greater than", matching
// the design's "files > threshold" wording.
func TestLargeFiles_ExactBoundary(t *testing.T) {
	dir := t.TempDir()
	entry := makeSparseFile(t, dir, "exact.bin", 1<<20)

	h := NewLargeFiles()
	got, err := h.Run(context.Background(), Input{
		Walked: []walker.Entry{entry},
		Config: InputConfig{LargeFileBytes: 1 << 20},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings at exact threshold, got %+v", got)
	}
}

// TestLargeFiles_Name: heuristic name is "large_files".
func TestLargeFiles_Name(t *testing.T) {
	if got, want := NewLargeFiles().Name(), "large_files"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}
