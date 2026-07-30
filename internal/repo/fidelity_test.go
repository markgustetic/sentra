package repo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSnapshotRestore_SymlinksAndDirs is the fidelity round-trip: a
// tree containing an empty directory (with a non-default mode), a
// relative symlink, and an absolute symlink must survive
// snapshot→restore intact. Symlinks are recreated as links (never
// followed or materialized), and the empty dir exists with its
// recorded permissions.
func TestSnapshotRestore_SymlinksAndDirs(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "sub", "a.txt"), "alpha")
	if err := os.Mkdir(filepath.Join(src, "emptyd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/a.txt", filepath.Join(src, "rel-link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := os.Symlink("/nonexistent/target", filepath.Join(src, "abs-link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// Stats.Files counts regular files only — dirs and symlinks are
	// not "files" in the summary operators read.
	if snap.Stats.Files != 1 {
		t.Errorf("Stats.Files: got %d, want 1 (dirs/symlinks must not inflate the count)", snap.Stats.Files)
	}

	dest := filepath.Join(t.TempDir(), "out")
	if err := r.Restore(ctx, snap.ID, dest, RestoreOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Empty dir with its recorded mode.
	info, err := os.Lstat(filepath.Join(dest, "emptyd"))
	if err != nil || !info.IsDir() {
		t.Fatalf("empty dir not restored: info=%v err=%v", info, err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("empty dir mode: got %v, want 0700", info.Mode().Perm())
	}

	// Symlinks restored as links with exact targets.
	for _, tc := range []struct{ name, want string }{
		{"rel-link", "sub/a.txt"},
		{"abs-link", "/nonexistent/target"},
	} {
		li, err := os.Lstat(filepath.Join(dest, tc.name))
		if err != nil {
			t.Fatalf("lstat %s: %v", tc.name, err)
		}
		if li.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s restored as %v, want symlink", tc.name, li.Mode())
		}
		got, err := os.Readlink(filepath.Join(dest, tc.name))
		if err != nil {
			t.Fatalf("readlink %s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s target: got %q, want %q", tc.name, got, tc.want)
		}
	}

	// The regular file still restores byte-exact.
	body, err := os.ReadFile(filepath.Join(dest, "sub", "a.txt"))
	if err != nil || string(body) != "alpha" {
		t.Errorf("file content: got %q err=%v", body, err)
	}
}

// TestRestore_RefusesWriteThroughSymlink pins the traversal defense: a
// crafted manifest that plants a symlink pointing outside the dest and
// then a file entry whose path passes through it must NOT write
// outside dest. The write must fail (or be skipped) — never land at
// the symlink's target.
func TestRestore_RefusesWriteThroughSymlink(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	// Seed a snapshot with one real file so a valid chunk exists for
	// the malicious entry to reference.
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "seed.txt"), "payload")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	m, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	seed := m.Tree[0]

	outside := t.TempDir() // the attacker's target directory

	evil := m
	evil.Tree = []FileEntry{
		{Path: "evil", Kind: EntryKindSymlink, LinkTarget: outside, Mode: 0o777 | os.ModeSymlink, MTime: time.Now()},
		{Path: "evil/pwned.txt", Size: seed.Size, Mode: 0o644, MTime: time.Now(), Chunks: seed.Chunks},
	}
	repoKey, err := r.keyOrErr()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.putManifest(ctx, repoKey, evil); err != nil {
		t.Fatalf("plant manifest: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "out")
	restoreErr := r.Restore(ctx, evil.ID, dest, RestoreOptions{})

	if _, statErr := os.Stat(filepath.Join(outside, "pwned.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("file escaped the restore dest through a symlink (stat err=%v)", statErr)
	}
	if restoreErr == nil {
		// Escaping silently succeeding would be worse; but even a
		// "skipped" outcome must surface as an error so the operator
		// knows the restore is incomplete.
		t.Error("restore of a traversal manifest should fail loudly")
	}
}

// TestDiff_SymlinkChanges: a retargeted symlink is a material change;
// directory entries never appear in diff output (they'd be noise for
// v1→v2 manifest comparisons).
func TestDiff_SymlinkChanges(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	if err := os.Symlink("a.txt", filepath.Join(src, "ln")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	snapA, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(src, "ln")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "b.txt"), "bravo")
	if err := os.Symlink("b.txt", filepath.Join(src, "ln")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "newdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	snapB, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	d, err := r.Diff(ctx, snapA.ID, snapB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(d.Changed, "ln") {
		t.Errorf("retargeted symlink must appear in Changed: %+v", d)
	}
	if containsPath(d.Added, "newdir") || containsPath(d.Changed, "newdir") {
		t.Errorf("directory entries must not appear in diff output: %+v", d)
	}
}

func containsPath(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestCreateSnapshot_ReusesUnchangedParentEntries proves the
// incremental scan behaviorally: after the first snapshot, the file is
// made unreadable (chmod 000). A full re-scan would fail at open; the
// incremental path never opens an unchanged file — it reuses the
// parent manifest's chunk list off size+mtime — so the second snapshot
// succeeds with identical chunks and zero new bytes.
func TestCreateSnapshot_ReusesUnchangedParentEntries(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("chmod 000 does not block reads for root")
	}
	r, _ := newTestRepo(t)
	ctx := context.Background()

	src := t.TempDir()
	path := filepath.Join(src, "a.txt")
	writeFile(t, path, "unchanged-content")
	snap1, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}
	m1, err := r.LoadSnapshot(ctx, snap1.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	snap2, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot 2 should reuse the parent entry without opening the file: %v", err)
	}
	if snap2.Stats.NewBytes != 0 {
		t.Errorf("unchanged tree uploaded %d new bytes, want 0", snap2.Stats.NewBytes)
	}
	m2, err := r.LoadSnapshot(ctx, snap2.ID)
	if err != nil {
		t.Fatal(err)
	}
	var e1, e2 *FileEntry
	for i := range m1.Tree {
		if m1.Tree[i].Path == "a.txt" {
			e1 = &m1.Tree[i]
		}
	}
	for i := range m2.Tree {
		if m2.Tree[i].Path == "a.txt" {
			e2 = &m2.Tree[i]
		}
	}
	if e1 == nil || e2 == nil {
		t.Fatalf("a.txt missing from a manifest: %v / %v", e1, e2)
	}
	if len(e2.Chunks) == 0 || !slicesEqual(e1.Chunks, e2.Chunks) {
		t.Errorf("reused entry must carry the parent's chunk list: %v vs %v", e1.Chunks, e2.Chunks)
	}
	// The reused entry records the CURRENT mode (chmod doesn't touch
	// mtime, so reuse still fires — but metadata must not go stale).
	if e2.Mode.Perm() != 0o000 {
		t.Errorf("reused entry mode: got %v, want 000 (current lstat, not the parent's)", e2.Mode.Perm())
	}

	// ForceRescan must actually re-read — which the 000 mode blocks.
	if _, err := r.CreateSnapshot(ctx, src, SnapshotOptions{ForceRescan: true}); err == nil {
		t.Error("ForceRescan must open every file; expected an error on the unreadable file")
	}
}

// TestCreateSnapshot_ChangedFileIsRechunked: touching content (and so
// mtime) must defeat reuse — the changed file gets fresh chunks.
func TestCreateSnapshot_ChangedFileIsRechunked(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	src := t.TempDir()
	path := filepath.Join(src, "a.txt")
	writeFile(t, path, "version-one")
	if _, err := r.CreateSnapshot(ctx, src, SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
	// Same byte length, different content, and a firmly later mtime
	// (explicit Chtimes guards against coarse filesystem clocks).
	writeFile(t, path, "version-two")
	if err := os.Chtimes(path, time.Now().Add(2*time.Second), time.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	snap2, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "out")
	if err := r.Restore(ctx, snap2.ID, dest, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dest, "a.txt"))
	if err != nil || string(body) != "version-two" {
		t.Errorf("changed file must restore its new content: got %q err=%v", body, err)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRestore_PartialByPath: RestoreOptions.Paths scopes the restore
// to exact entries or whole subtrees; a selector that matches nothing
// is an error (a typo'd path silently restoring nothing is how people
// discover backups the bad way).
func TestRestore_PartialByPath(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "bravo")
	writeFile(t, filepath.Join(src, "sub", "deep", "c.txt"), "charlie")
	writeFile(t, filepath.Join(src, "other", "d.txt"), "delta")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Subtree selector.
	dest := filepath.Join(t.TempDir(), "sub-only")
	if err := r.Restore(ctx, snap.ID, dest, RestoreOptions{Paths: []string{"sub"}}); err != nil {
		t.Fatalf("subtree restore: %v", err)
	}
	for _, want := range []string{"sub/b.txt", "sub/deep/c.txt"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(want))); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
	for _, absent := range []string{"a.txt", "other"} {
		if _, err := os.Stat(filepath.Join(dest, absent)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s should not be restored (err=%v)", absent, err)
		}
	}

	// Single-file selector.
	dest2 := filepath.Join(t.TempDir(), "one-file")
	if err := r.Restore(ctx, snap.ID, dest2, RestoreOptions{Paths: []string{"a.txt"}}); err != nil {
		t.Fatalf("single-file restore: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(dest2, "a.txt")); err != nil || string(body) != "alpha" {
		t.Errorf("a.txt: got %q err=%v", body, err)
	}

	// Typo'd selector must fail loudly, not restore nothing.
	dest3 := filepath.Join(t.TempDir(), "typo")
	if err := r.Restore(ctx, snap.ID, dest3, RestoreOptions{Paths: []string{"sub/missing.txt"}}); err == nil {
		t.Error("selector matching nothing must be an error")
	}
}

// TestRestore_OverlappingSelectors: a selector subsumed by an earlier,
// broader one (or duplicated outright) is still "matched" — the
// per-selector accounting must credit every selector an entry
// satisfies, not just the first. The regression: ["sub",
// "sub/b.txt"] failed with `path "sub/b.txt" matches nothing`.
func TestRestore_OverlappingSelectors(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "bravo")
	writeFile(t, filepath.Join(src, "other.txt"), "other")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	for _, paths := range [][]string{
		{"sub", "sub/b.txt"}, // narrower selector swallowed by broader
		{"sub/b.txt", "sub"}, // same, reversed
		{"sub", "sub"},       // duplicated selector
	} {
		dest := filepath.Join(t.TempDir(), "out")
		if err := r.Restore(ctx, snap.ID, dest, RestoreOptions{Paths: paths}); err != nil {
			t.Errorf("overlapping selectors %v must restore, got: %v", paths, err)
			continue
		}
		if body, err := os.ReadFile(filepath.Join(dest, "sub", "b.txt")); err != nil || string(body) != "bravo" {
			t.Errorf("%v: content wrong: %q err=%v", paths, body, err)
		}
		// The file must land exactly once — no duplicate-entry effects.
		if _, err := os.Stat(filepath.Join(dest, "other.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%v: out-of-scope file restored", paths)
		}
	}
}
