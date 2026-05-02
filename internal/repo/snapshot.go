package repo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/chunker"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/walker"
)

// SnapshotOptions tunes a CreateSnapshot call. The zero value is
// valid and produces an untagged snapshot.
type SnapshotOptions struct {
	// Tag is an optional human-readable label persisted in the
	// manifest (e.g. "weekly", "pre-upgrade"). The empty string is
	// stored as absent (omitempty) rather than as "".
	Tag string
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

// dataPrefix is the blobstore key prefix for content-addressed
// chunks. Each chunk lives at "data/<aa>/<sha256-hex>" where aa is
// the first two hex chars of the SHA-256 (sharding).
const dataPrefix = "data/"

// CreateSnapshot walks root, chunks every regular file, encrypts +
// uploads new chunks (skipping ones the store already has), and
// writes a sealed manifest to snapshots/<id>. Returns the snapshot's
// summary; callers that want the full tree should LoadSnapshot.
//
// Walks honor `.sentraignore` and the CACHEDIR.TAG convention. Files
// that vanish between WalkDir and chunker.ChunkAll (e.g. transient
// build artifacts) are logged-and-skipped, not fatal.
func (r *Repo) CreateSnapshot(ctx context.Context, root string, opts SnapshotOptions) (SnapshotInfo, error) {
	repoKey, err := r.keyOrErr()
	if err != nil {
		return SnapshotInfo{}, err
	}
	// Phase 5 review C2: keyOrErr returns a defensive copy. Zero it
	// when the operation completes so the key is not retained past
	// CreateSnapshot's lifetime (independent of GC timing).
	defer zeroize(repoKey)

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return SnapshotInfo{}, fmt.Errorf("repo: abs root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	// We collect FileEntry values inside the walker callback and
	// sort at the end. The walker's worker pool means callbacks fire
	// concurrently, so a small mutex guards the slices and counters.
	// The single-snapshot dedup happens for free via the store: if
	// two goroutines independently chunk identical content, the
	// second Stat will already see the blob the first one Put.
	state := &snapState{}

	walkErr := walker.Walk(ctx, absRoot, walker.Options{ExcludeCaches: true},
		func(e walker.Entry) error {
			fe, newBytes, err := r.captureFile(ctx, repoKey, e, state)
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
		Tag:       opts.Tag,
		Root:      absRoot,
		Tree:      tree,
		Stats:     stats,
	}
	if err := r.putManifest(ctx, repoKey, m); err != nil {
		return SnapshotInfo{}, err
	}
	return SnapshotInfo{
		ID:        m.ID,
		CreatedAt: m.CreatedAt,
		Tag:       m.Tag,
		Stats:     m.Stats,
	}, nil
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
	raw, err := chunker.Decompress(compressed)
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
func (r *Repo) captureFile(
	ctx context.Context,
	repoKey []byte,
	e walker.Entry,
	state *snapState,
) (*FileEntry, int64, error) {
	// TODO: streaming for large files — chunker.ChunkAll buffers
	// the entire file in memory (one ~1 MiB Chunk per slot). For
	// multi-GiB files we want a chunker.ChunkStream variant that
	// hashes-encrypts-uploads each chunk before reading the next.
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

	chunks, err := chunker.ChunkAll(f)
	if err != nil {
		return nil, 0, fmt.Errorf("repo: chunk %q: %w", e.AbsPath, err)
	}

	hashes := make([]string, 0, len(chunks))
	var newBytes int64
	for _, c := range chunks {
		hexHash := hex.EncodeToString(c.Hash)
		hashes = append(hashes, hexHash)
		key := chunkKey(hexHash)
		// Stat first to skip already-stored chunks. This is the
		// content-addressed dedup that lets identical chunks across
		// files / snapshots upload exactly once.
		if _, err := r.store.Stat(ctx, key); err == nil {
			continue
		} else if !errors.Is(err, blobstore.ErrNotFound) {
			return nil, 0, fmt.Errorf("repo: stat chunk %s: %w", key, err)
		}
		compressed, err := chunker.Compress(c.Data)
		if err != nil {
			return nil, 0, fmt.Errorf("repo: compress chunk: %w", err)
		}
		sealed, err := crypto.Seal(repoKey, compressed)
		if err != nil {
			return nil, 0, fmt.Errorf("repo: seal chunk: %w", err)
		}
		if err := r.store.Put(ctx, key, bytes.NewReader(sealed)); err != nil {
			return nil, 0, fmt.Errorf("repo: put chunk %s: %w", key, err)
		}
		newBytes += int64(len(sealed))
	}

	return &FileEntry{
		Path:   e.RelPath,
		Size:   e.Size,
		Mode:   e.Mode,
		MTime:  e.MTime,
		Chunks: hashes,
	}, newBytes, nil
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

// chunkKey returns the blobstore key for a chunk with the given
// hex-encoded SHA-256. The first two hex chars are the shard prefix:
// "data/<aa>/<sha256-hex>".
func chunkKey(hexHash string) string {
	if len(hexHash) < 2 {
		// Shouldn't happen — SHA-256 hex is always 64 chars — but
		// guard against panics if a caller passes garbage.
		return dataPrefix + "00/" + hexHash
	}
	return dataPrefix + hexHash[:2] + "/" + hexHash
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
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
