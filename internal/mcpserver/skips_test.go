package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/repo"
)

// TestConfirmBackup_ReportsSkippedFolders: an MCP client has no stderr
// to watch, so the confirm_backup result itself must carry the count
// of folders the walk dropped for a denied listing — otherwise the
// assistant tells the operator the backup succeeded and neither knows
// it is short.
func TestConfirmBackup_ReportsSkippedFolders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod permission denial is not modeled on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod cannot deny access")
	}
	ctx := context.Background()
	r, err := repo.Init(ctx, blobstore.NewMemory(), []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	sess := connect(t, New(r, Options{Version: "test"}))

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "ok.txt"), []byte("visible"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(src, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "hidden.txt"), []byte("unreachable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, text := call(t, sess, "plan_backup", map[string]any{"path": src, "tag": "skips"})
	res, text := call(t, sess, "confirm_backup", map[string]any{"token": extractToken(t, text)})
	if res.IsError {
		t.Fatalf("confirm_backup errored: %s", text)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Files   int `json:"files"`
		Skipped int `json:"skipped"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if out.Files != 1 || out.Skipped != 1 {
		t.Errorf("confirm_backup result = %s, want files=1 skipped=1", raw)
	}
}
