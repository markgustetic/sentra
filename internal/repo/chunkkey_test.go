package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestChunkKey_Format pins the on-disk chunk-key shape. This is part
// of the storage format: changing the layout requires migrating every
// existing repo, so the test is a tripwire — failing it means a
// genuine format change, not a refactor.
//
// The agent and heuristics packages call ChunkKey directly, so any
// drift between callers is impossible by construction; this test
// guards against an accidental change to the format itself.
func TestChunkKey_Format(t *testing.T) {
	sum := sha256.Sum256([]byte("hello"))
	hexHash := hex.EncodeToString(sum[:])

	got := ChunkKey(hexHash)

	if !strings.HasPrefix(got, DataPrefix) {
		t.Errorf("ChunkKey output %q must start with DataPrefix %q", got, DataPrefix)
	}
	wantPrefix := DataPrefix + hexHash[:2] + "/"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("ChunkKey output %q does not start with %q", got, wantPrefix)
	}
	if !strings.HasSuffix(got, hexHash) {
		t.Errorf("ChunkKey output %q does not end with full hex hash", got)
	}
}

// TestChunkKey_ShortInputUsesSentinelShard guards the safety branch:
// any input shorter than two hex chars (a programmer error, since
// SHA-256 is always 64 hex chars) lands in the "00" sentinel shard
// rather than panicking. This keeps a regression upstream localized
// to misclassified findings rather than a runtime crash.
func TestChunkKey_ShortInputUsesSentinelShard(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: DataPrefix + "00/"},
		{name: "one char", in: "a", want: DataPrefix + "00/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChunkKey(tt.in)
			if got != tt.want {
				t.Errorf("ChunkKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestDataPrefix_Stable pins the prefix value. If you're changing this
// constant, you're changing the on-disk format and need a migration
// strategy for existing repos.
func TestDataPrefix_Stable(t *testing.T) {
	if DataPrefix != "data/" {
		t.Errorf("DataPrefix = %q, want %q", DataPrefix, "data/")
	}
}
