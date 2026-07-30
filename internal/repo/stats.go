package repo

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/markgustetic/sentra/internal/crypto"
)

// RepoStats summarizes storage efficiency across the whole repository:
// how much data the snapshots logically describe, how much sealed data
// actually sits in the bucket, and which snapshots own unshared bytes
// (what deleting them would eventually reclaim).
type RepoStats struct {
	Snapshots int `json:"snapshots"`
	// LogicalBytes is the sum of plaintext file bytes across every
	// snapshot — what a naive full-copy-per-snapshot scheme would
	// store.
	LogicalBytes int64 `json:"logical_bytes"`
	// StoredBytes is the sealed size of the referenced data blobs —
	// what the bucket actually holds for snapshot content (compressed
	// and encrypted, orphans excluded).
	StoredBytes int64 `json:"stored_bytes"`
	// UniqueChunks is the number of distinct referenced chunks.
	UniqueChunks int `json:"unique_chunks"`
	// PerSnapshot is newest-first; UniqueBytes is the sealed size of
	// the chunks referenced by that snapshot alone.
	PerSnapshot []SnapshotStorageStats `json:"per_snapshot"`
}

// SnapshotStorageStats is one snapshot's storage footprint.
type SnapshotStorageStats struct {
	ID          string `json:"id"`
	Tag         string `json:"tag,omitempty"`
	Files       int    `json:"files"`
	Bytes       int64  `json:"bytes"`
	UniqueBytes int64  `json:"unique_bytes"`
}

// DedupFactor is LogicalBytes over StoredBytes — "how many times over
// the stored data pays for itself". Zero stored bytes yields 0.
func (s RepoStats) DedupFactor() float64 {
	if s.StoredBytes == 0 {
		return 0
	}
	return float64(s.LogicalBytes) / float64(s.StoredBytes)
}

// Stats walks every manifest and the data/ listing to compute storage
// efficiency. Cost: one manifest load per snapshot plus one data/
// listing — the same order of work as Check, minus the per-chunk
// Stat round-trips.
func (r *Repo) Stats(ctx context.Context) (RepoStats, error) {
	repoKey, err := r.keyOrErr()
	if err != nil {
		return RepoStats{}, err
	}
	crypto.Zeroize(repoKey) // fail-fast-after-Close; nothing below needs the key directly

	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		return RepoStats{}, err
	}

	// Sealed sizes come from one data/ listing.
	sizeByKey := map[string]int64{}
	dataObjects, err := r.store.List(ctx, DataPrefix)
	if err != nil {
		return RepoStats{}, fmt.Errorf("repo: list data: %w", err)
	}
	for _, obj := range dataObjects {
		sizeByKey[obj.Key] = obj.Size
	}

	stats := RepoStats{Snapshots: len(snaps)}
	refcount := map[string]int{}
	chunksBySnap := make(map[string][]string, len(snaps))
	for _, s := range snaps {
		m, err := r.LoadSnapshot(ctx, s.ID)
		if err != nil {
			return RepoStats{}, fmt.Errorf("repo: load %s: %w", s.ID, err)
		}
		stats.LogicalBytes += m.Stats.Bytes
		seen := map[string]struct{}{}
		for _, fe := range m.Tree {
			for _, h := range fe.Chunks {
				if _, dup := seen[h]; dup {
					continue // count a chunk once per snapshot
				}
				seen[h] = struct{}{}
				refcount[h]++
				chunksBySnap[s.ID] = append(chunksBySnap[s.ID], h)
			}
		}
	}

	for h := range refcount {
		stats.UniqueChunks++
		stats.StoredBytes += sizeByKey[ChunkKey(h)]
	}

	for _, s := range snaps { // newest-first from ListSnapshots
		var unique int64
		for _, h := range chunksBySnap[s.ID] {
			if refcount[h] == 1 {
				unique += sizeByKey[ChunkKey(h)]
			}
		}
		stats.PerSnapshot = append(stats.PerSnapshot, SnapshotStorageStats{
			ID:          s.ID,
			Tag:         s.Tag,
			Files:       s.Stats.Files,
			Bytes:       s.Stats.Bytes,
			UniqueBytes: unique,
		})
	}
	slices.SortFunc(stats.PerSnapshot, func(a, b SnapshotStorageStats) int {
		return cmp.Compare(b.ID, a.ID) // stable newest-first (IDs embed the timestamp)
	})
	return stats, nil
}
