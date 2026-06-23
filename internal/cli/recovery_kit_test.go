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
