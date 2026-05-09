package repo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/progress"
)

// TestCreateSnapshot_ReportsProgress verifies the progress contract:
// CreateSnapshot must call SnapshotOptions.Progress with one Total()
// at the start (best-effort estimate from the walk) and Add() per
// uploaded chunk. Skipped (deduped) chunks count zero — they didn't
// move bytes.
func TestCreateSnapshot_ReportsProgress(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	root := t.TempDir()
	body := strings.Repeat("hello progress world\n", 1000)
	writeFile(t, filepath.Join(root, "a.txt"), body)
	writeFile(t, filepath.Join(root, "b.txt"), body)

	rep := &progress.RecordingReporter{}
	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{Progress: rep})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	total, done, events := rep.Snapshot()
	if total <= 0 {
		t.Errorf("total: got %d, want > 0 (estimate from walk)", total)
	}
	if events < 2 {
		t.Errorf("events: got %d, want at least 2 (Total + at least one Add)", events)
	}
	// done must equal the new bytes uploaded — content is identical so
	// the second file's chunks dedup against the first, meaning done
	// matches NewBytes (not 2x).
	if done != snap.Stats.NewBytes {
		t.Errorf("progress done: got %d, want %d (NewBytes)", done, snap.Stats.NewBytes)
	}
}

// TestCreateSnapshot_NilProgressOK confirms a nil Progress in SnapshotOptions
// is handled — CreateSnapshot defaults to NopReporter and runs unchanged.
func TestCreateSnapshot_NilProgressOK(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "x")
	if _, err := r.CreateSnapshot(ctx, root, SnapshotOptions{}); err != nil {
		t.Fatalf("create with nil progress: %v", err)
	}
}

// TestRestore_ReportsProgress verifies Restore calls RestoreOptions.Progress:
// Total() once at the start with the manifest's total bytes, Add() per file
// restored (or per chunk).
func TestRestore_ReportsProgress(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha alpha alpha")
	writeFile(t, filepath.Join(src, "b.txt"), "beta beta beta")

	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	rep := &progress.RecordingReporter{}
	dst := filepath.Join(t.TempDir(), "restored")
	if err := r.Restore(ctx, snap.ID, dst, RestoreOptions{Progress: rep}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	total, done, events := rep.Snapshot()
	if total <= 0 {
		t.Errorf("total: got %d, want > 0", total)
	}
	if events < 2 {
		t.Errorf("events: got %d, want at least 2", events)
	}
	if done <= 0 {
		t.Errorf("done: got %d, want > 0", done)
	}
}

// TestRestore_NilProgressOK confirms RestoreOptions{} (zero value) works.
func TestRestore_NilProgressOK(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "x.txt"), "y")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "out")
	if err := r.Restore(ctx, snap.ID, dst, RestoreOptions{}); err != nil {
		t.Fatalf("restore with nil progress: %v", err)
	}
}
