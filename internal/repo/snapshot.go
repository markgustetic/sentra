package repo

import (
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

	// ForceRescan disables the incremental scan: every file is read
	// and re-chunked even when its size+mtime match the parent
	// snapshot's entry. The escape hatch for the incremental scan's
	// one blind spot — content rewritten without the mtime moving
	// (clock skew, deliberate timestomping, sub-granularity writes).
	ForceRescan bool

	// Walker tunes the directory walk: ignore-file name and the
	// CACHEDIR.TAG opt-in. The zero value preserves the previous
	// hardcoded behaviour ({ExcludeCaches: true, IgnoreFile: ""},
	// which the walker treats as ".sentraignore"). See
	// defaultWalkerOptions for the canonical zero-value handling.
	Walker walker.Options

	// OnSkip hears about every subtree the walk dropped because its
	// listing was denied (see walker.Options.OnSkip). It is the seat
	// the CLI and TUI use to print "skipped <path>" while the backup
	// runs; the count also lands in SnapshotStats.Skipped whether or
	// not a callback is set, so a denied folder is never omitted in
	// silence. Called from the walk's producer goroutine, possibly
	// while worker callbacks are running — keep it cheap and
	// concurrency-safe against whatever else the caller touches.
	// Walker.OnSkip, if also set, still fires.
	OnSkip func(path string, err error)
}

// resolveWalkerOptions returns the user-provided walker options if
// any tunable has been set; otherwise the legacy defaults
// ({ExcludeCaches: true}, which preserves pre-config behaviour).
//
// Detection: "zero value" means Concurrency==0, IgnoreFile=="", and
// ExcludeCaches==false. CLI callers that want to disable cache
// exclusion explicitly pass an IgnoreFile (typically the configured
// value, defaulting to ".sentraignore") so the options are non-zero
// and the explicit ExcludeCaches=false is honored. The repo's own
// tests that don't care just use SnapshotOptions{} and get the
// legacy behaviour for free.
//
// OnSkip is not a tunable and is carried through either way: it does
// not change what the walk yields, only who hears about a denied
// subtree, and a callback silently dropped by the defaulting is a
// backup that omits a folder without telling anyone.
func resolveWalkerOptions(opts walker.Options) walker.Options {
	if opts.Concurrency == 0 && opts.IgnoreFile == "" && !opts.ExcludeCaches {
		return walker.Options{ExcludeCaches: true, OnSkip: opts.OnSkip}
	}
	return opts
}

// chainSkips joins the callbacks a caller may have wired into either
// seat (SnapshotOptions.OnSkip, Walker.OnSkip) into one walker
// callback. Nil seats are dropped, and no seats yields nil so the
// walker's own nil check stays meaningful.
func chainSkips(fns ...func(string, error)) func(string, error) {
	var live []func(string, error)
	for _, fn := range fns {
		if fn != nil {
			live = append(live, fn)
		}
	}
	if len(live) == 0 {
		return nil
	}
	return func(path string, err error) {
		for _, fn := range live {
			fn(path, err)
		}
	}
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

// ErrRootNotDir is returned when a backup root resolves to something
// other than a directory (a regular file, or a symlink to one).
var ErrRootNotDir = errors.New("repo: backup root is not a directory")

// ErrManifestIDMismatch is returned by LoadSnapshot when the manifest
// stored under snapshots/<id> declares a different ID — a manifest
// copied over another key out of band. Every loader aborts on it:
// restore would otherwise silently restore the wrong tree, and GC
// would compute the wrong live set and reap the real one's chunks.
var ErrManifestIDMismatch = errors.New("repo: manifest id does not match its key")

// ResolveRoot turns an operator-supplied backup root into the
// canonical path a snapshot records as Manifest.Root: absolute,
// cleaned, symlinks resolved, and confirmed to be a directory.
//
// Resolving symlinks is what makes `sentra backup ~/Dropbox` work
// when ~/Dropbox is a link: filepath.WalkDir does not descend a root
// that is itself a symlink, so the unresolved path walked to a
// single "." symlink entry — a zero-file snapshot with exit 0. The
// RESOLVED path is the one recorded because retention groups by
// Root: the linked and the real spelling of one directory must land
// in one group, or each spelling prunes the other's dailies. The
// directory check closes the sibling failure (a file root walks to
// one "." file entry) without a generic zero-entry guard, which
// would wrongly refuse the legitimate backup of an empty directory.
//
// Exported so every surface that compares a configured path against
// SnapshotInfo.Root (policy last-run, the Schedules view) can
// normalise the same way.
func ResolveRoot(root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("repo: abs root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(absRoot))
	if err != nil {
		return "", fmt.Errorf("repo: resolve root %q: %w", absRoot, err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("repo: stat root %q: %w", resolved, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%w: %q", ErrRootNotDir, absRoot)
	}
	return resolved, nil
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
	// Resolve before taking the lock: a bad root is refused without
	// a lock round trip, and the resolved path is what the parent
	// lookup and the manifest both key on.
	absRoot, err := ResolveRoot(root)
	if err != nil {
		return SnapshotInfo{}, err
	}

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
	// Every denied subtree is counted into the stats before the
	// caller hears of it, so the manifest records the omission even
	// when nobody wired a callback.
	walkerOpts.OnSkip = state.countSkips(chainSkips(opts.OnSkip, walkerOpts.OnSkip))

	// Incremental scan: files whose size AND mtime match the newest
	// prior snapshot of the same root reuse that snapshot's chunk
	// list without being opened — re-backups of a quiet tree read
	// ~zero bytes. The repo lock held above keeps GC from reaping a
	// referenced chunk, but nothing stops an out-of-band delete (an
	// operator, a bucket lifecycle rule), and a reused list would
	// carry that dangling reference into every later snapshot — the
	// file intact on disk, unrestorable forever. So reuse confirms
	// each chunk still exists (one Stat per unique chunk per
	// snapshot, see reusedChunkProbe) and re-reads the file when one
	// is gone. The mtime check is equality on the lstat timestamp;
	// content rewritten without the mtime moving is the classic
	// blind spot, covered by ForceRescan.
	var parent map[string]FileEntry
	if !opts.ForceRescan {
		parent = r.parentFileEntries(ctx, absRoot)
	}
	probe := &reusedChunkProbe{store: r.store}

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

			if pe, ok := parent[e.RelPath]; ok && pe.Size == e.Size && pe.MTime.Equal(e.MTime) {
				present, err := probe.allPresent(ctx, pe.Chunks)
				if err != nil {
					return err
				}
				if present {
					// Unchanged since the parent: reuse its chunks,
					// but record the CURRENT mode — a chmod doesn't
					// move the mtime and must not go stale in the
					// new manifest.
					state.add(FileEntry{
						Path:   e.RelPath,
						Size:   e.Size,
						Mode:   e.Mode,
						MTime:  e.MTime,
						Chunks: pe.Chunks,
					}, 0)
					return nil
				}
				// A chunk vanished out of band: fall through and
				// read the file as if it had changed.
			}

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
	// The key is the caller's claim about which snapshot this is; the
	// body is the manifest's own. Decrypting proves the bytes are ours,
	// not that they belong under this key — a manifest copied over
	// another key passes every check above. Callers (restore, GC,
	// check, list) key everything off the id they asked for, so the
	// two must agree.
	if m.ID != id {
		return Manifest{}, fmt.Errorf("%w: key snapshots/%s holds manifest %q", ErrManifestIDMismatch, id, m.ID)
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
