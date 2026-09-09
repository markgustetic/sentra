package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/repo"
)

// TestPlanBackup_ReportsTheRootTheSnapshotWillRecord: the human confirms
// what plan_backup shows, and CreateSnapshot records repo.ResolveRoot's
// spelling as Manifest.Root. Planning through a symlink (or /tmp on macOS)
// must therefore show the resolved path, or the reviewed path and the
// recorded root differ.
func TestPlanBackup_ReportsTheRootTheSnapshotWillRecord(t *testing.T) {
	ctx := context.Background()
	r, err := repo.Init(ctx, blobstore.NewMemory(), []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	sess := connect(t, New(r, Options{Version: "test"}))

	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	_, text := call(t, sess, "plan_backup", map[string]any{"path": link, "tag": "root"})
	if !strings.Contains(text, real) {
		t.Fatalf("plan summary does not name the resolved root %s:\n%s", real, text)
	}
	res, text := call(t, sess, "confirm_backup", map[string]any{"token": extractToken(t, text)})
	if res.IsError {
		t.Fatalf("confirm_backup errored: %s", text)
	}
	snaps, err := r.ListSnapshots(ctx)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots = %v, %v", snaps, err)
	}
	if snaps[0].Root != real {
		t.Fatalf("recorded root %q, planned root %q", snaps[0].Root, real)
	}
}
