package repo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestAcquireLock_FreshSucceeds covers the simple case: no lock
// blob exists, acquireLock writes one and returns LockInfo.
func TestAcquireLock_FreshSucceeds(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	info, err := r.acquireLock(ctx, "test")
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	if info.UUID == "" {
		t.Error("LockInfo.UUID must be populated")
	}
	if info.Operation != "test" {
		t.Errorf("LockInfo.Operation: got %q, want %q", info.Operation, "test")
	}
	// Clean up so other tests in this file don't see a stale lock.
	r.releaseLock(ctx, info)
}

// TestAcquireLock_TakenReturnsErrRepoLocked is the central
// contract: a second acquireLock against a held lock fails with
// ErrRepoLocked, NOT a corruption / silent overwrite. The error
// message also includes diagnostic info naming the holder so an
// operator can debug stale locks without raw S3 access.
func TestAcquireLock_TakenReturnsErrRepoLocked(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	info1, err := r.acquireLock(ctx, "first")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer r.releaseLock(ctx, info1)

	_, err = r.acquireLock(ctx, "second")
	if !errors.Is(err, ErrRepoLocked) {
		t.Fatalf("second acquire: got %v, want ErrRepoLocked", err)
	}
	// Diagnostic suffix should mention the holder's operation.
	if !strings.Contains(err.Error(), "first") {
		t.Errorf("error message should include holder's operation %q: got %q",
			"first", err.Error())
	}
}

// TestReleaseLock_MismatchDoesNotDelete protects the recovery
// scenario: an operator manually deleted a stale lock and
// re-acquired it; the original (crashed) process must not later
// release somebody else's lock when it eventually runs cleanup.
//
// The behavior is "log and skip" rather than "error" — the original
// caller has already finished its protected work; it shouldn't fail
// just because the lock state diverged.
func TestReleaseLock_MismatchDoesNotDelete(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	original, err := r.acquireLock(ctx, "first")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// Simulate a stale-lock manual recovery: someone deleted the
	// lock blob out-of-band, then re-acquired with a new UUID.
	if err := r.store.Delete(ctx, lockKey); err != nil {
		t.Fatalf("manual delete: %v", err)
	}
	current, err := r.acquireLock(ctx, "second")
	if err != nil {
		t.Fatalf("second acquire after manual cleanup: %v", err)
	}
	defer r.releaseLock(ctx, current)

	// The original caller's release must NOT delete the new lock.
	r.releaseLock(ctx, original)

	// Verify the current lock blob is still there with the second
	// holder's UUID.
	rc, err := r.store.Get(ctx, lockKey)
	if err != nil {
		t.Fatalf("get lock: %v", err)
	}
	defer rc.Close()
	body := make([]byte, 1024)
	n, _ := rc.Read(body)
	got := string(body[:n])
	if !strings.Contains(got, current.UUID) {
		t.Errorf("lock blob should hold the second holder's UUID %q: %s",
			current.UUID, got)
	}
	if strings.Contains(got, original.UUID) {
		t.Errorf("releaseLock stomped on someone else's lock; original UUID still present: %s", got)
	}
}

// TestCreateSnapshot_AndGC_Mutex is the integration test that
// proves the architectural-review GC concurrency fix. We start a
// fake "long-running" GC by acquiring the lock manually, then try
// to CreateSnapshot in another goroutine. CreateSnapshot must fail
// with ErrRepoLocked rather than racing with the held lock.
func TestCreateSnapshot_AndGC_Mutex(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	root := t.TempDir()
	if err := putFile(root, "a.txt", "x"); err != nil {
		t.Fatal(err)
	}

	// Hold the lock manually to simulate a GC in progress.
	lock, err := r.acquireLock(ctx, "gc")
	if err != nil {
		t.Fatalf("manual acquire: %v", err)
	}

	// CreateSnapshot must fail-fast with ErrRepoLocked.
	_, err = r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if !errors.Is(err, ErrRepoLocked) {
		t.Fatalf("CreateSnapshot during GC: got %v, want ErrRepoLocked", err)
	}

	// Release and re-try; this time CreateSnapshot should succeed.
	r.releaseLock(ctx, lock)
	if _, err := r.CreateSnapshot(ctx, root, SnapshotOptions{}); err != nil {
		t.Fatalf("CreateSnapshot after release: %v", err)
	}
}

// TestAcquireLock_ConcurrentExactlyOneWins is the strict mutex
// invariant — N goroutines all try to acquire the same lock from
// a sync.Barrier-released start; exactly one succeeds, the rest
// see ErrRepoLocked. No scheduler timing needed: every winning
// acquirer holds the lock until the test cleanup, so siblings
// observe the contended state regardless of race-finish order.
//
// (The earlier "5-way concurrent GC" version of this test was
// flaky on fast machines because a no-op GC could finish before
// the next goroutine even tried to acquire — the lock was already
// re-released by then. Targeting the lock primitive directly
// removes that timing dependency.)
func TestAcquireLock_ConcurrentExactlyOneWins(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	successes := atomic.Int32{}
	conflicts := atomic.Int32{}
	winners := make(chan *LockInfo, workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start // synchronized release
			info, err := r.acquireLock(ctx, "stress")
			switch {
			case err == nil:
				successes.Add(1)
				winners <- info // hold for cleanup
			case errors.Is(err, ErrRepoLocked):
				conflicts.Add(1)
			default:
				t.Errorf("worker %d: unexpected error %v", i, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(winners)
	// Release any winning lock so other tests don't see a stale one.
	for info := range winners {
		r.releaseLock(ctx, info)
	}

	if got := successes.Load(); got != 1 {
		t.Errorf("successes: got %d, want exactly 1", got)
	}
	if got := conflicts.Load(); got != workers-1 {
		t.Errorf("conflicts: got %d, want %d", got, workers-1)
	}
}

// TestCreateSnapshot_ReleasesLockOnError confirms the lock is not
// orphaned when CreateSnapshot fails after acquiring it. We trigger
// an error by passing a path that doesn't exist (filepath.Abs
// succeeds, but the walker fails).
func TestCreateSnapshot_ReleasesLockOnError(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	// A relative-but-impossible path the walker will reject.
	missing := filepath.Join(t.TempDir(), "definitely-not-here")
	_, err := r.CreateSnapshot(ctx, missing, SnapshotOptions{})
	if err == nil {
		t.Fatal("expected error for nonexistent root, got nil")
	}

	// Lock blob should be absent — releaseLock ran on the deferred
	// path even though CreateSnapshot returned an error.
	if _, err := store.Stat(ctx, lockKey); err == nil {
		t.Error("lock blob still exists after CreateSnapshot error; defer release didn't fire")
	}
}
