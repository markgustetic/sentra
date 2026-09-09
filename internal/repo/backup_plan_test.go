package repo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanSnapshot_ReviewableJSON(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha")
	writeFile(t, filepath.Join(root, "sub", "b.txt"), "bravo")

	plan, err := PlanSnapshot(ctx, root, SnapshotOptions{
		Tag:    "review-me",
		Walker: walkerOptionsExcludeCaches(false),
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Version != BackupPlanVersion {
		t.Fatalf("version: got %d want %d", plan.Version, BackupPlanVersion)
	}
	if plan.Tag != "review-me" {
		t.Fatalf("tag: got %q want review-me", plan.Tag)
	}
	if plan.Stats.Files != 2 {
		t.Fatalf("files: got %d want 2", plan.Stats.Files)
	}
	if plan.Stats.Bytes != int64(len("alpha")+len("bravo")) {
		t.Fatalf("bytes: got %d", plan.Stats.Bytes)
	}
	if len(plan.Files) != 2 || plan.Files[0].Path != "a.txt" || plan.Files[1].Path != "sub/b.txt" {
		t.Fatalf("files not sorted/reviewable: %+v", plan.Files)
	}
	if plan.Files[0].Mode != "0600" {
		t.Fatalf("mode should be octal string, got %q", plan.Files[0].Mode)
	}

	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	for _, want := range []string{`"root":`, `"tag": "review-me"`, `"path": "a.txt"`, `"mode": "0600"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("reviewable JSON missing %s:\n%s", want, got)
		}
	}
}

func TestCreateSnapshotFromPlan_RoundTrip(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha")
	writeFile(t, filepath.Join(root, "sub", "b.txt"), "bravo")

	plan, err := PlanSnapshot(ctx, root, SnapshotOptions{Tag: "planned"})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	snap, err := r.CreateSnapshotFromPlan(ctx, plan, SnapshotOptions{})
	if err != nil {
		t.Fatalf("apply plan: %v", err)
	}
	if snap.Tag != "planned" {
		t.Fatalf("snapshot tag: got %q want planned", snap.Tag)
	}

	loaded, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Apply-created snapshots carry the same tree fidelity as direct
	// backups: the `sub` directory entry rides along with the files.
	if len(loaded.Tree) != 3 {
		t.Fatalf("tree: got %d want 3 (2 files + 1 dir)", len(loaded.Tree))
	}
	if loaded.Tree[0].Path != "a.txt" || loaded.Tree[1].Path != "sub" || loaded.Tree[2].Path != "sub/b.txt" {
		t.Fatalf("unexpected tree: %+v", loaded.Tree)
	}
	if !loaded.Tree[1].IsDir() {
		t.Fatalf("tree[1] should be the sub directory entry: %+v", loaded.Tree[1])
	}
}

func TestCreateSnapshotFromPlan_RejectsModifiedFile(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	root := t.TempDir()
	path := filepath.Join(root, "a.txt")
	writeFile(t, path, "alpha")

	plan, err := PlanSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	writeFile(t, path, "changed")

	if _, err := r.CreateSnapshotFromPlan(ctx, plan, SnapshotOptions{}); err == nil {
		t.Fatal("expected apply to reject file drift")
	}
}

func TestCreateSnapshotFromPlan_RejectsAddedFile(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha")

	plan, err := PlanSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	writeFile(t, filepath.Join(root, "b.txt"), "bravo")

	if _, err := r.CreateSnapshotFromPlan(ctx, plan, SnapshotOptions{}); err == nil {
		t.Fatal("expected apply to reject added file drift")
	}
}

// TestCreateSnapshotFromPlan_RefusesUnresolvedRoot: PlanSnapshot writes
// the ResolveRoot form of the root, but the plan file is operator-
// editable JSON, and an older plan or a hand-edited one can carry the
// symlinked spelling. Apply must refuse that rather than snapshot
// under it: CreateSnapshot resolves its root, so a manifest recorded
// under the link would land in a different retention group from every
// direct backup of the same tree, and prune would count them apart.
// A symlink the operator typed and the aliased mount macOS hands out
// under /var both arrive as an unresolved root, so the rule is
// "equals ResolveRoot", not "is not a symlink", and the error names
// both spellings so the operator can fix the plan by hand.
func TestCreateSnapshotFromPlan_RefusesUnresolvedRoot(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	base := t.TempDir()
	real := filepath.Join(base, "real")
	writeFile(t, filepath.Join(real, "a.txt"), "alpha")
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	plan, err := PlanSnapshot(ctx, real, SnapshotOptions{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := r.CreateSnapshotFromPlan(ctx, plan, SnapshotOptions{}); err != nil {
		t.Fatalf("apply of the canonical plan: %v", err)
	}

	canonical := plan.Root
	plan.Root = link
	_, err = r.CreateSnapshotFromPlan(ctx, plan, SnapshotOptions{})
	if !errors.Is(err, ErrBackupPlanRootUnresolved) {
		t.Fatalf("apply with a symlinked root: got %v, want ErrBackupPlanRootUnresolved", err)
	}
	for _, want := range []string{link, canonical} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}
