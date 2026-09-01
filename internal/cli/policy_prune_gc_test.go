package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/repo"
)

// gcPolicyFixture builds a memory-store repo with n snapshots from one
// source root, returning the repo and the store so tests can plant and
// probe blobs directly.
func gcPolicyFixture(t *testing.T, n int) (*repo.Repo, *blobstore.Memory) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("pw"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	src := t.TempDir()
	for i := 0; i < n; i++ {
		body := strings.Repeat("body", 100+i)
		if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
	}
	return r, store
}

// TestRunPolicyPrune_ApplyReclaimsOrphansWhenRetentionDropsNothing: the
// scheduled-run mirror of the interactive prune rule. A policy in apply
// mode is unattended — if its prune step skips GC whenever retention
// drops nothing, orphans from crashed backups accumulate forever on the
// surface meant to run without an operator watching.
func TestRunPolicyPrune_ApplyReclaimsOrphansWhenRetentionDropsNothing(t *testing.T) {
	r, store := gcPolicyFixture(t, 1)
	const orphanKey = "data/de/deadbeef2222222222222222222222222222222222222222222222222222beef"
	if err := store.Put(context.Background(), orphanKey, strings.NewReader("orphan")); err != nil {
		t.Fatalf("plant orphan: %v", err)
	}

	cfg := config.Defaults()
	cfg.Retention.KeepLast = 5
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	if err := runPolicyPrune(cmd, &out, r, &cfg, policycfg.PruneApply); err != nil {
		t.Fatalf("runPolicyPrune: %v", err)
	}

	if _, err := store.Stat(context.Background(), orphanKey); err == nil {
		t.Errorf("orphan blob survived policy apply prune with an empty drop set")
	}
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(snaps) != 1 {
		t.Errorf("snapshot count changed: got %d, want 1", len(snaps))
	}
}

// TestRunPolicyPrune_ApplyRefusesGCOnZeroSnapshotStore: the dangerous
// condition. Zero snapshots makes every blob look orphaned; a scheduled
// apply against a misconfigured bucket or prefix must not reclaim
// anything, and must stay a calm no-op rather than an error that marks
// the whole policy run failed.
func TestRunPolicyPrune_ApplyRefusesGCOnZeroSnapshotStore(t *testing.T) {
	r, store := gcPolicyFixture(t, 0)
	const blobKey = "data/de/deadbeef3333333333333333333333333333333333333333333333333333beef"
	if err := store.Put(context.Background(), blobKey, strings.NewReader("not actually garbage")); err != nil {
		t.Fatalf("plant blob: %v", err)
	}

	cfg := config.Defaults()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	if err := runPolicyPrune(cmd, &out, r, &cfg, policycfg.PruneApply); err != nil {
		t.Fatalf("zero-snapshot store must be a calm no-op, got: %v", err)
	}

	if _, err := store.Stat(context.Background(), blobKey); err != nil {
		t.Errorf("blob deleted from a zero-snapshot store: %v", err)
	}
}
