package repo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/crypto"
)

func TestListSnapshots_Empty(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	infos, err := r.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("expected empty list, got %d entries", len(infos))
	}
}

// TestListSnapshots_RebuildSkipsIndexWriteWhileLocked: the fallback
// path's opportunistic index write is a read-modify-write of
// meta/snapshots with no lock, racing the append a concurrent
// CreateSnapshot makes under the lock — a stale rebuild landing last
// erased the new entry. The rebuilt index is persisted only when the
// repo lock can be taken without waiting; while it is held, the list
// is still served (from manifests) and nothing is written. Once the
// lock is free, the next listing persists the index as before.
func TestListSnapshots_RebuildSkipsIndexWriteWhileLocked(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha")
	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Force the fallback path.
	if err := store.Delete(ctx, snapshotIndexKey); err != nil {
		t.Fatal(err)
	}

	held, err := acquireLock(ctx, store, "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	infos, err := r.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListSnapshots under a held lock must still answer: %v", err)
	}
	if len(infos) != 1 || infos[0].ID != snap.ID {
		t.Errorf("list under held lock: got %+v, want the one snapshot", infos)
	}
	if _, err := store.Stat(ctx, snapshotIndexKey); !errors.Is(err, blobstore.ErrNotFound) {
		t.Errorf("index written while another operation held the lock (stat err=%v)", err)
	}
	// The listing must not have disturbed the holder's lock.
	if !strings.Contains(readLockHolder(ctx, store), held.UUID) {
		t.Errorf("ListSnapshots disturbed a lock it did not own: %s", readLockHolder(ctx, store))
	}
	releaseLock(ctx, store, held)

	if _, err := r.ListSnapshots(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(ctx, snapshotIndexKey); err != nil {
		t.Errorf("index not persisted once the lock was free: %v", err)
	}
	// And the rebuild released the lock it took.
	if _, err := store.Stat(ctx, lockKey); !errors.Is(err, blobstore.ErrNotFound) {
		t.Errorf("ListSnapshots left the lock behind (stat err=%v)", err)
	}
}

func TestListSnapshots_NewestFirst(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	repoKey, err := r.keyOrErr()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	defer crypto.Zeroize(repoKey)

	base := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	entries := []SnapshotInfo{
		{ID: "snap-20260626T120100Z-00000001", CreatedAt: base.Add(time.Minute), Tag: "middle"},
		{ID: "snap-20260626T120000Z-00000000", CreatedAt: base, Tag: "oldest"},
		{ID: "snap-20260626T120200Z-00000002", CreatedAt: base.Add(2 * time.Minute), Tag: "newest"},
	}
	if err := r.saveSnapshotIndex(ctx, repoKey, &snapshotIndex{Entries: entries}); err != nil {
		t.Fatalf("save index: %v", err)
	}

	infos, err := r.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(infos))
	}
	// ListSnapshots orders newest-first even if an index is stored out
	// of order.
	if infos[0].ID != entries[2].ID {
		t.Errorf("infos[0].ID: got %q, want %q (newest)", infos[0].ID, entries[2].ID)
	}
	if infos[1].ID != entries[0].ID {
		t.Errorf("infos[1].ID: got %q, want %q", infos[1].ID, entries[0].ID)
	}
	if infos[2].ID != entries[1].ID {
		t.Errorf("infos[2].ID: got %q, want %q (oldest)", infos[2].ID, entries[1].ID)
	}
	// CreatedAt must be monotone non-increasing.
	for i := 1; i < len(infos); i++ {
		if infos[i].CreatedAt.After(infos[i-1].CreatedAt) {
			t.Errorf("infos[%d].CreatedAt %v after infos[%d].CreatedAt %v",
				i, infos[i].CreatedAt, i-1, infos[i-1].CreatedAt)
		}
	}
}
