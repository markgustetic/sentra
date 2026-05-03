package heuristics

import (
	"context"

	"github.com/markgustetic/sentra/internal/repo"
)

// RetentionDrift surfaces snapshot history that would be pruned by
// the configured retention policy. It's a passive signal — the agent
// is telling the user "your policy says drop these N snapshots, but
// they're still here." Acting on the finding is a separate step (the
// `prune` CLI from Phase 8).
//
// Severity is "info" because retention drift isn't a problem on its
// own — it's normal between scheduled prunes. The agent calls it out
// so the LLM can decide whether to recommend a prune run.
type RetentionDrift struct{}

// NewRetentionDrift constructs a RetentionDrift heuristic.
func NewRetentionDrift() *RetentionDrift { return &RetentionDrift{} }

// Name is the registry-visible name of this heuristic.
func (rd *RetentionDrift) Name() string { return "retention_drift" }

// Run feeds Input.Snapshots through repo.PlanRetention with the
// configured policy. If any snapshots would be dropped, it emits one
// info finding aggregating the drop count, keep count, and full list
// of drop IDs in Details.
//
// IMPORTANT: PlanRetention treats every zero limit as a no-op rule.
// A fully-zero RetentionPolicy therefore *drops every snapshot* —
// the union of zero rules picks nothing. That's a footgun if the
// user hasn't configured retention at all, so the heuristic gates
// on "is at least one policy field non-zero" before reporting drift.
// Result: a user with no policy gets no findings; a user with a
// configured policy gets accurate drift output.
func (rd *RetentionDrift) Run(ctx context.Context, in Input) ([]Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !policyConfigured(in.Config.Retention) {
		return nil, nil
	}
	keep, drop := repo.PlanRetention(in.Snapshots, in.Config.Retention)
	if len(drop) == 0 {
		return nil, nil
	}
	return []Finding{{
		ID:       makeFindingID("retention_drift", "policy"),
		Category: "retention_drift",
		Severity: SeverityInfo,
		Target:   "policy",
		Details: map[string]any{
			"would_drop": len(drop),
			"would_keep": len(keep),
			"drop_ids":   drop,
		},
	}}, nil
}

// policyConfigured reports whether at least one retention rule has a
// non-zero limit. The all-zeros case is treated as "user has not set
// a retention policy" — see Run for why this matters.
func policyConfigured(p repo.RetentionPolicy) bool {
	return p.KeepLast > 0 || p.KeepDaily > 0 || p.KeepWeekly > 0 || p.KeepMonthly > 0
}
