package heuristics

import (
	"context"
	"time"
)

// DefaultStaleDays is the default age threshold for stale_paths.
// 365 days matches the design doc's "untouched > N days" guidance
// and is the right starting point for "I haven't looked at this in
// a year, do I still need it?" prompts.
const DefaultStaleDays = 365

// StalePaths flags walked files whose mtime is older than the
// configured threshold. The intent is *informational*: stale files
// aren't necessarily wrong, but the agent calls them out so the user
// can decide whether to keep backing them up or move them out of
// the snapshot scope.
//
// The clock used for "now" is captured once at Run time so all
// findings within a single run agree on the cutoff.
type StalePaths struct{}

// NewStalePaths constructs a StalePaths heuristic.
func NewStalePaths() *StalePaths { return &StalePaths{} }

// Name is the registry-visible name of this heuristic.
func (s *StalePaths) Name() string { return "stale_paths" }

// Run emits one info finding per walked file older than the
// configured StaleDays. Threshold falls back to DefaultStaleDays
// when InputConfig.StaleDays is zero.
func (s *StalePaths) Run(ctx context.Context, in Input) ([]Finding, error) {
	days := in.Config.StaleDays
	if days <= 0 {
		days = DefaultStaleDays
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	var out []Finding
	for _, e := range in.Walked {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !e.MTime.Before(cutoff) {
			continue
		}
		out = append(out, Finding{
			ID:       makeFindingID("stale_paths", e.AbsPath),
			Category: "stale_paths",
			Severity: SeverityInfo,
			Target:   e.AbsPath,
			Details: map[string]any{
				"mtime":     e.MTime,
				"threshold": days,
			},
		})
	}
	return out, nil
}
