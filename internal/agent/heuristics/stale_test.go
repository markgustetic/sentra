package heuristics

import (
	"context"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/walker"
)

// staleEntry returns a walker.Entry with the given mtime. Path strings
// are inert here — the heuristic looks at MTime only.
func staleEntry(rel string, mtime time.Time) walker.Entry {
	return walker.Entry{
		AbsPath: "/repo/" + rel,
		RelPath: rel,
		MTime:   mtime,
	}
}

// TestStalePaths_FlagsOldFile: a file 400 days old is flagged at the
// default 365-day threshold; a file modified today is not.
func TestStalePaths_FlagsOldFile(t *testing.T) {
	now := time.Now()
	old := now.Add(-400 * 24 * time.Hour)
	in := Input{
		Walked: []walker.Entry{
			staleEntry("old.txt", old),
			staleEntry("new.txt", now),
		},
	}

	h := NewStalePaths()
	got, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Category != "stale_paths" || f.Severity != SeverityInfo {
		t.Errorf("category/severity: got %s/%s", f.Category, f.Severity)
	}
	if f.Target != "/repo/old.txt" {
		t.Errorf("target: got %s, want /repo/old.txt", f.Target)
	}
	// Details carries the actual mtime + a human-readable age in days.
	mtimeOut, ok := f.Details["mtime"].(time.Time)
	if !ok || !mtimeOut.Equal(old) {
		t.Errorf("details mtime: got %v, want %v", f.Details["mtime"], old)
	}
}

// TestStalePaths_HonorsConfigOverride: setting StaleDays=30 turns a
// 60-day-old file into a finding (where the default 365 would not).
func TestStalePaths_HonorsConfigOverride(t *testing.T) {
	now := time.Now()
	mid := now.Add(-60 * 24 * time.Hour)
	in := Input{
		Walked: []walker.Entry{staleEntry("mid.txt", mid)},
		Config: InputConfig{StaleDays: 30},
	}
	h := NewStalePaths()
	got, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
}

// TestStalePaths_NotStaleBelowThreshold: at the default 365-day
// threshold, a 100-day-old file is not flagged.
func TestStalePaths_NotStaleBelowThreshold(t *testing.T) {
	in := Input{
		Walked: []walker.Entry{staleEntry("recent.txt", time.Now().Add(-100*24*time.Hour))},
	}
	h := NewStalePaths()
	got, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %+v", got)
	}
}

// TestStalePaths_Name: heuristic name is "stale_paths".
func TestStalePaths_Name(t *testing.T) {
	if got, want := NewStalePaths().Name(), "stale_paths"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}
