package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
