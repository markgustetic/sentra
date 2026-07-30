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

// restoreFixture sets up a memory-backed repo with a single snapshot
// of a known tree. Returns the deps, the snapshot ID, the source
// path (for byte-for-byte comparison), and the captured output.
func restoreFixture(t *testing.T, passphrase string) (RestoreDeps, string, string, *bytes.Buffer) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(passphrase))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer r.Close()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.bin"), bytes.Repeat([]byte("\x00\x01\x02"), 64), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{Tag: "rt"})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	out := &bytes.Buffer{}
	deps := RestoreDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return store, nil
			},
			Passphrase: func() ([]byte, error) { return []byte(passphrase), nil },
			Stdout:     out,
		},
		Stderr: io.Discard,
	}
	return deps, snap.ID, src, out
}

// TestRestore_RoundTrip restores into a fresh dir and asserts every
// file is byte-identical to the source.
func TestRestore_RoundTrip(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, snapID, src, out := restoreFixture(t, "hunter2")

	dst := filepath.Join(t.TempDir(), "restored")
	cmd := NewRestore(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{snapID, dst})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Walk both trees and assert byte-for-byte parity.
	srcEntries := readTree(t, src)
	dstEntries := readTree(t, dst)
	if len(srcEntries) != len(dstEntries) {
		t.Fatalf("entry count: src=%d dst=%d", len(srcEntries), len(dstEntries))
	}
	for path, content := range srcEntries {
		got, ok := dstEntries[path]
		if !ok {
			t.Errorf("missing in dst: %s", path)
			continue
		}
		if !bytes.Equal(content, got) {
			t.Errorf("%s: content mismatch", path)
		}
	}

	// Output mentions the snap id and dest dir.
	got := out.String()
	if !strings.Contains(got, snapID) {
		t.Errorf("output missing snapshot ID: %q", got)
	}
}

func TestRestore_DryRunDoesNotCreateDestination(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, snapID, _, out := restoreFixture(t, "hunter2")

	dst := filepath.Join(t.TempDir(), "dry-run-dest")
	cmd := NewRestore(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{snapID, dst, "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dry-run created destination or got unexpected stat error: %v", err)
	}
	if !strings.Contains(strings.ToLower(out.String()), "dry-run") {
		t.Fatalf("output missing dry-run summary: %q", out.String())
	}
}

func TestRestore_VerifyAfterRestore(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, snapID, _, out := restoreFixture(t, "hunter2")

	dst := filepath.Join(t.TempDir(), "restored")
	cmd := NewRestore(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{snapID, dst, "--verify"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := strings.ToLower(out.String())
	if !strings.Contains(got, "verify") || !strings.Contains(got, "passed") {
		t.Fatalf("output missing verification success: %q", out.String())
	}
}

// TestRestore_RequiresArgs enforces the two positional arguments.
func TestRestore_RequiresArgs(t *testing.T) {
	chDir(t, t.TempDir())
	deps, _, _, _ := restoreFixture(t, "h")
	cmd := NewRestore(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"only-one-arg"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error with one arg, got nil")
	}
}

// TestRestore_RejectsBadID surfaces the validation error from
// repo.Restore when the snapshot ID is malformed (e.g. traversal).
func TestRestore_RejectsBadID(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, _, _, _ := restoreFixture(t, "hunter2")
	cmd := NewRestore(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"../../etc", t.TempDir()})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for traversal ID, got nil")
	}
}

// readTree walks root and returns a map of relative path → content.
// Used in TestRestore_RoundTrip for the parity assertion.
func readTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path) //nolint:gosec // test helper, path under t.TempDir()
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = raw
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// TestRestore_JSON: --json emits the restore summary as a stable
// schema (and folds in the verification report when --verify ran).
func TestRestore_JSON(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, dir)

	deps, snapID, _, out := restoreFixture(t, "hunter2")
	dest := filepath.Join(dir, "out")
	cmd := NewRestore(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{snapID, dest, "--verify", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var row struct {
		SnapshotID string `json:"snapshot_id"`
		Dest       string `json:"dest"`
		Files      int    `json:"files"`
		Verify     *struct {
			VerifiedFiles int `json:"verified_files"`
			Mismatches    int `json:"mismatches"`
		} `json:"verify"`
	}
	if err := json.Unmarshal(out.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if row.SnapshotID != snapID || row.Verify == nil || row.Verify.Mismatches != 0 {
		t.Errorf("unexpected row: %+v", row)
	}
}
