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
	"log/slog"
	"runtime"
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
// rather than the underlying AEAD auth-tag error so callers can't
// distinguish "wrong passphrase" from "config tampered with" via
// timing or message text.
var ErrWrongPassphrase = errors.New("repo: wrong passphrase")

// ErrConfigTampered is returned by Open when the config blob's MAC
// is present but doesn't match what we'd compute under the
// passphrase-derived auth key. This is a strong signal that
// somebody (or something) modified the on-disk config bytes
// after the original write — typically the on-bucket attacker
// scenario the MAC was added to defend against.
//
// Distinct from ErrWrongPassphrase: the passphrase already unwrapped
// the repo key successfully, but the surrounding config metadata
// (KDF params, salt, etc.) doesn't match what was originally signed.
// The two sentinels are intentionally separate so an operator
// triaging an Open failure knows which class of attack they're
// looking at.
var ErrConfigTampered = errors.New("repo: config MAC verification failed (possible tampering)")

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
	if err := signConfig(&cfg, kek); err != nil {
		return nil, fmt.Errorf("repo: sign config: %w", err)
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

// signConfig computes the config's MAC under a sub-key derived
// from kek and writes it back into cfg.MAC. The canonical bytes
// the MAC covers are the JSON-encoded cfg with MAC explicitly
// nil, so verification on Open re-marshals into the same bytes.
func signConfig(cfg *RepoConfig, kek []byte) error {
	authKey, err := crypto.DeriveSubKey(kek, configMACInfo)
	if err != nil {
		return err
	}
	canonical, err := canonicalConfigBytes(cfg)
	if err != nil {
		return err
	}
	cfg.MAC = crypto.HMACSHA256(authKey, canonical)
	return nil
}

// canonicalConfigBytes returns the deterministic JSON encoding of
// cfg with MAC field explicitly nil. The MAC itself MUST not be
// part of the bytes the MAC covers — otherwise verifying the MAC
// would require knowing the MAC, which is circular.
//
// We marshal a struct copy with MAC zeroed rather than a separate
// "MACless" type so a future field addition to RepoConfig is
// automatically covered without forgetting to update this helper.
func canonicalConfigBytes(cfg *RepoConfig) ([]byte, error) {
	cp := *cfg
	cp.MAC = nil
	return json.Marshal(&cp)
}

// Open loads the repository config from s and unwraps the repo key
// using passphrase. It returns ErrWrongPassphrase if the unwrap fails
// (intentionally collapsing all AEAD auth errors to a single sentinel)
// and surfaces validation errors from KDFParams.Validate so
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

	// Validate KDF params from disk before feeding them to Argon2id.
	// A corrupted Memory field would otherwise OOM the process before
	// we ever get to the passphrase check.
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

	// Verify the config MAC under the sub-key derived from kek.
	// Mismatch means either:
	//   - the on-disk config was tampered with (e.g. an operator
	//     with bucket write access downgraded KDF.Memory), OR
	//   - the file format / canonicalization changed in this build
	//     vs the build that wrote the config.
	// Either case is alarming, so we surface ErrConfigTampered.
	//
	// Empty MAC = legacy repo. We log a warning and proceed; an
	// upcoming `sentra passwd` will rewrite the config with a MAC.
	if err := verifyConfig(&cfg, kek); err != nil {
		return nil, err
	}

	return &Repo{store: s, cfg: cfg, repoKey: repoKey}, nil
}

// verifyConfig validates the config's MAC. Returns nil on success
// or a legacy (no-MAC) config; ErrConfigTampered when the MAC is
// present but wrong; or an error from the sub-key derivation.
func verifyConfig(cfg *RepoConfig, kek []byte) error {
	if len(cfg.MAC) == 0 {
		// Legacy repo: written before the MAC field existed. We
		// trust the contents because the caller's passphrase just
		// successfully unwrapped the repo key — that itself is a
		// strong signal the wrapped key wasn't tampered with. The
		// KDF params COULD have been weakened, though, so log a
		// warning so an operator running with --log-level=info
		// sees the upgrade prompt.
		slog.Warn("repo config has no MAC (legacy repo); a future sentra passwd will add one")
		return nil
	}
	authKey, err := crypto.DeriveSubKey(kek, configMACInfo)
	if err != nil {
		return fmt.Errorf("repo: derive auth key: %w", err)
	}
	canonical, err := canonicalConfigBytes(cfg)
	if err != nil {
		return fmt.Errorf("repo: canonical config: %w", err)
	}
	if !crypto.VerifyHMACSHA256(authKey, canonical, cfg.MAC) {
		return ErrConfigTampered
	}
	return nil
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
			crypto.Zeroize(r.repoKey)
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
// and for the CLI to share the connection with other subsystems
// (e.g. the agent's orphan-blob check).
func (r *Repo) Store() blobstore.Store { return r.store }

// keyOrErr returns a *defensive copy* of the repo key, or ErrClosed
// if Close has been called. Callers must zeroize the returned copy
// when done — see crypto.Zeroize().
//
// Returning the live slice header was unsafe: CreateSnapshot fans
// out concurrent Seal calls that capture the slice. If Close ran
// while a goroutine still held the captured slice, the backing
// array was zeroed in place — but the captured slice still pointed
// at it, now 32 zero bytes. crypto.Seal happily encrypts under that
// all-zero key, producing chunks that are silently un-decryptable
// with the real key (and trivially decryptable for anyone with the
// all-zero key — i.e. anyone). The fix is to hand each caller its
// own copy and let them zeroize after the operation completes. The
// lock is still needed to read the live slice atomically with
// respect to Close.
func (r *Repo) keyOrErr() ([]byte, error) {
	r.keyMu.RLock()
	defer r.keyMu.RUnlock()
	if r.repoKey == nil {
		return nil, ErrClosed
	}
	cp := make([]byte, len(r.repoKey))
	copy(cp, r.repoKey)
	return cp, nil
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

// resolveConcurrency normalizes a user-supplied concurrency cap to a
// usable goroutine limit. Zero means "use GOMAXPROCS" (the default
// for Restore, SyncTo, and any future fan-out path). Negative values
// are clamped to 1 because errgroup.SetLimit(0) would block forever
// and SetLimit with a negative value is documented as "no limit",
// which is almost certainly NOT what the caller intended when they
// asked for negative concurrency.
//
// Centralized here so every fan-out caller agrees on the semantics —
// when the policy changes (e.g. a future "auto-throttle when the
// store reports rate-limit headers" knob), there's a single place
// to thread it through.
func resolveConcurrency(n int) int {
	switch {
	case n == 0:
		return runtime.GOMAXPROCS(0)
	case n < 1:
		return 1
	}
	return n
}
