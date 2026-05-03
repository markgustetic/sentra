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
// Caveat to keep in mind: jotfs's NewChunker XORs a package-level
// gear table by the configured Seed on every construction. We never
// set Seed, so the table stays at its default and chunkers are
// deterministic across calls. If a future caller starts using Seed,
// they need to be aware that two chunkers in the same process will
// not be independent.
package chunker

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"

	fastcdc "github.com/jotfs/fastcdc-go"
)

// chunkerMu serializes ChunkAll against itself. The fastcdc-go
// library writes a package-level "gear" table inside NewChunker
// (XOR-by-Seed; we never set Seed so the writes are no-ops) and
// reads the same table during Chunker.Next. Even the no-op writes
// are flagged by the race detector when they overlap with another
// goroutine's reads, so we hold this mutex for the entire chunking
// pipeline rather than just NewChunker. This trades intra-package
// parallelism for race-detector cleanliness; ChunkAll is already
// memory-bounded by the file it ingests, so the parallel-snapshot
// case still gets per-file isolation through the walker's worker
// pool calling other parts of the snapshot pipeline (encrypt,
// upload, hash) in parallel.
//
// Discovered during Phase 5 integration when Walk's worker pool
// concurrently invokes ChunkAll. Surface this in any future review
// of the chunker package; the right long-term fix is upstreaming a
// patch to fastcdc-go.
var chunkerMu sync.Mutex

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
	chunkerMu.Lock()
	defer chunkerMu.Unlock()

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
