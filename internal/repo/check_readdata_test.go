package repo

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheck_ReadDataDetectsCorruptChunk: a chunk overwritten with
// garbage is invisible to the presence-only check (Stat sees a blob of
// plausible size) but must fail --read-data, which downloads,
// decrypts, and re-hashes every referenced chunk.
func TestCheck_ReadDataDetectsCorruptChunk(t *testing.T) {
	r, store := newTestRepo(t)
	ctx := context.Background()

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), strings.Repeat("payload-", 100))
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	for _, fe := range m.Tree {
		if len(fe.Chunks) > 0 {
			hash = fe.Chunks[0]
			break
		}
	}
	if hash == "" {
		t.Fatal("no chunk to corrupt")
	}
	if err := store.Put(ctx, ChunkKey(hash), bytes.NewReader([]byte("garbage-not-a-sealed-chunk"))); err != nil {
		t.Fatal(err)
	}

	shallow, err := r.Check(ctx, CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !shallow.Healthy() {
		t.Fatalf("presence-only check should not see the corruption: %+v", shallow)
	}

	deep, err := r.Check(ctx, CheckOptions{ReadData: true})
	if err != nil {
		t.Fatal(err)
	}
	if deep.Healthy() {
		t.Fatal("deep check must flag the corrupt chunk as unhealthy")
	}
	if len(deep.CorruptBlobs) != 1 || deep.CorruptBlobs[0].Hash != hash {
		t.Errorf("CorruptBlobs: got %+v, want the corrupted chunk %s", deep.CorruptBlobs, hash)
	}
	if deep.ReadDataBlobs == 0 {
		t.Error("ReadDataBlobs should count the chunks that were deep-verified")
	}
}

// TestCheck_ReadDataSubsetSamples: a fractional subset verifies a
// deterministic strict subset of the referenced chunks — the S3-egress
// cost lever for large repos.
func TestCheck_ReadDataSubsetSamples(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	src := t.TempDir()
	// Distinct content per file → at least 6 distinct chunks.
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		writeFile(t, filepath.Join(src, n+".txt"), strings.Repeat(n+"-content-", 50))
	}
	if _, err := r.CreateSnapshot(ctx, src, SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}

	full, err := r.Check(ctx, CheckOptions{ReadData: true})
	if err != nil {
		t.Fatal(err)
	}
	half, err := r.Check(ctx, CheckOptions{ReadData: true, ReadDataSubset: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if full.ReadDataBlobs < 6 {
		t.Fatalf("full deep check should verify every referenced chunk, got %d", full.ReadDataBlobs)
	}
	if half.ReadDataBlobs == 0 || half.ReadDataBlobs >= full.ReadDataBlobs {
		t.Errorf("subset must verify a non-empty strict subset: %d of %d", half.ReadDataBlobs, full.ReadDataBlobs)
	}
	// Determinism: the same subset twice verifies the same count.
	again, err := r.Check(ctx, CheckOptions{ReadData: true, ReadDataSubset: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if again.ReadDataBlobs != half.ReadDataBlobs {
		t.Errorf("sampling must be deterministic: %d then %d", half.ReadDataBlobs, again.ReadDataBlobs)
	}
}
