package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/crypto"
)

func TestInit_WritesConfig(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer r.Close()

	rc, err := store.Get(ctx, configKey)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg RepoConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Version != RepoConfigVersion {
		t.Errorf("Version: got %d want %d", cfg.Version, RepoConfigVersion)
	}
	if cfg.ID == "" {
		t.Errorf("ID empty")
	}
	if len(cfg.Salt) != crypto.SaltLen {
		t.Errorf("Salt: got %d want %d", len(cfg.Salt), crypto.SaltLen)
	}
	if len(cfg.WrappedRepoKey) == 0 {
		t.Errorf("WrappedRepoKey empty")
	}
	if cfg.CreatedAt.IsZero() {
		t.Errorf("CreatedAt zero")
	}
}

func TestInit_TwiceFails(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r1, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("first init: %v", err)
	}
	r1.Close()
	_, err = Init(ctx, store, []byte("hunter2"))
	if !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second init: got %v, want ErrAlreadyInitialized", err)
	}
}

func TestOpen_WrongPassphraseFails(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	r.Close()

	_, err = Open(ctx, store, []byte("wrong"))
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("open with wrong passphrase: got %v, want ErrWrongPassphrase", err)
	}
}

func TestOpen_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()

	r1, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	// Encrypt a probe blob with the first repo's key, save the
	// ciphertext, then close.
	probe := []byte("the quick brown fox jumps over the lazy dog")
	sealed, err := crypto.Seal(r1.repoKey, probe)
	if err != nil {
		t.Fatalf("seal probe: %v", err)
	}
	r1.Close()

	r2, err := Open(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r2.Close()

	got, err := crypto.Open(r2.repoKey, sealed)
	if err != nil {
		t.Fatalf("open probe: %v", err)
	}
	if !bytes.Equal(got, probe) {
		t.Fatalf("probe round-trip mismatch")
	}
}

func TestClose_Idempotent(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestClose_ZeroesKey(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	// Sanity: key is non-zero before close.
	if isAllZero(r.repoKey) {
		t.Fatalf("repoKey already zero before close")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if r.repoKey != nil && !isAllZero(r.repoKey) {
		t.Fatalf("repoKey not zeroed after close")
	}
}

func TestOpen_RejectsBadKDFParams(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	r.Close()

	// Corrupt the on-disk config: set KDF.Memory to 0, which Validate
	// rejects.
	rc, err := store.Get(ctx, configKey)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	raw, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var cfg RepoConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cfg.KDF.Memory = 0
	corrupt, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.Put(ctx, configKey, bytes.NewReader(corrupt)); err != nil {
		t.Fatalf("put corrupt: %v", err)
	}

	_, err = Open(ctx, store, []byte("hunter2"))
	if err == nil {
		t.Fatal("expected error from Open on corrupted KDF params, got nil")
	}
	if !strings.Contains(err.Error(), "Memory") {
		t.Fatalf("expected error mentioning Memory, got %v", err)
	}
}

// TestKeyOrErr_ReturnsDefensiveCopy: keyOrErr must return a copy of
// the live key (so callers holding the slice don't see the bytes
// zeroed by Close), not a slice header pointing into r.repoKey.
func TestKeyOrErr_ReturnsDefensiveCopy(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	captured, err := r.keyOrErr()
	if err != nil {
		t.Fatalf("keyOrErr: %v", err)
	}
	// Snapshot the captured copy so we can check that mutating r.repoKey
	// does NOT affect the captured slice.
	before := make([]byte, len(captured))
	copy(before, captured)

	// Now Close() — this zeroes r.repoKey in place. If keyOrErr had
	// returned a slice header into r.repoKey, the captured slice would
	// be 32 zeros now too.
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !bytes.Equal(before, captured) {
		t.Fatal("captured key changed after Close — keyOrErr aliased the live key")
	}
	// And the original repoKey is zeroed.
	if r.repoKey != nil {
		t.Fatalf("repoKey not nilled after Close")
	}
}

func TestRepo_AfterClose_ReturnsErrClosed(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	r.Close()
	// Methods that need the repo key must return ErrClosed once the
	// caller has called Close. We exercise this via the unexported
	// keyOrErr helper to keep the surface minimal until Phase 7 wires
	// the real CLI commands.
	if _, err := r.keyOrErr(); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v, want ErrClosed", err)
	}
}

// isAllZero reports whether b is non-nil and entirely zero bytes,
// or nil. Used by TestClose_ZeroesKey.
func isAllZero(b []byte) bool {
	if b == nil {
		return true
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
