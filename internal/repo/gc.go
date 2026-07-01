package repo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/crypto"
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
	defer crypto.Zeroize(repoKey)

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
// The live set is ALWAYS computed from the snapshots actually present
// in the store, listed here under the lock. This is the load-bearing
// invariant: a blob referenced by ANY manifest present at GC time is
// retained, so a snapshot committed by a concurrent CreateSnapshot in
// the window before GC acquired the lock is protected. (An earlier
// version treated a non-nil keepIDs as the SOLE live set; because prune
// froze keepIDs from a ListSnapshots taken outside any lock, a backup
// that committed in the prune→GC window was absent from keepIDs and had
// its chunks reaped — silent, unrecoverable data loss. keepIDs no
// longer governs which blobs are live.)
//
// keepIDs (optional, may be nil) now only distinguishes caller intent
// for the empty-store safety guard:
//   - nil     => a bare "GC orphans" convenience pass (e.g. after a
//     manual DeleteSnapshot). GC refuses to run against a
//     store with zero snapshots (ErrEmptyRepo) rather than
//     wipe every data blob.
//   - non-nil => the caller (prune/policy) has already decided the drop
//     set and deleted those manifests. An empty live set is
//     then a deliberate full drop (e.g. prune --all), so GC
//     proceeds and reclaims everything now orphaned.
//
// Concurrency: GC and CreateSnapshot serialize via the repo-wide
// advisory lock at meta/lock. Acquiring the lock fails fast with
// ErrRepoLocked when another snapshot or GC is already running,
// rather than corrupting the live-set computation by racing with
// a concurrent CreateSnapshot. A crashed lock-holder leaves the
// blob behind; recovery is currently manual (delete the lock key
// out-of-band).
func (r *Repo) GC(ctx context.Context, keepIDs map[string]bool) (GCStats, error) {
	// Local var is `heldLock` (not `lockInfo`) because `lockInfo` is
	// now the unexported type name in this package; reusing it as a
	// local would shadow the type.
	heldLock, err := acquireLock(ctx, r.store, "gc")
	if err != nil {
		return GCStats{}, err
	}
	defer releaseLock(ctx, r.store, heldLock)

	// Same fail-on-Close contract as DeleteSnapshot. The actual key
	// access happens inside LoadSnapshot, so the local copy here is
	// solely a "check Closed" probe; zeroize immediately.
	k, err := r.keyOrErr()
	if err != nil {
		return GCStats{}, err
	}
	crypto.Zeroize(k)

	// Resolve the live ID set from the snapshots present in the store,
	// listed under the lock. Never from keepIDs — see the doc comment
	// for why that was a data-loss race.
	entries, err := r.store.List(ctx, snapshotPrefix)
	if err != nil {
		return GCStats{}, fmt.Errorf("repo: list snapshots: %w", err)
	}
	var liveIDs []string
	for _, info := range entries {
		id := strings.TrimPrefix(info.Key, snapshotPrefix)
		if id == "" || id == info.Key {
			continue
		}
		liveIDs = append(liveIDs, id)
	}
	if len(liveIDs) == 0 && keepIDs == nil {
		// Bare "GC orphans" pass against an empty store: refuse rather
		// than delete every data blob. A caller-supplied keepIDs (even
		// empty) signals a deliberate, already-decided prune, so that
		// path is allowed to proceed to a full reclaim.
		return GCStats{}, ErrEmptyRepo
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
