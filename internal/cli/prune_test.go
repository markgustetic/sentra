package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// pruneFixture builds PruneDeps backed by a memory store with N
// snapshots already created. Each snapshot has its OWN root tree
// containing one unique file, so dropping a snapshot means GC has
// work to do (the unique file's chunks become orphaned).
//
// We use distinct root dirs rather than mutating a shared dir because
// snapshot N+1 would otherwise include snapshot N's file (cumulative
// trees), which means even after dropping the older snapshot the
// blobs survive — making it impossible to assert "blob count went
// down" for the apply test.
//
// Returns the deps, the IDs in creation order (oldest-first), and a
// stdout buffer wired into deps.
func pruneFixture(t *testing.T, passphrase string, n int) (PruneDeps, *blobstore.Memory, []string, *bytes.Buffer) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(passphrase))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer r.Close()

	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		// Fresh per-snapshot root with one unique file. The chunker
		// dedupes across snapshots, but distinct content means each
		// snapshot's chunks are unique to it — exactly what GC needs
		// to reap something interesting.
		root := t.TempDir()
		fname := filepath.Join(root, "f"+string(rune('a'+i))+".txt")
		body := strings.Repeat("body-"+string(rune('a'+i))+"-", 200)
		if err := os.WriteFile(fname, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		s, err := r.CreateSnapshot(context.Background(), root, repo.SnapshotOptions{Tag: "t" + string(rune('a'+i))})
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		ids = append(ids, s.ID)
	}

	out := &bytes.Buffer{}
	deps := PruneDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte(passphrase), nil },
		Stdout:     out,
		// Tests skip the interactive confirm by default — the --yes
		// flag bypasses the Confirm callback entirely.
		Confirm: func(string) (bool, error) {
			t.Fatal("Confirm should not be called when --yes is used or in dry-run")
			return false, nil
		},
	}
	return deps, store, ids, out
}

// TestPrune_DryRun: prune --keep-last=1 without --apply prints what
// would be dropped but doesn't actually delete anything. ListSnapshots
// after the run should still show every snapshot.
func TestPrune_DryRun(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	deps, store, ids, out := pruneFixture(t, "hunter2", 3)
	cmd := NewPrune(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--keep-last", "1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got := out.String()
	// Output should mention the would-drop IDs (the two oldest).
	if !strings.Contains(got, ids[0]) {
		t.Errorf("expected oldest ID %s in dry-run output, got %q", ids[0], got)
	}
	if !strings.Contains(got, ids[1]) {
		t.Errorf("expected ID %s in dry-run output, got %q", ids[1], got)
	}
	// Output should NOT mention "deleted" or "freed" — that's apply-
	// mode language. We allow "would" instead.
	gotLower := strings.ToLower(got)
	if !strings.Contains(gotLower, "would") && !strings.Contains(gotLower, "dry") {
		t.Errorf("expected dry-run hint (would/dry) in output, got %q", got)
	}

	// Snapshots are intact in the store.
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(snaps) != 3 {
		t.Errorf("dry-run mutated repo: expected 3 snapshots, got %d", len(snaps))
	}
}

// TestPrune_Apply: --apply --yes actually deletes the dropped
// snapshots and runs GC. After, only the kept snapshot remains and
// the data/ blob count went down.
func TestPrune_Apply(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	deps, store, ids, out := pruneFixture(t, "hunter2", 3)
	// pruneFixture's confirm callback panics; --yes should skip it.
	cmd := NewPrune(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--keep-last", "1", "--apply", "--yes"})

	beforeBlobs, err := store.List(context.Background(), "data/")
	if err != nil {
		t.Fatalf("list before: %v", err)
	}

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got := out.String()
	if !strings.Contains(strings.ToLower(got), "deleted") {
		t.Errorf("expected 'deleted' in apply-mode output, got %q", got)
	}

	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot after prune, got %d", len(snaps))
	}
	// The newest of the original three is the one that survived.
	if snaps[0].ID != ids[2] {
		t.Errorf("unexpected survivor: got %s, want %s (newest)", snaps[0].ID, ids[2])
	}

	afterBlobs, err := store.List(context.Background(), "data/")
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(afterBlobs) >= len(beforeBlobs) {
		t.Errorf("expected blob count drop after prune, before=%d after=%d",
			len(beforeBlobs), len(afterBlobs))
	}
}

// TestPrune_NothingToDelete: with KeepLast >= snapshot count nothing
// is dropped. The command exits 0 and prints a "nothing to delete"
// message.
func TestPrune_NothingToDelete(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	deps, store, _, out := pruneFixture(t, "hunter2", 2)
	cmd := NewPrune(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--keep-last", "5", "--apply", "--yes"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got := strings.ToLower(out.String())
	if !strings.Contains(got, "nothing") {
		t.Errorf("expected 'nothing' in no-op output, got %q", out.String())
	}

	// Snapshot count unchanged.
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(snaps) != 2 {
		t.Errorf("snapshot count changed unexpectedly: got %d, want 2", len(snaps))
	}
}

// TestPrune_RefusesWipeAllWithoutAllFlag: explicit --keep-*=0 with
// --apply --yes would otherwise wipe the repo silently. The safety
// rail forces the user to acknowledge with --all.
func TestPrune_RefusesWipeAllWithoutAllFlag(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	deps, store, _, out := pruneFixture(t, "hunter2", 3)
	cmd := NewPrune(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--keep-last", "0",
		"--keep-daily", "0",
		"--keep-weekly", "0",
		"--keep-monthly", "0",
		"--apply", "--yes",
	})

	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected error for wipe-all without --all, got nil; output=%q", out.String())
	} else if !strings.Contains(err.Error(), "--all") {
		t.Errorf("error should mention --all, got %v", err)
	}

	// Repo state must be unchanged.
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(snaps) != 3 {
		t.Errorf("snapshot count must be unchanged when guard fires: got %d, want 3", len(snaps))
	}
}

// TestPrune_AllFlagAllowsWipe: with --all explicitly passed, the
// guard releases and the repo is wiped as the user requested.
func TestPrune_AllFlagAllowsWipe(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	deps, store, _, out := pruneFixture(t, "hunter2", 3)
	cmd := NewPrune(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--keep-last", "0",
		"--keep-daily", "0",
		"--keep-weekly", "0",
		"--keep-monthly", "0",
		"--apply", "--yes", "--all",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute with --all: %v", err)
	}

	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 0 {
		t.Errorf("expected 0 snapshots after wipe, got %d", len(snaps))
	}
}

// TestPrune_RegisteredOnRoot verifies users see `prune` in --help.
func TestPrune_RegisteredOnRoot(t *testing.T) {
	deps := PruneDeps{
		NewStore:   func(context.Context, *config.Config) (blobstore.Store, error) { return blobstore.NewMemory(), nil },
		Passphrase: func() ([]byte, error) { return []byte("x"), nil },
		Stdout:     io.Discard,
		Confirm:    func(string) (bool, error) { return true, nil },
	}
	root := NewRoot("v", "c", "d")
	root.AddCommand(NewPrune(deps))
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "prune" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("prune command not registered on root")
	}
}

// TestPrune_ConfirmDecline: in apply-mode without --yes, a Confirm
// that returns false short-circuits the prune. No snapshots deleted.
func TestPrune_ConfirmDecline(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("h"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("body"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	for i := 0; i < 2; i++ {
		body := []byte("body-" + string(rune('a'+i)))
		if err := os.WriteFile(filepath.Join(src, "f.txt"), body, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
	}
	r.Close()

	out := &bytes.Buffer{}
	deps := PruneDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte("h"), nil },
		Stdout:     out,
		Confirm:    func(string) (bool, error) { return false, nil },
	}
	cmd := NewPrune(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--keep-last", "1", "--apply"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(strings.ToLower(out.String()), "abort") &&
		!strings.Contains(strings.ToLower(out.String()), "cancel") {
		t.Errorf("expected abort/cancel in declined-confirm output, got %q", out.String())
	}

	r2, err := repo.Open(context.Background(), store, []byte("h"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r2.Close()
	snaps, err := r2.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(snaps) != 2 {
		t.Errorf("declined confirm should leave snapshots intact, got %d", len(snaps))
	}
}
