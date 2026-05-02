package crypto

import (
	"bytes"
	"strings"
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

func TestKDFParams_Validate(t *testing.T) {
	tests := []struct {
		name    string
		params  KDFParams
		wantErr string // substring expected; "" means no error
	}{
		{
			name:    "default params are valid",
			params:  DefaultKDFParams(),
			wantErr: "",
		},
		{
			name:    "Time zero rejected",
			params:  KDFParams{Time: 0, Memory: 64 * 1024, Threads: 4, KeyLen: 32},
			wantErr: "Time",
		},
		{
			name:    "Threads zero rejected",
			params:  KDFParams{Time: 3, Memory: 64 * 1024, Threads: 0, KeyLen: 32},
			wantErr: "Threads",
		},
		{
			name:    "KeyLen wrong rejected",
			params:  KDFParams{Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 16},
			wantErr: "KeyLen",
		},
		{
			name:    "Memory zero rejected",
			params:  KDFParams{Time: 3, Memory: 0, Threads: 4, KeyLen: 32},
			wantErr: "Memory",
		},
		{
			name:    "Memory above ceiling rejected",
			params:  KDFParams{Time: 3, Memory: (1 << 24) + 1, Threads: 4, KeyLen: 32},
			wantErr: "Memory",
		},
		{
			name:    "Memory at ceiling allowed",
			params:  KDFParams{Time: 3, Memory: 1 << 24, Threads: 4, KeyLen: 32},
			wantErr: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.params.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
