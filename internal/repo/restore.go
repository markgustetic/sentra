package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/markgustetic/sentra/internal/chunker"
	"github.com/markgustetic/sentra/internal/crypto"
)

// Restore writes every file in snapshot snapID into destDir. It
// refuses to clobber an existing non-empty directory: if destDir
// exists and contains entries, Restore returns an error rather than
// silently merging on top.
//
// Permissions are restored from the manifest (Mode.Perm() — only
// permission bits, never setuid/setgid/sticky to avoid re-introducing
// surprises across user boundaries). MTime is restored best-effort.
func (r *Repo) Restore(ctx context.Context, snapID, destDir string) error {
	repoKey, err := r.keyOrErr()
	if err != nil {
		return err
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

	for _, fe := range m.Tree {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.restoreFile(ctx, repoKey, destDir, fe); err != nil {
			return err
		}
	}
	return nil
}

// restoreFile writes a single FileEntry into dest, creating parent
// directories as needed and applying mode + mtime from the manifest.
func (r *Repo) restoreFile(ctx context.Context, repoKey []byte, dest string, fe FileEntry) error {
	dst := filepath.Join(dest, filepath.FromSlash(fe.Path))

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("repo: mkdir %s: %w", filepath.Dir(dst), err)
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
	rc, err := r.store.Get(ctx, chunkKey(hexHash))
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
