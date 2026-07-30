package blobstore

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeWaiter records the byte counts WaitN was asked to admit, letting
// the pacing contract be asserted without real clocks.
type fakeWaiter struct{ waited atomic.Int64 }

func (f *fakeWaiter) WaitN(_ context.Context, n int) error {
	f.waited.Add(int64(n))
	return nil
}

// TestRateLimitedStore_PacesUploads: every byte written through Put or
// PutIfAbsent passes through the limiter; reads (Get) are untouched —
// this is an UPLOAD cap.
func TestRateLimitedStore_PacesUploads(t *testing.T) {
	mem := NewMemory()
	w := &fakeWaiter{}
	s := newRateLimitedStore(mem, w)
	ctx := context.Background()

	body := strings.Repeat("x", 10_000)
	if err := s.Put(ctx, "data/aa/one", strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if got := w.waited.Load(); got != int64(len(body)) {
		t.Errorf("Put paced %d bytes, want %d", got, len(body))
	}

	w.waited.Store(0)
	if err := s.PutIfAbsent(ctx, "data/aa/two", strings.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if got := w.waited.Load(); got != int64(len(body)) {
		t.Errorf("PutIfAbsent paced %d bytes, want %d", got, len(body))
	}

	w.waited.Store(0)
	rc, err := s.Get(ctx, "data/aa/one")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, []byte(body)) {
		t.Fatalf("round-trip corrupted: %v", err)
	}
	if w.waited.Load() != 0 {
		t.Errorf("Get must not be paced (upload cap only), waited %d", w.waited.Load())
	}
}
