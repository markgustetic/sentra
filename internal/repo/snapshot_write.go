package repo

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/chunker"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/progress"
	"github.com/markgustetic/sentra/internal/walker"
)

// captureFile reads a single file, chunks it, uploads any new chunks,
// and returns the FileEntry for the manifest. A nil entry means the
// file vanished between the walk and the open — caller should skip.
//
// reporter.Add is called once per uploaded chunk with the sealed-blob
// size; chunks already in the store contribute zero (they didn't move
// any bytes). reporter is required (CreateSnapshot defaults to a
// NopReporter when the caller didn't supply one).
func (r *Repo) captureFile(
	ctx context.Context,
	repoKey []byte,
	e walker.Entry,
	state *snapState,
	reporter progress.Reporter,
) (*FileEntry, int64, error) {
	// Streaming chunker: each chunk is hashed + sealed + uploaded
	// before the next is read, so memory stays bounded at O(1 chunk)
	// regardless of file size. The previous ChunkAll path buffered
	// the entire file (~1 MiB per slot, O(N) total) and would OOM
	// on multi-GiB inputs (databases, VM disks, photo libraries).
	f, err := os.Open(e.AbsPath) //nolint:gosec // path comes from walker, not user input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// File vanished between WalkDir and Open: e.g. a build
			// tool deleted a temp file mid-walk. Don't fail the
			// whole snapshot for it.
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("repo: open %q: %w", e.AbsPath, err)
	}
	defer f.Close()

	var hashes []string
	var newBytes int64
	streamErr := chunker.ChunkStream(f, func(c chunker.Chunk) error {
		// Hex-encode the hash inside the callback — c.Hash is borrowed
		// from a stack-allocated array and invalid after fn returns.
		// The resulting string is heap-allocated and safe to retain.
		hexHash := hex.EncodeToString(c.Hash)
		hashes = append(hashes, hexHash)
		key := ChunkKey(hexHash)
		// Stat first to skip already-stored chunks. This is the
		// content-addressed dedup that lets identical chunks across
		// files / snapshots upload exactly once.
		if _, statErr := r.store.Stat(ctx, key); statErr == nil {
			return nil
		} else if !errors.Is(statErr, blobstore.ErrNotFound) {
			return fmt.Errorf("repo: stat chunk %s: %w", key, statErr)
		}
		// c.Data is borrowed; Compress reads it synchronously and
		// returns a freshly-allocated []byte that we own. The borrow
		// is no longer needed once Compress returns.
		compressed, err := chunker.Compress(c.Data)
		if err != nil {
			return fmt.Errorf("repo: compress chunk: %w", err)
		}
		sealed, err := crypto.Seal(repoKey, compressed)
		if err != nil {
			return fmt.Errorf("repo: seal chunk: %w", err)
		}
		// PutIfAbsent consumes the reader synchronously and closes the
		// Stat-then-write race for concurrent identical chunks. If a
		// peer won the same content-addressed key after our Stat, this
		// chunk is deduplicated and should not count toward NewBytes.
		if err := r.store.PutIfAbsent(ctx, key, bytes.NewReader(sealed)); err != nil {
			if errors.Is(err, blobstore.ErrAlreadyExists) {
				return nil
			}
			return fmt.Errorf("repo: put chunk %s: %w", key, err)
		}
		sealedSize := int64(len(sealed))
		newBytes += sealedSize
		// Report only chunks that actually moved bytes. Deduplicated
		// chunks above return without an Add — that's how the
		// reporter's `done` reflects "real work" rather than "fake
		// progress through cached content."
		reporter.Add(sealedSize)
		return nil
	})
	if streamErr != nil {
		return nil, 0, fmt.Errorf("repo: chunk %q: %w", e.AbsPath, streamErr)
	}

	return &FileEntry{
		Path:   e.RelPath,
		Size:   e.Size,
		Mode:   e.Mode,
		MTime:  e.MTime,
		Chunks: hashes,
	}, newBytes, nil
}

func (r *Repo) finishSnapshot(
	ctx context.Context,
	repoKey []byte,
	absRoot string,
	tag string,
	state *snapState,
) (SnapshotInfo, error) {
	tree := state.snapshotTree()
	stats := SnapshotStats{
		Files:    len(tree),
		Bytes:    state.totalBytes(),
		NewBytes: state.newBytes(),
	}

	id, err := newSnapshotID(time.Now().UTC())
	if err != nil {
		return SnapshotInfo{}, fmt.Errorf("repo: id: %w", err)
	}
	host, _ := os.Hostname() // best-effort; "" on error is fine

	m := Manifest{
		Version:   ManifestVersion,
		ID:        id,
		CreatedAt: time.Now().UTC(),
		Host:      host,
		Tag:       tag,
		Root:      absRoot,
		Tree:      tree,
		Stats:     stats,
	}
	if err := r.putManifest(ctx, repoKey, m); err != nil {
		return SnapshotInfo{}, err
	}

	info := SnapshotInfo{
		ID:        m.ID,
		CreatedAt: m.CreatedAt,
		Tag:       m.Tag,
		Stats:     m.Stats,
	}

	// Update the snapshot summary index so the next ListSnapshots is
	// O(1). Failure here is non-fatal: the manifest above is the
	// source of truth and the index will self-heal on the next
	// ListSnapshots call (manifest fan-back rebuilds it). We still
	// log a warning so an operator running with --log-level=info
	// sees recurring index-write failures.
	if err := r.updateSnapshotIndex(ctx, repoKey, func(idx *snapshotIndex) error {
		idx.Entries = append(idx.Entries, info)
		sortNewestFirst(idx.Entries)
		return nil
	}); err != nil {
		slog.LogAttrs(ctx, slog.LevelWarn,
			"failed to update snapshot index after CreateSnapshot",
			slog.String("snapshot_id", info.ID),
			slog.String("error", err.Error()),
		)
	}

	return info, nil
}

// putManifest serializes m, compresses, encrypts, and writes it to
// snapshots/<id>.
func (r *Repo) putManifest(ctx context.Context, repoKey []byte, m Manifest) error {
	raw, err := json.Marshal(&m)
	if err != nil {
		return fmt.Errorf("repo: marshal manifest: %w", err)
	}
	compressed, err := chunker.Compress(raw)
	if err != nil {
		return fmt.Errorf("repo: compress manifest: %w", err)
	}
	sealed, err := crypto.Seal(repoKey, compressed)
	if err != nil {
		return fmt.Errorf("repo: seal manifest: %w", err)
	}
	if err := r.store.Put(ctx, snapshotPrefix+m.ID, bytes.NewReader(sealed)); err != nil {
		return fmt.Errorf("repo: put manifest: %w", err)
	}
	return nil
}

// snapState aggregates per-snapshot state populated concurrently by
// walker callbacks. The mutex is not held across I/O — each callback
// does its chunking + uploads outside the lock and only takes it for
// the brief append + counter bump.
type snapState struct {
	mu       sync.Mutex
	tree     []FileEntry
	bytes    int64
	uploaded int64
}

// add records a captured file and the size of the new (uploaded)
// blobs that resulted. Safe for concurrent calls.
func (s *snapState) add(fe FileEntry, newBytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tree = append(s.tree, fe)
	s.bytes += fe.Size
	s.uploaded += newBytes
}

func (s *snapState) totalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes
}

func (s *snapState) newBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.uploaded
}

// snapshotTree returns the tree sorted by Path so manifests are
// deterministic across runs.
func (s *snapState) snapshotTree() []FileEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FileEntry, len(s.tree))
	copy(out, s.tree)
	slices.SortFunc(out, func(a, b FileEntry) int { return cmp.Compare(a.Path, b.Path) })
	return out
}
