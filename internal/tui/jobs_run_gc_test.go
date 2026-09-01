package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/repo"
)

// gcFlowRepo mirrors newFlowRepo but returns the memory store too, so
// tests can plant and probe orphan blobs directly.
func gcFlowRepo(t *testing.T) (*repo.Repo, *blobstore.Memory) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("flow-test-pass"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r, store
}

// TestRunPolicyRetentionPrune_ApplyReclaimsOrphansWhenNothingDrops: the
// TUI job-run mirror of the CLI rule — an apply-mode prune step must
// reclaim orphaned blobs even when retention drops no snapshot, or the
// scheduled surface accumulates uncollectable garbage from crashed
// backups.
func TestRunPolicyRetentionPrune_ApplyReclaimsOrphansWhenNothingDrops(t *testing.T) {
	r, store := gcFlowRepo(t)
	seedTwoSnapshots(t, r)
	const orphanKey = "data/de/deadbeef4444444444444444444444444444444444444444444444444444beef"
	if err := store.Put(context.Background(), orphanKey, strings.NewReader("orphan")); err != nil {
		t.Fatalf("plant orphan: %v", err)
	}

	policy := repo.RetentionPolicy{KeepLast: 5}
	if err := runPolicyRetentionPrune(context.Background(), r, policy, policycfg.PruneApply); err != nil {
		t.Fatalf("runPolicyRetentionPrune: %v", err)
	}

	if _, err := store.Stat(context.Background(), orphanKey); err == nil {
		t.Errorf("orphan blob survived apply-mode policy prune with an empty drop set")
	}
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Errorf("snapshot count changed: got %d, want 2", len(snaps))
	}
}

// TestRunPolicyRetentionPrune_ApplyRefusesGCOnZeroSnapshotStore: zero
// snapshots makes every blob look orphaned — an unattended job run
// against a misconfigured bucket/prefix must not reclaim anything, and
// must not fail the run either.
func TestRunPolicyRetentionPrune_ApplyRefusesGCOnZeroSnapshotStore(t *testing.T) {
	r, store := gcFlowRepo(t)
	const blobKey = "data/de/deadbeef5555555555555555555555555555555555555555555555555555beef"
	if err := store.Put(context.Background(), blobKey, strings.NewReader("not actually garbage")); err != nil {
		t.Fatalf("plant blob: %v", err)
	}

	policy := repo.RetentionPolicy{KeepLast: 1}
	if err := runPolicyRetentionPrune(context.Background(), r, policy, policycfg.PruneApply); err != nil {
		t.Fatalf("zero-snapshot store must be a calm no-op, got: %v", err)
	}

	if _, err := store.Stat(context.Background(), blobKey); err != nil {
		t.Errorf("blob deleted from a zero-snapshot store: %v", err)
	}
}
