package blobstore

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

// uploadWaiter admits n bytes of upload. Satisfied by *rate.Limiter;
// tests inject a recorder so pacing is asserted without real clocks.
type uploadWaiter interface {
	WaitN(ctx context.Context, n int) error
}

// rateLimitedStore caps UPLOAD bandwidth by pacing the readers handed
// to Put/PutIfAbsent — the S3 SDK pulls the body through the paced
// reader, so the wire transfer slows to the token rate. Reads and
// metadata operations pass through untouched: the knob exists so a
// nightly backup doesn't saturate a home uplink, and throttling
// restore would only make a bad day worse.
//
// Layering: wrap the base store BELOW RetryStore. RetryStore buffers
// the body and re-Puts on retry, so each attempt pays for its bytes —
// a retried upload really does transfer twice.
type rateLimitedStore struct {
	Store
	limiter uploadWaiter
}

// NewRateLimitedStore wraps inner with an upload cap of bytesPerSec.
// A zero or negative rate returns inner unchanged.
func NewRateLimitedStore(inner Store, bytesPerSec int64) Store {
	if bytesPerSec <= 0 {
		return inner
	}
	// Burst of one second's budget: small enough to keep the average
	// honest, large enough that a single chunked read (32–64 KiB) is
	// always admissible even at low rates.
	burst := int(bytesPerSec)
	if burst < 64<<10 {
		burst = 64 << 10
	}
	return newRateLimitedStore(inner, rate.NewLimiter(rate.Limit(bytesPerSec), burst))
}

func newRateLimitedStore(inner Store, limiter uploadWaiter) Store {
	return &rateLimitedStore{Store: inner, limiter: limiter}
}

func (s *rateLimitedStore) Put(ctx context.Context, key string, r io.Reader) error {
	return s.Store.Put(ctx, key, &pacedReader{ctx: ctx, r: r, limiter: s.limiter})
}

func (s *rateLimitedStore) PutIfAbsent(ctx context.Context, key string, r io.Reader) error {
	return s.Store.PutIfAbsent(ctx, key, &pacedReader{ctx: ctx, r: r, limiter: s.limiter})
}

// pacedReader waits for limiter tokens AFTER each read, sized to the
// bytes actually produced — pacing what really moved rather than a
// guess about what the next read might return.
type pacedReader struct {
	ctx     context.Context
	r       io.Reader
	limiter uploadWaiter
}

func (p *pacedReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		if werr := p.limiter.WaitN(p.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}
