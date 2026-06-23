package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

func checkFixture(t *testing.T, passphrase string) (CheckDeps, *blobstore.Memory, *bytes.Buffer, string) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(passphrase))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "doc.txt")
	if err := os.WriteFile(path, []byte("body"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap, err := r.CreateSnapshot(context.Background(), root, repo.SnapshotOptions{Tag: "check"})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	out := &bytes.Buffer{}
	deps := CheckDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte(passphrase), nil },
		Stdout:     out,
	}
	return deps, store, out, snap.ID
}

func TestCheck_HealthyTextOutput(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	deps, _, out, _ := checkFixture(t, "hunter2")
	cmd := NewCheck(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := strings.ToLower(out.String())
	for _, want := range []string{"healthy", "snapshots", "referenced blobs"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q: %q", want, out.String())
		}
	}
}

func TestCheck_JSONOutputIncludesWarnings(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	deps, store, out, _ := checkFixture(t, "hunter2")
	orphanKey := repo.DataPrefix + "ab/abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	if err := store.Put(context.Background(), orphanKey, bytes.NewReader([]byte("orphan"))); err != nil {
		t.Fatalf("put orphan: %v", err)
	}

	cmd := NewCheck(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var report struct {
		Status      string `json:"status"`
		OrphanBlobs []struct {
			Key string `json:"key"`
		} `json:"orphan_blobs"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if report.Status != "healthy" {
		t.Fatalf("Status = %q, want healthy; output=%s", report.Status, out.String())
	}
	if len(report.OrphanBlobs) != 1 || report.OrphanBlobs[0].Key != orphanKey {
		t.Fatalf("OrphanBlobs = %+v, want %s", report.OrphanBlobs, orphanKey)
	}
}

func TestCheck_ReturnsErrorWhenMissingChunk(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	deps, store, out, snapID := checkFixture(t, "hunter2")
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	manifest, err := r.LoadSnapshot(context.Background(), snapID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r.Close()
	if err := store.Delete(context.Background(), repo.ChunkKey(manifest.Tree[0].Chunks[0])); err != nil {
		t.Fatalf("delete chunk: %v", err)
	}

	cmd := NewCheck(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--json"})
	err = cmd.Execute()
	if !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("execute err = %v, want ErrCheckFailed", err)
	}
	if !strings.Contains(out.String(), `"status": "failed"`) {
		t.Fatalf("expected failed JSON report, got %s", out.String())
	}
}
