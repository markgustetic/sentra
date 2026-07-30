package repo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveSnapshotID covers the addressing forms operators actually
// type: "latest", a full ID, a unique prefix, and the memorable 8-hex
// suffix — plus the two refusals (ambiguous, unknown).
func TestResolveSnapshotID(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "one")
	s1, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "a.txt"), "two-longer")
	s2, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if got, err := r.ResolveSnapshotID(ctx, "latest"); err != nil || got != s2.ID {
		t.Errorf("latest: got %q err=%v, want %q", got, err, s2.ID)
	}
	if got, err := r.ResolveSnapshotID(ctx, s1.ID); err != nil || got != s1.ID {
		t.Errorf("exact: got %q err=%v, want %q", got, err, s1.ID)
	}
	// The trailing 8-hex is the part humans remember from listings.
	suffix := s1.ID[strings.LastIndex(s1.ID, "-")+1:]
	if got, err := r.ResolveSnapshotID(ctx, suffix); err != nil || got != s1.ID {
		t.Errorf("suffix: got %q err=%v, want %q (suffix %q)", got, err, s1.ID, suffix)
	}
	// A prefix shared by every ID is ambiguous, not first-match.
	if _, err := r.ResolveSnapshotID(ctx, "snap-"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("shared prefix must be ambiguous, got %v", err)
	}
	if _, err := r.ResolveSnapshotID(ctx, "nope-never"); err == nil {
		t.Error("unknown ref must error")
	}
	if _, err := r.ResolveSnapshotID(ctx, ""); err == nil {
		t.Error("empty ref must error")
	}
}
