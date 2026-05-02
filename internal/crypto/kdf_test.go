package crypto

import (
	"bytes"
	"testing"
)

func TestDeriveKEK_Deterministic(t *testing.T) {
	salt := []byte("0123456789abcdef")
	k1 := DeriveKEK([]byte("hunter2"), salt, DefaultKDFParams())
	k2 := DeriveKEK([]byte("hunter2"), salt, DefaultKDFParams())
	if !bytes.Equal(k1, k2) {
		t.Fatal("KDF must be deterministic")
	}
	if len(k1) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(k1))
	}
}

func TestDeriveKEK_DifferentSalt(t *testing.T) {
	k1 := DeriveKEK([]byte("hunter2"), []byte("0123456789abcdef"), DefaultKDFParams())
	k2 := DeriveKEK([]byte("hunter2"), []byte("fedcba9876543210"), DefaultKDFParams())
	if bytes.Equal(k1, k2) {
		t.Fatal("different salt must produce different keys")
	}
}

func TestDeriveKEK_DifferentPassphrase(t *testing.T) {
	salt := []byte("0123456789abcdef")
	k1 := DeriveKEK([]byte("hunter2"), salt, DefaultKDFParams())
	k2 := DeriveKEK([]byte("correcthorse"), salt, DefaultKDFParams())
	if bytes.Equal(k1, k2) {
		t.Fatal("different passphrase must produce different keys")
	}
}

func TestDefaultKDFParams_MatchesDesign(t *testing.T) {
	p := DefaultKDFParams()
	if p.Time != 3 {
		t.Errorf("Time: got %d, want 3", p.Time)
	}
	if p.Memory != 64*1024 {
		t.Errorf("Memory: got %d, want %d", p.Memory, 64*1024)
	}
	if p.Threads != 4 {
		t.Errorf("Threads: got %d, want 4", p.Threads)
	}
	if p.KeyLen != 32 {
		t.Errorf("KeyLen: got %d, want 32", p.KeyLen)
	}
}
