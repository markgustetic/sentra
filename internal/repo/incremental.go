package repo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/markgustetic/sentra/internal/blobstore"
)

// reusedChunkProbe confirms, once per snapshot, that a chunk the
// incremental scan wants to reuse still exists in the store. Walker
// workers call it concurrently, so it bounds the cost the way
// captureFile's dedup Stat is bounded — by the walker's pool — and
// adds single-flight per hash: two unchanged files sharing a chunk
// (or two workers racing on one) produce exactly one Stat. Results
// are cached for the run, present or absent; a transient transport
// error is cached too, because the snapshot aborts on it anyway.
type reusedChunkProbe struct {
	store blobstore.Store
	seen  sync.Map // hex hash → *chunkProbeResult
}

type chunkProbeResult struct {
	once    sync.Once
	present bool
	err     error
}

// allPresent reports whether every chunk in hashes exists. ErrNotFound
// is an answer (false), not an error; anything else aborts the caller.
func (p *reusedChunkProbe) allPresent(ctx context.Context, hashes []string) (bool, error) {
	for _, h := range hashes {
		v, _ := p.seen.LoadOrStore(h, &chunkProbeResult{})
		res := v.(*chunkProbeResult)
		res.once.Do(func() {
			_, err := p.store.Stat(ctx, ChunkKey(h))
			switch {
			case err == nil:
				res.present = true
			case errors.Is(err, blobstore.ErrNotFound):
				res.present = false
			default:
				res.err = fmt.Errorf("repo: stat reused chunk %s: %w", ChunkKey(h), err)
			}
		})
		if res.err != nil {
			return false, res.err
		}
		if !res.present {
			return false, nil
		}
	}
	return true, nil
}

// parentFileEntries returns the regular-file entries of the newest
// snapshot whose Root matches absRoot, keyed by path — the incremental
// scan's reuse source. Any failure (no parent, unreadable index or
// manifest) degrades to a full scan: nil is always a correct answer,
// just a slower one.
func (r *Repo) parentFileEntries(ctx context.Context, absRoot string) map[string]FileEntry {
	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		slog.LogAttrs(ctx, slog.LevelWarn, "incremental scan disabled: list snapshots failed",
			slog.String("error", err.Error()))
		return nil
	}
	for _, s := range snaps { // newest-first
		if s.Root != absRoot {
			continue
		}
		m, err := r.LoadSnapshot(ctx, s.ID)
		if err != nil {
			slog.LogAttrs(ctx, slog.LevelWarn, "incremental scan disabled: parent manifest unreadable",
				slog.String("snapshot_id", s.ID),
				slog.String("error", err.Error()))
			return nil
		}
		out := make(map[string]FileEntry, len(m.Tree))
		for _, fe := range m.Tree {
			if fe.IsFile() {
				out[fe.Path] = fe
			}
		}
		return out
	}
	return nil
}
