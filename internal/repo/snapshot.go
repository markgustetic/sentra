package repo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
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
	// Root is the absolute source directory the snapshot captured
	// (Manifest.Root). Retention groups by it so multiple sources
	// backed up into one repo each get the policy's full budget.
	Root  string
	Stats SnapshotStats
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
	// diagnostic message naming the holder. Local var is `heldLock`
	// (not `lockInfo`) because `lockInfo` is now the unexported type
	// name in this package; reusing it as a local would shadow the
	// type.
	heldLock, err := acquireLock(ctx, r.store, "snapshot")
	if err != nil {
		return SnapshotInfo{}, err
	}
	defer releaseLock(ctx, r.store, heldLock)

	repoKey, err := r.keyOrErr()
	if err != nil {
		return SnapshotInfo{}, err
	}
	// keyOrErr returns a defensive copy. Zero it when the operation
	// completes so the key is not retained past CreateSnapshot's
	// lifetime (independent of GC timing).
	defer crypto.Zeroize(repoKey)

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
	// Fidelity: snapshots record dirs (modes, empty dirs) and symlinks
	// (targets), not just files. The opt-in is set here rather than in
	// resolveWalkerOptions so plan/heuristic walks stay file-only.
	walkerOpts.IncludeNonRegular = true

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
			if e.Kind != walker.KindFile {
				// Dirs and symlinks carry no content bytes — record
				// their metadata entry and skip the chunk pipeline.
				state.add(entryFromNonRegular(e), 0)
				return nil
			}
			reporter.Total(estimated.Add(e.Size))

			fe, newBytes, err := r.captureFile(ctx, repoKey, e, reporter)
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
	defer crypto.Zeroize(repoKey)
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
	// Manifests are unbounded by file count, so we can't share the
	// chunk decoder's 8 MiB cap. 1 GiB bounds zip-bomb expansion while
	// comfortably covering manifests for repos of many millions of
	// files.
	raw, err := chunker.DecompressLimit(compressed, 1<<30)
	if err != nil {
		return Manifest{}, fmt.Errorf("repo: decompress manifest %q: %w", id, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("repo: unmarshal manifest %q: %w", id, err)
	}
	// Older versions load fine (absent fields keep zero values with
	// the right meaning), but a NEWER manifest may carry entry kinds
	// this binary would silently mis-restore — refuse instead.
	if m.Version > ManifestVersion {
		return Manifest{}, fmt.Errorf("repo: manifest %q is format v%d, newer than this binary supports (v%d) — upgrade sentra",
			id, m.Version, ManifestVersion)
	}
	return m, nil
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
// snapshots/ prefix or otherwise sneak past the blobstore. Without
// this guard, LoadSnapshot("../config") would become a Get on
// "snapshots/../config" — the in-memory store treats that as
// not-found (HasPrefix mismatch) but the S3 store collapses it via
// path.Join to fetch the config blob, producing an opaque
// "decompress" error that obscures the real bug.
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
