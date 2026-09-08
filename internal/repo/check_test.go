package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
)

func TestCheck_HealthySnapshot(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "keep this safe")
	if _, err := r.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "healthy"}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	report, err := r.Check(ctx, CheckOptions{Now: time.Unix(100, 0).UTC()})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !report.Healthy() {
		t.Fatalf("expected healthy report, got %+v", report)
	}
	if report.Snapshots != 1 {
		t.Errorf("Snapshots = %d, want 1", report.Snapshots)
	}
	if report.Files != 1 {
		t.Errorf("Files = %d, want 1", report.Files)
	}
	if report.ReferencedBlobs == 0 {
		t.Errorf("ReferencedBlobs = %d, want > 0", report.ReferencedBlobs)
	}
	if len(report.MissingBlobs) != 0 {
		t.Errorf("MissingBlobs = %+v, want empty", report.MissingBlobs)
	}
	if len(report.OrphanBlobs) != 0 {
		t.Errorf("OrphanBlobs = %+v, want empty", report.OrphanBlobs)
	}
}

func TestCheck_ReportsMissingReferencedChunk(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "broken.txt"), "the chunk will vanish")
	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	manifest, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if len(manifest.Tree) != 1 || len(manifest.Tree[0].Chunks) == 0 {
		t.Fatalf("unexpected manifest tree: %+v", manifest.Tree)
	}
	missingKey := ChunkKey(manifest.Tree[0].Chunks[0])
	if err := store.Delete(ctx, missingKey); err != nil {
		t.Fatalf("delete chunk: %v", err)
	}

	report, err := r.Check(ctx, CheckOptions{})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if report.Healthy() {
		t.Fatalf("expected unhealthy report after deleting %s", missingKey)
	}
	if len(report.MissingBlobs) != 1 {
		t.Fatalf("MissingBlobs = %+v, want one", report.MissingBlobs)
	}
	got := report.MissingBlobs[0]
	if got.Key != missingKey {
		t.Errorf("missing Key = %q, want %q", got.Key, missingKey)
	}
	if got.SnapshotID != snap.ID {
		t.Errorf("missing SnapshotID = %q, want %q", got.SnapshotID, snap.ID)
	}
	if got.Path != "broken.txt" {
		t.Errorf("missing Path = %q, want broken.txt", got.Path)
	}
}

func TestCheck_ReportsOrphanBlob(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "live.txt"), "live")
	if _, err := r.CreateSnapshot(ctx, root, SnapshotOptions{}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	orphanKey := DataPrefix + "ff/ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if err := store.Put(ctx, orphanKey, bytes.NewReader([]byte("sealed-ish"))); err != nil {
		t.Fatalf("put orphan: %v", err)
	}

	report, err := r.Check(ctx, CheckOptions{})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !report.Healthy() {
		t.Fatalf("orphan blobs should be a warning, got unhealthy report: %+v", report)
	}
	if len(report.OrphanBlobs) != 1 {
		t.Fatalf("OrphanBlobs = %+v, want one", report.OrphanBlobs)
	}
	if report.OrphanBlobs[0].Key != orphanKey {
		t.Errorf("orphan Key = %q, want %q", report.OrphanBlobs[0].Key, orphanKey)
	}
	if report.OrphanBytes == 0 {
		t.Errorf("OrphanBytes = %d, want > 0", report.OrphanBytes)
	}
}

func TestCheck_ReportsStaleLock(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer r.Close()

	started := time.Date(2026, 6, 23, 9, 0, 0, 0, time.UTC)
	lock := lockInfo{
		UUID:      "abc123",
		Operation: "snapshot",
		Host:      "host-a",
		PID:       os.Getpid(),
		StartedAt: started,
	}
	body, err := json.Marshal(lock)
	if err != nil {
		t.Fatalf("marshal lock: %v", err)
	}
	if err := store.Put(ctx, lockKey, bytes.NewReader(body)); err != nil {
		t.Fatalf("put lock: %v", err)
	}

	report, err := r.Check(ctx, CheckOptions{
		Now:            started.Add(3 * time.Hour),
		StaleLockAfter: time.Hour,
	})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if report.Lock == nil {
		t.Fatal("Lock = nil, want lock report")
	}
	if !report.Lock.Stale {
		t.Fatalf("Lock.Stale = false, want true: %+v", report.Lock)
	}
	if report.Healthy() {
		t.Fatalf("stale lock should make report unhealthy")
	}
}

// statCountingStore wraps a Store and counts Stat calls, so a test can
// prove Check derives blob presence from one List rather than one HEAD
// per chunk reference.
type statCountingStore struct {
	blobstore.Store
	stats atomic.Int32
}

func (s *statCountingStore) Stat(ctx context.Context, key string) (blobstore.Info, error) {
	s.stats.Add(1)
	return s.Store.Stat(ctx, key)
}

// TestCheck_PresenceFromListNotStat pins the rule that the missing-blob
// pass costs zero Stat calls regardless of how many snapshots reference
// a chunk: presence comes from the DataPrefix listing Check already
// performs for orphan detection. Per-reference HEADs made check cost
// (and take) O(references) requests on a large repo. A deleted chunk
// must still be reported, with the same attribution as before.
func TestCheck_PresenceFromListNotStat(t *testing.T) {
	ctx := context.Background()
	inner := blobstore.NewMemory()
	r, err := Init(ctx, inner, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer r.Close()

	// Several snapshots of the same content share every chunk, so
	// the reference count is a multiple of the unique chunk count.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "shared.txt"), "the same bytes, snapshotted repeatedly")
	var ids []string
	for i := 0; i < 3; i++ {
		snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
		if err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
		ids = append(ids, snap.ID)
	}
	r.Close()
	// Check walks manifests in key order and attributes a missing
	// blob to the first one it meets; IDs minted in the same second
	// differ only in their random suffix, so "first sorted" is the
	// rule, not "first created".
	slices.Sort(ids)
	firstID := ids[0]

	counter := &statCountingStore{Store: inner}
	r2, err := Open(ctx, counter, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r2.Close()
	counter.stats.Store(0)

	report, err := r2.Check(ctx, CheckOptions{})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !report.Healthy() {
		t.Fatalf("expected healthy report, got %+v", report)
	}
	if report.Snapshots != 3 {
		t.Errorf("Snapshots = %d, want 3", report.Snapshots)
	}
	if got := counter.stats.Load(); got != 0 {
		t.Errorf("Stat calls during healthy check = %d, want 0 (presence must come from List)", got)
	}

	manifest, err := r2.LoadSnapshot(ctx, firstID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if len(manifest.Tree) != 1 || len(manifest.Tree[0].Chunks) == 0 {
		t.Fatalf("unexpected manifest tree: %+v", manifest.Tree)
	}
	missingKey := ChunkKey(manifest.Tree[0].Chunks[0])
	if err := inner.Delete(ctx, missingKey); err != nil {
		t.Fatalf("delete chunk: %v", err)
	}
	counter.stats.Store(0)

	report, err = r2.Check(ctx, CheckOptions{})
	if err != nil {
		t.Fatalf("check after delete: %v", err)
	}
	if report.Healthy() {
		t.Fatalf("expected unhealthy report after deleting %s", missingKey)
	}
	if len(report.MissingBlobs) != 1 {
		t.Fatalf("MissingBlobs = %+v, want exactly one (deduplicated across snapshots)", report.MissingBlobs)
	}
	got := report.MissingBlobs[0]
	if got.Key != missingKey {
		t.Errorf("missing Key = %q, want %q", got.Key, missingKey)
	}
	if got.SnapshotID != firstID {
		t.Errorf("missing SnapshotID = %q, want first referencing snapshot %q", got.SnapshotID, firstID)
	}
	if got.Path != "shared.txt" {
		t.Errorf("missing Path = %q, want shared.txt", got.Path)
	}
	if n := counter.stats.Load(); n != 0 {
		t.Errorf("Stat calls during missing-blob check = %d, want 0", n)
	}
}
