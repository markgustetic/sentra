package repo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/walker"
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

// walkerOptionsExcludeCaches builds the walker options struct used by
// the SnapshotOptions Walker test cases. We always set IgnoreFile so
// the resulting struct is non-zero — the repo's resolveWalkerOptions
// treats a zero Options as "use legacy default ExcludeCaches=true",
// so opting OUT of cache exclusion requires non-zero options.
func walkerOptionsExcludeCaches(b bool) walker.Options {
	return walker.Options{IgnoreFile: ".sentraignore", ExcludeCaches: b}
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

type concurrentChunkStore struct {
	blobstore.Store

	releaseStats         chan struct{}
	notFoundDataStats    atomic.Int32
	successfulDataWrites atomic.Int32
}

func newConcurrentChunkStore() *concurrentChunkStore {
	return &concurrentChunkStore{
		Store:        blobstore.NewMemory(),
		releaseStats: make(chan struct{}),
	}
}

func (s *concurrentChunkStore) Stat(ctx context.Context, key string) (blobstore.Info, error) {
	info, err := s.Store.Stat(ctx, key)
	if strings.HasPrefix(key, DataPrefix) && errors.Is(err, blobstore.ErrNotFound) {
		if s.notFoundDataStats.Add(1) == 2 {
			close(s.releaseStats)
		}
		select {
		case <-s.releaseStats:
		case <-time.After(2 * time.Second):
		}
	}
	return info, err
}

func (s *concurrentChunkStore) Put(ctx context.Context, key string, r io.Reader) error {
	err := s.Store.Put(ctx, key, r)
	if err == nil && strings.HasPrefix(key, DataPrefix) {
		s.successfulDataWrites.Add(1)
	}
	return err
}

func (s *concurrentChunkStore) PutIfAbsent(ctx context.Context, key string, r io.Reader) error {
	err := s.Store.PutIfAbsent(ctx, key, r)
	if err == nil && strings.HasPrefix(key, DataPrefix) {
		s.successfulDataWrites.Add(1)
	}
	return err
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
	// v2 manifests record the `sub` directory alongside the two files.
	if len(loaded.Tree) != 3 {
		t.Fatalf("loaded.Tree: got %d entries, want 3 (2 files + 1 dir)", len(loaded.Tree))
	}
	// Tree must be in stable (sorted) order.
	if loaded.Tree[0].Path != "a.txt" {
		t.Errorf("tree[0].Path: got %q, want a.txt", loaded.Tree[0].Path)
	}
	if loaded.Tree[1].Path != "sub" || !loaded.Tree[1].IsDir() {
		t.Errorf("tree[1]: got %q kind %q, want the sub directory entry", loaded.Tree[1].Path, loaded.Tree[1].Kind)
	}
	if loaded.Tree[2].Path != "sub/b.txt" {
		t.Errorf("tree[2].Path: got %q, want sub/b.txt", loaded.Tree[2].Path)
	}
	for i, fe := range loaded.Tree {
		if fe.IsFile() && len(fe.Chunks) == 0 {
			t.Errorf("tree[%d] %q: empty chunks", i, fe.Path)
		}
	}
}

// TestCreateSnapshot_HonorsWalkerOptions verifies that
// SnapshotOptions.Walker is plumbed into the underlying walker call.
// Specifically: with ExcludeCaches=false and a CACHEDIR.TAG-marked
// directory, the snapshot must include the cache contents — the
// previous hardcoded ExcludeCaches=true would silently drop them.
func TestCreateSnapshot_HonorsWalkerOptions(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	if err := os.Mkdir(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cache, "CACHEDIR.TAG"),
		"Signature: 8a477f597d28d172789f06886806bc55\n")
	writeFile(t, filepath.Join(cache, "junk.txt"), "still important")
	writeFile(t, filepath.Join(root, "real.txt"), "x")

	// With ExcludeCaches=false, the cache dir must be walked in full.
	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{
		Walker: walkerOptionsExcludeCaches(false),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	loaded, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := make(map[string]bool)
	for _, fe := range loaded.Tree {
		got[fe.Path] = true
	}
	for _, want := range []string{"cache/CACHEDIR.TAG", "cache/junk.txt", "real.txt"} {
		if !got[want] {
			t.Errorf("expected %q in snapshot tree, got %v", want, got)
		}
	}
}

// TestCreateSnapshot_DefaultExcludeCaches retains the previous default
// behavior so callers passing a zero SnapshotOptions still get the
// CACHEDIR.TAG-honoring walk that was hardcoded before this change.
func TestCreateSnapshot_DefaultExcludeCaches(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	if err := os.Mkdir(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cache, "CACHEDIR.TAG"),
		"Signature: 8a477f597d28d172789f06886806bc55\n")
	writeFile(t, filepath.Join(cache, "junk.txt"), "skip me")
	writeFile(t, filepath.Join(root, "real.txt"), "x")

	// Zero-value SnapshotOptions: cache dir should be skipped per the
	// preserved default. real.txt is the only file in the snapshot.
	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	loaded, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, fe := range loaded.Tree {
		if strings.HasPrefix(fe.Path, "cache/") {
			t.Errorf("cache dir should be skipped by default, got %q", fe.Path)
		}
	}
	found := false
	for _, fe := range loaded.Tree {
		if fe.Path == "real.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("real.txt missing from snapshot tree: %+v", loaded.Tree)
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

func TestCreateSnapshot_DedupsConcurrentIdenticalChunksAtomically(t *testing.T) {
	ctx := context.Background()
	store := newConcurrentChunkStore()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	root := t.TempDir()
	body := strings.Repeat("same small file body\n", 100)
	writeFile(t, filepath.Join(root, "one.txt"), body)
	writeFile(t, filepath.Join(root, "two.txt"), body)

	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{
		Walker: walker.Options{IgnoreFile: ".sentraignore", Concurrency: 2},
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Stats.Files != 2 {
		t.Fatalf("files: got %d, want 2", snap.Stats.Files)
	}
	if got := store.successfulDataWrites.Load(); got != 1 {
		t.Fatalf("successful data writes: got %d, want 1", got)
	}
	infos, err := store.List(ctx, DataPrefix)
	if err != nil {
		t.Fatalf("list data: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("data blobs: got %d, want 1", len(infos))
	}
	if snap.Stats.NewBytes != infos[0].Size {
		t.Fatalf("new bytes: got %d, want single stored blob size %d", snap.Stats.NewBytes, infos[0].Size)
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

	// Well-formed-but-nonexistent ID: must pass shape validation and
	// then surface ErrNotFound from the store.
	_, err := r.LoadSnapshot(ctx, "snap-19700101T000000Z-deadbeef")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestLoadSnapshot_RejectsInvalidID: an attacker / careless caller
// passing "../config" must not become a blobstore Get on
// "snapshots/../config" (which path.Join collapses in the S3 store,
// fetching the config blob and producing an opaque "decompress"
// error). LoadSnapshot must reject the ID up-front with a validation
// error, NOT with an ErrNotFound from the store.
func TestLoadSnapshot_RejectsInvalidID(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	cases := []string{
		"",
		"../config",
		"snap-/../etc",
		"foo",
		"snap-bad-id/with-slash",
		"snap-bad-id\\with-backslash",
		"..",
	}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			_, err := r.LoadSnapshot(ctx, id)
			if err == nil {
				t.Fatalf("LoadSnapshot(%q): expected error, got nil", id)
			}
			// The fix must surface its own validation error, not
			// punt to the blobstore and return ErrNotFound. That's
			// how we know the validator ran *before* the lookup.
			if errors.Is(err, blobstore.ErrNotFound) {
				t.Fatalf("LoadSnapshot(%q): got ErrNotFound — id reached the store unvalidated: %v", id, err)
			}
			if !strings.Contains(err.Error(), "invalid") {
				t.Errorf("LoadSnapshot(%q): expected error mentioning 'invalid', got %v", id, err)
			}
			// Restore should also reject the same.
			err = r.Restore(ctx, id, t.TempDir(), RestoreOptions{})
			if err == nil {
				t.Fatalf("Restore(%q): expected error, got nil", id)
			}
			if errors.Is(err, blobstore.ErrNotFound) {
				t.Fatalf("Restore(%q): got ErrNotFound — id reached the store unvalidated: %v", id, err)
			}
		})
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

// TestRepo_CloseDuringSnapshot_DoesNotCorruptChunks: if Close()
// races with an in-flight CreateSnapshot, chunks must NEVER be
// encrypted under the all-zero key. Either the snapshot completes
// correctly (chunks decryptable with the original key) or it fails
// with a clear error — but it must not silently produce zero-key
// ciphertext that the original key can't decrypt and a future
// attacker with the all-zero key could.
func TestRepo_CloseDuringSnapshot_DoesNotCorruptChunks(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	// Save a copy of the original key so we can decrypt manually
	// after Close has zeroed the live one.
	origKey := make([]byte, len(r.repoKey))
	copy(origKey, r.repoKey)

	// Big enough tree to take measurable time to chunk-encrypt: ~64 MiB
	// across ~16 files of 4 MiB random bytes. Random bytes don't
	// compress so we stress the encrypt path without zstd shortcuts.
	root := t.TempDir()
	const fileCount = 16
	const fileSize = 4 << 20 // 4 MiB
	for i := 0; i < fileCount; i++ {
		buf := make([]byte, fileSize)
		// Deterministic-but-incompressible: simple PRNG seeded by index.
		seed := byte(i + 1)
		for j := range buf {
			seed = seed*31 + 7
			buf[j] = seed
		}
		path := filepath.Join(root, "file", "i", fmt.Sprintf("blob-%02d.bin", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, buf, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// Kick off Close from a goroutine after a small delay. The
	// timing here is best-effort; the assertion below holds
	// regardless of whether Close lands during, before, or after
	// the snapshot finishes.
	go func() {
		// Sleep long enough for some chunks to flow but short enough
		// that we usually catch in-flight Seal calls.
		time.Sleep(20 * time.Millisecond)
		_ = r.Close()
	}()

	snap, snapErr := r.CreateSnapshot(ctx, root, SnapshotOptions{})

	// Open a fresh handle that we control — we're going to fetch and
	// decrypt chunks manually with origKey.
	zeroKey := make([]byte, len(origKey)) // 32 zero bytes

	// Walk every chunk in the data prefix and confirm none decrypts
	// under the all-zero key. If any do, the bug is live.
	infos, err := store.List(ctx, "data/")
	if err != nil {
		t.Fatalf("list data: %v", err)
	}
	for _, info := range infos {
		rc, err := store.Get(ctx, info.Key)
		if err != nil {
			t.Fatalf("get %s: %v", info.Key, err)
		}
		sealed, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", info.Key, err)
		}
		if _, err := crypto.Open(zeroKey, sealed); err == nil {
			t.Fatalf("CRITICAL: chunk %s decrypts under all-zero key — Close race corrupted ciphertext", info.Key)
		}
	}

	if snapErr != nil {
		// Fine: snapshot bailed out cleanly with ErrClosed (or a
		// wrapped version). The data we wrote so far is still
		// trustworthy (we just verified above), so the test passes.
		if !errors.Is(snapErr, ErrClosed) {
			t.Logf("snapshot failed with non-ErrClosed error %v — acceptable as long as no zero-key chunks were written", snapErr)
		}
		return
	}

	// If the snapshot succeeded, we should be able to re-Open the
	// repo and Restore byte-identical content. This is the strongest
	// possible "not corrupted" assertion.
	r2, err := Open(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer r2.Close()
	dst := filepath.Join(t.TempDir(), "restored")
	if err := r2.Restore(ctx, snap.ID, dst, RestoreOptions{}); err != nil {
		t.Fatalf("restore after race: %v", err)
	}
	want := treeFingerprint(t, root)
	got := treeFingerprint(t, dst)
	if len(want) != len(got) {
		t.Fatalf("file count: want %d got %d", len(want), len(got))
	}
	for i := range want {
		if !bytes.Equal(want[i].data, got[i].data) {
			t.Fatalf("%q content mismatch after race", want[i].rel)
		}
	}
}
