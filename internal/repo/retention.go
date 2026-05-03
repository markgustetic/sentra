package repo

import (
	"fmt"
	"sort"
	"time"
)

// RetentionPolicy describes how many snapshots to keep at each
// granularity. All fields are "keep N most recent..."; zero means
// "no rule at this granularity".
//
// The policy is borg-style: a snapshot kept by ANY rule is kept overall.
// The four rules combine via union, not intersection — so a stricter
// rule can't silently drop a snapshot that a looser rule wanted to
// keep. That's the right shape: the user expresses intent additively
// ("keep at least the last N daily" + "and at least M weekly") and the
// planner respects each limit independently.
type RetentionPolicy struct {
	// KeepLast keeps the most recent N snapshots, regardless of date.
	KeepLast int

	// KeepDaily keeps the newest snapshot per calendar day for the
	// last N days. "Day" is the snapshot's CreatedAt in UTC.
	KeepDaily int

	// KeepWeekly keeps the newest snapshot per ISO week for the last
	// N weeks. ISO weeks start Monday and the year boundary follows
	// time.Time.ISOWeek's semantics — important when computing buckets
	// across the December/January transition.
	KeepWeekly int

	// KeepMonthly keeps the newest snapshot per calendar month for the
	// last N months. Bucket key is "YYYY-MM" in UTC.
	KeepMonthly int
}

// PlanRetention returns two slices: the IDs to keep and the IDs to
// drop. The input is assumed to be in any order; the function sorts a
// copy internally so the caller's slice is not mutated.
//
// Algorithm (borg-style):
//  1. Sort newest-first.
//  2. For each rule with non-zero limit: walk newest-to-oldest, take
//     the newest snapshot per bucket (day/week/month), stop after N
//     buckets.
//  3. Union the kept-set from all rules.
//  4. Drop = all IDs not in keep.
//
// Output ordering is deterministic: keep is sorted by CreatedAt
// descending (with ID as tie-break, also descending), and drop is
// sorted the same way over the rest. This matches what the prune CLI
// wants to print: newest first, scripts can rely on stable order.
func PlanRetention(snaps []SnapshotInfo, policy RetentionPolicy) (keep, drop []string) {
	// Defensive copy so callers don't see their slice reordered. Cheap
	// — SnapshotInfo is a small struct.
	sorted := make([]SnapshotInfo, len(snaps))
	copy(sorted, snaps)
	sortNewestFirst(sorted)

	// keepSet collects IDs from all rules' picks. The walking helpers
	// below treat zero limits as no-ops, so we don't need to gate on
	// policy.KeepLast > 0 here — the helper just returns nothing.
	keepSet := make(map[string]struct{}, len(sorted))

	if policy.KeepLast > 0 {
		for i := 0; i < len(sorted) && i < policy.KeepLast; i++ {
			keepSet[sorted[i].ID] = struct{}{}
		}
	}
	collectByBucket(sorted, policy.KeepDaily, dayBucket, keepSet)
	collectByBucket(sorted, policy.KeepWeekly, isoWeekBucket, keepSet)
	collectByBucket(sorted, policy.KeepMonthly, monthBucket, keepSet)

	keep = make([]string, 0, len(keepSet))
	drop = make([]string, 0, len(sorted)-len(keepSet))
	for _, s := range sorted {
		if _, ok := keepSet[s.ID]; ok {
			keep = append(keep, s.ID)
		} else {
			drop = append(drop, s.ID)
		}
	}
	return keep, drop
}

// collectByBucket walks sorted (newest-first) and adds the newest
// snapshot per bucket to keepSet, stopping after limit distinct
// buckets. A zero limit is a no-op so callers can pass policy fields
// directly without gating.
func collectByBucket(
	sorted []SnapshotInfo,
	limit int,
	bucket func(time.Time) string,
	keepSet map[string]struct{},
) {
	if limit <= 0 {
		return
	}
	seen := make(map[string]struct{}, limit)
	for _, s := range sorted {
		b := bucket(s.CreatedAt)
		if _, ok := seen[b]; ok {
			// We've already taken the newest snapshot for this bucket
			// — anything older with the same bucket key is skipped by
			// the rule (other rules may still pick it up via the union).
			continue
		}
		seen[b] = struct{}{}
		keepSet[s.ID] = struct{}{}
		if len(seen) >= limit {
			return
		}
	}
}

// sortNewestFirst sorts in place by CreatedAt descending, with ID
// (descending) as the tie-break. The ID tie-break matches what
// ListSnapshots does so the two functions agree on order when the
// clock has limited resolution.
func sortNewestFirst(snaps []SnapshotInfo) {
	sort.Slice(snaps, func(i, j int) bool {
		if snaps[i].CreatedAt.Equal(snaps[j].CreatedAt) {
			return snaps[i].ID > snaps[j].ID
		}
		return snaps[i].CreatedAt.After(snaps[j].CreatedAt)
	})
}

// dayBucket returns "YYYY-MM-DD" in UTC. Used by KeepDaily so two
// snapshots taken on the same calendar day share a bucket regardless
// of host timezone — the user's intent is "one per day", not "one
// per local-clock-day".
func dayBucket(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// isoWeekBucket returns "<year>-W<week>" derived from time.ISOWeek.
// Note: ISO weeks span calendar years, so the *ISO year* — not the
// Gregorian one — is the right namespace for the bucket key. The
// week is zero-padded to two digits so "2026-W02" sorts before
// "2026-W10" alphabetically (helpful when a downstream tool dumps the
// bucket keys for inspection).
func isoWeekBucket(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

// monthBucket returns "YYYY-MM" in UTC.
func monthBucket(t time.Time) string {
	return t.UTC().Format("2006-01")
}
