package blobstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMemory_PutGet(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if err := s.Put(ctx, "k", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	rc, err := s.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("got %q", got)
	}
}

func TestMemory_GetMissing(t *testing.T) {
	s := NewMemory()
	_, err := s.Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestMemory_List(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if err := s.Put(ctx, "a/1", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "a/2", strings.NewReader("yy")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "b/1", strings.NewReader("zzz")); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(ctx, "a/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	// Keys must be sorted for stable output.
	if got[0].Key != "a/1" || got[1].Key != "a/2" {
		t.Fatalf("want sorted [a/1 a/2], got %+v", got)
	}
	// Sizes must reflect what was written.
	if got[0].Size != 1 || got[1].Size != 2 {
		t.Fatalf("sizes wrong: %+v", got)
	}
}

func TestMemory_Delete(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if err := s.Put(ctx, "k", strings.NewReader("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete")
	}
}

func TestMemory_Stat(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if err := s.Put(ctx, "k", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Stat(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "k" {
		t.Errorf("Key: got %q want %q", got.Key, "k")
	}
	if got.Size != 5 {
		t.Errorf("Size: got %d want %d", got.Size, 5)
	}
}

func TestMemory_StatMissingKey(t *testing.T) {
	s := NewMemory()
	if _, err := s.Stat(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestMemory_List_TrailingSlashSemantics locks in the byte-prefix
// match contract documented on Store.List: "data/" must match
// "data/foo" but not "dataX". The S3 implementation has to mirror
// these semantics; if either impl drifts, this test catches it.
func TestMemory_List_TrailingSlashSemantics(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	for _, k := range []string{"data/a", "data/b", "dataX/c"} {
		if err := s.Put(ctx, k, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.List(ctx, "data/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("List(\"data/\") want 2, got %d: %+v", len(got), got)
	}
	for _, info := range got {
		if !strings.HasPrefix(info.Key, "data/") {
			t.Errorf("unexpected key in result: %q", info.Key)
		}
	}
}

func TestMemory_DeleteMissing(t *testing.T) {
	s := NewMemory()
	if err := s.Delete(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestMemory_BatchDelete_HappyPath confirms that BatchDelete returns
// the count of removed objects and that each key is gone afterwards.
func TestMemory_BatchDelete_HappyPath(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	for _, k := range []string{"a", "b", "c", "keep"} {
		if err := s.Put(ctx, k, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := s.BatchDelete(ctx, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("BatchDelete: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted: got %d, want 3", deleted)
	}
	// Removed keys must not be present.
	for _, k := range []string{"a", "b", "c"} {
		if _, err := s.Stat(ctx, k); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q still present after BatchDelete: %v", k, err)
		}
	}
	// Untouched key must still be present.
	if _, err := s.Stat(ctx, "keep"); err != nil {
		t.Errorf("\"keep\" should still exist: %v", err)
	}
}

// TestMemory_BatchDelete_TolerantOfMissingKeys: BatchDelete is
// idempotent — passing already-missing keys mixed with existing
// ones is not an error. The deleted count is the total of keys
// the store confirms absent after the call (matching S3's
// DeleteObjects semantic — missing keys count toward the total
// because they ARE absent after the call, vacuously).
func TestMemory_BatchDelete_TolerantOfMissingKeys(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if err := s.Put(ctx, "exists", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	keys := []string{"exists", "missing-1", "missing-2"}
	deleted, err := s.BatchDelete(ctx, keys)
	if err != nil {
		t.Fatalf("BatchDelete: %v", err)
	}
	if deleted != len(keys) {
		t.Errorf("deleted: got %d, want %d (idempotent semantic: every input key counts)",
			deleted, len(keys))
	}
	// "exists" must actually be gone (delete actually deleted, not
	// just counted).
	if _, err := s.Stat(ctx, "exists"); !errors.Is(err, ErrNotFound) {
		t.Errorf("\"exists\" key still present after BatchDelete: %v", err)
	}
}

// TestMemory_BatchDelete_EmptyInput is a trivial case — passing zero
// keys must not error and must return zero. Saves callers from
// having to special-case empty slices.
func TestMemory_BatchDelete_EmptyInput(t *testing.T) {
	s := NewMemory()
	deleted, err := s.BatchDelete(context.Background(), nil)
	if err != nil {
		t.Fatalf("BatchDelete: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted: got %d, want 0", deleted)
	}
}

// TestMemory_PutIfAbsent_NewKey writes to a fresh key and observes
// the bytes on the read path — proves PutIfAbsent stores the body
// when the key was absent.
func TestMemory_PutIfAbsent_NewKey(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if err := s.PutIfAbsent(ctx, "lock", strings.NewReader("payload")); err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	rc, err := s.Get(ctx, "lock")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, []byte("payload")) {
		t.Errorf("body: got %q, want %q", got, "payload")
	}
}

// TestMemory_PutIfAbsent_ConflictReturnsSentinel covers the
// race-loser path: a second PutIfAbsent at the same key returns
// ErrAlreadyExists (and does NOT overwrite the existing body).
func TestMemory_PutIfAbsent_ConflictReturnsSentinel(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if err := s.Put(ctx, "lock", strings.NewReader("first")); err != nil {
		t.Fatal(err)
	}
	err := s.PutIfAbsent(ctx, "lock", strings.NewReader("second"))
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("PutIfAbsent: got %v, want ErrAlreadyExists", err)
	}
	// The original body must still be there — PutIfAbsent must not
	// have overwritten it on conflict.
	rc, _ := s.Get(ctx, "lock")
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, []byte("first")) {
		t.Errorf("body: got %q, want %q (PutIfAbsent must not overwrite on conflict)", got, "first")
	}
}

// TestMemory_PutIfAbsent_ConcurrentExactlyOneWins is the contract
// that makes PutIfAbsent useful as an advisory lock primitive: 100
// concurrent PutIfAbsent calls at the same key must succeed exactly
// once. Anything else means there's a race window where two
// processes could both think they own the lock.
func TestMemory_PutIfAbsent_ConcurrentExactlyOneWins(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	const workers = 100

	var wg sync.WaitGroup
	wg.Add(workers)
	successes := atomic.Int32{}
	conflicts := atomic.Int32{}
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			err := s.PutIfAbsent(ctx, "lock",
				strings.NewReader(fmt.Sprintf("worker-%d", i)))
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, ErrAlreadyExists):
				conflicts.Add(1)
			default:
				t.Errorf("worker %d: unexpected error %v", i, err)
			}
		}()
	}
	wg.Wait()

	if got := successes.Load(); got != 1 {
		t.Errorf("successes: got %d, want exactly 1", got)
	}
	if got := conflicts.Load(); got != workers-1 {
		t.Errorf("conflicts: got %d, want %d", got, workers-1)
	}
}
