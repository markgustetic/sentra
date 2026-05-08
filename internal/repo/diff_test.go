package repo

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestDiff_AddedRemovedChanged covers all three categories with one
// pair of snapshots:
//   - "kept.txt" stays the same → not in any list
//   - "added.txt" only in B → added
//   - "removed.txt" only in A → removed
//   - "changed.txt" in both, different size → changed
func TestDiff_AddedRemovedChanged(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "kept.txt"), "stable")
	writeFile(t, filepath.Join(root, "removed.txt"), "deleted next")
	writeFile(t, filepath.Join(root, "changed.txt"), "v1")
	a, err := r.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "A"})
	if err != nil {
		t.Fatalf("snapshot A: %v", err)
	}

	// Mutate the tree.
	if err := os.Remove(filepath.Join(root, "removed.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	writeFile(t, filepath.Join(root, "added.txt"), "fresh")
	writeFile(t, filepath.Join(root, "changed.txt"), "v2 longer text")
	b, err := r.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "B"})
	if err != nil {
		t.Fatalf("snapshot B: %v", err)
	}

	res, err := r.Diff(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	slices.Sort(res.Added)
	slices.Sort(res.Removed)
	slices.Sort(res.Changed)

	if got, want := res.Added, []string{"added.txt"}; !equalStrings(got, want) {
		t.Errorf("Added: got %v, want %v", got, want)
	}
	if got, want := res.Removed, []string{"removed.txt"}; !equalStrings(got, want) {
		t.Errorf("Removed: got %v, want %v", got, want)
	}
	if got, want := res.Changed, []string{"changed.txt"}; !equalStrings(got, want) {
		t.Errorf("Changed: got %v, want %v", got, want)
	}
	// kept.txt must not appear anywhere.
	for _, list := range [][]string{res.Added, res.Removed, res.Changed} {
		for _, p := range list {
			if p == "kept.txt" {
				t.Errorf("kept.txt should not appear in %v", list)
			}
		}
	}
}

// TestDiff_IdenticalSnapshots returns three empty slices when both
// IDs point at the same content.
func TestDiff_IdenticalSnapshots(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "x")
	writeFile(t, filepath.Join(root, "b.txt"), "y")
	s, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	res, err := r.Diff(ctx, s.ID, s.ID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(res.Added)+len(res.Removed)+len(res.Changed) != 0 {
		t.Fatalf("expected empty diff for self vs self, got %+v", res)
	}
}

// TestDiff_DeterministicOrder asserts that calling Diff repeatedly on
// the same input pair returns byte-identical slices each time. Map
// iteration order in Go is randomized, so the previous implementation
// happened to pass tests that pre-sorted, but the underlying repo
// API was non-deterministic — fatal for the agent path that hashes
// (idA, idB) → tool-result for prompt caching.
func TestDiff_DeterministicOrder(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	root := t.TempDir()
	// Many entries so map iteration randomness has many degrees of
	// freedom to expose itself.
	for i := 0; i < 50; i++ {
		writeFile(t, filepath.Join(root, "kept", fileN(i)), "kept")
		writeFile(t, filepath.Join(root, "removed", fileN(i)), "rm")
		writeFile(t, filepath.Join(root, "changed", fileN(i)), "v1")
	}
	a, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot A: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, "removed")); err != nil {
		t.Fatalf("removeall: %v", err)
	}
	for i := 0; i < 50; i++ {
		writeFile(t, filepath.Join(root, "added", fileN(i)), "added")
		writeFile(t, filepath.Join(root, "changed", fileN(i)), "v2 longer")
	}
	b, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot B: %v", err)
	}

	first, err := r.Diff(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for i := 0; i < 9; i++ {
		got, err := r.Diff(ctx, a.ID, b.ID)
		if err != nil {
			t.Fatalf("Diff iter %d: %v", i, err)
		}
		if !equalStrings(got.Added, first.Added) {
			t.Fatalf("Added order changed on iter %d:\nfirst: %v\ngot:   %v", i, first.Added, got.Added)
		}
		if !equalStrings(got.Removed, first.Removed) {
			t.Fatalf("Removed order changed on iter %d:\nfirst: %v\ngot:   %v", i, first.Removed, got.Removed)
		}
		if !equalStrings(got.Changed, first.Changed) {
			t.Fatalf("Changed order changed on iter %d:\nfirst: %v\ngot:   %v", i, first.Changed, got.Changed)
		}
	}
}

// fileN is a small helper that gives 50 distinct filenames in a
// natural-looking order (so a deterministic sort visibly differs from
// a randomized map walk for human inspection on failure).
func fileN(i int) string {
	return "file-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + ".txt"
}

// TestDiff_RejectsBadID surfaces repo.validateSnapshotID for both
// arguments — a CLI caller pasting in "../config" must not reach the
// store with that path.
func TestDiff_RejectsBadID(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "x.txt"), "x")
	s, _ := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if _, err := r.Diff(ctx, "../config", s.ID); err == nil {
		t.Fatal("expected error for traversal A, got nil")
	}
	if _, err := r.Diff(ctx, s.ID, ""); err == nil {
		t.Fatal("expected error for empty B, got nil")
	}
}

// equalStrings reports whether two slices have identical elements in
// the same order. Helper for the diff test's sorted comparison.
//
// gosec false-positives on the indexed access despite the explicit
// length-equality guard at the top of the function. Suppressing
// G602 just on the offending line keeps the rest of the linter
// running.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] { //nolint:gosec // G602: bounds guaranteed by len check above
			return false
		}
	}
	return true
}
