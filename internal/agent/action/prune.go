package action

import (
	"context"
	"errors"
	"fmt"

	"github.com/markgustetic/sentra/internal/repo"
)

// PruneSnapshotHandler implements the prune_snapshot verb: delete
// a snapshot's manifest, then run GC to reclaim chunks that were
// referenced only by the now-deleted manifest.
//
// The dispatcher (cli/agent.go) is responsible for the "wipe guard"
// — refusing to apply a prune that would empty the repo unless
// --allow-wipe was passed. PruneSnapshotHandler does not re-check;
// the safety rail belongs in the layer that has the full context of
// the running --apply session.
type PruneSnapshotHandler struct{}

// Name returns the verb the LLM emits for this handler.
func (PruneSnapshotHandler) Name() Action { return PruneSnapshot }

// Description goes into the system prompt fragment.
func (PruneSnapshotHandler) Description() string {
	return "delete a snapshot by ID; GC then reclaims chunks no other snapshot references"
}

// Apply deletes the manifest and runs GC. ErrEmptyRepo from GC
// (we just deleted the last snapshot) is treated as success, not
// a failure — the prune itself succeeded even though there's
// nothing left to GC against.
func (PruneSnapshotHandler) Apply(
	ctx context.Context,
	env Env,
	id, target, _, _ string,
) error {
	if env.Repo == nil {
		return fmt.Errorf("prune_snapshot: no repo configured")
	}
	if err := env.Repo.DeleteSnapshot(ctx, target); err != nil {
		return fmt.Errorf("delete snapshot: %w", err)
	}
	stats, err := env.Repo.GC(ctx, nil)
	if err != nil {
		// repo.ErrEmptyRepo means we just deleted the last snapshot —
		// that's a successful prune, not a failure.
		if !errors.Is(err, repo.ErrEmptyRepo) {
			return fmt.Errorf("gc: %w", err)
		}
		fmt.Fprintf(env.Stdout, "  - %s: pruned %s (last snapshot; nothing to GC)\n",
			id, target)
		return nil
	}
	fmt.Fprintf(env.Stdout, "  - %s: pruned %s (reclaimed %d blobs, %s)\n",
		id, target, stats.DeletedBlobs, env.formatBytes(stats.DeletedBytes))
	return nil
}
