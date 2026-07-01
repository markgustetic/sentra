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
	//
	// Only permission bits from the manifest mode get applied; type bits
	// come from the open call, and setuid/setgid/sticky are dropped. The
	// perm is set with fchmod (tmp.Chmod) AFTER creation, so the recorded
	// mode is reproduced EXACTLY and the restoring process's umask is
	// intentionally NOT applied — matching manifest.go's "permission bits
	// as observed at backup time" contract and the behavior of tar /
	// restic / rsync -p. (os.CreateTemp itself makes the staged file 0600.)
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
	exists, _, err := statDestDir(dest)
	if err != nil {
		return err
	}
	if !exists {
		return os.MkdirAll(dest, 0o755)
	}
	return nil
}

// statDestDir stats a restore destination and reports whether it
// exists and (if a directory) whether it is empty. A non-directory
// at dest, or a non-empty directory, is reported as an error. Shared
// by ensureDestDir (which creates dest on absence) and inspectDestDir
// (which only reports).
func statDestDir(dest string) (exists bool, empty bool, err error) {
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
