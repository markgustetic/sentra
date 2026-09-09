package policy

import (
	"context"
	"fmt"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// RetentionFromConfig builds the retention policy every prune planner
// must use: the config's keep counts plus the repo's pin set. The pins
// are not optional decoration — PlanRetentionExplain keeps a pinned
// snapshot only if it is told about it, and DeleteSnapshot refuses one
// with ErrSnapshotPinned, so a planner that forgot the pins turned a
// pinned snapshot in the drop set into a failed run after the backup
// had already succeeded. Five call sites once built this literal by
// hand and one of them forgot; this is the one place it is built. cfg
// nil means ZERO keep counts, not config.Defaults(): a plan built from
// it drops every unpinned snapshot, so a caller without a config (the
// TUI before one is loaded) must treat the result as preview-only and
// never hand it to an apply. The pins still apply either way.
func RetentionFromConfig(ctx context.Context, r *repo.Repo, cfg *config.Config) (repo.RetentionPolicy, error) {
	var policy repo.RetentionPolicy
	if cfg != nil {
		policy = repo.RetentionPolicy{
			KeepLast:    cfg.Retention.KeepLast,
			KeepDaily:   cfg.Retention.KeepDaily,
			KeepWeekly:  cfg.Retention.KeepWeekly,
			KeepMonthly: cfg.Retention.KeepMonthly,
		}
	}
	pins, err := r.Pins(ctx)
	if err != nil {
		return repo.RetentionPolicy{}, fmt.Errorf("load pins: %w", err)
	}
	policy.Pinned = pins
	return policy, nil
}
