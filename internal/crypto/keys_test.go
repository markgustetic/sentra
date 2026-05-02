package crypto

import (
	"bytes"
	"testing"
)

func TestGenerateRepoKey_Length(t *testing.T) {
	k, err := GenerateRepoKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(k) != 32 {
		t.Fatalf("want 32 bytes, got %d", len(k))
	}
}

func TestGenerateRepoKey_Unique(t *testing.T) {
	a, err := GenerateRepoKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateRepoKey()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two calls returned identical bytes — RNG seems broken")
	}
}

func TestGenerateRepoKey_NotZero(t *testing.T) {
	k, err := GenerateRepoKey()
	if err != nil {
		t.Fatal(err)
	}
	zero := make([]byte, 32)
	if bytes.Equal(k, zero) {
		t.Fatal("repo key is all zeros")
	}
}

func TestGenerateSalt_Length(t *testing.T) {
	s, err := GenerateSalt()
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 16 {
		t.Fatalf("want 16 bytes, got %d", len(s))
	}
}

func TestGenerateSalt_Unique(t *testing.T) {
	a, err := GenerateSalt()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateSalt()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two calls returned identical bytes — RNG seems broken")
	}
}

func TestGenerateRepoKey_UsableForSealOpen(t *testing.T) {
	// End-to-end sanity: a freshly generated key actually works with
	// Seal/Open (catches subtle wiring bugs like wrong length or
	// uninitialized buffer).
	k, err := GenerateRepoKey()
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("payload")
	sealed, err := Seal(k, plaintext)
	if err != nil {
		t.Fatalf("seal with generated key: %v", err)
	}
	got, err := Open(k, sealed)
	if err != nil {
		t.Fatalf("open with generated key: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip failed: got %q want %q", got, plaintext)
	}
}
