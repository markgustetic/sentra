package heuristics

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/repo"
)

// newOrphanRepo builds a fresh in-memory repo for the orphan tests.
// Just enough setup to give us a *repo.Repo whose Store() we can poke
// directly to seed orphan blobs.
func newOrphanRepo(t *testing.T) *repo.Repo {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// putBlob writes content under the given key in the repo's store.
// Mirrors what GC walks over: any "data/<aa>/<hex>" key becomes an
// orphan candidate if it's not in LiveBlobs.
func putBlob(t *testing.T, r *repo.Repo, key, content string) {
	t.Helper()
	if err := r.Store().Put(context.Background(), key, bytes.NewReader([]byte(content))); err != nil {
		t.Fatalf("Put %s: %v", key, err)
	}
}

// TestOrphanBlobs_FlagsUnreferencedBlob: a blob in data/ that isn't
// in LiveBlobs becomes a warn finding.
func TestOrphanBlobs_FlagsUnreferencedBlob(t *testing.T) {
	r := newOrphanRepo(t)
	orphanKey := "data/aa/aa" + strings.Repeat("0", 62)
	putBlob(t, r, orphanKey, "orphan content")

	h := NewOrphanBlobs()
	got, err := h.Run(context.Background(), Input{
		Repo:      r,
		LiveBlobs: map[string]struct{}{}, // nothing is live
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Category != "orphan_blobs" || f.Severity != SeverityWarn {
		t.Errorf("category/severity: got %s/%s", f.Category, f.Severity)
	}
	if f.Target != orphanKey {
		t.Errorf("target: got %s, want %s", f.Target, orphanKey)
	}
}

// TestOrphanBlobs_NoFindingWhenLive: when the same blob is in
// LiveBlobs, it's NOT flagged.
func TestOrphanBlobs_NoFindingWhenLive(t *testing.T) {
	r := newOrphanRepo(t)
	liveKey := "data/bb/bb" + strings.Repeat("1", 62)
	putBlob(t, r, liveKey, "live content")

	h := NewOrphanBlobs()
	got, err := h.Run(context.Background(), Input{
		Repo:      r,
		LiveBlobs: map[string]struct{}{liveKey: {}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %+v", got)
	}
}

// TestOrphanBlobs_MixedSet: two live, one orphan → exactly one
// finding for the orphan, deterministic ID stamping.
func TestOrphanBlobs_MixedSet(t *testing.T) {
	r := newOrphanRepo(t)
	live1 := "data/aa/aa" + strings.Repeat("0", 62)
	live2 := "data/bb/bb" + strings.Repeat("1", 62)
	orphan := "data/cc/cc" + strings.Repeat("2", 62)
	putBlob(t, r, live1, "1")
	putBlob(t, r, live2, "2")
	putBlob(t, r, orphan, "3")

	h := NewOrphanBlobs()
	got, err := h.Run(context.Background(), Input{
		Repo: r,
		LiveBlobs: map[string]struct{}{
			live1: {},
			live2: {},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Target != orphan {
		t.Errorf("target: got %s, want %s", got[0].Target, orphan)
	}
}

// TestOrphanBlobs_NilRepo: when Input.Repo is nil (e.g. registry was
// fed by a caller that doesn't have a repo handy), the heuristic
// silently no-ops rather than panicking. This matches how other
// heuristics treat missing inputs.
func TestOrphanBlobs_NilRepo(t *testing.T) {
	h := NewOrphanBlobs()
	got, err := h.Run(context.Background(), Input{Repo: nil})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %+v", got)
	}
}

// TestOrphanBlobs_Name: heuristic name is "orphan_blobs".
func TestOrphanBlobs_Name(t *testing.T) {
	if got, want := NewOrphanBlobs().Name(), "orphan_blobs"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}
