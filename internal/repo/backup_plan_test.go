package repo

import (
	"context"
	"encoding/json"
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
	if len(loaded.Tree) != 2 {
		t.Fatalf("tree: got %d want 2", len(loaded.Tree))
	}
	if loaded.Tree[0].Path != "a.txt" || loaded.Tree[1].Path != "sub/b.txt" {
		t.Fatalf("unexpected tree: %+v", loaded.Tree)
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
