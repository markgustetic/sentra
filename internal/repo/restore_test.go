package repo

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// fileFingerprint is the content + mode + size of one file relative
// to a tree root. Used by treeFingerprint for byte-for-byte
// comparison of two trees.
type fileFingerprint struct {
	rel  string
	size int64
	mode os.FileMode
	data []byte
}

func treeFingerprint(t *testing.T, root string) []fileFingerprint {
	t.Helper()
	var out []fileFingerprint
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path) //nolint:gosec // path is from WalkDir under our test temp root
		if err != nil {
			return err
		}
		out = append(out, fileFingerprint{
			rel:  rel,
			size: info.Size(),
			mode: info.Mode().Perm(),
			data: raw,
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

func TestRestore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	writeFile(t, filepath.Join(src, "b.bin"), strings.Repeat("\x00\x01\x02\x03", 256))
	writeFile(t, filepath.Join(src, "sub", "c.md"), "# heading\n\nbody\n")

	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{Tag: "rt"})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if err := r.Restore(ctx, snap.ID, dst); err != nil {
		t.Fatalf("restore: %v", err)
	}

	want := treeFingerprint(t, src)
	got := treeFingerprint(t, dst)

	if len(want) != len(got) {
		t.Fatalf("file count: src=%d dst=%d", len(want), len(got))
	}
	for i := range want {
		if want[i].rel != got[i].rel {
			t.Errorf("rel: want %q, got %q", want[i].rel, got[i].rel)
		}
		if want[i].size != got[i].size {
			t.Errorf("%q size: want %d, got %d", want[i].rel, want[i].size, got[i].size)
		}
		// Mode comparison is permission-only — that's what the
		// manifest carries through the round-trip. Skip on Windows
		// where the perm-bits model diverges; the tree is unix-only
		// for now anyway.
		if runtime.GOOS != "windows" && want[i].mode != got[i].mode {
			t.Errorf("%q mode: want %v, got %v", want[i].rel, want[i].mode, got[i].mode)
		}
		if !bytes.Equal(want[i].data, got[i].data) {
			t.Errorf("%q content mismatch (want %d bytes, got %d bytes)",
				want[i].rel, len(want[i].data), len(got[i].data))
		}
	}
}

func TestRestore_RefusesNonEmptyDest(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := t.TempDir()
	// dst exists and is non-empty.
	writeFile(t, filepath.Join(dst, "leftover.txt"), "stale")

	if err := r.Restore(ctx, snap.ID, dst); err == nil {
		t.Fatal("expected error restoring into non-empty dir, got nil")
	}
}

func TestRestore_NestedDirectories(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	for _, p := range []string{
		"top.txt",
		"a/one.txt",
		"a/b/two.txt",
		"a/b/c/three.txt",
		"a/b/c/d/four.txt",
	} {
		writeFile(t, filepath.Join(src, p), "x="+p)
	}
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "fresh")
	if err := r.Restore(ctx, snap.ID, dst); err != nil {
		t.Fatalf("restore: %v", err)
	}
	want := treeFingerprint(t, src)
	got := treeFingerprint(t, dst)
	if len(want) != len(got) {
		t.Fatalf("count: want %d, got %d", len(want), len(got))
	}
	for i := range want {
		if want[i].rel != got[i].rel || !bytes.Equal(want[i].data, got[i].data) {
			t.Errorf("mismatch on %q: want %q, got %q", want[i].rel,
				string(want[i].data), string(got[i].data))
		}
	}
}

func TestRestore_AllowsEmptyExistingDest(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "x.txt"), "value")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := t.TempDir() // exists, empty
	if err := r.Restore(ctx, snap.ID, dst); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := treeFingerprint(t, dst)
	if len(got) != 1 || got[0].rel != "x.txt" || string(got[0].data) != "value" {
		t.Fatalf("restore output unexpected: %+v", got)
	}
}
