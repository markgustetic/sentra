package repo

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/chunker"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/progress"
	"github.com/markgustetic/sentra/internal/walker"
)

// SnapshotOptions tunes a CreateSnapshot call. The zero value is
// valid and produces an untagged snapshot.
type SnapshotOptions struct {
	// Tag is an optional human-readable label persisted in the
	// manifest (e.g. "weekly", "pre-upgrade"). The empty string is
	// stored as absent (omitempty) rather than as "".
	Tag string

	// Progress receives Total() once at snapshot start (best-effort
	// estimate from the walk) and Add(n) for each chunk *uploaded*
	// (deduplicated chunks count zero — they didn't move bytes).
	// Nil is treated as a NopReporter, so callers that don't care
	// about progress can leave it unset.
	Progress progress.Reporter

	// Walker tunes the directory walk: ignore-file name and the
	// CACHEDIR.TAG opt-in. The zero value preserves the previous
	// hardcoded behaviour ({ExcludeCaches: true, IgnoreFile: ""},
	// which the walker treats as ".sentraignore"). See
	// defaultWalkerOptions for the canonical zero-value handling.
	Walker walker.Options
}

// resolveWalkerOptions returns the user-provided walker options if
// any field has been set; otherwise the legacy defaults
// ({ExcludeCaches: true}, which preserves pre-config behaviour).
//
// Detection: "zero value" means Concurrency==0, IgnoreFile=="", and
// ExcludeCaches==false. CLI callers that want to disable cache
// exclusion explicitly pass an IgnoreFile (typically the configured
// value, defaulting to ".sentraignore") so the options are non-zero
// and the explicit ExcludeCaches=false is honored. The repo's own
// tests that don't care just use SnapshotOptions{} and get the
// legacy behaviour for free.
func resolveWalkerOptions(opts walker.Options) walker.Options {
	if opts.Concurrency == 0 && opts.IgnoreFile == "" && !opts.ExcludeCaches {
		return walker.Options{ExcludeCaches: true}
	}
	return opts
}

// SnapshotInfo is the lightweight summary returned by CreateSnapshot
// and ListSnapshots. The full file tree lives in the Manifest, which
// callers retrieve via LoadSnapshot when they need it.
type SnapshotInfo struct {
	ID        string
	CreatedAt time.Time
	Tag       string
	Stats     SnapshotStats
}

// snapshotPrefix is the blobstore key prefix under which manifests
// live. Used by both CreateSnapshot (write) and ListSnapshots (read).
const snapshotPrefix = "snapshots/"

// DataPrefix is the blobstore key prefix for content-addressed
// chunks. Each chunk lives at "data/<aa>/<sha256-hex>" where aa is
// the first two hex chars of the SHA-256 (sharding).
//
// Exported so the agent (orphan-blob detection) and any future
// consumers operate on a single source of truth — the on-disk format
// must change in lockstep across every package that addresses chunks.
const DataPrefix = "data/"

// CreateSnapshot walks root, chunks every regular file, encrypts +
// uploads new chunks (skipping ones the store already has), and
// writes a sealed manifest to snapshots/<id>. Returns the snapshot's
// summary; callers that want the full tree should LoadSnapshot.
//
// Walks honor `.sentraignore` and the CACHEDIR.TAG convention. Files
// that vanish between WalkDir and chunker.ChunkAll (e.g. transient
// build artifacts) are logged-and-skipped, not fatal.
//
// If opts.Progress is non-nil, CreateSnapshot calls Total() once at
// the start with a best-effort byte estimate from a stat-only pre-
// walk, and Add(n) for each chunk *uploaded* (deduplicated chunks
// count zero — they didn't move bytes). A nil reporter is treated as
// a NopReporter so call sites stay free of nil checks.
func (r *Repo) CreateSnapshot(ctx context.Context, root string, opts SnapshotOptions) (SnapshotInfo, error) {
	// Acquire the repo-wide advisory lock so a concurrent GC can't
	// see this snapshot's chunks land while it's deciding what to
	// delete. The lock is released on every exit path (success and
	// error) via the deferred releaseLock. ErrRepoLocked surfaces a
	// diagnostic message naming the holder.
	lockInfo, err := r.acquireLock(ctx, "snapshot")
	if err != nil {
		return SnapshotInfo{}, err
	}
	defer r.releaseLock(ctx, lockInfo)

	repoKey, err := r.keyOrErr()
	if err != nil {
		return SnapshotInfo{}, err
	}
	// Phase 5 review C2: keyOrErr returns a defensive copy. Zero it
	// when the operation completes so the key is not retained past
	// CreateSnapshot's lifetime (independent of GC timing).
	defer zeroize(repoKey)

	// Local var name avoids shadowing the imported `progress` package.
	reporter := opts.Progress
	if reporter == nil {
		reporter = progress.NopReporter{}
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return SnapshotInfo{}, fmt.Errorf("repo: abs root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	// Resolve walker options once: zero-value SnapshotOptions.Walker
	// preserves the previous hardcoded ExcludeCaches=true behaviour;
	// non-zero values flow through untouched (this is how the CLI
	// drives ignore_file / exclude_caches from sentra.yaml).
	walkerOpts := resolveWalkerOptions(opts.Walker)

	// We collect FileEntry values inside the walker callback and
	// sort at the end. The walker's worker pool means callbacks fire
	// concurrently, so a small mutex guards the slices and counters.
	// The single-snapshot dedup happens for free via the store: if
	// two goroutines independently chunk identical content, the
	// second Stat will already see the blob the first one Put.
	state := &snapState{}

	// Single-walk progress: as each file is discovered, add its
	// plaintext size to the running total and update reporter.Total.
	// Add()s for uploaded chunks happen later in captureFile, so
	// total >= done at every point (each file's size lands in total
	// before captureFile has a chance to call Add for that file's
	// chunks). Reporters that only care about the final value see
	// the same end state as before; reporters that paint live see
	// the bar's denominator grow organically instead of waiting on
	// a full pre-walk.
	var estimated atomic.Int64
	reporter.Total(0) // signal start so empty trees still trigger one Total call

	walkErr := walker.Walk(ctx, absRoot, walkerOpts,
		func(e walker.Entry) error {
			reporter.Total(estimated.Add(e.Size))

			fe, newBytes, err := r.captureFile(ctx, repoKey, e, state, reporter)
			if err != nil {
				return err
			}
			if fe == nil {
				// File vanished between walk and open; skip silently.
				return nil
			}
			state.add(*fe, newBytes)
			return nil
		},
	)
	if walkErr != nil {
		return SnapshotInfo{}, fmt.Errorf("repo: walk: %w", walkErr)
	}

	return r.finishSnapshot(ctx, repoKey, absRoot, opts.Tag, state)
}

// LoadSnapshot fetches the manifest at snapshots/<id>, decrypts and
// decompresses it, and returns the parsed Manifest.
//
// Returns blobstore.ErrNotFound (wrapped) when the snapshot does not
// exist; callers can errors.Is against the sentinel.
func (r *Repo) LoadSnapshot(ctx context.Context, id string) (Manifest, error) {
	if err := validateSnapshotID(id); err != nil {
		return Manifest{}, err
	}
	repoKey, err := r.keyOrErr()
	if err != nil {
		return Manifest{}, err
	}
	defer zeroize(repoKey)
	rc, err := r.store.Get(ctx, snapshotPrefix+id)
	if err != nil {
		// Preserve the sentinel for errors.Is callers.
		if errors.Is(err, blobstore.ErrNotFound) {
			return Manifest{}, err
		}
		return Manifest{}, fmt.Errorf("repo: get manifest %q: %w", id, err)
	}
	defer rc.Close()
	sealed, err := io.ReadAll(rc)
	if err != nil {
		return Manifest{}, fmt.Errorf("repo: read manifest %q: %w", id, err)
	}
	compressed, err := crypto.Open(repoKey, sealed)
	if err != nil {
		return Manifest{}, fmt.Errorf("repo: decrypt manifest %q: %w", id, err)
	}
	// Phase 5 review I4: manifests are unbounded by file count, so we
	// can't share the chunk decoder's 8 MiB cap. 1 GiB bounds zip-bomb
	// expansion while comfortably covering manifests for repos of
	// many millions of files.
	raw, err := chunker.DecompressLimit(compressed, 1<<30)
	if err != nil {
		return Manifest{}, fmt.Errorf("repo: decompress manifest %q: %w", id, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("repo: unmarshal manifest %q: %w", id, err)
	}
	return m, nil
}

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
		// Put consumes the reader synchronously: the in-memory store
		// drains it on the calling goroutine, the S3 store's PutObject
		// returns once the SDK has its own buffered copy. Either way,
		// `sealed` is no longer in flight when Put returns.
		if err := r.store.Put(ctx, key, bytes.NewReader(sealed)); err != nil {
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

// ChunkKey returns the blobstore key for a chunk with the given
// hex-encoded SHA-256. The first two hex chars are the shard prefix:
// "data/<aa>/<sha256-hex>".
//
// Exported so the agent and heuristics use exactly the same key shape
// the repo writes — diverging copies were the failure mode this
// replaces. Callers should always pass the SHA-256 hex (64 chars);
// shorter inputs land in the "00" sentinel shard rather than panic so
// upstream bugs surface as misclassification rather than crash.
func ChunkKey(hexHash string) string {
	if len(hexHash) < 2 {
		// Shouldn't happen — SHA-256 hex is always 64 chars — but
		// guard against panics if a caller passes garbage.
		return DataPrefix + "00/" + hexHash
	}
	return DataPrefix + hexHash[:2] + "/" + hexHash
}

// newSnapshotID returns a sortable, collision-resistant ID:
// "snap-<UTC timestamp in 20060102T150405Z>-<4 random hex bytes>".
func newSnapshotID(t time.Time) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("snap-%s-%s", t.UTC().Format("20060102T150405Z"), hex.EncodeToString(b[:])), nil
}

// snapshotIDPattern matches the shape produced by newSnapshotID:
// "snap-<digits/T/Z>-<hex>". Permissive on the timestamp body so
// older test fixtures with slightly different stamps still parse.
var snapshotIDPattern = regexp.MustCompile(`^snap-[0-9TZ]+-[0-9a-f]+$`)

// validateSnapshotID rejects any ID that could escape the
// snapshots/ prefix or otherwise sneak past the blobstore. Phase 5
// review I2: LoadSnapshot("../config") would otherwise become a Get
// on "snapshots/../config" which the in-memory store treats as
// not-found (HasPrefix mismatch) but which the S3 store collapses
// via path.Join to fetch the config blob, producing an opaque
// "decompress" error.
func validateSnapshotID(id string) error {
	if id == "" {
		return fmt.Errorf("repo: invalid snapshot id: empty")
	}
	if strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("repo: invalid snapshot id %q: contains path separator", id)
	}
	// Reject any "." or ".." segment outright. Splitting is overkill
	// here — the simple equality + prefix/suffix checks cover all
	// forms after the separator check above.
	if id == "." || id == ".." {
		return fmt.Errorf("repo: invalid snapshot id %q: traversal segment", id)
	}
	if !snapshotIDPattern.MatchString(id) {
		return fmt.Errorf("repo: invalid snapshot id %q: does not match expected shape", id)
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
