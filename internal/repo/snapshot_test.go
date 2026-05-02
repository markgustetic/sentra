package repo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newTestRepo(t *testing.T) (*Repo, blobstore.Store) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r, store
}

func TestCreateSnapshot_RoundTrip(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "hello")
	writeFile(t, filepath.Join(root, "sub", "b.txt"), "world")

	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if snap.Stats.Files != 2 {
		t.Errorf("files: got %d, want 2", snap.Stats.Files)
	}
	if snap.Stats.Bytes != int64(len("hello")+len("world")) {
		t.Errorf("bytes: got %d, want %d", snap.Stats.Bytes, len("hello")+len("world"))
	}
	if snap.Stats.NewBytes <= 0 {
		t.Errorf("new_bytes: got %d, want > 0 (sealed blobs are bigger than plaintext)", snap.Stats.NewBytes)
	}
	if snap.Tag != "test" {
		t.Errorf("tag: got %q, want %q", snap.Tag, "test")
	}
	if snap.ID == "" {
		t.Errorf("id empty")
	}
	if snap.CreatedAt.IsZero() {
		t.Errorf("created_at zero")
	}

	loaded, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ID != snap.ID {
		t.Errorf("loaded.ID: got %q, want %q", loaded.ID, snap.ID)
	}
	if loaded.Tag != "test" {
		t.Errorf("loaded.Tag: got %q, want %q", loaded.Tag, "test")
	}
	if len(loaded.Tree) != 2 {
		t.Fatalf("loaded.Tree: got %d entries, want 2", len(loaded.Tree))
	}
	// Tree must be in stable (sorted) order.
	if loaded.Tree[0].Path != "a.txt" {
		t.Errorf("tree[0].Path: got %q, want a.txt", loaded.Tree[0].Path)
	}
	if loaded.Tree[1].Path != "sub/b.txt" {
		t.Errorf("tree[1].Path: got %q, want sub/b.txt", loaded.Tree[1].Path)
	}
	for i, fe := range loaded.Tree {
		if len(fe.Chunks) == 0 {
			t.Errorf("tree[%d] %q: empty chunks", i, fe.Path)
		}
	}
}

func TestCreateSnapshot_DedupsAcrossSnapshots(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), strings.Repeat("hello ", 1000))

	if _, err := r.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "first"}); err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	before, err := store.List(ctx, "data/")
	if err != nil {
		t.Fatalf("list 1: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("expected at least one data blob after first snapshot")
	}
	if _, err := r.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "second"}); err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	after, err := store.List(ctx, "data/")
	if err != nil {
		t.Fatalf("list 2: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("expected dedup: blobs went from %d to %d", len(before), len(after))
	}
}

func TestCreateSnapshot_DedupsWithinSnapshot(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	// Two files with identical content. Their chunks share hashes,
	// so each unique chunk uploads exactly once.
	root := t.TempDir()
	body := strings.Repeat("identical content ", 1000)
	writeFile(t, filepath.Join(root, "one.txt"), body)
	writeFile(t, filepath.Join(root, "two.txt"), body)

	if _, err := r.CreateSnapshot(ctx, root, SnapshotOptions{}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	infos, err := store.List(ctx, "data/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// One file alone would produce N chunks; two identical files
	// must produce the same N (not 2N).
	rSingle, _ := newTestRepo(t)
	root2 := t.TempDir()
	writeFile(t, filepath.Join(root2, "one.txt"), body)
	if _, err := rSingle.CreateSnapshot(ctx, root2, SnapshotOptions{}); err != nil {
		t.Fatalf("single snapshot: %v", err)
	}
	singleInfos, err := rSingle.Store().List(ctx, "data/")
	if err != nil {
		t.Fatalf("list single: %v", err)
	}
	if len(infos) != len(singleInfos) {
		t.Fatalf("intra-snapshot dedup failed: 2x file = %d blobs, 1x file = %d blobs",
			len(infos), len(singleInfos))
	}
}

func TestCreateSnapshot_HonorsIgnore(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.txt"), "kept")
	writeFile(t, filepath.Join(root, "drop.log"), "skipped")
	writeFile(t, filepath.Join(root, ".sentraignore"), "*.log\n")

	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	loaded, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, fe := range loaded.Tree {
		if strings.HasSuffix(fe.Path, ".log") {
			t.Fatalf("expected .log to be ignored, found %q", fe.Path)
		}
	}
	// Make sure keep.txt did make it.
	found := false
	for _, fe := range loaded.Tree {
		if fe.Path == "keep.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected keep.txt in tree, got %+v", loaded.Tree)
	}
}

func TestCreateSnapshot_EmptyDir(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	root := t.TempDir()
	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Stats.Files != 0 {
		t.Errorf("files: got %d, want 0", snap.Stats.Files)
	}
	loaded, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Tree) != 0 {
		t.Errorf("tree: got %d entries, want 0", len(loaded.Tree))
	}
}

func TestLoadSnapshot_Missing(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	_, err := r.LoadSnapshot(ctx, "snap-doesnotexist-0000")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateSnapshot_AfterCloseFails(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	r.Close()

	_, err := r.CreateSnapshot(ctx, t.TempDir(), SnapshotOptions{})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}
