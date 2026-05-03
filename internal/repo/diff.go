package repo

import (
	"context"
	"fmt"
	"sort"
)

// DiffResult is the structured output of Repo.Diff. Each slice is a
// list of paths from the file trees of the two snapshots being
// compared, sorted lexicographically. Determinism matters because
// (idA, idB) → DiffResult is the input to the Phase 11 agent's tool-
// call cache; non-deterministic order would invalidate that cache on
// every identical call.
type DiffResult struct {
	// Added paths are present in B but not in A.
	Added []string
	// Removed paths are present in A but not in B.
	Removed []string
	// Changed paths exist in both manifests but differ in size or
	// mtime. We deliberately don't include "permission only" diffs
	// in v1: a chmod on an unchanged file isn't a delta the user
	// usually cares about, and surfacing it would noise up the
	// diff for common workflows. A future flag can opt in.
	Changed []string
}

// Diff returns the per-path delta between two snapshots. The
// returned DiffResult lists paths added (in B not A), removed (in A
// not B), and changed (in both, with different size or mtime).
//
// Both IDs are validated up-front via the same validator
// LoadSnapshot uses, so a malformed argument fails before reaching
// the blobstore.
func (r *Repo) Diff(ctx context.Context, idA, idB string) (DiffResult, error) {
	if err := validateSnapshotID(idA); err != nil {
		return DiffResult{}, fmt.Errorf("repo: diff snapshot A: %w", err)
	}
	if err := validateSnapshotID(idB); err != nil {
		return DiffResult{}, fmt.Errorf("repo: diff snapshot B: %w", err)
	}

	manifestA, err := r.LoadSnapshot(ctx, idA)
	if err != nil {
		return DiffResult{}, fmt.Errorf("repo: load snapshot A: %w", err)
	}
	manifestB, err := r.LoadSnapshot(ctx, idB)
	if err != nil {
		return DiffResult{}, fmt.Errorf("repo: load snapshot B: %w", err)
	}

	indexA := make(map[string]FileEntry, len(manifestA.Tree))
	for _, fe := range manifestA.Tree {
		indexA[fe.Path] = fe
	}
	indexB := make(map[string]FileEntry, len(manifestB.Tree))
	for _, fe := range manifestB.Tree {
		indexB[fe.Path] = fe
	}

	out := DiffResult{
		Added:   make([]string, 0),
		Removed: make([]string, 0),
		Changed: make([]string, 0),
	}

	// Walk B for added/changed; walk A for removed.
	for path, feB := range indexB {
		feA, ok := indexA[path]
		if !ok {
			out.Added = append(out.Added, path)
			continue
		}
		// Same path in both: did anything material change?
		if entriesDiffer(feA, feB) {
			out.Changed = append(out.Changed, path)
		}
	}
	for path := range indexA {
		if _, ok := indexB[path]; !ok {
			out.Removed = append(out.Removed, path)
		}
	}
	// Sort all three slices so the result is byte-identical for
	// repeated calls on the same inputs. The CLI was already sorting
	// before printing, but downstream callers (notably the Phase 11
	// agent) need deterministic order at the API boundary so cache
	// hashes are stable.
	sort.Strings(out.Added)
	sort.Strings(out.Removed)
	sort.Strings(out.Changed)
	return out, nil
}

// entriesDiffer reports whether two FileEntry values represent a
// material change. Compares Size and MTime. Permission-only changes
// are intentionally not flagged in v1 — see DiffResult.Changed for
// the rationale.
func entriesDiffer(a, b FileEntry) bool {
	if a.Size != b.Size {
		return true
	}
	if !a.MTime.Equal(b.MTime) {
		return true
	}
	return false
}
