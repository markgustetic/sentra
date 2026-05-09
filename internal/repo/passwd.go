package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/markgustetic/sentra/internal/crypto"
)

// ErrSamePassphrase is returned by Passwd when the new passphrase
// matches the one currently in use. Catching this at the repo
// layer (in addition to the CLI's pre-call check) makes Passwd
// safe for any future caller that skips the CLI's own check.
//
// Distinct sentinel so tests can errors.Is against it; the message
// is also operator-readable.
var ErrSamePassphrase = errors.New("repo: new passphrase matches old (nothing to rotate)")

// Passwd rotates the wrapping passphrase. The repo key (which seals
// every chunk, manifest, and the snapshot index) is unchanged; only
// the on-disk `config` blob is rewritten with:
//
//   - a fresh salt (decision Q6: rotate-on-every-passwd, defensive
//     hygiene — see docs/plans/2026-05-08-passwd-design.md),
//   - the repo key re-wrapped under a KEK derived from newPassphrase
//     and the new salt under the existing KDF parameters,
//   - a fresh MAC computed via crypto/mac.go's HKDF-derived auth
//     sub-key under the new KEK.
//
// All existing snapshots remain readable because the repo key
// itself is unchanged. After Passwd succeeds, the old passphrase
// no longer Opens the repo and the new passphrase does.
//
// Concurrency: Passwd takes the same advisory lock as
// CreateSnapshot and GC (decision Q4: shared meta/lock). A
// concurrent backup or GC blocks Passwd with ErrRepoLocked. The
// converse — Passwd blocking concurrent backup — is also
// enforced because backup acquires the same lock.
//
// Caller contract: the repo must already be Open with the OLD
// passphrase. Passwd does NOT take the old passphrase as an
// argument — the operator's authentication happened during Open.
// This shape mirrors how POSIX `passwd(1)` works (it asks for
// the current password to authenticate, then asks for the new
// one) but the authentication step is hoisted up to the CLI's
// existing passphrase-resolution chain.
//
// Errors:
//   - ErrClosed: repo has been Close'd.
//   - ErrSamePassphrase: newPassphrase equals the in-use one.
//   - ErrRepoLocked: another sentra operation holds meta/lock.
//   - any underlying store / crypto error from the rewrite path.
//
// Atomicity: the on-disk rewrite is a single Put against the
// `config` key, which is atomic at the S3 level. A network failure
// mid-Put either leaves the old config intact (operator re-runs
// with the old passphrase) or replaces it with the new one
// (operator re-runs with the new passphrase). No half-state.
func (r *Repo) Passwd(ctx context.Context, newPassphrase []byte) error {
	// keyOrErr returns ErrClosed when the repo is closed, and a
	// defensive copy of the in-memory repo key otherwise. We MUST
	// zero the copy on every exit path so a goroutine peer that
	// happens to see this stack via core dump or live memory
	// acquisition only sees zeros.
	repoKey, err := r.keyOrErr()
	if err != nil {
		return err
	}
	defer crypto.Zeroize(repoKey)

	// Sanity refusal: rotating to the same passphrase is a no-op
	// the operator almost certainly didn't mean. The check happens
	// BEFORE the lock acquisition so a same-passphrase mistake
	// never blocks unrelated repo operations on the lock.
	//
	// The check uses the existing salt+KDF to re-derive what the
	// caller's old KEK would have been, and tries to unwrap with a
	// KEK derived from newPassphrase under the existing salt. If
	// that succeeds, newPassphrase IS the current one.
	if currentlySamePassphrase(newPassphrase, &r.cfg, repoKey) {
		return ErrSamePassphrase
	}

	// Acquire the repo-wide advisory lock so no concurrent backup,
	// GC, or peer Passwd can race with the rewrite. Local var is
	// `heldLock` (not `lockInfo`) because `lockInfo` is now the
	// unexported type name in this package; reusing it as a local
	// would shadow the type.
	heldLock, err := acquireLock(ctx, r.store, "passwd")
	if err != nil {
		return err
	}
	defer releaseLock(ctx, r.store, heldLock)

	// Generate a fresh salt and derive a new KEK. Salt rotation
	// makes any pre-existing precomputation against the old salt
	// useless against the new wrapped key — it's free and
	// defensible at audit time.
	newSalt, err := crypto.GenerateSalt()
	if err != nil {
		return fmt.Errorf("repo: salt: %w", err)
	}
	newKEK := crypto.DeriveKEK(newPassphrase, newSalt, r.cfg.KDF)

	// Re-wrap the repo key under the new KEK. The repo key itself
	// is unchanged; only its envelope is rewritten.
	newWrapped, err := crypto.Seal(newKEK, repoKey)
	if err != nil {
		return fmt.Errorf("repo: wrap key: %w", err)
	}

	// Build the new config. Note what's preserved (Version, ID,
	// KDF, CreatedAt) vs rotated (Salt, WrappedRepoKey, MAC).
	newCfg := r.cfg
	newCfg.Salt = newSalt
	newCfg.WrappedRepoKey = newWrapped
	newCfg.MAC = nil // signConfig will populate; clear so a stale value can't slip through
	if err := signConfig(&newCfg, newKEK); err != nil {
		return fmt.Errorf("repo: sign config: %w", err)
	}

	// Marshal and write. S3 Put is atomic at the blob level so
	// the on-disk transition is all-or-nothing.
	raw, err := json.Marshal(&newCfg)
	if err != nil {
		return fmt.Errorf("repo: marshal config: %w", err)
	}
	if err := r.store.Put(ctx, configKey, bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("repo: put config: %w", err)
	}

	// Update the in-memory copy so a subsequent operation in the
	// same process sees the post-rotation config (no current
	// methods re-read it from the store, but a future caller that
	// inspects r.Config() after Passwd should see the new salt /
	// wrap / MAC, not the pre-rotation values).
	r.cfg = newCfg
	return nil
}

// currentlySamePassphrase reports whether newPassphrase, derived
// under the cfg's existing salt + KDF, produces a KEK that
// successfully unwraps the cfg's WrappedRepoKey to the same
// repoKey already in memory. That's the cleanest way to check
// "is this passphrase the current one" without storing the
// passphrase plaintext anywhere.
//
// We don't lift this into the public crypto package — it's
// repo-specific (it knows about cfg's Salt + KDF + WrappedRepoKey
// shape) and only used by Passwd.
func currentlySamePassphrase(newPassphrase []byte, cfg *RepoConfig, repoKey []byte) bool {
	candidateKEK := crypto.DeriveKEK(newPassphrase, cfg.Salt, cfg.KDF)
	candidateRepoKey, err := crypto.Open(candidateKEK, cfg.WrappedRepoKey)
	if err != nil {
		// New passphrase doesn't unwrap; definitely different.
		return false
	}
	defer crypto.Zeroize(candidateRepoKey)
	return bytes.Equal(candidateRepoKey, repoKey)
}
