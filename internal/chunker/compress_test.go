package chunker

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestCompressDecompress_RoundTrip(t *testing.T) {
	in := bytes.Repeat([]byte("hello world "), 1000)
	c, err := Compress(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(c) >= len(in) {
		t.Errorf("compression should shrink redundant data, got %d (input %d)", len(c), len(in))
	}
	out, err := Decompress(c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Fatal("round-trip failed")
	}
}

// Random bytes are nearly incompressible. zstd may add a few bytes of
// framing — that's fine. What matters is that the round-trip is exact:
// compression must never corrupt input that doesn't shrink.
func TestCompressDecompress_IncompressibleInput(t *testing.T) {
	in := make([]byte, 64*1024)
	if _, err := rand.Read(in); err != nil {
		t.Fatal(err)
	}
	c, err := Compress(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decompress(c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Fatal("round-trip failed for random bytes")
	}
}

func TestCompressDecompress_Empty(t *testing.T) {
	c, err := Compress(nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decompress(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty output, got %d bytes", len(out))
	}
}

func TestDecompress_RejectsBadInput(t *testing.T) {
	// Garbage that is definitely not a valid zstd frame.
	garbage := []byte("this is not a zstd frame, definitely not compressed data")
	if _, err := Decompress(garbage); err == nil {
		t.Fatal("expected error decompressing garbage, got nil")
	}
}

// TestDecompressLimit_RejectsOversized covers Phase 5 review I4: the
// shared 8 MiB cap on Decompress is correct for chunks (max 4 MiB
// plaintext) but wrong for manifests, which are unbounded by file
// count. DecompressLimit lets each caller specify its own cap, and
// must enforce it. We compress 10 MiB of zeros (trivially compressible
// to a few hundred bytes, would balloon back to 10 MiB) and verify
// decoding with a 1 MiB cap fails.
func TestDecompressLimit_RejectsOversized(t *testing.T) {
	in := make([]byte, 10<<20) // 10 MiB of zeros
	c, err := Compress(in)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if _, err := DecompressLimit(c, 1<<20); err == nil {
		t.Fatal("expected error from 1 MiB cap on 10 MiB payload, got nil")
	}
	// Sanity: the same payload decompresses fine when the cap is
	// large enough, proving the input itself is valid.
	out, err := DecompressLimit(c, 16<<20)
	if err != nil {
		t.Fatalf("16 MiB cap should succeed: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("decoded len: got %d, want %d", len(out), len(in))
	}
}

// TestDecompressLimit_ChunkSizeBackwardsCompatible verifies that
// passing the same 8 MiB cap as Decompress's hardcoded one yields
// the same behavior, so Decompress is a pure specialization.
func TestDecompressLimit_ChunkSizeBackwardsCompatible(t *testing.T) {
	in := bytes.Repeat([]byte("hello world "), 1000)
	c, err := Compress(in)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	out, err := DecompressLimit(c, 8<<20)
	if err != nil {
		t.Fatalf("DecompressLimit: %v", err)
	}
	if !bytes.Equal(in, out) {
		t.Fatal("round-trip mismatch")
	}
}
