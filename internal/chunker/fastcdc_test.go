package chunker

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// TestChunker_StableBoundaries: chunking the same input twice must
// produce identical chunk sequences. This is the core determinism
// property — without it, dedup is impossible.
func TestChunker_StableBoundaries(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefghij"), 200_000) // 2 MiB
	a, err := ChunkAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ChunkAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("chunk count differs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i].Data, b[i].Data) {
			t.Fatalf("chunk %d differs", i)
		}
		if !bytes.Equal(a[i].Hash, b[i].Hash) {
			t.Fatalf("chunk %d hash differs", i)
		}
		if a[i].Offset != b[i].Offset {
			t.Fatalf("chunk %d offset differs: %d vs %d", i, a[i].Offset, b[i].Offset)
		}
	}
}

// TestChunker_LocalChange_AffectsFewChunks: a single byte change in
// the middle of the stream must invalidate only a small number of
// chunks. This is the "shift resistance" property that makes content-
// defined chunking valuable for incremental backups.
func TestChunker_LocalChange_AffectsFewChunks(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefghij"), 200_000)
	mod := make([]byte, len(data))
	copy(mod, data)
	mod[len(mod)/2] = 'Z' // single byte change in the middle
	a, err := ChunkAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ChunkAll(bytes.NewReader(mod))
	if err != nil {
		t.Fatal(err)
	}
	differing := 0
	am := map[string]struct{}{}
	for _, c := range a {
		am[string(c.Hash)] = struct{}{}
	}
	for _, c := range b {
		if _, ok := am[string(c.Hash)]; !ok {
			differing++
		}
	}
	if differing > 5 {
		t.Fatalf("local change should produce few new chunks, got %d", differing)
	}
}

// TestChunker_Hashes: every emitted chunk's Hash field must be the
// SHA-256 of its Data. This is what makes the addressed in
// "content-addressed".
func TestChunker_Hashes(t *testing.T) {
	data := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog "), 50_000)
	chunks, err := ChunkAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for i, c := range chunks {
		want := sha256.Sum256(c.Data)
		if !bytes.Equal(c.Hash, want[:]) {
			t.Fatalf("chunk %d hash mismatch", i)
		}
		if len(c.Hash) != 32 {
			t.Fatalf("chunk %d hash length %d, want 32", i, len(c.Hash))
		}
	}
}

// TestChunker_Reassemble: concatenating all chunk Data in order must
// yield the original input byte-for-byte. Without this, restore is
// broken.
func TestChunker_Reassemble(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefghij"), 200_000)
	chunks, err := ChunkAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	var lastEnd int64
	for i, c := range chunks {
		if c.Offset != lastEnd {
			t.Fatalf("chunk %d has gap: offset=%d, expected=%d", i, c.Offset, lastEnd)
		}
		buf.Write(c.Data)
		lastEnd = c.Offset + int64(len(c.Data))
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("reassembled %d bytes != original %d bytes", buf.Len(), len(data))
	}
}

// TestChunker_EmptyInput: zero bytes in must yield zero chunks out
// without error. The repo layer relies on this for empty files.
func TestChunker_EmptyInput(t *testing.T) {
	chunks, err := ChunkAll(bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("empty input should produce 0 chunks, got %d", len(chunks))
	}
}

// TestChunker_SmallInput: input smaller than the minimum chunk size
// must come back as exactly one chunk. The whole input is the chunk.
func TestChunker_SmallInput(t *testing.T) {
	data := []byte("only a few bytes")
	chunks, err := ChunkAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("small input should produce 1 chunk, got %d", len(chunks))
	}
	if !bytes.Equal(chunks[0].Data, data) {
		t.Fatal("single chunk should contain the entire input")
	}
	want := sha256.Sum256(data)
	if !bytes.Equal(chunks[0].Hash, want[:]) {
		t.Fatal("hash mismatch on single chunk")
	}
	if chunks[0].Offset != 0 {
		t.Fatalf("first chunk offset should be 0, got %d", chunks[0].Offset)
	}
}

// TestChunker_ChunkSizeBounds: average chunk size should be roughly
// in the configured 256 KiB - 4 MiB band. Not a strict check — content-
// defined chunking is probabilistic — just a sanity guard against
// accidentally configuring 1 KiB chunks.
func TestChunker_ChunkSizeBounds(t *testing.T) {
	data := bytes.Repeat([]byte("abcdefghij"), 1_000_000) // 10 MiB
	chunks, err := ChunkAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range chunks {
		// The last chunk may be smaller than min — that's the
		// remainder of the stream and is allowed.
		if i < len(chunks)-1 && len(c.Data) < 256*1024 {
			t.Fatalf("non-final chunk %d under min size: %d bytes", i, len(c.Data))
		}
		if len(c.Data) > 4*1024*1024 {
			t.Fatalf("chunk %d over max size: %d bytes", i, len(c.Data))
		}
	}
}

// TestChunker_ReaderError: a reader that errors mid-stream with a
// non-EOF error must surface the error rather than silently
// truncating. (io.EOF and io.ErrUnexpectedEOF are treated as the
// end of stream by FastCDC, which is correct — those mean "no more
// data" rather than "the reader is broken".)
func TestChunker_ReaderError(t *testing.T) {
	r := &errReader{err: errors.New("simulated I/O failure")}
	if _, err := ChunkAll(r); err == nil {
		t.Fatal("expected error from broken reader, got nil")
	}
}

// TestChunker_AllowsConcurrentReaders guards against the old package
// mutex that serialized the full chunking pipeline. Both readers block
// on their first Read; if ChunkAll still holds a global lock for the
// whole call, the second reader never reaches that first Read.
func TestChunker_AllowsConcurrentReaders(t *testing.T) {
	release := make(chan struct{})
	r1 := newGateReader([]byte("alpha"), release)
	r2 := newGateReader([]byte("bravo"), release)

	errCh := make(chan error, 2)
	go func() {
		_, err := ChunkAll(r1)
		errCh <- err
	}()
	go func() {
		_, err := ChunkAll(r2)
		errCh <- err
	}()

	if !waitAllClosed(2*time.Second, r1.started, r2.started) {
		close(release)
		t.Fatal("both readers did not start; ChunkAll is serialized by a global lock")
	}
	close(release)

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("ChunkAll returned error: %v", err)
		}
	}
}

type errReader struct{ err error }

func (e errReader) Read(_ []byte) (int, error) {
	return 0, e.err
}

type gateReader struct {
	mu      sync.Mutex
	data    []byte
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func newGateReader(data []byte, release <-chan struct{}) *gateReader {
	return &gateReader{
		data:    append([]byte(nil), data...),
		started: make(chan struct{}),
		release: release,
	}
}

func (r *gateReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func waitAllClosed(timeout time.Duration, chans ...<-chan struct{}) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		allClosed := true
		for _, ch := range chans {
			select {
			case <-ch:
			default:
				allClosed = false
			}
		}
		if allClosed {
			return true
		}
		select {
		case <-timer.C:
			return false
		case <-ticker.C:
		}
	}
}

// randomBytes returns n bytes from a deterministic SHA-256 chain
// seeded by seed. We deliberately use a hash chain (not math/rand) so
// the bytes look random to FastCDC's gear hash, the test is fully
// reproducible across machines and Go versions, and gosec doesn't
// flag the (here-fine) use of a weak RNG.
func randomBytes(t *testing.T, n int, seed uint64) []byte {
	t.Helper()
	var seedBytes [8]byte
	binary.BigEndian.PutUint64(seedBytes[:], seed)
	h := sha256.Sum256(seedBytes[:])
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, h[:]...)
		h = sha256.Sum256(h[:])
	}
	return out[:n]
}

// TestChunker_RandomMultiChunk exercises the algorithm on input that
// actually triggers content-defined boundaries.
//
// The smaller, repeating-content tests above pass even when ChunkAll
// returns a single max-size chunk, because uniform content has no
// boundary-defining fingerprints. This test uses 16 MiB of random
// data and asserts: (a) we produce multiple chunks, (b) chunking is
// deterministic, (c) every chunk's hash matches its data, and (d)
// concatenating the chunks reproduces the input byte-for-byte.
func TestChunker_RandomMultiChunk(t *testing.T) {
	const size = 16 << 20 // 16 MiB
	data := randomBytes(t, size, 0xC0DEFEED)

	a, err := ChunkAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) < 4 {
		t.Fatalf("expected multiple chunks for 16 MiB random input, got %d", len(a))
	}

	b, err := ChunkAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("non-deterministic chunk count: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i].Hash, b[i].Hash) {
			t.Fatalf("chunk %d hash mismatch across runs", i)
		}
	}

	var buf bytes.Buffer
	for _, c := range a {
		want := sha256.Sum256(c.Data)
		if !bytes.Equal(c.Hash, want[:]) {
			t.Fatal("hash != sha256(data)")
		}
		buf.Write(c.Data)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("reassembly mismatch: %d vs %d bytes", buf.Len(), len(data))
	}
}

// TestChunker_RandomInsertion_ShiftResistance is the actual shift-
// resistance test. It inserts a single byte at the midpoint of a
// random stream — a true shift, not a modify-in-place — and asserts
// that few new chunks are introduced. This is the property that
// makes content-defined chunking valuable for incremental backups
// of files that grow at the front (e.g. log files prepended to).
func TestChunker_RandomInsertion_ShiftResistance(t *testing.T) {
	const size = 16 << 20
	data := randomBytes(t, size, 0xC0DEFEED)

	mid := size / 2
	mod := make([]byte, 0, size+1)
	mod = append(mod, data[:mid]...)
	mod = append(mod, 0xFF)
	mod = append(mod, data[mid:]...)

	a, err := ChunkAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ChunkAll(bytes.NewReader(mod))
	if err != nil {
		t.Fatal(err)
	}

	original := map[string]struct{}{}
	for _, c := range a {
		original[string(c.Hash)] = struct{}{}
	}

	// Allow up to ~3 new chunks: the chunk straddling the insertion
	// point plus its immediate neighbors. More than that means the
	// algorithm is failing to re-synchronize after the shift.
	maxNew := 3
	newChunks := 0
	for _, c := range b {
		if _, ok := original[string(c.Hash)]; !ok {
			newChunks++
		}
	}
	if newChunks > maxNew {
		t.Fatalf("insertion shift produced %d new chunks (max %d): "+
			"FastCDC is not re-synchronizing", newChunks, maxNew)
	}
	if len(b) < 4 {
		t.Fatalf("expected multiple chunks after insertion, got %d", len(b))
	}
}
