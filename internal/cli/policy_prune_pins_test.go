package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
)

// TestRunPolicyPrune_ApplyKeepsPinnedSnapshot: a pinned snapshot that
// retention would otherwise drop must be planned as kept, not
// discovered at DeleteSnapshot — ErrSnapshotPinned there is not
// tolerated, so the unattended run failed after its backup succeeded
// and fired on_failure every night.
func TestRunPolicyPrune_ApplyKeepsPinnedSnapshot(t *testing.T) {
	r, _ := gcPolicyFixture(t, 3)
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Oldest first: the one keep_last=1 would drop.
	oldest := snaps[0]
	for _, s := range snaps[1:] {
		if s.CreatedAt.Before(oldest.CreatedAt) {
			oldest = s
		}
	}
	if err := r.Pin(context.Background(), oldest.ID); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.Retention.KeepLast = 1
	cfg.Retention.KeepDaily, cfg.Retention.KeepWeekly, cfg.Retention.KeepMonthly = 0, 0, 0
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	if err := runPolicyPrune(cmd, &out, r, &cfg, policycfg.PruneApply); err != nil {
		t.Fatalf("policy prune must succeed with a pinned snapshot in range: %v", err)
	}

	after, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, s := range after {
		if s.ID == oldest.ID {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("pinned snapshot %s was deleted", oldest.ID)
	}
	if len(after) != 2 {
		t.Fatalf("snapshots after prune: got %d, want 2 (newest + pinned)", len(after))
	}
}
