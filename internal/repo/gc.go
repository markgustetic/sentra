package repo

import (
	"context"
	"errors"
	"fmt"
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
// Returns blobstore.ErrNotFound (wrapped) if the manifest doesn't
// exist; callers can errors.Is against the sentinel.
func (r *Repo) DeleteSnapshot(ctx context.Context, id string) error {
	if err := validateSnapshotID(id); err != nil {
		return err
	}
	// Guard with keyOrErr so a closed repo fails fast instead of
	// silently issuing an unauthenticated Delete to the store. We
	// don't actually need the key for delete (manifests are sealed,
	// not the keys), but failing on Close is the contract. zeroize
	// the defensive copy immediately.
	k, err := r.keyOrErr()
	if err != nil {
		return err
	}
	zeroize(k)

	if err := r.store.Delete(ctx, snapshotPrefix+id); err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			return err
		}
		return fmt.Errorf("repo: delete snapshot %q: %w", id, err)
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
// Concurrency: GC is not safe to run concurrently with CreateSnapshot.
// A snapshot in flight could write a chunk *after* GC built its live
// set, then write its manifest *after* GC finished — leaving the
// manifest pointing at a chunk GC has just deleted. Higher layers are
// responsible for serializing the two operations.
func (r *Repo) GC(ctx context.Context, keepIDs map[string]bool) (GCStats, error) {
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
				liveKeys[chunkKey(h)] = struct{}{}
			}
		}
	}

	// Walk every data/ entry and delete the ones that aren't in the
	// live set. We Stat first to capture the sealed-blob size so the
	// returned DeletedBytes is meaningful even if the store doesn't
	// expose Size on Delete.
	dataEntries, err := r.store.List(ctx, dataPrefix)
	if err != nil {
		return GCStats{}, fmt.Errorf("repo: list data: %w", err)
	}

	stats := GCStats{}
	for _, info := range dataEntries {
		if _, ok := liveKeys[info.Key]; ok {
			stats.LiveBlobs++
			continue
		}
		// Stat before delete so DeletedBytes covers the actual
		// sealed-blob size, not the plaintext. info.Size is what
		// List returned but we re-Stat in case the underlying store
		// updates size lazily — paranoia, but cheap.
		if size, err := lookupSize(ctx, r.store, info); err == nil {
			stats.DeletedBytes += size
		}
		if err := r.store.Delete(ctx, info.Key); err != nil {
			if errors.Is(err, blobstore.ErrNotFound) {
				// Race-with-something-else: a parallel GC or admin
				// cleanup got there first. Not our problem.
				continue
			}
			return stats, fmt.Errorf("repo: delete %q: %w", info.Key, err)
		}
		stats.DeletedBlobs++
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
