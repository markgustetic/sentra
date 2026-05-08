package heuristics

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/repo"
)

// makeSnaps builds n snapshots, each one day apart. ID is "snap-N"
// for readability; CreatedAt walks backwards from a fixed base so
// PlanRetention's newest-first ordering is deterministic in the tests.
func makeSnaps(n int) []repo.SnapshotInfo {
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	out := make([]repo.SnapshotInfo, n)
	for i := 0; i < n; i++ {
		out[i] = repo.SnapshotInfo{
			ID:        fmt.Sprintf("snap-%02d", i),
			CreatedAt: base.Add(-time.Duration(i) * 24 * time.Hour),
		}
	}
	return out
}

// TestRetentionDrift_FindsDrift: 10 snapshots with KeepLast=3 produces
// a finding with would_drop=7, would_keep=3, and the drop_ids list
// populated.
func TestRetentionDrift_FindsDrift(t *testing.T) {
	snaps := makeSnaps(10)
	in := Input{
		Snapshots: snaps,
		Config:    InputConfig{Retention: repo.RetentionPolicy{KeepLast: 3}},
	}
	h := NewRetentionDrift()
	got, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Category != "retention_drift" || f.Severity != SeverityInfo {
		t.Errorf("category/severity: got %s/%s", f.Category, f.Severity)
	}
	if f.Target != "policy" {
		t.Errorf("target: got %s, want policy", f.Target)
	}
	if drop, _ := f.Details["would_drop"].(int); drop != 7 {
		t.Errorf("would_drop: got %v, want 7", f.Details["would_drop"])
	}
	if keep, _ := f.Details["would_keep"].(int); keep != 3 {
		t.Errorf("would_keep: got %v, want 3", f.Details["would_keep"])
	}
	dropIDs, _ := f.Details["drop_ids"].([]string)
	if len(dropIDs) != 7 {
		t.Errorf("drop_ids: got %d entries, want 7 (%v)", len(dropIDs), dropIDs)
	}
	// drop_ids should not include any of the kept (newest 3) IDs.
	slices.Sort(dropIDs)
	kept := map[string]struct{}{"snap-00": {}, "snap-01": {}, "snap-02": {}}
	for _, id := range dropIDs {
		if _, isKept := kept[id]; isKept {
			t.Errorf("drop_ids contains kept ID %q", id)
		}
	}
}

// TestRetentionDrift_NoDrift: 3 snapshots with KeepLast=10 → policy
// keeps everything → no finding.
func TestRetentionDrift_NoDrift(t *testing.T) {
	snaps := makeSnaps(3)
	in := Input{
		Snapshots: snaps,
		Config:    InputConfig{Retention: repo.RetentionPolicy{KeepLast: 10}},
	}
	h := NewRetentionDrift()
	got, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %+v", got)
	}
}

// TestRetentionDrift_ZeroPolicy: zero RetentionPolicy keeps every
// snapshot, so even with snapshots present, no drift is reported.
// This is the documented "no policy configured" no-op.
func TestRetentionDrift_ZeroPolicy(t *testing.T) {
	snaps := makeSnaps(50)
	in := Input{
		Snapshots: snaps,
		Config:    InputConfig{Retention: repo.RetentionPolicy{}},
	}
	h := NewRetentionDrift()
	got, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings on zero policy, got %+v", got)
	}
}

// TestRetentionDrift_NoSnapshots: empty snapshot list → no finding,
// no error. (Nothing to drop, regardless of policy.)
func TestRetentionDrift_NoSnapshots(t *testing.T) {
	in := Input{
		Snapshots: nil,
		Config:    InputConfig{Retention: repo.RetentionPolicy{KeepLast: 3}},
	}
	h := NewRetentionDrift()
	got, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings on empty snapshots, got %+v", got)
	}
}

// TestRetentionDrift_Name: heuristic name is "retention_drift".
func TestRetentionDrift_Name(t *testing.T) {
	if got, want := NewRetentionDrift().Name(), "retention_drift"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}
