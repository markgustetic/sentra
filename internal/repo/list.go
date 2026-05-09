package repo

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/markgustetic/sentra/internal/crypto"
)

// ListSnapshots returns the repository's snapshots, ordered newest-
// first by CreatedAt. Each entry's full file tree is *not* loaded —
// callers wanting that should LoadSnapshot by ID.
//
// Fast path: a single GET against the snapshot index blob. Mature
// repos with many snapshots see this as O(1) network cost. The index
// is maintained by CreateSnapshot and DeleteSnapshot.
//
// Fallback path: when the index is absent (legacy repos that haven't
// been written by an index-aware build, or a freshly-init'd one) or
// unreadable (corrupt blob, version mismatch), ListSnapshots falls
// back to manifest fan-out — load each snapshots/<id> blob individually.
// On a successful fallback, the index is opportunistically rebuilt so
// the next call is fast.
func (r *Repo) ListSnapshots(ctx context.Context) ([]SnapshotInfo, error) {
	repoKey, err := r.keyOrErr()
	if err != nil {
		return nil, err
	}
	defer crypto.Zeroize(repoKey)

	// Fast path: read the index blob.
	idx, idxErr := r.loadSnapshotIndex(ctx, repoKey)
	if idxErr == nil && idx != nil {
		// Defensive copy so callers can't mutate the in-memory cache.
		out := slices.Clone(idx.Entries)
		sortNewestFirst(out)
		return out, nil
	}
	if idxErr != nil {
		// A corrupt or version-mismatched index falls through to the
		// manifest path; the rebuilt index from below replaces the
		// bad blob so this is self-healing.
		slog.LogAttrs(ctx, slog.LevelWarn,
			"snapshot index unreadable, falling back to manifest scan",
			slog.String("error", idxErr.Error()))
	}

	// Fallback: O(N) manifest fan-out.
	out, err := r.listSnapshotsFromManifests(ctx)
	if err != nil {
		return nil, err
	}
	// Best-effort write of the rebuilt index so the next call is fast.
	// A write failure is non-fatal — we already have the answer.
	if werr := r.saveSnapshotIndex(ctx, repoKey, &snapshotIndex{Entries: out}); werr != nil {
		slog.LogAttrs(ctx, slog.LevelWarn,
			"failed to write snapshot index after rebuild",
			slog.String("error", werr.Error()))
	}
	return out, nil
}

// listSnapshotsFromManifests is the original ListSnapshots logic,
// retained as the fallback path for repos without an index blob and
// for the index-rebuild path. The cost is O(N) manifest reads.
//
// Kept package-private — production callers go through ListSnapshots
// which prefers the indexed path. Exported only for the test suite to
// drive both paths independently if needed.
func (r *Repo) listSnapshotsFromManifests(ctx context.Context) ([]SnapshotInfo, error) {
	entries, err := r.store.List(ctx, snapshotPrefix)
	if err != nil {
		return nil, fmt.Errorf("repo: list snapshots: %w", err)
	}
	out := make([]SnapshotInfo, 0, len(entries))
	for _, info := range entries {
		id := strings.TrimPrefix(info.Key, snapshotPrefix)
		if id == "" || id == info.Key {
			// Defensive: List returned something that didn't have
			// the expected prefix. Skip rather than fail the whole
			// list operation.
			continue
		}
		m, err := r.LoadSnapshot(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("repo: load %q: %w", id, err)
		}
		out = append(out, SnapshotInfo{
			ID:        m.ID,
			CreatedAt: m.CreatedAt,
			Tag:       m.Tag,
			Stats:     m.Stats,
		})
	}
	sortNewestFirst(out)
	return out, nil
}
