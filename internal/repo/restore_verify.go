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
