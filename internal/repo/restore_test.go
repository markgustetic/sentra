package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/chunker"
	"github.com/markgustetic/sentra/internal/crypto"
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

// TestRestore_RejectsPathTraversal verifies that a manifest with a
// FileEntry.Path containing ".." cannot escape the destination
// directory. We forge a tampered manifest, write it under a fresh
// snapshot ID, then call Restore and assert it errors out and that no
// file appears at the would-be escape location.
func TestRestore_RejectsPathTraversal(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "good.txt"), "harmless")

	// Take a real snapshot so we have a manifest with valid chunks to
	// borrow.
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	m, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(m.Tree) != 1 {
		t.Fatalf("expected exactly 1 entry in seed manifest, got %d", len(m.Tree))
	}

	// Tamper with the path so it tries to escape the destination.
	m.Tree[0].Path = "../escaped.txt"
	// Re-stamp with a new ID so it co-exists with the real snapshot.
	tamperedID, err := newSnapshotID(time.Now().UTC())
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	m.ID = tamperedID

	// Re-marshal, compress, encrypt, upload at snapshots/<tamperedID>.
	raw, err := json.Marshal(&m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	compressed, err := chunker.Compress(raw)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	sealed, err := crypto.Seal(r.repoKey, compressed)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := store.Put(ctx, snapshotPrefix+tamperedID, bytes.NewReader(sealed)); err != nil {
		t.Fatalf("put tampered manifest: %v", err)
	}

	destParent := t.TempDir()
	destDir := filepath.Join(destParent, "restored")
	err = r.Restore(ctx, tamperedID, destDir)
	if err == nil {
		t.Fatal("expected error restoring traversal manifest, got nil")
	}
	// Error should mention escape/traversal/destination.
	msg := strings.ToLower(err.Error())
	if !(strings.Contains(msg, "escape") || strings.Contains(msg, "traversal") ||
		strings.Contains(msg, "outside") || strings.Contains(msg, "refus")) {
		t.Fatalf("error did not mention traversal/escape: %v", err)
	}

	// Most important: no file leaked outside destDir.
	leakPath := filepath.Join(destParent, "escaped.txt")
	if _, err := os.Stat(leakPath); err == nil {
		t.Fatalf("traversal succeeded — file written at %s", leakPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected stat error on %s: %v", leakPath, err)
	}
}

// silence unused-import linters when only some tests reference these
// helpers under build flags.
var _ = io.Discard
