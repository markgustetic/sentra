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
	return s.Store.Put(ctx, key, paceBody(ctx, r, s.limiter))
}

func (s *rateLimitedStore) PutIfAbsent(ctx context.Context, key string, r io.Reader) error {
	return s.Store.PutIfAbsent(ctx, key, paceBody(ctx, r, s.limiter))
}

// paceBody wraps r with token pacing, PRESERVING io.Seeker when r has
// it. The AWS SDK type-asserts the body for Seek at request-build
// time: a Read-only wrapper forces the unseekable-stream path, which
// loses the content length and — on plain-HTTP endpoints (MinIO
// without TLS), where trailing checksums are unavailable — fails
// every upload outright. RetryStore always hands a *bytes.Reader, so
// the seekable branch is the one production takes; a rewind after
// admitted bytes simply pays for them again on the re-read, matching
// the documented "a retried upload really does transfer twice".
func paceBody(ctx context.Context, r io.Reader, limiter uploadWaiter) io.Reader {
	paced := &pacedReader{ctx: ctx, r: r, limiter: limiter}
	if s, ok := r.(io.Seeker); ok {
		return &pacedReadSeeker{pacedReader: paced, s: s}
	}
	return paced
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

// pacedReadSeeker is pacedReader plus a delegated Seek, so wrapping a
// seekable body doesn't strip the capability the SDK depends on.
type pacedReadSeeker struct {
	*pacedReader
	s io.Seeker
}

func (p *pacedReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return p.s.Seek(offset, whence)
}
