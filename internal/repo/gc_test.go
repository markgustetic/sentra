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

// TestGC_KeepIDsProtectsConcurrentSnapshot reproduces the prune/GC
// TOCTOU data-loss race. prune freezes its keep-set from a ListSnapshots
// taken before it holds any lock, deletes the drop manifests, then calls
// GC(keepIDs). If a backup commits a brand-new snapshot in that window,
// its ID is absent from the frozen keepIDs. GC must NOT reap the new
// snapshot's chunks — a blob referenced by ANY snapshot present at GC
// time is live, regardless of keepIDs.
func TestGC_KeepIDsProtectsConcurrentSnapshot(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	// Snapshot A — the one prune decides to keep.
	rootA := t.TempDir()
	writeFile(t, filepath.Join(rootA, "a.txt"), strings.Repeat("keep-content-", 300))
	a, err := r.CreateSnapshot(ctx, rootA, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot A: %v", err)
	}

	// prune freezes the keep-set here (no lock held).
	keepIDs := map[string]bool{a.ID: true}

	// A concurrent backup commits snapshot C with unique content in the
	// window between prune's ListSnapshots and prune's GC.
	rootC := t.TempDir()
	writeFile(t, filepath.Join(rootC, "c.txt"), strings.Repeat("concurrent-content-", 300))
	c, err := r.CreateSnapshot(ctx, rootC, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot C: %v", err)
	}

	// Collect C's chunk keys — every one must survive GC.
	cm, err := r.LoadSnapshot(ctx, c.ID)
	if err != nil {
		t.Fatalf("load C: %v", err)
	}
	var cKeys []string
	for _, fe := range cm.Tree {
		for _, h := range fe.Chunks {
			cKeys = append(cKeys, ChunkKey(h))
		}
	}
	if len(cKeys) == 0 {
		t.Fatal("snapshot C has no chunks; test cannot detect the race")
	}

	// GC with the stale keep-set that predates C.
	if _, err := r.GC(ctx, keepIDs); err != nil {
		t.Fatalf("gc: %v", err)
	}

	// C's chunks must all remain: reaping any of them corrupts a
	// fully-committed snapshot (silent, unrecoverable data loss).
	for _, k := range cKeys {
		if _, err := store.Stat(ctx, k); err != nil {
			t.Errorf("GC reaped chunk %s referenced by concurrently-created snapshot C: %v", k, err)
		}
	}
}

// TestGC_ReapsDroppedSnapshotChunks: the prune flow deletes a dropped
// snapshot's manifest, then calls GC(keepIDs). GC reclaims the chunks
// unique to the now-deleted manifest while retaining those referenced by
// the surviving snapshots.
func TestGC_ReapsDroppedSnapshotChunks(t *testing.T) {
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

	// Mirror the real prune flow: delete the dropped manifest FIRST,
	// then GC with the kept ID. GC now builds its live set from the
	// snapshots actually present, so `old`'s unique chunks are orphans.
	if err := r.DeleteSnapshot(ctx, old.ID); err != nil {
		t.Fatalf("delete old: %v", err)
	}
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

	// The surviving snapshot must remain fully intact.
	km, err := r.LoadSnapshot(ctx, keep.ID)
	if err != nil {
		t.Fatalf("load keep: %v", err)
	}
	for _, fe := range km.Tree {
		for _, h := range fe.Chunks {
			if _, err := store.Stat(ctx, ChunkKey(h)); err != nil {
				t.Errorf("GC reaped chunk %s of the surviving snapshot: %v", ChunkKey(h), err)
			}
		}
	}
}
