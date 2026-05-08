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
)

// TestInit_WritesConfigMAC confirms the new write path actually
// embeds a MAC. Without this, any "Open verifies MAC" test would
// be vacuous on freshly-initialized repos that happened to skip
// the signing step.
func TestInit_WritesConfigMAC(t *testing.T) {
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
	raw, _ := io.ReadAll(rc)
	var cfg RepoConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.MAC) == 0 {
		t.Fatal("Init did not write a MAC; cfg.MAC is empty")
	}
	if len(cfg.MAC) != 32 {
		t.Errorf("MAC length: got %d, want 32 (HMAC-SHA256)", len(cfg.MAC))
	}
}

// TestOpen_RejectsTamperedKDFParams is the headline security
// property: an attacker with bucket-write access who downgrades
// KDF.Memory or KDF.Time to make brute-force trivial must NOT be
// able to fool Open. We tamper with KDF.Memory (4096 KiB — the
// floor that passes Validate, but is 16x weaker than the 64 MiB
// default) and assert Open rejects.
//
// Two valid rejection paths exist:
//   - Wrap-shadow: the modified KDF derives a different KEK so
//     the wrapped-repo-key unwrap fails with ErrWrongPassphrase
//     before the MAC check runs. Still secure — Open refuses.
//   - MAC failure: if KDF tampering somehow yielded the same KEK
//     (impossible with sound KDFs but worth covering), the MAC
//     check fires and surfaces ErrConfigTampered.
//
// The test accepts either — both prove the same property: the
// passphrase-correct path does NOT silently accept a weakened KDF.
func TestOpen_RejectsTamperedKDFParams(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()

	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	r.Close()

	// Load, tamper, re-marshal (preserving the original MAC bytes
	// — the attacker can't recompute the MAC without the auth key).
	rc, _ := store.Get(ctx, configKey)
	raw, _ := io.ReadAll(rc)
	rc.Close()
	var cfg RepoConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	originalMemory := cfg.KDF.Memory
	cfg.KDF.Memory = 4096 // floor; passes Validate but is much weaker
	tampered, _ := json.Marshal(&cfg)
	if err := store.Put(ctx, configKey, bytes.NewReader(tampered)); err != nil {
		t.Fatalf("put: %v", err)
	}

	_, err = Open(ctx, store, []byte("hunter2"))
	if err == nil {
		t.Fatalf("Open accepted tampered KDF (Memory: %d → %d)", originalMemory, cfg.KDF.Memory)
	}
	if !errors.Is(err, ErrWrongPassphrase) && !errors.Is(err, ErrConfigTampered) {
		t.Errorf("Open with tampered KDF: got %v, want ErrWrongPassphrase or ErrConfigTampered", err)
	}
}

// TestOpen_RejectsTamperedID exclusively exercises the MAC path:
// changing the ID field doesn't affect KEK derivation (the KEK
// only depends on passphrase + Salt + KDF), so the wrapped key
// unwraps correctly. Only the MAC catches the tamper.
//
// This is the test that proves the MAC layer is materially
// useful — without the MAC, an attacker could rename a victim's
// repo ID in their bucket logs to attribute backups to a
// different account, and Open would accept it silently.
func TestOpen_RejectsTamperedID(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
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
	original := cfg.ID
	cfg.ID = "repo-attackercontrolled"
	tampered, _ := json.Marshal(&cfg)
	if err := store.Put(ctx, configKey, bytes.NewReader(tampered)); err != nil {
		t.Fatalf("put: %v", err)
	}

	_, err = Open(ctx, store, []byte("hunter2"))
	if !errors.Is(err, ErrConfigTampered) {
		t.Fatalf("Open with tampered ID: got %v, want ErrConfigTampered (was %q)", err, original)
	}
}

// TestOpen_RejectsTamperedSalt covers the same property for the
// salt field — flipping the salt would force the operator to
// re-derive (and re-brute-force) under whatever salt the attacker
// chose. The MAC must catch this.
func TestOpen_RejectsTamperedSalt(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	r.Close()

	rc, err := store.Get(ctx, configKey)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	raw, _ := io.ReadAll(rc)
	rc.Close()
	var cfg RepoConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Flip every byte of the salt.
	for i := range cfg.Salt {
		cfg.Salt[i] ^= 0xff
	}
	tampered, _ := json.Marshal(&cfg)
	if err := store.Put(ctx, configKey, bytes.NewReader(tampered)); err != nil {
		t.Fatalf("put: %v", err)
	}

	// With a tampered salt the KEK will be wrong, so unwrap fails
	// FIRST with ErrWrongPassphrase. That's actually correct — we
	// don't even get to the MAC check, but the tampering is still
	// detected. The contract is: Open never accepts a tampered
	// config with the original passphrase.
	_, err = Open(ctx, store, []byte("hunter2"))
	if err == nil {
		t.Fatal("Open accepted tampered salt")
	}
	// Either ErrWrongPassphrase (kek changed → unwrap fails) or
	// ErrConfigTampered (somehow the kek still works) — both
	// indicate Open didn't silently use the tampered config.
	if !errors.Is(err, ErrWrongPassphrase) && !errors.Is(err, ErrConfigTampered) {
		t.Errorf("Open with tampered salt: got %v, want ErrWrongPassphrase or ErrConfigTampered", err)
	}
}

// TestOpen_AcceptsLegacyConfigWithWarning covers the migration
// path: a config blob written by a pre-MAC build (no MAC field)
// must still Open. We simulate this by Init'ing then stripping
// the MAC and re-writing.
func TestOpen_AcceptsLegacyConfigWithWarning(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()

	r, err := Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	r.Close()

	// Simulate a legacy repo: read the config, strip the MAC,
	// re-marshal, re-write.
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
		t.Fatalf("put: %v", err)
	}

	// Confirm the on-disk JSON really has no MAC field (omitempty
	// drops it from the output).
	if strings.Contains(string(legacy), `"mac"`) {
		t.Errorf("legacy config should omit MAC field; got %s", legacy)
	}

	// Open must succeed.
	r2, err := Open(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("Open legacy config: %v", err)
	}
	r2.Close()
}

// TestOpen_RejectsCorruptMAC: the MAC is present but doesn't
// match. This is the explicit ErrConfigTampered path we want a
// dedicated assertion on (TestOpen_RejectsTamperedKDFParams uses
// a tampered struct field; this one isolates the MAC bytes).
func TestOpen_RejectsCorruptMAC(t *testing.T) {
	ctx := context.Background()
	store := blobstore.NewMemory()
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
	// Flip one byte of the MAC. Even a single-bit corruption must
	// be detected by HMAC-SHA256.
	cfg.MAC[0] ^= 0x01
	tampered, _ := json.Marshal(&cfg)
	if err := store.Put(ctx, configKey, bytes.NewReader(tampered)); err != nil {
		t.Fatalf("put: %v", err)
	}

	_, err = Open(ctx, store, []byte("hunter2"))
	if !errors.Is(err, ErrConfigTampered) {
		t.Fatalf("Open with flipped MAC byte: got %v, want ErrConfigTampered", err)
	}
}
