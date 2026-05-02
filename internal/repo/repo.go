package repo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/crypto"
)

// ErrAlreadyInitialized is returned by Init when a config blob already
// exists in the store. Callers should refuse to overwrite an existing
// repo — destroying the wrapped repo key would render every blob in
// the store undecryptable.
var ErrAlreadyInitialized = errors.New("repo: already initialized")

// ErrWrongPassphrase is returned by Open when the passphrase fails to
// unwrap the repo key. We intentionally surface a single sentinel
// rather than the underlying AES-GCM auth-tag error so callers can't
// distinguish "wrong passphrase" from "config tampered with" via
// timing or message text.
var ErrWrongPassphrase = errors.New("repo: wrong passphrase")

// ErrClosed is returned by methods on a *Repo whose Close has been
// called. The repo key has been zeroed and is no longer usable; the
// caller must re-Open to do more work.
var ErrClosed = errors.New("repo: closed")

// Repo is an opened sentra repository. It holds the (in-memory only)
// repo key plus the parsed config. *Repo is safe for concurrent use
// by multiple goroutines for snapshot operations; Close is also safe
// to call concurrently with any in-flight operation, though the
// next call after Close returns ErrClosed.
type Repo struct {
	store blobstore.Store
	cfg   RepoConfig

	// keyMu guards repoKey from racing with Close. Reads use RLock;
	// Close takes the write lock and zeroizes the slice in place
	// before nilling it, so a concurrent reader either sees the live
	// key or sees nil (handled by keyOrErr).
	keyMu   sync.RWMutex
	repoKey []byte // 32 bytes; nil after Close

	// closeOnce makes Close idempotent and concurrency-safe.
	closeOnce sync.Once
}

// Init creates a fresh repository in s using passphrase. It refuses to
// overwrite an existing config — the caller must use Open on an
// already-initialized store.
//
// The returned *Repo is ready to create snapshots; the caller owns it
// and must Close when done to zero the in-memory key.
func Init(ctx context.Context, s blobstore.Store, passphrase []byte) (*Repo, error) {
	// Refuse to clobber an existing repo: even if the user has the
	// right passphrase, regenerating the salt would change the KEK
	// and orphan the wrapped repo key (i.e. all stored blobs become
	// undecryptable). The right move on a stale store is Open.
	if _, err := s.Stat(ctx, configKey); err == nil {
		return nil, ErrAlreadyInitialized
	} else if !errors.Is(err, blobstore.ErrNotFound) {
		return nil, fmt.Errorf("repo: stat config: %w", err)
	}

	salt, err := crypto.GenerateSalt()
	if err != nil {
		return nil, fmt.Errorf("repo: salt: %w", err)
	}
	repoKey, err := crypto.GenerateRepoKey()
	if err != nil {
		return nil, fmt.Errorf("repo: repo key: %w", err)
	}

	kdf := crypto.DefaultKDFParams()
	kek := crypto.DeriveKEK(passphrase, salt, kdf)
	wrapped, err := crypto.Seal(kek, repoKey)
	if err != nil {
		return nil, fmt.Errorf("repo: wrap key: %w", err)
	}

	id, err := newRepoID()
	if err != nil {
		return nil, fmt.Errorf("repo: id: %w", err)
	}

	cfg := RepoConfig{
		Version:        RepoConfigVersion,
		ID:             id,
		KDF:            kdf,
		Salt:           salt,
		WrappedRepoKey: wrapped,
		CreatedAt:      time.Now().UTC(),
	}
	raw, err := json.Marshal(&cfg)
	if err != nil {
		return nil, fmt.Errorf("repo: marshal config: %w", err)
	}
	if err := s.Put(ctx, configKey, bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("repo: put config: %w", err)
	}

	return &Repo{store: s, cfg: cfg, repoKey: repoKey}, nil
}

// Open loads the repository config from s and unwraps the repo key
// using passphrase. It returns ErrWrongPassphrase if the unwrap fails
// (intentionally collapsing all crypto auth errors to a single
// sentinel) and surfaces validation errors from KDFParams.Validate so
// a corrupted on-disk config does not trigger an Argon2 OOM.
func Open(ctx context.Context, s blobstore.Store, passphrase []byte) (*Repo, error) {
	rc, err := s.Get(ctx, configKey)
	if err != nil {
		return nil, fmt.Errorf("repo: get config: %w", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("repo: read config: %w", err)
	}
	var cfg RepoConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("repo: unmarshal config: %w", err)
	}

	// Phase 1 review carry-over #2: validate KDF params from disk
	// before feeding them to Argon2id. A corrupted Memory field
	// would otherwise OOM the process before we ever get to the
	// passphrase check.
	if err := cfg.KDF.Validate(); err != nil {
		return nil, fmt.Errorf("repo: invalid KDF params: %w", err)
	}

	kek := crypto.DeriveKEK(passphrase, cfg.Salt, cfg.KDF)
	repoKey, err := crypto.Open(kek, cfg.WrappedRepoKey)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	if len(repoKey) != crypto.RepoKeyLen {
		return nil, fmt.Errorf("repo: unwrapped key has wrong length %d", len(repoKey))
	}

	return &Repo{store: s, cfg: cfg, repoKey: repoKey}, nil
}

// Close zeroes the in-memory repo key in place and releases it. It is
// idempotent and safe to call concurrently. After Close, methods
// requiring the repo key return ErrClosed.
//
// Best-effort defense in depth: Go's GC will eventually reclaim the
// key memory, but a heap dump or core file taken between use and GC
// could otherwise leak the key. Zeroing on demand collapses that
// window to "between Close and next allocation."
func (r *Repo) Close() error {
	r.closeOnce.Do(func() {
		r.keyMu.Lock()
		defer r.keyMu.Unlock()
		if r.repoKey != nil {
			for i := range r.repoKey {
				r.repoKey[i] = 0
			}
			r.repoKey = nil
		}
	})
	return nil
}

// Config returns a copy of the repo's parsed config. Useful for tests
// and for debug/info commands that want to surface the repo ID or
// CreatedAt without needing the raw JSON.
func (r *Repo) Config() RepoConfig { return r.cfg }

// Store returns the underlying blobstore. Exposed primarily for tests
// and for the Phase 7 CLI to share the connection with other
// subsystems (e.g. the agent's orphan-blob check).
func (r *Repo) Store() blobstore.Store { return r.store }

// keyOrErr returns the repo key under read-lock or ErrClosed if Close
// has been called. Internal helper for snapshot/restore code paths.
//
// The returned slice is the live key; do not mutate it. Callers must
// not retain it past the function call to avoid racing with a
// concurrent Close.
func (r *Repo) keyOrErr() ([]byte, error) {
	r.keyMu.RLock()
	defer r.keyMu.RUnlock()
	if r.repoKey == nil {
		return nil, ErrClosed
	}
	return r.repoKey, nil
}

// newRepoID returns a random hex string used as the repo's stable
// identifier. 16 hex chars = 64 bits of entropy, more than enough to
// disambiguate repos in logs without bloating the config.
func newRepoID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "repo-" + hex.EncodeToString(b[:]), nil
}
