// FastCDC implementation: github.com/jotfs/fastcdc-go v0.2.0.
//
// Rationale: jotfs/fastcdc-go is the library the design names. It's
// small (single file, ~400 LOC), licensed Apache 2.0, declares
// `go 1.14` in its module so it never bumps our go directive, and it
// implements FastCDC as published in the original paper. The two
// alternatives we considered:
//
//   - github.com/restic/chunker — battle-tested but uses a different
//     algorithm (Rabin fingerprints, not FastCDC) and would require
//     adapting the design's chunk-size targets.
//   - rolling our own ~150 LOC inline — viable, but not worth the
//     correctness risk this early in the project.
//
// The upstream v0.2.0 constructor mutates a package-level gear table
// while applying Options.Seed. Sentra currently uses the default seed,
// but the writes still race under concurrent construction. We
// serialize construction only; once NewChunker returns, each chunker
// streams independently and concurrent snapshot workers can proceed.
package chunker

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"

	fastcdc "github.com/jotfs/fastcdc-go"
)

// Chunk size targets follow the design: ~1 MiB average, with a
// quartered minimum (256 KiB) and a quadrupled maximum (4 MiB). These
// are also the library defaults when only AverageSize is set, but we
// pin them explicitly so the contract is in the source rather than
// a transitive dependency.
const (
	avgChunkSize = 1 << 20 // 1 MiB
	minChunkSize = 1 << 18 // 256 KiB
	maxChunkSize = 1 << 22 // 4 MiB
)

var fastCDCNewMu sync.Mutex

// Chunk is a single content-defined chunk produced by ChunkAll.
//
// Hash is the SHA-256 of Data and is exactly 32 bytes. Data is owned
// by the caller — the chunker library reuses its internal buffer
// across Next() calls, so we copy on the way out.
type Chunk struct {
	Hash   []byte
	Data   []byte
	Offset int64
}

// ChunkAll reads r to EOF and returns all FastCDC chunks. Each chunk
// is hashed with SHA-256. An empty reader returns (nil, nil).
//
// The caller owns the returned Data slices; they will not be aliased
// to subsequent chunker state.
//
// MEMORY CEILING: ChunkAll holds every chunk in memory simultaneously
// (~1 MiB each). For an N-byte file the resident set is O(N). This is
// fine for manifests, indexes, and small files; for large files use
// ChunkStream instead — it processes chunks one at a time so memory
// stays bounded at O(1 chunk).
func ChunkAll(r io.Reader) ([]Chunk, error) {
	c, err := newFastCDCChunker(r)
	if err != nil {
		return nil, err
	}

	var out []Chunk
	for {
		chunk, err := c.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("chunker: read chunk: %w", err)
		}

		// Copy: chunk.Data points into the chunker's internal buffer
		// and is invalidated on the next Next() call.
		data := make([]byte, len(chunk.Data))
		copy(data, chunk.Data)

		sum := sha256.Sum256(data)
		hash := make([]byte, sha256.Size)
		copy(hash, sum[:])

		out = append(out, Chunk{
			Hash:   hash,
			Data:   data,
			Offset: int64(chunk.Offset),
		})
	}
	return out, nil
}

func newFastCDCChunker(r io.Reader) (*fastcdc.Chunker, error) {
	fastCDCNewMu.Lock()
	defer fastCDCNewMu.Unlock()
	c, err := fastcdc.NewChunker(r, fastcdc.Options{
		AverageSize: avgChunkSize,
		MinSize:     minChunkSize,
		MaxSize:     maxChunkSize,
	})
	if err != nil {
		return nil, fmt.Errorf("chunker: new fastcdc: %w", err)
	}
	return c, nil
}

// ChunkStream reads r, calls fn for each FastCDC chunk in order, and
// returns on EOF or the callback's first non-nil error. Memory is
// bounded at O(1 chunk) because the callback is expected to process
// each chunk (hash + encrypt + upload, etc.) before returning; the
// next chunker.Next call reuses the underlying buffer.
//
// IMPORTANT: ChunkStream does NOT copy Data or Hash on the way out.
// Both slices are BORROWED for the duration of the callback only:
//
//   - Data points into the chunker's internal buffer, which is
//     invalidated on the next Next() call.
//   - Hash points into a stack-allocated [32]byte that goes out of
//     scope when fn returns.
//
// Callbacks that need either to outlive the callback boundary must
// copy. The two natural patterns are:
//
//   - Hex-encode the Hash inside the callback (the resulting string
//     is owned by the caller). This is what repo.captureFile does.
//   - Pass Data to a synchronous consumer (compress, encrypt, Put)
//     that finishes before fn returns. Goroutines that touch Data
//     must end before fn returns.
//
// fn's first non-nil error short-circuits the loop and is returned
// verbatim (not wrapped) so callers can errors.Is against their own
// domain sentinels — wrapping happens at the call site if at all.
//
// An empty reader returns nil with fn never being called.
func ChunkStream(r io.Reader, fn func(Chunk) error) error {
	c, err := newFastCDCChunker(r)
	if err != nil {
		return err
	}
	for {
		chunk, err := c.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("chunker: read chunk: %w", err)
		}
		sum := sha256.Sum256(chunk.Data)
		if err := fn(Chunk{
			Hash:   sum[:],
			Data:   chunk.Data,
			Offset: int64(chunk.Offset),
		}); err != nil {
			return err
		}
	}
}
