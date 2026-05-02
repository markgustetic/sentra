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
