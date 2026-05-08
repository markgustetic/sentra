package repo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/markgustetic/sentra/internal/blobstore"
)

// ErrEmptyRepo is returned by GC when the repository has zero
// snapshots and the caller did not supply an explicit keep-set.
// Otherwise GC would happily reap every chunk in the store — correct
// per the algorithm, but almost certainly not what the user wanted.
//
// The right way to opt in to the wipe-everything path is a future
// `--all` flag on `sentra prune`; until then we surface an error so
// the operator notices.
var ErrEmptyRepo = errors.New("repo: gc refused: no snapshots in repository")

// GCStats summarizes a garbage-collection run. All values are counts/
// sizes derived during the run itself; LiveBlobs is the number of
// data/ entries that survived (i.e. were referenced by at least one
// live manifest), DeletedBlobs / DeletedBytes describe what GC reaped.
type GCStats struct {
	// LiveBlobs is the number of data/ keys that were referenced by
	// the surviving manifests and therefore retained.
	LiveBlobs int

	// DeletedBlobs is the number of data/ keys actually removed.
	DeletedBlobs int

	// DeletedBytes is the sum of sealed-blob sizes for the deleted
	// objects, summed via Stat before each Delete. This is the wire-
	// format size, not the plaintext size — that's the right unit for
	// "how much storage did we free" in S3 billing terms.
	DeletedBytes int64
}

// DeleteSnapshot removes the manifest at snapshots/<id>. It does NOT
// touch any data/ blobs — call GC separately to reclaim orphaned
// chunks. The two-step shape lets prune batch deletions before
// running GC once at the end, so the live set is computed from the
// final state rather than walking the manifest tree O(N) times.
//
// The snapshot index is also updated to remove the entry, so the
// next ListSnapshots stays consistent. An index update failure is
// non-fatal — the manifest is gone (the source-of-truth delete
// already succeeded), so the worst case is a stale entry in the
// index that ListSnapshots will reconcile on the next manifest
// fan-back. The warning logs the slip for diagnosis.
//
// Returns blobstore.ErrNotFound (wrapped) if the manifest doesn't
// exist; callers can errors.Is against the sentinel.
func (r *Repo) DeleteSnapshot(ctx context.Context, id string) error {
	if err := validateSnapshotID(id); err != nil {
		return err
	}
	// We need the repo key to update the encrypted index. Acquiring
	// it here also serves the original purpose: a closed repo fails
	// fast instead of silently issuing an unauthenticated Delete.
	repoKey, err := r.keyOrErr()
	if err != nil {
		return err
	}
	defer zeroize(repoKey)

	if err := r.store.Delete(ctx, snapshotPrefix+id); err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			return err
		}
		return fmt.Errorf("repo: delete snapshot %q: %w", id, err)
	}

	// Drop the entry from the snapshot index. Best-effort: see
	// the docstring above for the failure-mode rationale.
	if err := r.updateSnapshotIndex(ctx, repoKey, func(idx *snapshotIndex) error {
		idx.Entries = slices.DeleteFunc(idx.Entries, func(e SnapshotInfo) bool {
			return e.ID == id
		})
		return nil
	}); err != nil {
		slog.LogAttrs(ctx, slog.LevelWarn,
			"failed to update snapshot index after DeleteSnapshot",
			slog.String("snapshot_id", id),
			slog.String("error", err.Error()),
		)
	}
	return nil
}

// GC removes blobs under "data/" not referenced by any current
// snapshot manifest. Call after deleting snapshots — orphaned blobs
// are reclaimed.
//
// keepIDs (optional, may be nil) is the set of snapshot IDs that
// should be considered "live" for the purposes of building the
// blob live-set. If nil, all snapshots in the repo are considered
// live (use this when you want a simple "GC orphans" pass after a
// manual DeleteSnapshot).
//
// Safety: GC refuses to run if the repo has zero snapshots and no
// keepIDs were provided. Otherwise it would gleefully delete every
// data blob in the store, which is technically correct per the
// algorithm but very rarely the user's intent. The intended override
// for that edge is a future `--all` flag on the prune CLI.
//
// Concurrency: GC and CreateSnapshot serialize via the repo-wide
// advisory lock at meta/lock. Acquiring the lock fails fast with
// ErrRepoLocked when another snapshot or GC is already running,
// rather than corrupting the live-set computation by racing with
// a concurrent CreateSnapshot. A crashed lock-holder leaves the
// blob behind; recovery is currently manual (delete the lock key
// out-of-band).
func (r *Repo) GC(ctx context.Context, keepIDs map[string]bool) (GCStats, error) {
	lockInfo, err := r.acquireLock(ctx, "gc")
	if err != nil {
		return GCStats{}, err
	}
	defer r.releaseLock(ctx, lockInfo)

	// Same fail-on-Close contract as DeleteSnapshot. The actual key
	// access happens inside LoadSnapshot, so the local copy here is
	// solely a "check Closed" probe; zeroize immediately.
	k, err := r.keyOrErr()
	if err != nil {
		return GCStats{}, err
	}
	zeroize(k)

	// Resolve the live ID set. Two paths: explicit keepIDs (passed in
	// by prune after deleting drop manifests but before they have any
	// effect on List) or "every snapshot currently in the repo".
	var liveIDs []string
	if keepIDs != nil {
		liveIDs = make([]string, 0, len(keepIDs))
		for id, keep := range keepIDs {
			if !keep {
				// keepIDs is a set-as-map; a key set to false is
				// explicitly NOT live. Lets prune carry around a single
				// map and toggle entries without rebuilding it.
				continue
			}
			liveIDs = append(liveIDs, id)
		}
	} else {
		entries, err := r.store.List(ctx, snapshotPrefix)
		if err != nil {
			return GCStats{}, fmt.Errorf("repo: list snapshots: %w", err)
		}
		for _, info := range entries {
			id := strings.TrimPrefix(info.Key, snapshotPrefix)
			if id == "" || id == info.Key {
				continue
			}
			liveIDs = append(liveIDs, id)
		}
		if len(liveIDs) == 0 {
			return GCStats{}, ErrEmptyRepo
		}
	}

	// Build the live blob-key set from each surviving manifest. A blob
	// referenced by ANY live manifest is live; the loop accumulates
	// every chunk hash into one map keyed by its data/ store key.
	liveKeys := make(map[string]struct{})
	for _, id := range liveIDs {
		m, err := r.LoadSnapshot(ctx, id)
		if err != nil {
			return GCStats{}, fmt.Errorf("repo: load snapshot %q: %w", id, err)
		}
		for _, fe := range m.Tree {
			for _, h := range fe.Chunks {
				liveKeys[ChunkKey(h)] = struct{}{}
			}
		}
	}

	// Walk every data/ entry, partition into live vs orphan, and
	// hand the orphan key set to BatchDelete. The S3 implementation
	// of BatchDelete uses DeleteObjects (1000 keys per request), so a
	// 10k-orphan GC drops from ~20k round trips to ~10 — the previous
	// implementation called Stat+Delete per orphan (Delete itself
	// also did a redundant Stat for ErrNotFound parity).
	dataEntries, err := r.store.List(ctx, DataPrefix)
	if err != nil {
		return GCStats{}, fmt.Errorf("repo: list data: %w", err)
	}

	stats := GCStats{}
	var orphanKeys []string
	for _, info := range dataEntries {
		if _, ok := liveKeys[info.Key]; ok {
			stats.LiveBlobs++
			continue
		}
		orphanKeys = append(orphanKeys, info.Key)
		// Sum sealed-blob sizes from the List result so DeletedBytes
		// reflects what we're freeing in S3-billing terms. On a
		// partial batch failure the value can slightly overstate
		// (we attempted N, S3 confirmed M); the returned error makes
		// that visible to the operator.
		if size, err := lookupSize(ctx, r.store, info); err == nil {
			stats.DeletedBytes += size
		}
	}

	if len(orphanKeys) == 0 {
		return stats, nil
	}

	deleted, err := r.store.BatchDelete(ctx, orphanKeys)
	stats.DeletedBlobs = deleted
	if err != nil {
		// Surface the error but keep stats populated so callers can
		// see the partial progress. BatchDelete is idempotent on
		// missing keys (per the Store contract), so we don't need to
		// special-case ErrNotFound here.
		return stats, fmt.Errorf("repo: batch delete orphans: %w", err)
	}
	return stats, nil
}

// lookupSize returns the size of the blob at info.Key. We try
// info.Size first (the List call already paid for it), falling back to
// a fresh Stat only when the listing didn't include sizes. The S3
// store always populates Size on List; the in-memory store does too,
// but the Stat fallback keeps GC robust against future backends that
// might lazy-populate.
func lookupSize(ctx context.Context, store blobstore.Store, info blobstore.Info) (int64, error) {
	if info.Size > 0 {
		return info.Size, nil
	}
	got, err := store.Stat(ctx, info.Key)
	if err != nil {
		return 0, err
	}
	return got.Size, nil
}
