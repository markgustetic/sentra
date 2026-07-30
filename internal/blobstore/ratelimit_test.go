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

// TestRateLimitedStore_PreservesSeekability: the paced wrapper must not
// hide the body's io.Seeker. The AWS SDK type-asserts the stream for
// Seek at request-build time; a Read-only wrapper forces the
// unseekable-stream path, which plain-HTTP endpoints (MinIO without
// TLS) reject outright — every upload would fail the moment a rate
// cap is set.
func TestRateLimitedStore_PreservesSeekability(t *testing.T) {
	w := &fakeWaiter{}
	inner := &captureStore{Store: NewMemory()}
	s := newRateLimitedStore(inner, w)
	if err := s.Put(context.Background(), "data/aa/k", bytes.NewReader([]byte("body-bytes"))); err != nil {
		t.Fatal(err)
	}
	if _, ok := inner.lastBody.(io.Seeker); !ok {
		t.Fatalf("paced body lost io.Seeker (got %T); the SDK needs it for content-length and HTTP endpoints", inner.lastBody)
	}
	if w.waited.Load() != int64(len("body-bytes")) {
		t.Errorf("pacing lost: waited %d, want %d", w.waited.Load(), len("body-bytes"))
	}
}

// captureStore records the reader handed to Put so tests can inspect
// the wrapper type the limiter produced.
type captureStore struct {
	Store
	lastBody io.Reader
}

func (c *captureStore) Put(ctx context.Context, key string, r io.Reader) error {
	c.lastBody = r
	return c.Store.Put(ctx, key, r)
}
