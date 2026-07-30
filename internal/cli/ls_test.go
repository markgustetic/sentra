package cli

import (
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

func lsFixture(t *testing.T) (LsDeps, string, *strings.Builder) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/a.txt", filepath.Join(src, "ln")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	snap, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	out := &strings.Builder{}
	deps := LsDeps{RepoDeps: RepoDeps{
		NewStore: func(context.Context, *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte("hunter2"), nil },
		Stdout:     out,
	}}
	return deps, snap.ID, out
}

// TestLs_ListsTreeWithKinds: `sentra ls latest` shows every entry —
// files with sizes, dirs with a trailing slash, symlinks with their
// target — and resolves the "latest" shorthand.
func TestLs_ListsTreeWithKinds(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, _, out := lsFixture(t)

	cmd := NewLs(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"latest"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := out.String()
	for _, want := range []string{"sub/a.txt", "sub/", "ln -> sub/a.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

// TestLs_JSON: --json emits a stable schema with explicit kinds.
func TestLs_JSON(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, snapID, out := lsFixture(t)

	cmd := NewLs(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{snapID, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var rows []struct {
		Path       string `json:"path"`
		Kind       string `json:"kind"`
		Size       int64  `json:"size"`
		LinkTarget string `json:"link_target,omitempty"`
	}
	if err := json.Unmarshal([]byte(out.String()), &rows); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	kinds := map[string]string{}
	for _, row := range rows {
		kinds[row.Path] = row.Kind
	}
	if kinds["sub/a.txt"] != "file" || kinds["sub"] != "dir" || kinds["ln"] != "symlink" {
		t.Errorf("kinds wrong: %v", kinds)
	}
}
