package repo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
)

// lockKey is the blobstore key used as an advisory mutex between
// CreateSnapshot, GC, and any other repo-mutating operation that
// can't safely overlap. We chose "meta/lock" rather than a snapshot
// of the repo state because:
//
//   - it's outside the data/ and snapshots/ key spaces, so the
//     existing prune/list logic ignores it,
//   - it's small (a few hundred bytes of diagnostic JSON), so
//     PUT/GET cost is negligible,
//   - if a process crashes mid-run the lock blob is left behind
//     and shows up in S3 listings — easy for an operator to spot
//     and delete by hand.
const lockKey = "meta/lock"

// lockReleaseTimeout bounds releaseLock's detached context (see the
// function comment). Generous for a GET + DELETE of a few hundred
// bytes; short enough that a cancelled operation on a hung endpoint
// does not hold the process open indefinitely.
const lockReleaseTimeout = 30 * time.Second

// ErrRepoLocked is returned by acquireLock when the lock blob is
// already taken. Callers can errors.Is against the sentinel; the
// returned error message also names the holder so the operator
// can debug who's holding it.
var ErrRepoLocked = errors.New("repo: another sentra operation is in progress")

// lockInfo is the JSON payload stored inside the lock blob. It's
// purely diagnostic — the dispatcher only cares whether the blob
// exists, not what it contains. Stored as plaintext JSON (not
// AEAD-sealed) because:
//   - The contents are not secret (host, pid, started_at).
//   - An operator inspecting the lock with `aws s3 cp` shouldn't
//     need the repo passphrase to read it.
//   - The acquire/release path runs on every snapshot — sealing
//     would add an Argon2 derivation per acquire, which we don't
//     pay for the manifests either.
type lockInfo struct {
	UUID      string    `json:"uuid"`
	Operation string    `json:"operation"`
	Host      string    `json:"host"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// acquireLock attempts to claim the repo-wide advisory lock against
// the supplied store. The op string is recorded in the lock blob so
// an operator inspecting a stale lock can tell which subsystem held
// it ("snapshot", "gc", "passwd", "sync", etc.).
//
// Returns the lockInfo it wrote on success, ErrRepoLocked when the
// lock is already taken, or the underlying transport error for
// anything else.
//
// Free function (not a method on *Repo) so callers like SyncTo can
// lock a destination store that does not yet have an open *Repo.
// The previous code carried two near-identical implementations —
// one method on *Repo, one free function in sync.go — that drifted
// in subtle ways (notably the release path's slog handling).
// Consolidating here keeps the lock-blob format and slog behavior
// in one place.
func acquireLock(ctx context.Context, store blobstore.Store, op string) (*lockInfo, error) {
	uuid, err := newLockUUID()
	if err != nil {
		return nil, fmt.Errorf("repo: generate lock uuid: %w", err)
	}
	host, _ := os.Hostname() // best-effort
	info := &lockInfo{
		UUID:      uuid,
		Operation: op,
		Host:      host,
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC(),
	}
	body, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("repo: marshal lock info: %w", err)
	}
	err = store.PutIfAbsent(ctx, lockKey, bytes.NewReader(body))
	if err != nil {
		if errors.Is(err, blobstore.ErrAlreadyExists) {
			// ErrAlreadyExists does not by itself mean someone else
			// holds the lock. A PutIfAbsent whose first attempt
			// committed but lost its response is retried by the S3
			// SDK (and by RetryStore); the retry sees the object we
			// just wrote and reports 412. Ownership is therefore
			// decided by reading the holder back: our own UUID in
			// the blob means the write went through and the acquire
			// succeeded. Treating it as a conflict would name this
			// process as its own blocker and strand a lock only it
			// could release.
			current, readErr := readLockInfo(ctx, store)
			if readErr == nil && current.UUID == uuid {
				return info, nil
			}
			// A read failure here is non-fatal — we still surface
			// ErrRepoLocked, just without the diagnostic suffix.
			return nil, fmt.Errorf("%w%s", ErrRepoLocked, formatLockHolder(current))
		}
		return nil, fmt.Errorf("repo: acquire lock: %w", err)
	}
	return info, nil
}

// releaseLock removes the lock blob from store if the blob's UUID
// still matches the one acquireLock wrote. A mismatch (someone
// else holds the lock) is logged at warn level but not surfaced as
// an error — the caller already finished its protected work and
// forcing a noisy error here would just obscure whatever the next
// operator sees.
//
// A NotFound on delete (the lock was already gone) is treated as
// success — same idempotency contract as DeleteSnapshot. Other
// transport errors are logged at warn level so an operator running
// with --log-level=info can spot a stuck lock that needs manual
// recovery.
//
// The release runs on a context detached from the caller's
// cancellation. Every lock holder releases from a defer, and the
// commonest reason to reach that defer early is that ctx was
// cancelled (TUI esc/quit, a signal, a parent op giving up). On the
// cancelled ctx the read-back below fails, the fail-closed rule
// declines to delete, and meta/lock is orphaned until an operator
// removes it by hand — every later backup reporting ErrRepoLocked
// against a process that no longer exists. Detaching keeps the
// fail-closed rule intact (an unreadable holder is still never
// deleted) while giving the release a real chance to run; the
// deadline bounds how long a cancelled operation lingers on a
// hung endpoint.
func releaseLock(ctx context.Context, store blobstore.Store, info *lockInfo) {
	if info == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lockReleaseTimeout)
	defer cancel()
	current := readLockHolder(ctx, store)
	if current == "" {
		// The current holder could not be read — either the lock is
		// already gone or (more dangerously) it is transiently
		// unreadable. We CANNOT confirm we still own it, so we must not
		// delete: deleting an unverifiable lock could stomp a lock that
		// another process legitimately re-acquired after ours was
		// cleared, silently breaking the mutual exclusion the lock
		// exists to provide. Leaving our own lock behind on a transient
		// read error is the safe failure — an operator sees a stale lock
		// (the documented manual-recovery path) instead. This is
		// fail-closed on purpose.
		slog.LogAttrs(ctx, slog.LevelWarn,
			"repo lock holder unreadable, not releasing",
			slog.String("our_uuid", info.UUID),
		)
		return
	}
	// readLockHolder returns " (held by ...)" — check our UUID is inside
	// before deleting. A mismatch means someone else holds the lock now
	// (e.g. after a manual stale-lock recovery); log and skip rather than
	// fail, since our protected work already finished.
	if !strings.Contains(current, info.UUID) {
		slog.LogAttrs(ctx, slog.LevelWarn,
			"repo lock changed under us, not releasing",
			slog.String("our_uuid", info.UUID),
			slog.String("found", current),
		)
		return
	}
	if err := store.Delete(ctx, lockKey); err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			return
		}
		slog.LogAttrs(ctx, slog.LevelWarn,
			"repo lock release failed",
			slog.String("uuid", info.UUID),
			slog.String("error", err.Error()),
		)
	}
}

// readLockInfo loads and decodes the current lock blob. Any failure —
// absent, unreadable, or undecodable — comes back as an error with a
// nil info, so callers that need to CONFIRM ownership (acquire's
// retry check, release's fail-closed check) cannot mistake a bad
// read for a known holder.
func readLockInfo(ctx context.Context, s blobstore.Store) (*lockInfo, error) {
	rc, err := s.Get(ctx, lockKey)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var info lockInfo
	if err := json.NewDecoder(rc).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode lock blob: %w", err)
	}
	return &info, nil
}

// formatLockHolder renders a decoded holder as the human-readable
// suffix tacked onto ErrRepoLocked, like " (held by host=foo pid=123
// op=gc since 2026-...)". A nil holder renders as "" so the caller
// still surfaces ErrRepoLocked, just without the diagnostic detail.
func formatLockHolder(info *lockInfo) string {
	if info == nil {
		return ""
	}
	return fmt.Sprintf(" (held by host=%q pid=%d op=%q since %s, uuid=%s)",
		info.Host, info.PID, info.Operation,
		info.StartedAt.Format(time.RFC3339),
		info.UUID)
}

// readLockHolder is readLockInfo + formatLockHolder: "" on any
// failure. releaseLock keys its fail-closed decision off that "".
func readLockHolder(ctx context.Context, s blobstore.Store) string {
	info, err := readLockInfo(ctx, s)
	if err != nil {
		return ""
	}
	return formatLockHolder(info)
}

// newLockUUID returns a 16-byte random hex string. Used for the
// lock owner's identity. We don't pull in a UUID library for this
// — the requirement is only "globally unique per process" and
// 16 random bytes from crypto/rand satisfies that with room to
// spare.
func newLockUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
