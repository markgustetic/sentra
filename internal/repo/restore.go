package repo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"golang.org/x/sync/errgroup"

	"github.com/markgustetic/sentra/internal/chunker"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/progress"
)

// ErrChunkHashMismatch is returned by the restore read path when a
// decrypted, decompressed chunk's SHA-256 does not match the content
// address it was fetched under — the blob is authentic (sealed under
// the repo key) but mis-addressed. Callers can errors.Is against it.
var ErrChunkHashMismatch = errors.New("repo: chunk content-address mismatch")

// RestoreOptions tunes a Restore call. The zero value runs with
// no progress reporting and the default concurrency (GOMAXPROCS).
type RestoreOptions struct {
	// Progress receives Total() once at the start with the manifest's
	// total plaintext bytes (Stats.Bytes) and Add(size) per file
	// written. Nil is treated as a NopReporter, so callers that don't
	// care about progress can leave it unset.
	Progress progress.Reporter

	// Concurrency caps the number of files restored in parallel.
	// Each worker fetches its file's chunks sequentially and writes
	// the destination file; multiple workers run in parallel against
	// the blobstore.
	//
	// Zero means use runtime.GOMAXPROCS(0). Set to 1 for sequential
	// restore — useful when the target filesystem is slow, the
	// bandwidth-delay product is small, or a regression suspect needs
	// to be ruled out.
	//
	// Negative values are clamped to 1.
	Concurrency int
}

// RestorePlan is a no-write preview of a restore operation.
type RestorePlan struct {
	SnapshotID  string
	DestDir     string
	DestExists  bool
	DestEmpty   bool
	Files       int
	Bytes       int64
	Paths       []string
	CreatedAt   string
	Description string
}

// RestoreMismatch is one verification difference between a restored
// destination tree and the snapshot manifest.
type RestoreMismatch struct {
	Path     string `json:"path"`
	Reason   string `json:"reason"`
	WantSize int64  `json:"want_size,omitempty"`
	GotSize  int64  `json:"got_size,omitempty"`
}

// RestoreVerification summarizes a restore verification pass.
type RestoreVerification struct {
	SnapshotID     string            `json:"snapshot_id"`
	DestDir        string            `json:"dest_dir"`
	Files          int               `json:"files"`
	Bytes          int64             `json:"bytes"`
	VerifiedFiles  int               `json:"verified_files"`
	Mismatches     []RestoreMismatch `json:"mismatches"`
	ExtraFileCount int               `json:"extra_file_count"`
}

// OK reports whether every manifest file was present, the right size,
// and chunked to the same hashes, with no extra regular files in dest.
func (v RestoreVerification) OK() bool {
	return len(v.Mismatches) == 0
}

// PlanRestore loads a snapshot and validates that a restore into
// destDir would be accepted, without creating or writing anything.
func (r *Repo) PlanRestore(ctx context.Context, snapID, destDir string) (RestorePlan, error) {
	m, err := r.LoadSnapshot(ctx, snapID)
	if err != nil {
		return RestorePlan{}, err
	}
	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return RestorePlan{}, fmt.Errorf("repo: abs dest %s: %w", destDir, err)
	}
	absDest = filepath.Clean(absDest)

	destExists, destEmpty, err := inspectDestDir(absDest)
	if err != nil {
		return RestorePlan{}, err
	}

	paths := make([]string, 0, len(m.Tree))
	for _, fe := range m.Tree {
		if _, err := safeJoinPath(absDest, fe.Path, "restore destination"); err != nil {
			return RestorePlan{}, err
		}
		paths = append(paths, fe.Path)
	}
	return RestorePlan{
		SnapshotID:  m.ID,
		DestDir:     absDest,
		DestExists:  destExists,
		DestEmpty:   destEmpty,
		Files:       m.Stats.Files,
		Bytes:       m.Stats.Bytes,
		Paths:       paths,
		CreatedAt:   m.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
		Description: "restore preview",
	}, nil
}

// VerifyRestore compares the destination tree to the snapshot manifest.
// It reads local files only; it does not fetch chunk blobs from the
// repository because the manifest's plaintext hashes are sufficient to
// validate restored content.
func (r *Repo) VerifyRestore(ctx context.Context, snapID, destDir string) (RestoreVerification, error) {
	m, err := r.LoadSnapshot(ctx, snapID)
	if err != nil {
		return RestoreVerification{}, err
	}
	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return RestoreVerification{}, fmt.Errorf("repo: abs dest %s: %w", destDir, err)
	}
	absDest = filepath.Clean(absDest)
	info, err := os.Stat(absDest)
	if err != nil {
		return RestoreVerification{}, fmt.Errorf("repo: stat dest %s: %w", absDest, err)
	}
	if !info.IsDir() {
		return RestoreVerification{}, fmt.Errorf("repo: dest %s exists and is not a directory", absDest)
	}

	report := RestoreVerification{
		SnapshotID: m.ID,
		DestDir:    absDest,
		Files:      m.Stats.Files,
		Bytes:      m.Stats.Bytes,
	}
	manifestPaths := make(map[string]struct{}, len(m.Tree))
	for _, fe := range m.Tree {
		manifestPaths[fe.Path] = struct{}{}
		matched, err := verifyRestoreFile(ctx, absDest, fe)
		if err != nil {
			return RestoreVerification{}, err
		}
		if matched == nil {
			report.VerifiedFiles++
			continue
		}
		report.Mismatches = append(report.Mismatches, *matched)
	}

	if err := filepath.WalkDir(absDest, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(absDest, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, ok := manifestPaths[rel]; !ok {
			report.ExtraFileCount++
			report.Mismatches = append(report.Mismatches, RestoreMismatch{
				Path:   rel,
				Reason: "extra file not present in snapshot",
			})
		}
		return nil
	}); err != nil {
		return RestoreVerification{}, fmt.Errorf("repo: walk dest %s: %w", absDest, err)
	}

	return report, nil
}

// Restore writes every file in snapshot snapID into destDir. It
// refuses to clobber an existing non-empty directory: if destDir
// exists and contains entries, Restore returns an error rather than
// silently merging on top.
//
// Permissions are restored from the manifest (Mode.Perm() — only
// permission bits, never setuid/setgid/sticky to avoid re-introducing
// surprises across user boundaries). MTime is restored best-effort.
//
// If opts.Progress is non-nil, Restore calls Total() once with the
// manifest's stat sum and Add(size) per file restored.
func (r *Repo) Restore(ctx context.Context, snapID, destDir string, opts RestoreOptions) error {
	if err := validateSnapshotID(snapID); err != nil {
		return err
	}
	repoKey, err := r.keyOrErr()
	if err != nil {
		return err
	}
	// keyOrErr returns a defensive copy; zero it at function exit so
	// the key is not retained past Restore.
	defer crypto.Zeroize(repoKey)

	// Local var name avoids shadowing the imported `progress` package.
	reporter := opts.Progress
	if reporter == nil {
		reporter = progress.NopReporter{}
	}

	m, err := r.LoadSnapshot(ctx, snapID)
	if err != nil {
		return err
	}

	// dest must either not exist (then we create it) or exist and
	// be empty.
	if err := ensureDestDir(destDir); err != nil {
		return err
	}

	// Resolve destDir to its absolute, symlink-cleaned form so that
	// safeJoinPath comparisons are stable regardless of how the
	// caller spells the path.
	absDest, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("repo: abs dest %s: %w", destDir, err)
	}
	absDest = filepath.Clean(absDest)

	reporter.Total(m.Stats.Bytes)

	// Bounded concurrency at the file level: each worker restores
	// one file (fetch its chunks in order, write the destination
	// file). This pipelines the per-chunk Get latency that previously
	// dominated restore wall-time — a 10K-file restore with 4 chunks
	// per file went from 40K sequential round trips to N-way
	// parallel. Per-file is the natural batching unit because chunks
	// within a file are appended in order, so within-file concurrency
	// would need a write coordinator without paying for itself.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(resolveConcurrency(opts.Concurrency))

	for _, fe := range m.Tree {
		fe := fe // capture by value for the goroutine closure
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			if err := r.restoreFile(gctx, repoKey, absDest, fe); err != nil {
				return err
			}
			// Reporter is concurrency-safe (per progress.Reporter
			// contract). Calls happen in worker-completion order
			// rather than manifest order, but the final sum equals
			// the manifest's Stats.Bytes either way.
			reporter.Add(fe.Size)
			return nil
		})
	}
	return g.Wait()
}

// restoreFile writes a single FileEntry into dest, creating parent
// directories as needed and applying mode + mtime from the manifest.
func (r *Repo) restoreFile(ctx context.Context, repoKey []byte, dest string, fe FileEntry) error {
	// Every FileEntry.Path comes from a manifest the caller controls
	// (or that an attacker has tampered with). safeJoinPath rejects
	// anything that would escape dest. The returned dst is absolute
	// and lexically inside dest, so its parent directory is also
	// inside dest by construction.
	dst, err := safeJoinPath(dest, fe.Path, "restore destination")
	if err != nil {
		return err
	}

	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("repo: mkdir %s: %w", parent, err)
	}

	// Stage into a sibling temp file and rename into place only after the
	// whole file is written and closed. A failed/interrupted restore
	// (missing chunk, integrity mismatch, ctx cancel, write error) then
	// leaves the destination exactly as it was rather than a truncated
	// file at the real path — the appearance of each file is atomic, and
	// re-running restore into the same dest is not poisoned by partials.
	// Only permission bits from the manifest mode get applied; type bits
	// come from the open call, and setuid/setgid/sticky are dropped.
	perm := fe.Mode.Perm()
	tmp, err := os.CreateTemp(parent, ".sentra-restore-*")
	if err != nil {
		return fmt.Errorf("repo: create temp for %s: %w", dst, err)
	}
	tmpName := tmp.Name()
	// committed tracks whether the rename succeeded; until then the
	// deferred cleanup removes the temp file so no partial is left behind.
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	for _, hexHash := range fe.Chunks {
		raw, err := r.fetchChunk(ctx, repoKey, hexHash)
		if err != nil {
			return fmt.Errorf("repo: fetch chunk %s for %s: %w", hexHash, fe.Path, err)
		}
		if _, err := tmp.Write(raw); err != nil {
			return fmt.Errorf("repo: write %s: %w", dst, err)
		}
	}
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("repo: chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("repo: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("repo: rename %s -> %s: %w", tmpName, dst, err)
	}
	committed = true

	// Best-effort mtime restoration. We log via the returned error
	// in tests if it ever fires, but in practice Chtimes only fails
	// for permission-denied cases that the open above already
	// would have caught.
	if !fe.MTime.IsZero() {
		if err := os.Chtimes(dst, fe.MTime, fe.MTime); err != nil {
			return fmt.Errorf("repo: chtimes %s: %w", dst, err)
		}
	}
	return nil
}

// fetchChunk downloads, decrypts, and decompresses a single chunk by
// hex hash. Returns the plaintext chunk bytes.
func (r *Repo) fetchChunk(ctx context.Context, repoKey []byte, hexHash string) ([]byte, error) {
	rc, err := r.store.Get(ctx, ChunkKey(hexHash))
	if err != nil {
		return nil, fmt.Errorf("repo: get chunk: %w", err)
	}
	defer rc.Close()
	sealed, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("repo: read chunk: %w", err)
	}
	compressed, err := crypto.Open(repoKey, sealed)
	if err != nil {
		return nil, fmt.Errorf("repo: decrypt chunk: %w", err)
	}
	raw, err := chunker.Decompress(compressed)
	if err != nil {
		return nil, fmt.Errorf("repo: decompress chunk: %w", err)
	}
	// Re-verify content addressing: the plaintext must hash to the key it
	// was fetched under. The AEAD tag only proves the blob was sealed
	// under the repo key, NOT that it is the chunk this address names, so
	// a validly-sealed-but-mis-addressed blob (a swapped object, a
	// corrupted manifest hash, or a future Put-path key-derivation bug)
	// would otherwise restore silently wrong bytes. Chunks are <=4 MiB so
	// the re-hash is cheap. sha256 is not secret; a plain compare is fine.
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != hexHash {
		return nil, fmt.Errorf("repo: chunk %s failed content-address integrity check (got hash %s): %w",
			hexHash, got, ErrChunkHashMismatch)
	}
	return raw, nil
}

// (safeRestorePath was the predecessor of safeJoinPath in path.go.
// See path.go for the consolidated implementation shared between
// restore and backup-plan validation.)

// ensureDestDir refuses to write into an already-populated dest
// directory. If dest does not exist it is created. If dest exists
// and is empty, the call returns nil. A non-directory file at dest
// is rejected.
func ensureDestDir(dest string) error {
	info, err := os.Stat(dest)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(dest, 0o755)
	}
	if err != nil {
		return fmt.Errorf("repo: stat dest %s: %w", dest, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repo: dest %s exists and is not a directory", dest)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return fmt.Errorf("repo: read dest %s: %w", dest, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("repo: dest %s is not empty (%d entries)", dest, len(entries))
	}
	return nil
}

func inspectDestDir(dest string) (exists bool, empty bool, err error) {
	info, err := os.Stat(dest)
	if errors.Is(err, os.ErrNotExist) {
		return false, true, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("repo: stat dest %s: %w", dest, err)
	}
	if !info.IsDir() {
		return true, false, fmt.Errorf("repo: dest %s exists and is not a directory", dest)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return true, false, fmt.Errorf("repo: read dest %s: %w", dest, err)
	}
	if len(entries) != 0 {
		return true, false, fmt.Errorf("repo: dest %s is not empty (%d entries)", dest, len(entries))
	}
	return true, true, nil
}

func verifyRestoreFile(ctx context.Context, absDest string, fe FileEntry) (*RestoreMismatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dst, err := safeJoinPath(absDest, fe.Path, "restore destination")
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dst)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &RestoreMismatch{
				Path:     fe.Path,
				Reason:   "missing file",
				WantSize: fe.Size,
				GotSize:  -1,
			}, nil
		}
		return nil, fmt.Errorf("repo: stat restored file %s: %w", dst, err)
	}
	if !info.Mode().IsRegular() {
		return &RestoreMismatch{
			Path:   fe.Path,
			Reason: "not a regular file",
		}, nil
	}
	if info.Size() != fe.Size {
		return &RestoreMismatch{
			Path:     fe.Path,
			Reason:   "size mismatch",
			WantSize: fe.Size,
			GotSize:  info.Size(),
		}, nil
	}
	gotHashes, err := hashFileChunks(dst)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(gotHashes, fe.Chunks) {
		return &RestoreMismatch{
			Path:     fe.Path,
			Reason:   "content hash mismatch",
			WantSize: fe.Size,
			GotSize:  info.Size(),
		}, nil
	}
	return nil, nil
}

func hashFileChunks(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // path is resolved under the requested restore destination
	if err != nil {
		return nil, fmt.Errorf("repo: open restored file %s: %w", path, err)
	}
	defer f.Close()

	var hashes []string
	if err := chunker.ChunkStream(f, func(c chunker.Chunk) error {
		hashes = append(hashes, hex.EncodeToString(c.Hash))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("repo: chunk restored file %s: %w", path, err)
	}
	return hashes, nil
}
