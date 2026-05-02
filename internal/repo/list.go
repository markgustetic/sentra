package repo

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ListSnapshots returns the repository's snapshots, ordered newest-
// first by CreatedAt. Each entry's full file tree is *not* loaded —
// callers wanting that should LoadSnapshot by ID.
//
// For v1 we download every manifest header to read its CreatedAt and
// Stats. Acceptable while repos are tens-to-hundreds of snapshots; a
// future phase will introduce an index file (see design doc) so we
// can answer this without a manifest fan-out.
func (r *Repo) ListSnapshots(ctx context.Context) ([]SnapshotInfo, error) {
	if _, err := r.keyOrErr(); err != nil {
		return nil, err
	}
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
	// Newest first; ties broken by ID (descending) for a stable
	// order even when timestamps collide on coarse clocks.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}
