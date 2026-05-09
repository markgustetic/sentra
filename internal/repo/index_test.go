package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/chunker"
	"github.com/markgustetic/sentra/internal/crypto"
)

// indexTestKey unwraps the repo's defensive key copy so the index
// helpers (which expect a plaintext key) can be exercised in tests
// without driving them through CreateSnapshot.
//
// The helpers themselves never inspect the key beyond passing it to
// crypto.Seal/crypto.Open, so there's no special handling required.
func indexTestKey(t *testing.T, r *Repo) []byte {
	t.Helper()
	k, err := r.keyOrErr()
	if err != nil {
		t.Fatalf("keyOrErr: %v", err)
	}
	return k
}

// TestSnapshotIndex_RoundTrip locks in the basic encrypt-decrypt
// round trip: save followed by load returns the same entries the
// caller wrote. Also confirms saveSnapshotIndex re-stamps Version
// and Updated even when the input struct's fields were left zero.
func TestSnapshotIndex_RoundTrip(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	repoKey := indexTestKey(t, r)
	defer crypto.Zeroize(repoKey)

	in := &snapshotIndex{
		Entries: []SnapshotInfo{
			{ID: "snap-20260507T120000Z-aaaa", CreatedAt: time.Now().UTC(), Tag: "first"},
			{ID: "snap-20260507T130000Z-bbbb", CreatedAt: time.Now().UTC(), Tag: "second"},
		},
	}
	if err := r.saveSnapshotIndex(ctx, repoKey, in); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := r.loadSnapshotIndex(ctx, repoKey)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("load returned nil after save")
	}
	if got.Version != snapshotIndexVersion {
		t.Errorf("Version: got %d, want %d", got.Version, snapshotIndexVersion)
	}
	if got.Updated.IsZero() {
		t.Error("Updated must be re-stamped on save")
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries: got %d, want 2", len(got.Entries))
	}
	if got.Entries[0].ID != in.Entries[0].ID || got.Entries[1].ID != in.Entries[1].ID {
		t.Errorf("entries do not round-trip: got %+v", got.Entries)
	}
}

// TestSnapshotIndex_LoadMissingReturnsNilNil confirms the contract
// that an absent index blob is signaled by (nil, nil) rather than an
// error — callers should fall back to manifest fan-out, which would
// fail unhelpfully if every fresh repo errored here.
func TestSnapshotIndex_LoadMissingReturnsNilNil(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	repoKey := indexTestKey(t, r)
	defer crypto.Zeroize(repoKey)

	got, err := r.loadSnapshotIndex(ctx, repoKey)
	if err != nil {
		t.Fatalf("load on empty repo: got error %v, want nil", err)
	}
	if got != nil {
		t.Errorf("load on empty repo: got %+v, want nil", got)
	}
}

// TestSnapshotIndex_LoadCorruptReturnsError covers the path where
// somebody (or something) put non-encrypted bytes at the index key.
// Decrypt should fail with a clear error so the caller can choose to
// rebuild rather than silently mis-read.
func TestSnapshotIndex_LoadCorruptReturnsError(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)
	repoKey := indexTestKey(t, r)
	defer crypto.Zeroize(repoKey)

	// Write garbage at the index key.
	if err := store.Put(ctx, snapshotIndexKey, bytes.NewReader([]byte("not encrypted"))); err != nil {
		t.Fatalf("put garbage: %v", err)
	}
	_, err := r.loadSnapshotIndex(ctx, repoKey)
	if err == nil {
		t.Fatal("load on corrupt index: got nil error, want non-nil")
	}
	// We don't assert the exact error message but the path matters: a
	// caller using errors.Is(err, blobstore.ErrNotFound) shouldn't
	// match — corruption is different from missing.
	if errors.Is(err, blobstore.ErrNotFound) {
		t.Errorf("corrupt index error must not satisfy errors.Is(_, ErrNotFound): %v", err)
	}
}

// TestSnapshotIndex_LoadVersionMismatch asserts the safety rail: if a
// future Sentra writes a v2 index and an older build reads it, the
// older build must NOT silently use the v2 entries (the schema may
// have changed in incompatible ways). Surfacing an error lets the
// caller log + fall back to manifests for a clean recovery.
func TestSnapshotIndex_LoadVersionMismatch(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	repoKey := indexTestKey(t, r)
	defer crypto.Zeroize(repoKey)

	// Construct a v999 index by hand and write it.
	in := &snapshotIndex{Entries: []SnapshotInfo{{ID: "snap-x"}}}
	if err := r.saveSnapshotIndex(ctx, repoKey, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Now mutate the version field by re-loading, bumping, and
	// re-saving via direct seal to bypass our own version stamper.
	got, err := r.loadSnapshotIndex(ctx, repoKey)
	if err != nil {
		t.Fatalf("load before tamper: %v", err)
	}
	got.Version = 999
	// Re-encode and write the tampered version. saveSnapshotIndex
	// re-stamps to the current version so we have to bypass it.
	if err := writeRawSnapshotIndex(ctx, r, repoKey, got); err != nil {
		t.Fatalf("write tampered: %v", err)
	}

	_, err = r.loadSnapshotIndex(ctx, repoKey)
	if err == nil {
		t.Fatal("load on v999 index: got nil, want version-mismatch error")
	}
	if !strings.Contains(err.Error(), "unknown index version") {
		t.Errorf("error %q should mention version mismatch", err.Error())
	}
}

// writeRawSnapshotIndex is the test-only escape hatch used by the
// version-mismatch test to write an index whose Version field doesn't
// match what saveSnapshotIndex would stamp. Mirrors the production
// save path (json + zstd + AEAD) but skips the version re-stamp.
func writeRawSnapshotIndex(ctx context.Context, r *Repo, repoKey []byte, idx *snapshotIndex) error {
	raw, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	compressed, err := chunker.Compress(raw)
	if err != nil {
		return err
	}
	sealed, err := crypto.Seal(repoKey, compressed)
	if err != nil {
		return err
	}
	return r.store.Put(ctx, snapshotIndexKey, bytes.NewReader(sealed))
}

// TestUpdateSnapshotIndex_AppendsAndPersists confirms the read-
// modify-write path: starting from a missing index, mutate adds an
// entry, the entry persists across calls, and a second mutate adds
// another without losing the first.
func TestUpdateSnapshotIndex_AppendsAndPersists(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	repoKey := indexTestKey(t, r)
	defer crypto.Zeroize(repoKey)

	// First update on an empty repo: index doesn't exist yet, but
	// updateSnapshotIndex creates it and adds the entry.
	if err := r.updateSnapshotIndex(ctx, repoKey, func(idx *snapshotIndex) error {
		idx.Entries = append(idx.Entries, SnapshotInfo{ID: "snap-1"})
		return nil
	}); err != nil {
		t.Fatalf("update#1: %v", err)
	}
	// Second update should see the first entry and add the second.
	if err := r.updateSnapshotIndex(ctx, repoKey, func(idx *snapshotIndex) error {
		if len(idx.Entries) != 1 || idx.Entries[0].ID != "snap-1" {
			t.Errorf("update#2 saw entries %+v, want [snap-1]", idx.Entries)
		}
		idx.Entries = append(idx.Entries, SnapshotInfo{ID: "snap-2"})
		return nil
	}); err != nil {
		t.Fatalf("update#2: %v", err)
	}

	// Final read confirms both entries present.
	got, err := r.loadSnapshotIndex(ctx, repoKey)
	if err != nil {
		t.Fatalf("final load: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Errorf("final entries: got %d, want 2", len(got.Entries))
	}
}

// TestCreateSnapshot_PopulatesIndex is the integration test that
// proves the fast path actually fires: after CreateSnapshot, the
// snapshot index blob exists and contains the new entry. This is
// what makes ListSnapshots O(1) on subsequent calls.
func TestCreateSnapshot_PopulatesIndex(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	repoKey := indexTestKey(t, r)
	defer crypto.Zeroize(repoKey)

	root := t.TempDir()
	if err := putFile(root, "x.txt", "hi"); err != nil {
		t.Fatal(err)
	}

	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "first"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	idx, err := r.loadSnapshotIndex(ctx, repoKey)
	if err != nil {
		t.Fatalf("load index: %v", err)
	}
	if idx == nil {
		t.Fatal("index missing after CreateSnapshot")
	}
	if len(idx.Entries) != 1 || idx.Entries[0].ID != snap.ID {
		t.Errorf("index entries: got %+v, want one entry with ID %s", idx.Entries, snap.ID)
	}
	if idx.Entries[0].Tag != "first" {
		t.Errorf("index entry Tag: got %q, want %q", idx.Entries[0].Tag, "first")
	}
}

// TestDeleteSnapshot_RemovesFromIndex confirms that DeleteSnapshot
// keeps the index in sync. After delete, the entry is gone from the
// index — so a subsequent ListSnapshots (which trusts the index)
// won't accidentally surface a snapshot whose manifest no longer
// exists.
func TestDeleteSnapshot_RemovesFromIndex(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	repoKey := indexTestKey(t, r)
	defer crypto.Zeroize(repoKey)

	root := t.TempDir()
	if err := putFile(root, "x.txt", "hi"); err != nil {
		t.Fatal(err)
	}
	snap1, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snap1: %v", err)
	}
	if err := putFile(root, "y.txt", "yo"); err != nil {
		t.Fatal(err)
	}
	snap2, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snap2: %v", err)
	}

	if err := r.DeleteSnapshot(ctx, snap1.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	idx, err := r.loadSnapshotIndex(ctx, repoKey)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if idx == nil || len(idx.Entries) != 1 {
		t.Fatalf("index after delete: got %+v, want one entry", idx)
	}
	if idx.Entries[0].ID != snap2.ID {
		t.Errorf("remaining entry: got %q, want %q (snap1 should have been removed)",
			idx.Entries[0].ID, snap2.ID)
	}
}

// TestListSnapshots_FastPath confirms that after CreateSnapshot
// has populated the index, ListSnapshots reads the index blob
// directly without touching the per-manifest blobs.
//
// We instrument the in-memory store with a Get-counter via a wrapper.
// On the indexed read path, ListSnapshots issues exactly ONE Get
// (against meta/snapshots) — none against snapshots/<id>. The
// fallback path would issue len(snapshots)+1 Gets.
func TestListSnapshots_FastPath(t *testing.T) {
	ctx := context.Background()

	// Build a normal repo, write a few snapshots, then re-open with
	// a counting wrapper so we observe Gets only during the test
	// list call (avoids counting Init / CreateSnapshot Gets).
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		path := filepath.Join(root, "f"+string(rune('a'+i))+".txt")
		if err := putFile(root, path, "x"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.CreateSnapshot(ctx, root, SnapshotOptions{}); err != nil {
			t.Fatalf("snap %d: %v", i, err)
		}
	}
	r.Close()

	// Re-open with a counter wrapping the same store.
	counter := &countingStore{Store: store}
	r2, err := Open(ctx, counter, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r2.Close()

	infos, err := r2.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(infos) != 3 {
		t.Errorf("infos: got %d, want 3", len(infos))
	}

	// Index path: one Get for meta/snapshots. Plus one Get for the
	// repo's config object on Open (that already happened above and
	// ISN'T counted because counter wraps starting from re-open).
	// Actually Open's config Get IS counted. So we expect:
	//   - 1 Get for the config (Open path)
	//   - 1 Get for the index (ListSnapshots fast path)
	// = 2 total. Anything higher means the manifest path fired.
	if got := counter.gets.Load(); got > 2 {
		t.Errorf("Get count: got %d, want <=2 (1 config + 1 index, no manifest fan-out)", got)
	}
}

// countingStore wraps a Store and counts Get calls. Used to assert
// that the indexed ListSnapshots path doesn't issue per-manifest
// Gets.
type countingStore struct {
	blobstore.Store
	gets atomic.Int32
}

func (c *countingStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	c.gets.Add(1)
	return c.Store.Get(ctx, key)
}

// putFile is a small helper that mirrors writeFile from snapshot_test.go
// but without the t.Helper indirection — keeps the index test file
// self-contained when individual tests are run in isolation.
func putFile(root, rel, body string) error {
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(body), 0o600)
}

// TestUpdateSnapshotIndex_RebuildsOnCorrupt confirms the self-heal
// behavior: a corrupt index doesn't bubble out to the caller. The
// mutate callback runs against a fresh empty index and the write
// path replaces the corrupt blob with a clean one.
func TestUpdateSnapshotIndex_RebuildsOnCorrupt(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)
	repoKey := indexTestKey(t, r)
	defer crypto.Zeroize(repoKey)

	// Plant a corrupt index.
	if err := store.Put(ctx, snapshotIndexKey, bytes.NewReader([]byte("garbage"))); err != nil {
		t.Fatalf("put garbage: %v", err)
	}

	if err := r.updateSnapshotIndex(ctx, repoKey, func(idx *snapshotIndex) error {
		// We expect a fresh index here since the corrupt one was
		// silently discarded.
		if len(idx.Entries) != 0 {
			t.Errorf("expected empty entries on rebuild, got %d", len(idx.Entries))
		}
		idx.Entries = append(idx.Entries, SnapshotInfo{ID: "snap-recovered"})
		return nil
	}); err != nil {
		t.Fatalf("update over corrupt: %v", err)
	}

	// The on-disk index should now be valid and contain the entry.
	got, err := r.loadSnapshotIndex(ctx, repoKey)
	if err != nil {
		t.Fatalf("load after rebuild: %v", err)
	}
	if got == nil || len(got.Entries) != 1 || got.Entries[0].ID != "snap-recovered" {
		t.Errorf("rebuilt index: %+v", got)
	}
}
