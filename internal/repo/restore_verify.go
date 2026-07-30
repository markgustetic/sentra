package repo

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/markgustetic/sentra/internal/chunker"
)

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
func (r *Repo) PlanRestore(ctx context.Context, snapID, destDir string, paths ...string) (RestorePlan, error) {
	m, err := r.LoadSnapshot(ctx, snapID)
	if err != nil {
		return RestorePlan{}, err
	}
	// Scope the plan exactly the way Restore will scope the run, so
	// the dry-run preview and the real thing agree on the file set.
	tree, err := filterTreeByPaths(m.Tree, paths)
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

	// Every entry — dirs and symlinks included — is path-validated,
	// but the preview lists regular files only, matching the plan's
	// Files/Bytes stats (dirs and symlinks write no content).
	preview := make([]string, 0, len(tree))
	files := 0
	var bytes int64
	for _, fe := range tree {
		if _, err := safeJoinPath(absDest, fe.Path, "restore destination"); err != nil {
			return RestorePlan{}, err
		}
		if fe.IsFile() {
			preview = append(preview, fe.Path)
			files++
			bytes += fe.Size
		}
	}
	return RestorePlan{
		SnapshotID:  m.ID,
		DestDir:     absDest,
		DestExists:  destExists,
		DestEmpty:   destEmpty,
		Files:       files,
		Bytes:       bytes,
		Paths:       preview,
		CreatedAt:   m.CreatedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
		Description: "restore preview",
	}, nil
}

// VerifyRestore compares the destination tree to the snapshot manifest.
// It reads local files only; it does not fetch chunk blobs from the
// repository because the manifest's plaintext hashes are sufficient to
// validate restored content.
func (r *Repo) VerifyRestore(ctx context.Context, snapID, destDir string, paths ...string) (RestoreVerification, error) {
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

	// Scope to the same file set a scoped Restore wrote, so verifying
	// a partial restore doesn't flag every unselected file as missing.
	tree, err := filterTreeByPaths(m.Tree, paths)
	if err != nil {
		return RestoreVerification{}, err
	}

	files := m.Stats.Files
	bytes := m.Stats.Bytes
	if len(paths) > 0 {
		files = 0
		bytes = 0
		for _, fe := range tree {
			if fe.IsFile() {
				files++
				bytes += fe.Size
			}
		}
	}
	report := RestoreVerification{
		SnapshotID: m.ID,
		DestDir:    absDest,
		Files:      files,
		Bytes:      bytes,
	}
	manifestPaths := make(map[string]struct{}, len(tree))
	for _, fe := range tree {
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

func inspectDestDir(dest string) (exists bool, empty bool, err error) {
	return statDestDir(dest)
}

// verifyRestoreNonRegular checks a dir or symlink entry: the path must
// exist (by lstat — never following the link under test), be the
// recorded kind, and a symlink must carry the exact recorded target.
func verifyRestoreNonRegular(dst string, fe FileEntry) (*RestoreMismatch, error) {
	info, err := os.Lstat(dst)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &RestoreMismatch{Path: fe.Path, Reason: "missing " + kindLabel(fe), GotSize: -1}, nil
		}
		return nil, fmt.Errorf("repo: lstat restored %s %s: %w", kindLabel(fe), dst, err)
	}
	switch {
	case fe.IsDir():
		if !info.IsDir() {
			return &RestoreMismatch{Path: fe.Path, Reason: "not a directory"}, nil
		}
	case fe.IsSymlink():
		if info.Mode()&os.ModeSymlink == 0 {
			return &RestoreMismatch{Path: fe.Path, Reason: "not a symlink"}, nil
		}
		target, err := os.Readlink(dst)
		if err != nil {
			return nil, fmt.Errorf("repo: readlink %s: %w", dst, err)
		}
		if target != fe.LinkTarget {
			return &RestoreMismatch{Path: fe.Path, Reason: "symlink target mismatch"}, nil
		}
	}
	return nil, nil
}

func kindLabel(fe FileEntry) string {
	switch {
	case fe.IsDir():
		return "directory"
	case fe.IsSymlink():
		return "symlink"
	default:
		return "file"
	}
}

func verifyRestoreFile(ctx context.Context, absDest string, fe FileEntry) (*RestoreMismatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dst, err := safeJoinPath(absDest, fe.Path, "restore destination")
	if err != nil {
		return nil, err
	}
	// Dir and symlink entries verify structurally — existence, kind,
	// and (for links) the exact target. They carry no chunks to hash.
	if fe.IsDir() || fe.IsSymlink() {
		return verifyRestoreNonRegular(dst, fe)
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
