package heuristics

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"slices"

	"github.com/markgustetic/sentra/internal/walker"
)

// DupPaths flags groups of two or more files that share identical
// content. The naive algorithm — hash every file, group by hash — is
// O(total bytes). For backup-style trees that can mean tens of GB of
// I/O for nothing useful, since most files are unique-by-size.
//
// The implementation here uses the standard size-bucket optimization:
//
//  1. Group entries by Size. Files with unique sizes can never be
//     duplicates and are dropped immediately — zero I/O.
//  2. For each remaining bucket (2+ files of the same size), hash
//     every file with SHA-256 and group by hash.
//  3. Hash buckets with 2+ entries become findings.
//
// Zero-byte files are excluded entirely: every empty file collides
// with every other empty file, but reporting them is noise (.gitkeep
// patterns, placeholder configs).
type DupPaths struct{}

// NewDupPaths constructs a DupPaths heuristic.
func NewDupPaths() *DupPaths { return &DupPaths{} }

// Name is the registry-visible name of this heuristic.
func (d *DupPaths) Name() string { return "dup_paths" }

// Run computes duplicate groups across in.Walked and emits one
// info-severity finding per group of size 2+. The Target of the
// finding is the *first* path (alphabetically) for stability;
// Details["paths"] carries the full set.
func (d *DupPaths) Run(ctx context.Context, in Input) ([]Finding, error) {
	// Step 1: bucket by size. Skip size=0 (see type doc).
	bySize := make(map[int64][]walker.Entry)
	for _, e := range in.Walked {
		if e.Size <= 0 {
			continue
		}
		bySize[e.Size] = append(bySize[e.Size], e)
	}

	// Step 2 + 3: for each multi-entry size bucket, hash each file
	// and look for duplicate hashes.
	type group struct {
		hash  string
		paths []string
	}
	var groups []group

	for _, bucket := range bySize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(bucket) < 2 {
			continue
		}
		byHash := make(map[string][]string, len(bucket))
		for _, e := range bucket {
			h, err := hashFile(e.AbsPath)
			if err != nil {
				// A transient open failure (file disappeared between
				// walk and hash) shouldn't fail the whole heuristic.
				// Skip the entry and keep going.
				continue
			}
			byHash[h] = append(byHash[h], e.AbsPath)
		}
		for h, paths := range byHash {
			if len(paths) < 2 {
				continue
			}
			slices.Sort(paths)
			groups = append(groups, group{hash: h, paths: paths})
		}
	}

	// Sort groups deterministically: smallest representative path
	// first, then by hash. Stable output makes downstream golden
	// tests (and the LLM prompt builder) easier to reason about.
	slices.SortFunc(groups, func(a, b group) int {
		if c := cmp.Compare(a.paths[0], b.paths[0]); c != 0 {
			return c
		}
		return cmp.Compare(a.hash, b.hash)
	})

	out := make([]Finding, 0, len(groups))
	for _, g := range groups {
		out = append(out, Finding{
			ID:       makeFindingID("dup_paths", g.hash),
			Category: "dup_paths",
			Severity: SeverityInfo,
			Target:   g.paths[0],
			Details: map[string]any{
				"paths": g.paths,
				"hash":  g.hash,
			},
		})
	}
	return out, nil
}

// hashFile returns the hex-encoded SHA-256 of the file at path.
// Streamed via io.Copy so we don't have to materialize large files in
// memory; OK for the duplicate-detection use case where we'll only
// hash files that share a size with at least one other file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from walker.Entry, validated upstream
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
