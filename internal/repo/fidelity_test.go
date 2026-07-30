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
