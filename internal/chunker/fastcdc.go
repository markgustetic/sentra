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
// The local replace in go.mod carries a small upstream-style patch:
// each Chunker owns its seeded gear table instead of NewChunker
// mutating package-level state. That keeps concurrent snapshot
// workers race-free without serializing every file through a global
// mutex.
package chunker

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

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
// fine for manifests, indexes, and small files but will OOM on real
// backup workloads (multi-GiB databases, photo libraries, etc.).
//
// Future: land a streaming variant
//
//	func ChunkStream(r io.Reader, fn func(Chunk) error) error
//
// where each chunk is delivered to the callback for hash+encrypt+upload
// before the next is read. The streaming variant can avoid the per-call
// copy because the callback contract bounds the slice's lifetime.
func ChunkAll(r io.Reader) ([]Chunk, error) {
	c, err := fastcdc.NewChunker(r, fastcdc.Options{
		AverageSize: avgChunkSize,
		MinSize:     minChunkSize,
		MaxSize:     maxChunkSize,
	})
	if err != nil {
		return nil, fmt.Errorf("chunker: new fastcdc: %w", err)
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
