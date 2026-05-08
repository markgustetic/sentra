// Package chunker provides content-defined chunking and compression
// primitives for Sentra's content-addressed blob layer. It is the
// pre-encryption stage: producers chunk a stream into ~1 MiB pieces,
// hash each chunk with SHA-256, optionally compress with zstd, and
// hand the bytes to the crypto layer.
package chunker

import (
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// We use SpeedDefault rather than SpeedBestCompression: chunks are
// AEAD-encrypted afterwards, so the marginal compression gain from
// the slowest level is rarely worth the CPU. The ratio on text-ish
// data is already excellent at the default.
//
// Encoder/decoder objects are safe for concurrent use, so we keep one
// of each at package scope rather than reconstructing per call. They
// hold internal state buffers that are expensive to reallocate; reuse
// is a meaningful win on backup workloads that compress thousands of
// chunks. The sync.Once guards lazy initialization so import is cheap
// when the package is pulled in but never used (e.g. by tests of
// other files in the package).
var (
	encOnce sync.Once
	encoder *zstd.Encoder
	encErr  error

	decOnce sync.Once
	decoder *zstd.Decoder
	decErr  error

	// decoderPool keeps a *zstd.Decoder per max-decoded-size so that
	// DecompressLimit doesn't pay constructor cost per call. zstd's
	// WithDecoderMaxMemory is set on the decoder, not per DecodeAll,
	// so we cannot share one decoder across different caps.
	decoderPoolMu sync.Mutex
	decoderPool   = map[uint64]*zstd.Decoder{}
)

func getEncoder() (*zstd.Encoder, error) {
	encOnce.Do(func() {
		encoder, encErr = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	})
	return encoder, encErr
}

func getDecoder() (*zstd.Decoder, error) {
	decOnce.Do(func() {
		// Decoder with nil reader supports the DecodeAll fast path.
		// Cap the decoded size at 8 MiB (2x our chunk max). Without
		// this, a malformed or hostile blob could expand to the
		// library's 64 GiB default and exhaust process memory.
		const maxDecodedSize = 8 << 20
		decoder, decErr = zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecodedSize))
	})
	return decoder, decErr
}

// getLimitedDecoder returns a cached decoder configured with the given
// max-decoded-size cap, lazily creating one on first use. Concurrent
// callers with the same cap share a single decoder; *zstd.Decoder is
// safe for concurrent use of DecodeAll.
func getLimitedDecoder(maxDecoded uint64) (*zstd.Decoder, error) {
	decoderPoolMu.Lock()
	defer decoderPoolMu.Unlock()
	if d, ok := decoderPool[maxDecoded]; ok {
		return d, nil
	}
	d, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecoded))
	if err != nil {
		return nil, err
	}
	decoderPool[maxDecoded] = d
	return d, nil
}

// Compress returns the zstd-compressed form of in. It is safe for
// concurrent use. Empty input is allowed and produces a valid empty
// zstd frame.
func Compress(in []byte) ([]byte, error) {
	enc, err := getEncoder()
	if err != nil {
		return nil, fmt.Errorf("chunker: zstd encoder init: %w", err)
	}
	// Pre-size the destination to avoid an immediate reallocation in
	// EncodeAll: zstd output is upper-bounded by input size for our
	// purposes (worst case adds a few framing bytes for incompressible
	// data — EncodeAll grows the slice if needed).
	dst := make([]byte, 0, len(in))
	return enc.EncodeAll(in, dst), nil
}

// Decompress returns the zstd-decompressed form of in. It returns an
// error for any input that is not a valid zstd frame, or whose decoded
// size exceeds the chunk cap (8 MiB, 2x our chunk max). Concurrent-safe.
//
// Use this for chunk decompression where the plaintext is bounded.
// For manifests, see DecompressLimit.
func Decompress(in []byte) ([]byte, error) {
	dec, err := getDecoder()
	if err != nil {
		return nil, fmt.Errorf("chunker: zstd decoder init: %w", err)
	}
	out, err := dec.DecodeAll(in, nil)
	if err != nil {
		return nil, fmt.Errorf("chunker: zstd decode: %w", err)
	}
	return out, nil
}

// DecompressLimit decompresses in with a configurable maximum decoded
// size. For chunks use Decompress (8 MiB cap). For manifests, callers
// should pass a generous bound (e.g. 1 GiB) — manifests are unbounded
// by file count but the cap prevents zip-bomb expansion.
//
// Concurrent-safe; decoders are pooled by maxDecoded so repeated
// callers with the same cap share state.
func DecompressLimit(in []byte, maxDecoded uint64) ([]byte, error) {
	dec, err := getLimitedDecoder(maxDecoded)
	if err != nil {
		return nil, fmt.Errorf("chunker: zstd decoder init: %w", err)
	}
	out, err := dec.DecodeAll(in, nil)
	if err != nil {
		return nil, fmt.Errorf("chunker: zstd decode: %w", err)
	}
	return out, nil
}
