package repo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/crypto"
)

// TestUseFastKDFForTests_InitRecordsValidCheapParams pins the contract the
// whole test suite leans on: after the hook, Init records the cheap
// params in the on-disk config (so Open, Passwd, and every other
// derivation reads them back and is cheap too), and those params still
// pass Validate — a repo that Init can write but Open refuses would
// break every test silently rather than speed it up.
func TestUseFastKDFForTests_InitRecordsValidCheapParams(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	r.Close()

	got := loadConfigForTest(t, store).KDF
	if err := got.Validate(); err != nil {
		t.Fatalf("recorded KDF params fail Validate: %v", err)
	}
	def := crypto.DefaultKDFParams()
	if got.Memory >= def.Memory || got.Time > def.Time {
		t.Fatalf("Init recorded %+v; want something cheaper than the default %+v", got, def)
	}
	if _, err := Open(ctx, store, []byte("hunter2")); err != nil {
		t.Fatalf("Open a fast-KDF repo: %v", err)
	}
}

// TestFastKDFParams_MustNotBeTheDefault guards the hook against silently
// becoming a no-op: a future edit that "tidies" the fast params back to
// the defaults would return the suite to multi-minute runs with no
// failing test to say why.
func TestFastKDFParams_MustNotBeTheDefault(t *testing.T) {
	if fastKDFParams == crypto.DefaultKDFParams() {
		t.Fatal("fastKDFParams equals DefaultKDFParams; the test hook does nothing")
	}
	if err := fastKDFParams.Validate(); err != nil {
		t.Fatalf("fastKDFParams fail Validate: %v", err)
	}
}

// loadConfigForTest reads meta/config back out of the store as the JSON
// Open would see, so a test can inspect what Init recorded.
func loadConfigForTest(t *testing.T, store blobstore.Store) RepoConfig {
	t.Helper()
	rc, err := store.Get(context.Background(), configKey)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer rc.Close()
	var cfg RepoConfig
	if err := json.NewDecoder(rc).Decode(&cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return cfg
}
