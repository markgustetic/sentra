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

// snapshotsFixture builds SnapshotsDeps backed by a memory store
// already containing N snapshots created from a temp tree.
func snapshotsFixture(t *testing.T, passphrase string, n int) (SnapshotsDeps, []string, *bytes.Buffer) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(passphrase))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer r.Close()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("body"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		// Mutate content slightly so each snapshot's manifest differs
		// (deduped chunks are fine; we just want distinct IDs).
		body := []byte("body-" + string(rune('a'+i)))
		if err := os.WriteFile(filepath.Join(src, "f.txt"), body, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		s, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{Tag: "t" + string(rune('a'+i))})
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		ids = append(ids, s.ID)
	}

	out := &bytes.Buffer{}
	deps := SnapshotsDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte(passphrase), nil },
		Stdout:     out,
	}
	return deps, ids, out
}

// TestSnapshots_Table prints all snapshots in a table layout.
func TestSnapshots_Table(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".") // satisfy config-load
	deps, ids, out := snapshotsFixture(t, "hunter2", 3)
	cmd := NewSnapshots(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := out.String()
	for _, id := range ids {
		if !strings.Contains(got, id) {
			t.Errorf("expected ID %s in output, got %q", id, got)
		}
	}
	// Each row should mention a tag.
	if !strings.Contains(got, "ta") || !strings.Contains(got, "tb") || !strings.Contains(got, "tc") {
		t.Errorf("expected each tag (ta/tb/tc) in output, got %q", got)
	}
}

// TestSnapshots_JSON emits a parseable JSON array on --json.
func TestSnapshots_JSON(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, ids, out := snapshotsFixture(t, "hunter2", 2)
	cmd := NewSnapshots(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out.String())
	}
	if len(rows) != 2 {
		t.Fatalf("rows: got %d, want 2", len(rows))
	}
	// Verify each row has the expected fields.
	for _, row := range rows {
		for _, k := range []string{"id", "created_at", "tag", "files", "bytes"} {
			if _, ok := row[k]; !ok {
				t.Errorf("row missing field %q: %+v", k, row)
			}
		}
	}
	// JSON output is newest-first per repo.ListSnapshots.
	gotIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		gotIDs = append(gotIDs, row["id"].(string))
	}
	// Reverse the snapshot creation order: rows should be ids[1], ids[0].
	if gotIDs[0] != ids[1] || gotIDs[1] != ids[0] {
		t.Errorf("order: got %v, want newest-first %v", gotIDs, []string{ids[1], ids[0]})
	}
}

// TestSnapshots_EmptyRepo prints an empty table and exits 0 (or
// emits a [] JSON array on --json) — either way the command should
// not error on a freshly-initialized repo.
func TestSnapshots_EmptyRepo(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("h"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	r.Close()
	out := &bytes.Buffer{}
	deps := SnapshotsDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte("h"), nil },
		Stdout:     out,
	}
	cmd := NewSnapshots(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := strings.TrimSpace(out.String())
	if got != "[]" {
		t.Errorf("expected [] for empty repo, got %q", got)
	}
}
