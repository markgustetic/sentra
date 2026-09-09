package repo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/markgustetic/sentra/internal/walker"
)

// denySubdir creates root/<name> holding one file and then chmods the
// directory 0o000 so its listing is denied — the shape of a
// TCC-protected folder under ~/Library. Skips where mode bits cannot
// deny (root, Windows). The mode is restored at cleanup so t.TempDir
// can remove the tree.
func denySubdir(t *testing.T, root, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("chmod permission denial is not modeled on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod cannot deny access")
	}
	dir := filepath.Join(root, name)
	writeFile(t, filepath.Join(dir, "hidden.txt"), "unreachable")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	// The walk starts from ResolveRoot's symlink-resolved spelling
	// (/private/var/... for a macOS TempDir), so the reported path
	// carries that prefix; compare against the same spelling.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// skipRecorder is a concurrency-safe OnSkip sink.
type skipRecorder struct {
	mu    sync.Mutex
	paths []string
	errs  []error
}

func (s *skipRecorder) onSkip(path string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paths = append(s.paths, path)
	s.errs = append(s.errs, err)
}

// TestResolveWalkerOptions_PreservesOnSkip pins the rule that the
// zero-value detection looks only at the three tunables: an Options
// carrying nothing but a callback must still carry it out, or every
// caller that leaves the tunables at their defaults loses its skip
// reporting without any error.
func TestResolveWalkerOptions_PreservesOnSkip(t *testing.T) {
	called := false
	cb := func(string, error) { called = true }
	cases := []struct {
		name string
		in   walker.Options
	}{
		{"only OnSkip", walker.Options{OnSkip: cb}},
		{"OnSkip with tunables", walker.Options{IgnoreFile: ".x", OnSkip: cb}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			got := resolveWalkerOptions(tc.in)
			if got.OnSkip == nil {
				t.Fatal("OnSkip dropped")
			}
			got.OnSkip("p", nil)
			if !called {
				t.Fatal("resolved OnSkip is not the caller's callback")
			}
		})
	}
	if got := resolveWalkerOptions(walker.Options{OnSkip: cb}); !got.ExcludeCaches {
		t.Error("an OnSkip-only Options must still take the legacy ExcludeCaches default")
	}
}

// TestCreateSnapshot_ReportsDeniedSubdir: a denied subdirectory is
// dropped from the snapshot, but never silently — the operator's
// callback hears which path, the persisted stats count it, and the
// rest of the tree is still captured. Both callback seats
// (SnapshotOptions.OnSkip and Walker.OnSkip) must fire, because either
// is a legitimate place for a caller to have wired one.
func TestCreateSnapshot_ReportsDeniedSubdir(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "ok.txt"), "visible")
	denied := denySubdir(t, src, "locked")

	var viaSnapshot, viaWalker skipRecorder
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{
		OnSkip: viaSnapshot.onSkip,
		Walker: walker.Options{OnSkip: viaWalker.onSkip},
	})
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	for name, rec := range map[string]*skipRecorder{"SnapshotOptions.OnSkip": &viaSnapshot, "Walker.OnSkip": &viaWalker} {
		if len(rec.paths) != 1 || rec.paths[0] != denied {
			t.Errorf("%s paths = %v, want [%s]", name, rec.paths, denied)
		}
		if len(rec.errs) != 1 || !errors.Is(rec.errs[0], fs.ErrPermission) {
			t.Errorf("%s errs = %v, want fs.ErrPermission", name, rec.errs)
		}
	}
	if snap.Stats.Skipped != 1 {
		t.Errorf("Stats.Skipped = %d, want 1", snap.Stats.Skipped)
	}
	if snap.Stats.Files != 1 {
		t.Errorf("Stats.Files = %d, want 1 (the readable file)", snap.Stats.Files)
	}
	// The count is persisted with the manifest, not just returned:
	// `snapshots --json` and the dashboard read stats from storage.
	m, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Stats.Skipped != 1 {
		t.Errorf("persisted Stats.Skipped = %d, want 1", m.Stats.Skipped)
	}
}

// TestCreateSnapshot_NoSkipsIsZero guards the counter's baseline so a
// clean tree never reports a skip it did not make.
func TestCreateSnapshot_NoSkipsIsZero(t *testing.T) {
	r, _ := newTestRepo(t)
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "ok.txt"), "visible")
	var rec skipRecorder
	snap, err := r.CreateSnapshot(context.Background(), src, SnapshotOptions{OnSkip: rec.onSkip})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Stats.Skipped != 0 || len(rec.paths) != 0 {
		t.Errorf("clean tree: Skipped=%d, callback paths=%v", snap.Stats.Skipped, rec.paths)
	}
}

// TestPlanAndApply_ReportDeniedSubdir: the plan file is JSON and cannot
// carry a callback, so both the plan walk and the apply walks must
// re-attach the caller's OnSkip from SnapshotOptions. Apply walks the
// tree twice (drift validation, then dirs/symlinks); the operator must
// hear about the denied folder once and the stats must count it once.
func TestPlanAndApply_ReportDeniedSubdir(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "ok.txt"), "visible")
	denied := denySubdir(t, src, "locked")

	var planRec skipRecorder
	plan, err := PlanSnapshot(ctx, src, SnapshotOptions{OnSkip: planRec.onSkip})
	if err != nil {
		t.Fatalf("PlanSnapshot: %v", err)
	}
	if len(planRec.paths) != 1 || planRec.paths[0] != denied {
		t.Errorf("plan OnSkip paths = %v, want [%s]", planRec.paths, denied)
	}
	if plan.Stats.Files != 1 {
		t.Errorf("plan files = %d, want 1", plan.Stats.Files)
	}

	var applyRec skipRecorder
	snap, err := r.CreateSnapshotFromPlan(ctx, plan, SnapshotOptions{OnSkip: applyRec.onSkip})
	if err != nil {
		t.Fatalf("CreateSnapshotFromPlan: %v", err)
	}
	if len(applyRec.paths) != 1 || applyRec.paths[0] != denied {
		t.Errorf("apply OnSkip paths = %v, want exactly one report of %s", applyRec.paths, denied)
	}
	if snap.Stats.Skipped != 1 {
		t.Errorf("apply Stats.Skipped = %d, want 1", snap.Stats.Skipped)
	}
}
