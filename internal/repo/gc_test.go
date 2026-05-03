package repo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
)

// TestGC_DeletesUnreferenced: take a snapshot, delete its manifest,
// then run GC. With no live snapshots the safety guard fires; we
// instead build a one-snapshot-then-empty scenario by keeping a
// minimal "live" manifest, then orphaning some blobs explicitly.
//
// Concretely: back up two distinct files into one snapshot, then
// re-back-up only one of them as the new sole snapshot. After
// deleting the first snapshot's manifest, GC should reap the chunks
// that only existed in the first snapshot.
func TestGC_DeletesUnreferenced(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	// First snapshot has both files.
	root1 := t.TempDir()
	writeFile(t, filepath.Join(root1, "a.txt"), strings.Repeat("alpha-content-", 200))
	writeFile(t, filepath.Join(root1, "b.txt"), strings.Repeat("bravo-content-", 200))
	first, err := r.CreateSnapshot(ctx, root1, SnapshotOptions{Tag: "first"})
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}

	// Second snapshot has only a.txt — different content so its chunks
	// are distinct from the first snapshot's a.txt.
	root2 := t.TempDir()
	writeFile(t, filepath.Join(root2, "a.txt"), strings.Repeat("alpha-content-", 200))
	if _, err := r.CreateSnapshot(ctx, root2, SnapshotOptions{Tag: "second"}); err != nil {
		t.Fatalf("second snapshot: %v", err)
	}

	beforeBlobs, err := store.List(ctx, "data/")
	if err != nil {
		t.Fatalf("list before: %v", err)
	}

	// Delete the first snapshot's manifest. Its unique chunks
	// (b.txt's content) are now unreferenced; a.txt's chunks remain
	// referenced by the second snapshot.
	if err := r.DeleteSnapshot(ctx, first.ID); err != nil {
		t.Fatalf("delete snapshot: %v", err)
	}

	stats, err := r.GC(ctx, nil)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if stats.DeletedBlobs == 0 {
		t.Fatal("expected GC to delete at least one orphaned blob")
	}
	if stats.LiveBlobs == 0 {
		t.Fatal("expected GC to leave at least one live blob")
	}
	if stats.DeletedBytes <= 0 {
		t.Fatalf("expected DeletedBytes > 0, got %d", stats.DeletedBytes)
	}

	afterBlobs, err := store.List(ctx, "data/")
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(afterBlobs) >= len(beforeBlobs) {
		t.Errorf("expected fewer blobs after GC, before=%d after=%d",
			len(beforeBlobs), len(afterBlobs))
	}
	if len(afterBlobs) != stats.LiveBlobs {
		t.Errorf("LiveBlobs (%d) should match data/ count after GC (%d)",
			stats.LiveBlobs, len(afterBlobs))
	}
}

// TestGC_KeepsReferenced: two snapshots with overlapping content. After
// deleting one, GC must keep blobs referenced by the surviving snapshot
// even though they were also referenced by the deleted one.
func TestGC_KeepsReferenced(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	// Both snapshots contain the same a.txt; the chunker is content-
	// addressed so the chunks dedupe across snapshots.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "shared.txt"), strings.Repeat("shared-", 500))
	first, err := r.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "first"})
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	// Second snapshot of identical content — manifest differs (id, time)
	// but data blobs are reused from the first snapshot.
	if _, err := r.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "second"}); err != nil {
		t.Fatalf("second: %v", err)
	}

	beforeBlobs, err := store.List(ctx, "data/")
	if err != nil {
		t.Fatalf("list before: %v", err)
	}

	if err := r.DeleteSnapshot(ctx, first.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	stats, err := r.GC(ctx, nil)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	// All shared blobs are still referenced by the surviving snapshot,
	// so GC should delete nothing.
	if stats.DeletedBlobs != 0 {
		t.Errorf("expected no deletions, got %d", stats.DeletedBlobs)
	}
	afterBlobs, err := store.List(ctx, "data/")
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(afterBlobs) != len(beforeBlobs) {
		t.Errorf("blob count changed: before=%d, after=%d",
			len(beforeBlobs), len(afterBlobs))
	}
}

// TestGC_RefusesEmptyRepo: GC on a freshly initialized repo refuses
// to run rather than deleting every blob in the store. The "no
// snapshots" condition is overwhelmingly more likely to be a bug than
// a real "I really do want to wipe everything" — that flow can ship
// behind a flag later.
func TestGC_RefusesEmptyRepo(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	_, err := r.GC(ctx, nil)
	if err == nil {
		t.Fatal("expected error from GC on empty repo, got nil")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "no snapshots") {
		t.Errorf("expected error to mention 'no snapshots', got %v", err)
	}
}

// TestDeleteSnapshot_RemovesManifest verifies the manifest at
// snapshots/<id> is gone after DeleteSnapshot. Reading it via
// LoadSnapshot returns ErrNotFound (preserved from the blobstore).
func TestDeleteSnapshot_RemovesManifest(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "f.txt"), "x")
	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "to-delete"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := r.DeleteSnapshot(ctx, snap.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = r.LoadSnapshot(ctx, snap.ID)
	if !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// TestDeleteSnapshot_InvalidID: a malformed snapshot ID is rejected
// before any blobstore call. Reuses the same validator as LoadSnapshot.
func TestDeleteSnapshot_InvalidID(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	if err := r.DeleteSnapshot(ctx, "../config"); err == nil {
		t.Fatal("expected error on path-traversal id, got nil")
	}
}

// TestGC_HonorsKeepIDs: when keepIDs is provided, only those snapshots
// contribute to the live set. Snapshots not in keepIDs are treated as
// not-live, so their unique blobs are reaped even if the manifest is
// still on disk. This is the integration point with prune: prune
// computes the keep-set, deletes drop manifests, then calls GC with
// keepIDs to clean up everything that was just dropped.
func TestGC_HonorsKeepIDs(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	root1 := t.TempDir()
	writeFile(t, filepath.Join(root1, "old.txt"), strings.Repeat("old-bytes-", 300))
	old, err := r.CreateSnapshot(ctx, root1, SnapshotOptions{})
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	root2 := t.TempDir()
	writeFile(t, filepath.Join(root2, "new.txt"), strings.Repeat("new-bytes-", 300))
	keep, err := r.CreateSnapshot(ctx, root2, SnapshotOptions{})
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	beforeBlobs, err := store.List(ctx, "data/")
	if err != nil {
		t.Fatalf("list before: %v", err)
	}

	// Pretend we're about to drop `old` and only keep `keep`. We pass
	// keepIDs without first deleting `old`'s manifest — GC builds its
	// live set from keepIDs only, so `old`'s blobs are reaped even
	// though the manifest is still there.
	keepIDs := map[string]bool{keep.ID: true}
	stats, err := r.GC(ctx, keepIDs)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if stats.DeletedBlobs == 0 {
		t.Errorf("expected at least one deletion, got 0")
	}

	afterBlobs, err := store.List(ctx, "data/")
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(afterBlobs) >= len(beforeBlobs) {
		t.Errorf("blob count: before=%d, after=%d (expected reduction)",
			len(beforeBlobs), len(afterBlobs))
	}

	// Sanity: `old`'s manifest is still on disk (we did NOT delete
	// it; that's the prune flow's responsibility).
	if _, err := r.LoadSnapshot(ctx, old.ID); err != nil {
		t.Errorf("old snapshot manifest gone but we didn't delete it: %v", err)
	}
}
