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
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

func recoveryKitFixture(t *testing.T, passphrase string) (RecoveryKitDeps, string, string, *bytes.Buffer) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(passphrase))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{Tag: "latest"})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	repoID := r.Config().ID
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	out := &bytes.Buffer{}
	deps := RecoveryKitDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte(passphrase), nil },
		Stdout:     out,
	}
	return deps, repoID, snap.ID, out
}

func TestRecoveryKit_WritesNonSecretMarkdown(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte(`repo:
  s3:
    bucket: sentra-prod
    prefix: backups/home
    region: us-east-1
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	deps, repoID, snapID, out := recoveryKitFixture(t, "hunter2")
	kitPath := filepath.Join(dir, "kit.md")
	cmd := NewRecoveryKit(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--out", kitPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	body, err := os.ReadFile(kitPath) //nolint:gosec // test path under t.TempDir()
	if err != nil {
		t.Fatalf("read kit: %v", err)
	}
	got := string(body)
	for _, want := range []string{repoID, snapID, "sentra-prod", "sentra restore"} {
		if !strings.Contains(got, want) {
			t.Fatalf("kit missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"hunter2", "WrappedRepoKey", "wrapped_repo_key"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("kit leaked %q:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(out.String(), kitPath) {
		t.Fatalf("stdout should mention written kit path, got %q", out.String())
	}
}

func TestMarshalRecoveryKitJSON(t *testing.T) {
	kit := recoveryKit{
		GeneratedAt:       time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC),
		ConfigPath:        "sentra.yaml",
		RepoID:            "repo-123",
		RepoCreatedAt:     time.Date(2026, 6, 1, 8, 30, 0, 0, time.UTC),
		Bucket:            "sentra-prod",
		Prefix:            "backups/home",
		Region:            "us-east-1",
		SnapshotCount:     1,
		LatestSnapshotID:  "20260624T120000Z-abcdef",
		LatestSnapshotTag: "latest",
		Commands: []string{
			"sentra check --config sentra.yaml",
			"sentra restore 20260624T120000Z-abcdef <dest-dir> --config sentra.yaml --verify",
		},
	}

	body, err := marshalRecoveryKitJSON(kit)
	if err != nil {
		t.Fatalf("marshalRecoveryKitJSON: %v", err)
	}
	if !bytes.HasSuffix(body, []byte("\n")) {
		t.Fatalf("JSON should end with newline, got %q", body)
	}
	var decoded recoveryKit
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if decoded.RepoID != kit.RepoID {
		t.Fatalf("RepoID = %q, want %q", decoded.RepoID, kit.RepoID)
	}
	if len(decoded.Commands) != len(kit.Commands) || decoded.Commands[1] != kit.Commands[1] {
		t.Fatalf("Commands = %+v, want %+v", decoded.Commands, kit.Commands)
	}
	if strings.Contains(string(body), "hunter2") || strings.Contains(string(body), "wrapped_repo_key") {
		t.Fatalf("recovery kit JSON leaked secret-like material:\n%s", body)
	}
}
