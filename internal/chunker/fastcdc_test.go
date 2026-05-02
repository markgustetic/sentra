package chunker

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"testing"
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

type errReader struct{ err error }

func (e errReader) Read(_ []byte) (int, error) {
	return 0, e.err
}
