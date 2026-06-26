package repo

import (
	"context"
	"testing"
	"time"

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
