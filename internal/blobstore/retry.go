package blobstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// RetryPolicy controls how RetryStore retries failed operations. The
// zero value is invalid; use DefaultRetryPolicy for sensible bounds.
//
// The retry loop applies exponential backoff with jitter: attempt N
// (zero-indexed) waits BaseDelay * 2^N, clamped to MaxDelay, then
// randomized within ±Jitter * delay.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts (including the
	// first). MaxAttempts=1 disables retry.
	MaxAttempts int

	// BaseDelay is how long to wait before the first retry. The
	// next retry waits 2x, the next 4x, and so on, capped by
	// MaxDelay.
	BaseDelay time.Duration

	// MaxDelay caps the per-attempt delay.
	MaxDelay time.Duration

	// Jitter (0..1) randomizes the per-attempt delay by ±this
	// fraction. 0.2 means each delay is uniform in [0.8d, 1.2d].
	// Jitter prevents the synchronized-retry storm pattern when many
	// callers hit the same throttling at once.
	Jitter float64
}

// DefaultRetryPolicy returns a policy tuned for AWS S3 / S3-compatible
// stores. Four attempts (1 try + 3 retries) covers typical transient
// 5xx / throttling without unbounded waits; the cap keeps a worst-case
// run from blocking the whole CLI for minutes.
//
// The AWS SDK already retries 3 times internally; this layer wraps the
// SDK's retry-exhausted errors with a coarser-grained outer retry so
// the CLI can recover from sustained throttling that exhausts the SDK's
// limit. With BaseDelay=500ms, the delays before retries 2/3/4 are
// roughly 0.5s, 1s, 2s, plus jitter — adding up to ~3.5s extra total.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 4,
		BaseDelay:   500 * time.Millisecond,
		MaxDelay:    30 * time.Second,
		Jitter:      0.2,
	}
}

// RetryStore wraps any Store and retries failed operations subject to
// the configured policy. Compose with NewRetryStore. Useful for the
// production S3 backend; in-memory tests don't need it but it works
// correctly there too (Memory operations never error transiently, so
// retries never fire).
//
// All retried operations are idempotent at the S3 level: PutObject is
// idempotent (same key + same body = same final object), DeleteObject
// is idempotent (404s are silent), HeadObject and GetObject are reads.
// BatchDelete uses S3's DeleteObjects which is also idempotent.
//
// Get and List can be retried but only on the *initial* request — once
// the underlying Store returned a body, errors during read are the
// caller's to handle (we can't replay an HTTP stream we've already
// committed to passing through). Put buffers the body so the retry can
// replay it from memory; this is fine for sentra's chunk sizes.
type RetryStore struct {
	inner  Store
	policy RetryPolicy

	// sleep is the timer factory used by the retry loop. Production
	// uses a real time.NewTimer; tests can swap a fake to make
	// backoff deterministic.
	sleep func(ctx context.Context, d time.Duration) error
}

// Compile-time assertion that *RetryStore implements Store.
var _ Store = (*RetryStore)(nil)

// NewRetryStore wraps inner with the given retry policy. Pass
// DefaultRetryPolicy() for a reasonable production setting.
func NewRetryStore(inner Store, policy RetryPolicy) *RetryStore {
	return &RetryStore{
		inner:  inner,
		policy: policy,
		sleep:  contextSleep,
	}
}

// contextSleep blocks for d, returning early with ctx.Err() if the
// context is cancelled. Production sleep used by NewRetryStore.
func contextSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// IsRetryable reports whether err is a transient backend error worth
// retrying. Conservative by design: an error not recognized as
// transient is treated as terminal so the caller sees it instead of
// the wrapper looping wastefully.
//
// Retryable: 5xx HTTP responses, AWS throttling codes, request timeout
// errors, generic context.DeadlineExceeded (could be a per-call
// timeout — the retry loop re-checks ctx.Err() between attempts so a
// parent-context expiry still terminates).
//
// Not retryable: ErrNotFound (definitive), context.Canceled (caller
// asked to stop), AWS API errors with client fault, anything else not
// matching the above.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotFound) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// AWS SDK / smithy-go HTTP-layer error: 5xx and 408/429 are
	// retryable. 4xx (other than throttling) is the caller's problem.
	var httpErr *smithyhttp.ResponseError
	if errors.As(err, &httpErr) {
		code := httpErr.HTTPStatusCode()
		if code >= 500 || code == 408 || code == 429 {
			return true
		}
	}
	// AWS API error with explicit code: throttling and a few well-
	// known transient codes. ErrorFault==server marks 5xx-style
	// upstream issues even when the wire status is non-standard.
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "RequestThrottled", "ThrottlingException", "SlowDown",
			"InternalError", "ServiceUnavailable",
			"RequestTimeout", "RequestTimeoutException":
			return true
		}
		if apiErr.ErrorFault() == smithy.FaultServer {
			return true
		}
	}
	return false
}

// retry runs op up to policy.MaxAttempts times, with exponential
// backoff between attempts. Returns the final error from op or
// ctx.Err() if the context was cancelled mid-loop.
func (r *RetryStore) retry(ctx context.Context, op func() error) error {
	if r.policy.MaxAttempts < 1 {
		return errors.New("blobstore/retry: invalid policy: MaxAttempts < 1")
	}
	var lastErr error
	for attempt := 0; attempt < r.policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := op()
		if err == nil {
			return nil
		}
		lastErr = err
		if !IsRetryable(err) {
			return err
		}
		// Surface a structured event on each retry firing so an
		// operator running with --log-level=info sees throttling /
		// 5xx storms in their cron logs. info, not warn — retries
		// are expected behavior on shared infrastructure, not a
		// failure indicator. The error string carries enough detail
		// for triage; we don't add the typed code separately because
		// the smithy.APIError interface already renders it via Error().
		slog.LogAttrs(ctx, slog.LevelInfo, "blobstore retry",
			slog.Int("attempt", attempt+1),
			slog.Int("max_attempts", r.policy.MaxAttempts),
			slog.String("error", err.Error()),
		)
		// Sleep before the next attempt; skip the sleep on the final
		// failed attempt (we're going to return either way).
		if attempt+1 < r.policy.MaxAttempts {
			if sleepErr := r.sleep(ctx, r.delay(attempt)); sleepErr != nil {
				return sleepErr
			}
		}
	}
	return fmt.Errorf("blobstore/retry: %d attempts exhausted: %w", r.policy.MaxAttempts, lastErr)
}

// delay returns the wait time before retry `attempt` (zero-indexed).
// Exponential backoff with optional jitter and an optional cap.
//
// Edge cases:
//   - BaseDelay == 0: every retry waits 0 (fast retry).
//   - MaxDelay == 0: no cap (unbounded growth, but the exponent is
//     clamped to 30 so worst-case is BaseDelay * 2^30 — about 12 days
//     for a 1-second base, which would never reasonably fire because
//     MaxAttempts caps the loop length first).
func (r *RetryStore) delay(attempt int) time.Duration {
	if r.policy.BaseDelay <= 0 {
		return 0
	}
	// Clamp the exponent so the shift below cannot overflow int64.
	exp := attempt
	if exp > 30 {
		exp = 30
	}
	d := time.Duration(int64(r.policy.BaseDelay) << exp) //nolint:gosec // exp clamped to 30
	if d < 0 {
		// Overflow guard — fall back to MaxDelay (or BaseDelay if no
		// cap was set).
		if r.policy.MaxDelay > 0 {
			d = r.policy.MaxDelay
		} else {
			d = r.policy.BaseDelay
		}
	}
	if r.policy.MaxDelay > 0 && d > r.policy.MaxDelay {
		d = r.policy.MaxDelay
	}
	if r.policy.Jitter > 0 {
		spread := float64(d) * r.policy.Jitter
		// Uniform in [-spread, +spread].
		d += time.Duration(rand.Float64()*spread*2 - spread) //nolint:gosec // not crypto
	}
	if d < 0 {
		d = 0
	}
	return d
}

// --- Store implementation: each method delegates to the inner store
// through the retry helper. ---

// Put buffers body once so the retry loop can replay it; sentra's
// blob bodies are bounded by the chunker's max chunk size, so this
// stays well within memory budget.
func (r *RetryStore) Put(ctx context.Context, key string, body io.Reader) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("blobstore/retry: buffer body for %q: %w", key, err)
	}
	return r.retry(ctx, func() error {
		return r.inner.Put(ctx, key, bytes.NewReader(raw))
	})
}

// Get retries the *initial* request only. The returned ReadCloser is
// passed through verbatim; if it errors mid-read the caller must
// handle it (we can't replay a half-consumed HTTP stream).
func (r *RetryStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	var rc io.ReadCloser
	err := r.retry(ctx, func() error {
		got, gerr := r.inner.Get(ctx, key)
		if gerr != nil {
			return gerr
		}
		rc = got
		return nil
	})
	return rc, err
}

// Stat retries the HeadObject call.
func (r *RetryStore) Stat(ctx context.Context, key string) (Info, error) {
	var info Info
	err := r.retry(ctx, func() error {
		got, gerr := r.inner.Stat(ctx, key)
		if gerr != nil {
			return gerr
		}
		info = got
		return nil
	})
	return info, err
}

// Delete retries the DeleteObject call.
func (r *RetryStore) Delete(ctx context.Context, key string) error {
	return r.retry(ctx, func() error {
		return r.inner.Delete(ctx, key)
	})
}

// List retries the ListObjectsV2 paginator. The whole pagination is
// retried on transient failure — partial pages are not preserved.
func (r *RetryStore) List(ctx context.Context, prefix string) ([]Info, error) {
	var out []Info
	err := r.retry(ctx, func() error {
		got, gerr := r.inner.List(ctx, prefix)
		if gerr != nil {
			return gerr
		}
		out = got
		return nil
	})
	return out, err
}

// BatchDelete retries the inner BatchDelete call. The inner S3
// implementation already chunks at 1000 keys/request; on retry the
// whole input is re-sent. Idempotency is from S3's DeleteObjects.
func (r *RetryStore) BatchDelete(ctx context.Context, keys []string) (int, error) {
	var deleted int
	err := r.retry(ctx, func() error {
		got, gerr := r.inner.BatchDelete(ctx, keys)
		if gerr != nil {
			return gerr
		}
		deleted = got
		return nil
	})
	return deleted, err
}
