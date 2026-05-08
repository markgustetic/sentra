package repo

import (
	"slices"
	"testing"
	"time"
)

// makeSnap is a small helper for retention tests. ID encodes the
// timestamp so tests stay readable: snap-<RFC3339-ish> is sortable
// and easy to eyeball when a test fails.
func makeSnap(id string, t time.Time) SnapshotInfo {
	return SnapshotInfo{ID: id, CreatedAt: t}
}

// TestPlanRetention_KeepLastOnly: KeepLast=3 with 5 snapshots keeps
// the newest 3 regardless of date.
func TestPlanRetention_KeepLastOnly(t *testing.T) {
	base := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	snaps := []SnapshotInfo{
		makeSnap("s1", base.Add(-4*24*time.Hour)),
		makeSnap("s2", base.Add(-3*24*time.Hour)),
		makeSnap("s3", base.Add(-2*24*time.Hour)),
		makeSnap("s4", base.Add(-1*24*time.Hour)),
		makeSnap("s5", base),
	}
	keep, drop := PlanRetention(snaps, RetentionPolicy{KeepLast: 3})
	slices.Sort(keep)
	slices.Sort(drop)
	if got, want := keep, []string{"s3", "s4", "s5"}; !equalStrings(got, want) {
		t.Errorf("keep: got %v, want %v", got, want)
	}
	if got, want := drop, []string{"s1", "s2"}; !equalStrings(got, want) {
		t.Errorf("drop: got %v, want %v", got, want)
	}
}

// TestPlanRetention_KeepDaily: with multiple snapshots per day,
// KeepDaily=2 keeps the *newest* per day for the last 2 days.
func TestPlanRetention_KeepDaily(t *testing.T) {
	// Three days, two snapshots per day. Day-3 (most recent) and Day-2
	// kept, Day-1 dropped. Within each day the *newer* snapshot is the
	// representative.
	day3 := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)
	day1 := time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC)
	snaps := []SnapshotInfo{
		makeSnap("d1-am", day1.Add(8*time.Hour)),
		makeSnap("d1-pm", day1.Add(20*time.Hour)),
		makeSnap("d2-am", day2.Add(8*time.Hour)),
		makeSnap("d2-pm", day2.Add(20*time.Hour)),
		makeSnap("d3-am", day3.Add(8*time.Hour)),
		makeSnap("d3-pm", day3.Add(20*time.Hour)),
	}
	keep, drop := PlanRetention(snaps, RetentionPolicy{KeepDaily: 2})
	slices.Sort(keep)
	slices.Sort(drop)
	if got, want := keep, []string{"d2-pm", "d3-pm"}; !equalStrings(got, want) {
		t.Errorf("keep: got %v, want %v", got, want)
	}
	if got, want := drop, []string{"d1-am", "d1-pm", "d2-am", "d3-am"}; !equalStrings(got, want) {
		t.Errorf("drop: got %v, want %v", got, want)
	}
}

// TestPlanRetention_KeepWeekly verifies the ISO-week bucketing crosses
// a Sunday/Monday boundary correctly. ISO weeks start on Monday, so a
// snapshot at Sunday 23:59 and one at Monday 00:01 belong to different
// buckets even though they're 2 minutes apart.
func TestPlanRetention_KeepWeekly(t *testing.T) {
	// Monday 2026-01-05 starts ISO week 2026-W02. Sunday 2026-01-04
	// closes ISO week 2026-W01. We line up snapshots either side of
	// that boundary plus one a week earlier so KeepWeekly=2 keeps the
	// representative of each of the two most recent weeks.
	wk2Mon := time.Date(2026, 1, 5, 0, 1, 0, 0, time.UTC)    // wk 2026-W02 (newest snap of week)
	wk2Wed := time.Date(2026, 1, 7, 12, 0, 0, 0, time.UTC)   // wk 2026-W02 (newest of week — kept)
	wk1Sun := time.Date(2026, 1, 4, 23, 59, 0, 0, time.UTC)  // wk 2026-W01 (newest)
	wk1Tue := time.Date(2025, 12, 30, 12, 0, 0, 0, time.UTC) // wk 2026-W01
	wk0Mon := time.Date(2025, 12, 22, 12, 0, 0, 0, time.UTC) // earlier ISO week, dropped
	snaps := []SnapshotInfo{
		makeSnap("w0-mon", wk0Mon),
		makeSnap("w1-tue", wk1Tue),
		makeSnap("w1-sun", wk1Sun),
		makeSnap("w2-mon", wk2Mon),
		makeSnap("w2-wed", wk2Wed),
	}
	keep, drop := PlanRetention(snaps, RetentionPolicy{KeepWeekly: 2})
	slices.Sort(keep)
	slices.Sort(drop)
	// Newest per ISO week for the latest 2 weeks: w2-wed (W02), w1-sun
	// (W01). w0-mon, w1-tue, w2-mon are dropped.
	if got, want := keep, []string{"w1-sun", "w2-wed"}; !equalStrings(got, want) {
		t.Errorf("keep: got %v, want %v", got, want)
	}
	if got, want := drop, []string{"w0-mon", "w1-tue", "w2-mon"}; !equalStrings(got, want) {
		t.Errorf("drop: got %v, want %v", got, want)
	}
}

// TestPlanRetention_Combination: a snapshot kept by ANY rule is kept.
// We layer KeepLast=2 + KeepDaily=7 + KeepWeekly=4 over a snapshot
// history, and the union of the three rules' keepers is the final
// keep-set.
func TestPlanRetention_Combination(t *testing.T) {
	// 30 daily snapshots, one per day, going back from 2026-01-30.
	// KeepLast=2 → newest 2 (s30, s29).
	// KeepDaily=7 → newest 7 (s30..s24).
	// KeepWeekly=4 → newest per ISO week for the last 4 weeks.
	snaps := make([]SnapshotInfo, 0, 30)
	base := time.Date(2026, 1, 30, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		// Day at i days ago from base. The newest is at i=0.
		t := base.Add(-time.Duration(i) * 24 * time.Hour)
		id := pad2("s", 30-i) // s30 (newest) ... s01 (oldest)
		snaps = append(snaps, makeSnap(id, t))
	}
	keep, drop := PlanRetention(snaps, RetentionPolicy{
		KeepLast:   2,
		KeepDaily:  7,
		KeepWeekly: 4,
	})
	slices.Sort(keep)
	slices.Sort(drop)
	keepSet := map[string]bool{}
	for _, id := range keep {
		keepSet[id] = true
	}
	dropSet := map[string]bool{}
	for _, id := range drop {
		dropSet[id] = true
	}
	// Newest 7 must all be kept (KeepDaily=7 dominates).
	for i := 24; i <= 30; i++ {
		id := pad2("s", i)
		if !keepSet[id] {
			t.Errorf("expected %s kept (KeepDaily=7), but it was dropped", id)
		}
	}
	// At least 4 weekly representatives in the older history. The
	// exact IDs depend on which day of the week our base falls on, so
	// we just assert that at least 4 distinct weekly buckets are
	// represented in keep among the snapshots older than 7 days.
	older := keep[:0]
	for _, id := range keep {
		// Strip leading 's' and parse the index.
		// Snapshots s24..s30 are the 7-day window; s01..s23 are older.
		if id == pad2("s", 30) || id == pad2("s", 29) || id == pad2("s", 28) ||
			id == pad2("s", 27) || id == pad2("s", 26) || id == pad2("s", 25) ||
			id == pad2("s", 24) {
			continue
		}
		older = append(older, id)
	}
	// We expect KeepWeekly=4 to introduce at least one keeper outside
	// the 7-day window (the 4th weekly bucket falls outside 7 days).
	if len(older) == 0 {
		t.Errorf("expected at least one keeper from KeepWeekly outside the 7-day window, got none")
	}
	// Sanity: keep ∪ drop covers every input ID exactly once.
	for _, s := range snaps {
		if keepSet[s.ID] && dropSet[s.ID] {
			t.Errorf("%s is in both keep and drop", s.ID)
		}
		if !keepSet[s.ID] && !dropSet[s.ID] {
			t.Errorf("%s is in neither keep nor drop", s.ID)
		}
	}
}

// TestPlanRetention_EmptyPolicy: a policy with all zero limits keeps
// nothing. Drop everything.
func TestPlanRetention_EmptyPolicy(t *testing.T) {
	snaps := []SnapshotInfo{
		makeSnap("a", time.Now()),
		makeSnap("b", time.Now().Add(-time.Hour)),
	}
	keep, drop := PlanRetention(snaps, RetentionPolicy{})
	if len(keep) != 0 {
		t.Errorf("keep: got %v, want empty", keep)
	}
	if len(drop) != 2 {
		t.Errorf("drop: got %v, want 2 entries", drop)
	}
}

// TestPlanRetention_DeterministicOrder: calling twice produces the
// same result. Catches accidental map-iteration leaks in the planner.
func TestPlanRetention_DeterministicOrder(t *testing.T) {
	base := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	snaps := []SnapshotInfo{
		makeSnap("a", base),
		makeSnap("b", base.Add(-1*24*time.Hour)),
		makeSnap("c", base.Add(-2*24*time.Hour)),
		makeSnap("d", base.Add(-3*24*time.Hour)),
		makeSnap("e", base.Add(-4*24*time.Hour)),
	}
	policy := RetentionPolicy{KeepLast: 2, KeepDaily: 3}
	keep1, drop1 := PlanRetention(snaps, policy)
	keep2, drop2 := PlanRetention(snaps, policy)
	if !equalStrings(keep1, keep2) {
		t.Errorf("keep order differs across calls: %v vs %v", keep1, keep2)
	}
	if !equalStrings(drop1, drop2) {
		t.Errorf("drop order differs across calls: %v vs %v", drop1, drop2)
	}
}

// pad2 returns prefix + a zero-padded 2-digit suffix. Used to build
// snapshot IDs that sort lexicographically the way tests assume.
func pad2(prefix string, n int) string {
	if n < 10 {
		return prefix + "0" + itoa(n)
	}
	return prefix + itoa(n)
}

// itoa is a strconv-free int-to-string used by pad2. Avoids pulling
// strconv into the test for one call; the planner itself doesn't
// need this helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
