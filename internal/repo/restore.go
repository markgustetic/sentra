package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/markgustetic/sentra/internal/chunker"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/progress"
)

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
	// restore (matches the pre-Phase-3 behavior — useful when the
	// target filesystem is slow, the bandwidth-delay product is
	// small, or a regression suspect needs to be ruled out).
	//
	// Negative values are clamped to 1.
	Concurrency int
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
	// Phase 5 review C2: keyOrErr returns a defensive copy; zero it
	// at function exit so the key is not retained past Restore.
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
	// safeRestorePath comparisons are stable regardless of how the
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
	concurrency := opts.Concurrency
	switch {
	case concurrency == 0:
		concurrency = runtime.GOMAXPROCS(0)
	case concurrency < 1:
		concurrency = 1
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)

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
	// Phase 5 review C1: every FileEntry.Path comes from a manifest
	// the caller controls (or that an attacker has tampered with).
	// safeRestorePath rejects anything that would escape dest. The
	// returned dst is absolute and lexically inside dest, so its
	// parent directory is also inside dest by construction.
	dst, err := safeRestorePath(dest, fe.Path)
	if err != nil {
		return err
	}

	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("repo: mkdir %s: %w", parent, err)
	}

	// Phase 4 review I2: only permission bits from the manifest
	// mode get applied to the on-disk file. Type bits (regular,
	// directory) are determined by the open call, and setuid /
	// setgid / sticky are intentionally dropped.
	perm := fe.Mode.Perm()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("repo: open dst %s: %w", dst, err)
	}
	closeErr := func() error {
		// Use a small closer fn so deferred Close errors propagate
		// when there isn't already a content-write error in flight.
		return f.Close()
	}

	for _, hexHash := range fe.Chunks {
		raw, err := r.fetchChunk(ctx, repoKey, hexHash)
		if err != nil {
			_ = closeErr()
			return fmt.Errorf("repo: fetch chunk %s for %s: %w", hexHash, fe.Path, err)
		}
		if _, err := f.Write(raw); err != nil {
			_ = closeErr()
			return fmt.Errorf("repo: write %s: %w", dst, err)
		}
	}
	if err := closeErr(); err != nil {
		return fmt.Errorf("repo: close %s: %w", dst, err)
	}

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
	return raw, nil
}

// safeRestorePath joins relPath under dest and verifies that the result
// is lexically contained inside dest. It rejects:
//
//   - empty relPath
//   - absolute relPath (a manifest must always store relative paths)
//   - any joined path whose dest-relative form starts with ".." or
//     equals ".." (which would walk above dest)
//
// dest must already be absolute and clean (the caller is responsible
// for that — Restore Abs+Cleans destDir once at the top).
//
// We compare on lexical paths only and do NOT call EvalSymlinks: the
// destination tree is freshly created (or empty) before restore, so
// a symlink-based escape would require us to follow our own writes.
// Phase 6 review can revisit if directory restore is added.
func safeRestorePath(dest, relPath string) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("repo: empty path in manifest")
	}
	// Manifest paths are stored slash-separated and relative.
	if filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "/") {
		return "", fmt.Errorf("repo: path %q escapes restore destination", relPath)
	}
	joined := filepath.Join(dest, filepath.FromSlash(relPath))
	rel, err := filepath.Rel(dest, joined)
	if err != nil {
		return "", fmt.Errorf("repo: path %q escapes restore destination", relPath)
	}
	// rel may be "." for relPath == "." (which we don't want as a
	// file target either), or start with ".." for any escape attempt.
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("repo: path %q escapes restore destination", relPath)
	}
	if rel == "." {
		return "", fmt.Errorf("repo: path %q escapes restore destination", relPath)
	}
	return joined, nil
}

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
