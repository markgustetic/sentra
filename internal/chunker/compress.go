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
// AES-GCM encrypted afterwards, so the marginal compression gain from
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
		decoder, decErr = zstd.NewReader(nil)
	})
	return decoder, decErr
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
// error for any input that is not a valid zstd frame. Concurrent-safe.
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
