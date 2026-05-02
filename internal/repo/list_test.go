package repo

import (
	"context"
	"path/filepath"
	"testing"
	"time"
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

func TestListSnapshots_NewestFirst(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "f.txt"), "x")

	// Create three snapshots with a gap so timestamps differ even on
	// fast clocks. The gap also ensures the snapshot ID's timestamp
	// component sorts the same way as CreatedAt would.
	var ids []string
	for i := 0; i < 3; i++ {
		snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "s"})
		if err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
		ids = append(ids, snap.ID)
		time.Sleep(50 * time.Millisecond)
	}

	infos, err := r.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(infos))
	}
	// ListSnapshots orders newest-first: index 0 is the last we
	// created.
	if infos[0].ID != ids[2] {
		t.Errorf("infos[0].ID: got %q, want %q (the last-created)", infos[0].ID, ids[2])
	}
	if infos[1].ID != ids[1] {
		t.Errorf("infos[1].ID: got %q, want %q", infos[1].ID, ids[1])
	}
	if infos[2].ID != ids[0] {
		t.Errorf("infos[2].ID: got %q, want %q (the first-created)", infos[2].ID, ids[0])
	}
	// CreatedAt must be monotone non-increasing.
	for i := 1; i < len(infos); i++ {
		if infos[i].CreatedAt.After(infos[i-1].CreatedAt) {
			t.Errorf("infos[%d].CreatedAt %v after infos[%d].CreatedAt %v",
				i, infos[i].CreatedAt, i-1, infos[i-1].CreatedAt)
		}
	}
}
