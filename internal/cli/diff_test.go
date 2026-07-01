package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// diffFixture sets up a memory-backed repo with two snapshots that
// have a known set of added/removed/changed paths.
func diffFixture(t *testing.T, passphrase string) (DiffDeps, string, string, *bytes.Buffer) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(passphrase))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer r.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kept.txt"), []byte("stable"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "removed.txt"), []byte("delete me"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "changed.txt"), []byte("v1"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, err := r.CreateSnapshot(context.Background(), root, repo.SnapshotOptions{Tag: "A"})
	if err != nil {
		t.Fatalf("snapshot A: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "removed.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "added.txt"), []byte("brand new"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "changed.txt"), []byte("v2 longer"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := r.CreateSnapshot(context.Background(), root, repo.SnapshotOptions{Tag: "B"})
	if err != nil {
		t.Fatalf("snapshot B: %v", err)
	}

	out := &bytes.Buffer{}
	deps := DiffDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return store, nil
			},
			Passphrase: func() ([]byte, error) { return []byte(passphrase), nil },
			Stdout:     out,
		},
	}
	return deps, a.ID, b.ID, out
}

// TestDiff_PrintsAllSections checks that the table output contains
// the expected paths in each category.
func TestDiff_PrintsAllSections(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, idA, idB, out := diffFixture(t, "hunter2")
	cmd := NewDiff(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{idA, idB})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := out.String()
	for _, want := range []string{"added.txt", "removed.txt", "changed.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output, got %q", want, got)
		}
	}
}

// TestDiff_JSON emits a stable schema with three named slices.
func TestDiff_JSON(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, idA, idB, out := diffFixture(t, "hunter2")
	cmd := NewDiff(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{idA, idB, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var payload struct {
		Added   []string `json:"added"`
		Removed []string `json:"removed"`
		Changed []string `json:"changed"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if !contains(payload.Added, "added.txt") {
		t.Errorf("Added missing added.txt: %v", payload.Added)
	}
	if !contains(payload.Removed, "removed.txt") {
		t.Errorf("Removed missing removed.txt: %v", payload.Removed)
	}
	if !contains(payload.Changed, "changed.txt") {
		t.Errorf("Changed missing changed.txt: %v", payload.Changed)
	}
}

// TestDiff_RequiresArgs enforces both positional arguments.
func TestDiff_RequiresArgs(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, _, _, _ := diffFixture(t, "hunter2")
	cmd := NewDiff(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"only-one"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error with one arg, got nil")
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
