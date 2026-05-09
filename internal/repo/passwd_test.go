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
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
)

// TestPasswd_RoundTrip is the headline contract: Init -> Passwd ->
// Close -> Open with the new passphrase succeeds; Open with the old
// passphrase fails with ErrWrongPassphrase. If this test passes,
// the rotation actually rotated.
func TestPasswd_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()

	old := []byte("hunter2-old")
	newPass := []byte("hunter2-new-much-longer")

	r, err := Init(ctx, store, old)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := r.Passwd(ctx, newPass); err != nil {
		t.Fatalf("Passwd: %v", err)
	}
	r.Close()

	// Old passphrase must NOT Open.
	if _, err := Open(ctx, store, old); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("Open with old passphrase: got %v, want ErrWrongPassphrase", err)
	}
	// New passphrase MUST Open.
	r2, err := Open(ctx, store, newPass)
	if err != nil {
		t.Fatalf("Open with new passphrase: %v", err)
	}
	r2.Close()
}

// TestPasswd_RotatesSalt locks decision Q6: every Passwd produces a
// fresh salt. Defensive hygiene per the design doc; if a future
// refactor removed the rotation, this test fails loudly.
func TestPasswd_RotatesSalt(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer r.Close()
	originalSalt := append([]byte(nil), r.Config().Salt...)

	if err := r.Passwd(ctx, []byte("a-different-passphrase")); err != nil {
		t.Fatalf("Passwd: %v", err)
	}
	if bytes.Equal(originalSalt, r.Config().Salt) {
		t.Errorf("Passwd should rotate the salt; original and new are equal")
	}
	if len(r.Config().Salt) != len(originalSalt) {
		t.Errorf("salt length changed: was %d, now %d", len(originalSalt), len(r.Config().Salt))
	}
}

// TestPasswd_PreservesRepoKey is the data-readability contract: a
// snapshot taken before Passwd must restore byte-identically after.
// Without this, rotation would break every existing chunk.
func TestPasswd_PreservesRepoKey(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	// Take a snapshot of a small known tree.
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "bravo")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{Tag: "pre-rotation"})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	if err := r.Passwd(ctx, []byte("brand-new-passphrase")); err != nil {
		t.Fatalf("Passwd: %v", err)
	}
	r.Close()

	// Re-open under the new passphrase and restore.
	r2, err := Open(ctx, store, []byte("brand-new-passphrase"))
	if err != nil {
		t.Fatalf("Open after Passwd: %v", err)
	}
	defer r2.Close()
	dst := filepath.Join(t.TempDir(), "restored")
	if err := r2.Restore(ctx, snap.ID, dst, RestoreOptions{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	gotA, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(gotA) != "alpha" {
		t.Errorf("a.txt: got %q, want %q", gotA, "alpha")
	}
	gotB, err := os.ReadFile(filepath.Join(dst, "sub", "b.txt"))
	if err != nil {
		t.Fatalf("read restored sub: %v", err)
	}
	if string(gotB) != "bravo" {
		t.Errorf("sub/b.txt: got %q, want %q", gotB, "bravo")
	}
}

// TestPasswd_PreservesRepoID_KDF_CreatedAt confirms the rotation
// touches only the fields it should. ID, KDF params, and CreatedAt
// are stable identifiers / config the operator may have tuned;
// silently rewriting them would be a footgun.
func TestPasswd_PreservesRepoID_KDF_CreatedAt(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer r.Close()
	before := r.Config()

	if err := r.Passwd(ctx, []byte("the-new-passphrase")); err != nil {
		t.Fatalf("Passwd: %v", err)
	}
	after := r.Config()

	if after.ID != before.ID {
		t.Errorf("ID changed: %q -> %q", before.ID, after.ID)
	}
	if after.KDF != before.KDF {
		t.Errorf("KDF params changed: %+v -> %+v", before.KDF, after.KDF)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("CreatedAt changed: %v -> %v", before.CreatedAt, after.CreatedAt)
	}
}

// TestPasswd_WritesValidMAC verifies the new config carries a fresh
// MAC valid under the new KEK. Reading the config back through Open
// (which calls verifyConfig) is the cleanest end-to-end assertion;
// if Open succeeds, the MAC was correctly computed.
func TestPasswd_WritesValidMAC(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := r.Passwd(ctx, []byte("the-new-passphrase")); err != nil {
		t.Fatalf("Passwd: %v", err)
	}
	r.Close()

	// Open will fail with ErrConfigTampered if the MAC is wrong.
	r2, err := Open(ctx, store, []byte("the-new-passphrase"))
	if err != nil {
		t.Fatalf("Open after Passwd: %v (MAC may be invalid)", err)
	}
	defer r2.Close()

	// Belt-and-suspenders: confirm the config blob actually contains a MAC.
	rc, _ := store.Get(ctx, configKey)
	defer rc.Close()
	raw, _ := io.ReadAll(rc)
	var cfg RepoConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.MAC) == 0 {
		t.Errorf("Passwd wrote a config with no MAC")
	}
}

// TestPasswd_LegacyConfigGetsMAC is the migration test: a repo
// written by a pre-Phase-4 build has no MAC. After Passwd, the
// rewritten config must carry a valid MAC. This is one of the
// primary reasons this feature exists.
func TestPasswd_LegacyConfigGetsMAC(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()

	// Init normally, then strip the MAC to simulate a legacy repo.
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	r.Close()
	rc, _ := store.Get(ctx, configKey)
	raw, _ := io.ReadAll(rc)
	rc.Close()
	var cfg RepoConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cfg.MAC = nil
	legacy, _ := json.Marshal(&cfg)
	if err := store.Put(ctx, configKey, bytes.NewReader(legacy)); err != nil {
		t.Fatalf("put legacy: %v", err)
	}

	// Open the legacy config (logs warn, succeeds).
	r2, err := Open(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	if err := r2.Passwd(ctx, []byte("upgraded-passphrase")); err != nil {
		t.Fatalf("Passwd on legacy: %v", err)
	}
	r2.Close()

	// Confirm the rewritten config has a MAC.
	rc, _ = store.Get(ctx, configKey)
	raw, _ = io.ReadAll(rc)
	rc.Close()
	var migrated RepoConfig
	if err := json.Unmarshal(raw, &migrated); err != nil {
		t.Fatalf("unmarshal migrated: %v", err)
	}
	if len(migrated.MAC) == 0 {
		t.Fatal("legacy config still missing MAC after Passwd")
	}
	// Sanity: open under the new passphrase succeeds (MAC verifies).
	r3, err := Open(ctx, store, []byte("upgraded-passphrase"))
	if err != nil {
		t.Fatalf("Open migrated: %v", err)
	}
	r3.Close()
}

// TestPasswd_RefusesIdenticalPassphrase catches the "Enter twice"
// stress case: an operator running rotation under suspected-
// compromise pressure types the same passphrase twice. The repo
// must refuse rather than silently rewriting the config to no
// effect.
func TestPasswd_RefusesIdenticalPassphrase(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	pass := []byte("hunter2-original")
	r, err := Init(ctx, store, pass)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer r.Close()

	originalSalt := append([]byte(nil), r.Config().Salt...)

	err = r.Passwd(ctx, pass)
	if err == nil {
		t.Fatal("Passwd with identical passphrase: got nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "match") && !strings.Contains(err.Error(), "same") {
		t.Errorf("error %q should mention identical/match", err.Error())
	}
	// Salt must NOT have rotated (no write happened).
	if !bytes.Equal(r.Config().Salt, originalSalt) {
		t.Errorf("Passwd refused but still rotated salt; should be no-op on refusal")
	}
}

// TestPasswd_HoldsLock confirms Passwd uses the same advisory
// lock as GC + CreateSnapshot (decision Q4=A). A second operator
// trying to Passwd while a backup is mid-flight gets a clean
// ErrRepoLocked.
func TestPasswd_HoldsLock(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer r.Close()

	// Hold the lock manually (simulates an in-progress GC).
	held, err := acquireLock(ctx, r.store, "test-holding")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer releaseLock(ctx, r.store, held)

	err = r.Passwd(ctx, []byte("blocked-rotation"))
	if !errors.Is(err, ErrRepoLocked) {
		t.Fatalf("Passwd while lock held: got %v, want ErrRepoLocked", err)
	}
}

// TestPasswd_LockReleasedOnError confirms the deferred releaseLock
// fires even when Passwd errors mid-execution. Without this, a
// transient store failure during the rotation would leave a stale
// lock blob around and block all subsequent operations.
//
// We engineer the error by wrapping the store and failing the
// config Put. The lock blob is acquired BEFORE the failing write,
// so it must still be released on the way out.
func TestPasswd_LockReleasedOnError(t *testing.T) {
	ctx := context.Background()
	mem := blobstore.NewMemory()
	store := &flakyStore{Store: mem}
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer r.Close()

	// Tell the wrapper to fail the next config Put.
	store.failKey = configKey
	err = r.Passwd(ctx, []byte("rotation-that-fails"))
	if err == nil {
		t.Fatal("expected Passwd to fail, got nil")
	}
	store.failKey = ""

	// Lock must be gone — the deferred releaseLock fired despite the error.
	if _, err := mem.Stat(ctx, lockKey); err == nil {
		t.Error("lock blob still present after failed Passwd; defer release didn't fire")
	} else if !errors.Is(err, blobstore.ErrNotFound) {
		t.Errorf("lock stat: got %v, want ErrNotFound", err)
	}
}

// TestPasswd_ClosedRepoErrors confirms Passwd respects the
// post-Close contract every other repo method follows.
func TestPasswd_ClosedRepoErrors(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	r.Close()

	err = r.Passwd(ctx, []byte("would-rotate-but-cant"))
	if !errors.Is(err, ErrClosed) {
		t.Errorf("Passwd on closed repo: got %v, want ErrClosed", err)
	}
}

// flakyStore wraps a Store and fails Put for a configurable key.
// Used by TestPasswd_LockReleasedOnError to engineer a write
// failure mid-rotation. Other methods pass through unchanged.
type flakyStore struct {
	blobstore.Store
	failKey string
}

func (f *flakyStore) Put(ctx context.Context, key string, r io.Reader) error {
	if f.failKey != "" && key == f.failKey {
		// Drain the reader so the caller doesn't observe a half-read.
		_, _ = io.Copy(io.Discard, r)
		return errors.New("flaky store: synthetic put failure")
	}
	return f.Store.Put(ctx, key, r)
}
