package blobstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/smithy-go"
)

// fakeAPIError implements smithy.APIError for testing IsRetryable's
// branches without depending on a real AWS SDK error path.
type fakeAPIError struct {
	code    string
	fault   smithy.ErrorFault
	message string
}

func (e *fakeAPIError) Error() string        { return e.code + ": " + e.message }
func (e *fakeAPIError) ErrorCode() string    { return e.code }
func (e *fakeAPIError) ErrorMessage() string { return e.message }
func (e *fakeAPIError) ErrorFault() smithy.ErrorFault {
	return e.fault
}

// TestIsRetryable covers each branch of the predicate. Conservative
// is the goal: known-transient errors retry, everything else does not.
func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil never retries", nil, false},
		{"ErrNotFound never retries", ErrNotFound, false},
		{"wrapped ErrNotFound never retries", fmt.Errorf("wrap: %w", ErrNotFound), false},
		{"context.Canceled never retries", context.Canceled, false},
		{"context.DeadlineExceeded retries", context.DeadlineExceeded, true},
		{
			"throttling code retries",
			&fakeAPIError{code: "RequestThrottled", fault: smithy.FaultClient},
			true,
		},
		{
			"slow-down code retries",
			&fakeAPIError{code: "SlowDown", fault: smithy.FaultClient},
			true,
		},
		{
			"server fault retries even with unknown code",
			&fakeAPIError{code: "WeirdInternalThing", fault: smithy.FaultServer},
			true,
		},
		{
			"client fault with unknown code does NOT retry",
			&fakeAPIError{code: "MalformedRequest", fault: smithy.FaultClient},
			false,
		},
		{
			"plain error is not retryable",
			errors.New("something else"),
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRetryable(tc.err); got != tc.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// failingStore is a Store implementation whose Put fails the first N
// times and then succeeds. Used to verify the retry loop fires the
// right number of times.
type failingStore struct {
	Store
	failures int          // number of Put calls that fail
	calls    atomic.Int32 // total Put calls
	err      error        // error returned on failure
}

func (f *failingStore) Put(ctx context.Context, key string, body io.Reader) error {
	n := f.calls.Add(1)
	if int(n) <= f.failures {
		return f.err
	}
	return f.Store.Put(ctx, key, body)
}

// fakeRetryableError is a smithy.APIError that IsRetryable will say
// is retryable. Used by failingStore to exercise the retry loop.
var fakeRetryableError = &fakeAPIError{code: "RequestThrottled", fault: smithy.FaultClient}

// noSleep is a sleep impl that returns immediately. Tests use this so
// the retry loop runs at full speed.
func noSleep(_ context.Context, _ time.Duration) error { return nil }

// TestRetryStore_RetriesUntilSuccess wraps a failingStore that fails
// twice then succeeds. The retry loop should reach the third (success)
// call.
func TestRetryStore_RetriesUntilSuccess(t *testing.T) {
	mem := NewMemory()
	fs := &failingStore{Store: mem, failures: 2, err: fakeRetryableError}
	rs := NewRetryStore(fs, RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond})
	rs.sleep = noSleep

	if err := rs.Put(context.Background(), "k", strings.NewReader("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := fs.calls.Load(); got != 3 {
		t.Errorf("Put calls: got %d, want 3 (2 failures + 1 success)", got)
	}
	// The data did land — verify via the underlying memory store.
	rc, err := mem.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get after retry: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, []byte("hello")) {
		t.Errorf("body: got %q, want %q", got, "hello")
	}
}

func TestDefaultRetryPolicySane(t *testing.T) {
	p := DefaultRetryPolicy()
	if p.MaxAttempts != 4 {
		t.Errorf("MaxAttempts = %d, want 4", p.MaxAttempts)
	}
	if p.BaseDelay <= 0 {
		t.Errorf("BaseDelay should be positive, got %v", p.BaseDelay)
	}
	if p.MaxDelay < p.BaseDelay {
		t.Errorf("MaxDelay %v should be >= BaseDelay %v", p.MaxDelay, p.BaseDelay)
	}
	if p.Jitter < 0 || p.Jitter > 1 {
		t.Errorf("Jitter = %v, want 0..1", p.Jitter)
	}
}

func TestRetryStore_RetriesReadAndDeleteOperations(t *testing.T) {
	ctx := context.Background()

	t.Run("Get", func(t *testing.T) {
		mem := NewMemory()
		if err := mem.Put(ctx, "k", strings.NewReader("value")); err != nil {
			t.Fatalf("seed: %v", err)
		}
		fs := &operationFailingStore{Store: mem, getFailures: 2, err: fakeRetryableError}
		rs := NewRetryStore(fs, RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond})
		rs.sleep = noSleep

		rc, err := rs.Get(ctx, "k")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer rc.Close()
		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(body) != "value" {
			t.Fatalf("body = %q, want value", body)
		}
		if fs.getCalls != 3 {
			t.Errorf("get calls = %d, want 3", fs.getCalls)
		}
	})

	t.Run("Stat", func(t *testing.T) {
		mem := NewMemory()
		if err := mem.Put(ctx, "k", strings.NewReader("value")); err != nil {
			t.Fatalf("seed: %v", err)
		}
		fs := &operationFailingStore{Store: mem, statFailures: 2, err: fakeRetryableError}
		rs := NewRetryStore(fs, RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond})
		rs.sleep = noSleep

		info, err := rs.Stat(ctx, "k")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if info.Key != "k" || info.Size != 5 {
			t.Fatalf("info = %+v, want key k size 5", info)
		}
		if fs.statCalls != 3 {
			t.Errorf("stat calls = %d, want 3", fs.statCalls)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		mem := NewMemory()
		if err := mem.Put(ctx, "k", strings.NewReader("value")); err != nil {
			t.Fatalf("seed: %v", err)
		}
		fs := &operationFailingStore{Store: mem, deleteFailures: 2, err: fakeRetryableError}
		rs := NewRetryStore(fs, RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond})
		rs.sleep = noSleep

		if err := rs.Delete(ctx, "k"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if fs.deleteCalls != 3 {
			t.Errorf("delete calls = %d, want 3", fs.deleteCalls)
		}
		if _, err := mem.Stat(ctx, "k"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("key should be gone, Stat err = %v", err)
		}
	})

	t.Run("List", func(t *testing.T) {
		mem := NewMemory()
		for _, k := range []string{"p/a", "p/b", "other"} {
			if err := mem.Put(ctx, k, strings.NewReader(k)); err != nil {
				t.Fatalf("seed %s: %v", k, err)
			}
		}
		fs := &operationFailingStore{Store: mem, listFailures: 2, err: fakeRetryableError}
		rs := NewRetryStore(fs, RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond})
		rs.sleep = noSleep

		infos, err := rs.List(ctx, "p/")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(infos) != 2 || infos[0].Key != "p/a" || infos[1].Key != "p/b" {
			t.Fatalf("infos = %+v, want p/a and p/b", infos)
		}
		if fs.listCalls != 3 {
			t.Errorf("list calls = %d, want 3", fs.listCalls)
		}
	})
}

func TestRetryStore_PutIfAbsentDoesNotRetry(t *testing.T) {
	mem := NewMemory()
	fs := &operationFailingStore{Store: mem, putIfAbsentFailures: 1, err: fakeRetryableError}
	rs := NewRetryStore(fs, RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond})
	rs.sleep = noSleep

	err := rs.PutIfAbsent(context.Background(), "lock", strings.NewReader("owner"))
	if !errors.Is(err, fakeRetryableError) {
		t.Fatalf("PutIfAbsent err = %v, want fake retryable error", err)
	}
	if fs.putIfAbsentCalls != 1 {
		t.Fatalf("PutIfAbsent calls = %d, want 1", fs.putIfAbsentCalls)
	}
	if _, err := mem.Stat(context.Background(), "lock"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PutIfAbsent should not have retried into success, Stat err = %v", err)
	}
}

// TestRetryStore_RetryExhausted runs out of attempts. The wrapped
// error should be returned (so callers can inspect what kind of
// transient failure took us out).
func TestRetryStore_RetryExhausted(t *testing.T) {
	mem := NewMemory()
	fs := &failingStore{Store: mem, failures: 100, err: fakeRetryableError}
	rs := NewRetryStore(fs, RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond})
	rs.sleep = noSleep

	err := rs.Put(context.Background(), "k", strings.NewReader("hi"))
	if err == nil {
		t.Fatal("expected exhaustion error, got nil")
	}
	// The original transient error should be wrapped so errors.As
	// works for callers that want to discriminate.
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Errorf("expected wrapped smithy.APIError, got %v", err)
	}
	if got := fs.calls.Load(); got != 3 {
		t.Errorf("calls: got %d, want 3 (MaxAttempts)", got)
	}
}

// TestRetryStore_NonRetryableErrorReturnedImmediately verifies that
// errors not matching IsRetryable bypass the loop — calling Put with
// a non-retryable error should fire exactly once.
func TestRetryStore_NonRetryableErrorReturnedImmediately(t *testing.T) {
	mem := NewMemory()
	fs := &failingStore{Store: mem, failures: 100, err: errors.New("permanent")}
	rs := NewRetryStore(fs, RetryPolicy{MaxAttempts: 5, BaseDelay: time.Millisecond})
	rs.sleep = noSleep

	err := rs.Put(context.Background(), "k", strings.NewReader("hi"))
	if err == nil || err.Error() != "permanent" {
		t.Fatalf("expected 'permanent' error, got %v", err)
	}
	if got := fs.calls.Load(); got != 1 {
		t.Errorf("calls: got %d, want 1 (no retry on permanent error)", got)
	}
}

// TestRetryStore_ContextCancelDuringSleep aborts the retry loop early
// when the parent context is cancelled while we're waiting between
// attempts. The contextSleep helper must observe the cancellation
// and propagate ctx.Err() up.
func TestRetryStore_ContextCancelDuringSleep(t *testing.T) {
	mem := NewMemory()
	fs := &failingStore{Store: mem, failures: 100, err: fakeRetryableError}
	// Real sleep here so the cancel actually happens during the
	// timer wait. Long-enough delay to make the race trivial.
	rs := NewRetryStore(fs, RetryPolicy{
		MaxAttempts: 5,
		BaseDelay:   200 * time.Millisecond,
		MaxDelay:    1 * time.Second, // cap, but must be >= BaseDelay
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := rs.Put(ctx, "k", strings.NewReader("hi"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestRetryStore_BatchDeleteRetries extends the same retry contract
// to BatchDelete. The signature is different (returns deleted count)
// so it has its own happy-path test.
func TestRetryStore_BatchDeleteRetries(t *testing.T) {
	mem := NewMemory()
	for _, k := range []string{"a", "b"} {
		_ = mem.Put(context.Background(), k, strings.NewReader("x"))
	}
	calls := atomic.Int32{}
	failingBatch := &batchFailingStore{
		Store: mem,
		fail:  func() error { return fakeRetryableError },
		until: 2,
		count: &calls,
	}
	rs := NewRetryStore(failingBatch, RetryPolicy{MaxAttempts: 5, BaseDelay: time.Millisecond})
	rs.sleep = noSleep

	deleted, err := rs.BatchDelete(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("BatchDelete: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted: got %d, want 2", deleted)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls: got %d, want 3 (2 fail + 1 success)", got)
	}
}

// batchFailingStore wraps a Store and intercepts BatchDelete to fail
// the first N times. Other methods pass through unchanged.
type batchFailingStore struct {
	Store
	fail  func() error
	until int
	count *atomic.Int32
}

func (b *batchFailingStore) BatchDelete(ctx context.Context, keys []string) (int, error) {
	n := b.count.Add(1)
	if int(n) <= b.until {
		return 0, b.fail()
	}
	return b.Store.BatchDelete(ctx, keys)
}

// operationFailingStore intercepts non-Put methods for RetryStore
// tests. Each failure counter makes the corresponding method return a
// retryable error before delegating to the embedded Store.
type operationFailingStore struct {
	Store
	err error

	getFailures         int
	getCalls            int
	statFailures        int
	statCalls           int
	deleteFailures      int
	deleteCalls         int
	listFailures        int
	listCalls           int
	putIfAbsentFailures int
	putIfAbsentCalls    int
}

func (s *operationFailingStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	s.getCalls++
	if s.getCalls <= s.getFailures {
		return nil, s.err
	}
	return s.Store.Get(ctx, key)
}

func (s *operationFailingStore) Stat(ctx context.Context, key string) (Info, error) {
	s.statCalls++
	if s.statCalls <= s.statFailures {
		return Info{}, s.err
	}
	return s.Store.Stat(ctx, key)
}

func (s *operationFailingStore) Delete(ctx context.Context, key string) error {
	s.deleteCalls++
	if s.deleteCalls <= s.deleteFailures {
		return s.err
	}
	return s.Store.Delete(ctx, key)
}

func (s *operationFailingStore) List(ctx context.Context, prefix string) ([]Info, error) {
	s.listCalls++
	if s.listCalls <= s.listFailures {
		return nil, s.err
	}
	return s.Store.List(ctx, prefix)
}

func (s *operationFailingStore) PutIfAbsent(ctx context.Context, key string, body io.Reader) error {
	s.putIfAbsentCalls++
	if s.putIfAbsentCalls <= s.putIfAbsentFailures {
		return s.err
	}
	return s.Store.PutIfAbsent(ctx, key, body)
}

// TestRetryStore_DelayBackoffShape pins the exponential schedule. We
// don't assert exact values (Jitter randomizes); we assert the
// sequence is monotonic and capped by MaxDelay.
func TestRetryStore_DelayBackoffShape(t *testing.T) {
	rs := NewRetryStore(NewMemory(), RetryPolicy{
		MaxAttempts: 6,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    1 * time.Second,
		Jitter:      0, // disable jitter for deterministic comparisons
	})
	want := []time.Duration{
		100 * time.Millisecond, // 100 * 2^0
		200 * time.Millisecond, // 100 * 2^1
		400 * time.Millisecond, // 100 * 2^2
		800 * time.Millisecond, // 100 * 2^3
		1 * time.Second,        // capped (would be 1.6s)
		1 * time.Second,        // capped
	}
	for i, w := range want {
		if got := rs.delay(i); got != w {
			t.Errorf("attempt %d: got %v, want %v", i, got, w)
		}
	}
}

func TestRetryStore_DelayEdgeCases(t *testing.T) {
	t.Run("zero base", func(t *testing.T) {
		rs := NewRetryStore(NewMemory(), RetryPolicy{
			MaxAttempts: 2,
			BaseDelay:   0,
			MaxDelay:    time.Second,
			Jitter:      1,
		})
		if got := rs.delay(10); got != 0 {
			t.Fatalf("delay with zero base = %v, want 0", got)
		}
	})

	t.Run("uncapped growth", func(t *testing.T) {
		rs := NewRetryStore(NewMemory(), RetryPolicy{
			MaxAttempts: 2,
			BaseDelay:   time.Millisecond,
			MaxDelay:    0,
			Jitter:      0,
		})
		if got := rs.delay(3); got != 8*time.Millisecond {
			t.Fatalf("delay attempt 3 = %v, want 8ms", got)
		}
	})
}
