package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/chunker"
	"github.com/markgustetic/sentra/internal/crypto"
)

// snapshotIndexKey is the blobstore key for the encrypted snapshot
// summary index. Reading this single blob replaces O(N) manifest
// fan-out for ListSnapshots-style operations on mature repositories.
//
// The "meta/" prefix keeps the index out of the way of "snapshots/<id>"
// (the manifest path) and "data/<aa>/<hex>" (the chunk path). Snapshot
// IDs always start with "snap-" so collision with a manifest key is
// not possible. A future "meta/<other>" file is unblocked too.
const snapshotIndexKey = "meta/snapshots"

// snapshotIndexVersion is the on-disk schema version of the snapshot
// index. Bumped only when the wire format changes incompatibly.
// loadSnapshotIndex returns an error on version mismatch so an older
// build doesn't silently mis-read a newer index.
const snapshotIndexVersion = 1

// snapshotIndex is the wire shape of the encrypted snapshot summary
// index. The full file tree per snapshot is NOT kept here — callers
// that need the tree still LoadSnapshot by ID. The point of this
// blob is to make "give me the list of snapshots" an O(1) read.
//
// Entries are stored newest-first by CreatedAt. The save path
// re-stamps Updated to the current UTC time so the wire value
// reflects the actual write moment, not whatever the in-memory
// struct happened to carry.
type snapshotIndex struct {
	Version int            `json:"version"`
	Updated time.Time      `json:"updated"`
	Entries []SnapshotInfo `json:"entries"`
}

// loadSnapshotIndex fetches and decodes the snapshot index. Returns
// (nil, nil) if the index blob is absent — callers should fall back
// to manifest fan-out and write a fresh index opportunistically.
//
// Decrypt, decompress, decode, and version errors are surfaced; the
// caller decides whether to log+rebuild or hard-fail. The encrypted
// envelope is the same one used for manifests so any blob that can
// be opened by the repo key is a valid index from a crypto standpoint.
func (r *Repo) loadSnapshotIndex(ctx context.Context, repoKey []byte) (*snapshotIndex, error) {
	rc, err := r.store.Get(ctx, snapshotIndexKey)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("repo: get index: %w", err)
	}
	defer rc.Close()
	sealed, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("repo: read index: %w", err)
	}
	compressed, err := crypto.Open(repoKey, sealed)
	if err != nil {
		return nil, fmt.Errorf("repo: decrypt index: %w", err)
	}
	// 1 GiB decompress cap mirrors LoadSnapshot's policy: an attacker
	// or a corrupted blob shouldn't be able to expand into multiple
	// gigabytes of memory. Real indexes for million-snapshot repos
	// stay well under 100 MiB plain JSON.
	raw, err := chunker.DecompressLimit(compressed, 1<<30)
	if err != nil {
		return nil, fmt.Errorf("repo: decompress index: %w", err)
	}
	var idx snapshotIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("repo: unmarshal index: %w", err)
	}
	if idx.Version != snapshotIndexVersion {
		return nil, fmt.Errorf("repo: unknown index version %d (this build expects %d)",
			idx.Version, snapshotIndexVersion)
	}
	return &idx, nil
}

// saveSnapshotIndex serializes, compresses, encrypts, and writes the
// index blob. The Version and Updated fields are re-stamped here so
// callers don't have to remember.
func (r *Repo) saveSnapshotIndex(ctx context.Context, repoKey []byte, idx *snapshotIndex) error {
	idx.Version = snapshotIndexVersion
	idx.Updated = time.Now().UTC()
	return r.putSealedJSON(ctx, repoKey, snapshotIndexKey, idx, "index")
}

// updateSnapshotIndex performs a read-modify-write on the index. The
// mutate callback receives the decoded index (or a freshly-zero one
// if the index doesn't exist yet) and modifies it in place. The
// callback's return error short-circuits the write.
//
// A corrupt or version-mismatched index is non-fatal here: we log and
// rebuild from a fresh struct. The next ListSnapshots manifest fan-back
// path will populate the entries set if mutate adds entries to a
// previously-empty index. This keeps a one-time blip from cascading
// into a permanent failure.
//
// Concurrency: this is single-write at the S3 level. Two concurrent
// CreateSnapshot calls can race — one's update can overwrite the
// other's. v1 tolerates this because the manifests are the source of
// truth and ListSnapshots' manifest fan-back recovers any temporarily-
// missing entry. A future advisory lock (see architecture review's
// GC concurrency note) closes the window.
func (r *Repo) updateSnapshotIndex(
	ctx context.Context,
	repoKey []byte,
	mutate func(*snapshotIndex) error,
) error {
	idx, err := r.loadSnapshotIndex(ctx, repoKey)
	if err != nil {
		slog.LogAttrs(ctx, slog.LevelWarn, "snapshot index unreadable, rebuilding from scratch",
			slog.String("error", err.Error()))
		idx = nil
	}
	if idx == nil {
		idx = &snapshotIndex{}
	}
	if err := mutate(idx); err != nil {
		return err
	}
	return r.saveSnapshotIndex(ctx, repoKey, idx)
}
