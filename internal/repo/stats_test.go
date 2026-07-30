package repo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestStats_DedupAndUniqueBytes: two snapshots sharing most content —
// logical bytes double-count the shared file, stored bytes don't, so
// the dedup factor exceeds 1; each snapshot's unique bytes reflect
// only the chunks no other snapshot references.
func TestStats_DedupAndUniqueBytes(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "shared.txt"), strings.Repeat("shared-content-", 400))
	s1, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "extra.txt"), strings.Repeat("only-in-two-", 400))
	s2, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	stats, err := r.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Snapshots != 2 {
		t.Errorf("Snapshots = %d, want 2", stats.Snapshots)
	}
	if stats.LogicalBytes <= stats.StoredBytes {
		t.Errorf("shared content must dedup: logical %d should exceed stored %d",
			stats.LogicalBytes, stats.StoredBytes)
	}
	if stats.DedupFactor() <= 1.0 {
		t.Errorf("dedup factor = %v, want > 1", stats.DedupFactor())
	}

	unique := map[string]int64{}
	for _, s := range stats.PerSnapshot {
		unique[s.ID] = s.UniqueBytes
	}
	// s1 is entirely shared with s2 → no unique chunks.
	if unique[s1.ID] != 0 {
		t.Errorf("s1 unique bytes = %d, want 0 (fully shared)", unique[s1.ID])
	}
	// s2 owns extra.txt's chunks alone.
	if unique[s2.ID] == 0 {
		t.Errorf("s2 unique bytes = 0, want > 0 (owns extra.txt)")
	}
}
