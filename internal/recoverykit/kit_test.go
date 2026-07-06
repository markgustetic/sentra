package recoverykit

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// buildRealKit inits an in-memory repo, seeds one tagged snapshot, and
// returns a Kit plus the repo ID and snapshot ID for assertions.
func buildRealKit(t *testing.T) (Kit, string, string) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{Tag: "latest"})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	cfg := &config.Config{}
	cfg.Repo.S3.Bucket = "sentra-prod"
	cfg.Repo.S3.Prefix = "backups/home"
	cfg.Repo.S3.Region = "us-east-1"

	kit, err := Build(context.Background(), r, cfg, "sentra.yaml")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return kit, r.Config().ID, snap.ID
}

func TestBuild_PopulatesNonSecretFields(t *testing.T) {
	kit, repoID, snapID := buildRealKit(t)
	if kit.RepoID != repoID {
		t.Fatalf("RepoID = %q, want %q", kit.RepoID, repoID)
	}
	if kit.SnapshotCount != 1 {
		t.Fatalf("SnapshotCount = %d, want 1", kit.SnapshotCount)
	}
	if kit.LatestSnapshotID != snapID {
		t.Fatalf("LatestSnapshotID = %q, want %q", kit.LatestSnapshotID, snapID)
	}
	if kit.LatestSnapshotTag != "latest" {
		t.Fatalf("LatestSnapshotTag = %q, want latest", kit.LatestSnapshotTag)
	}
	if kit.Bucket != "sentra-prod" {
		t.Fatalf("Bucket = %q, want sentra-prod", kit.Bucket)
	}
	if len(kit.Commands) != 3 {
		t.Fatalf("Commands = %v, want 3 entries", kit.Commands)
	}
	// The restore command must name the concrete latest snapshot.
	if !strings.Contains(kit.Commands[2], snapID) {
		t.Fatalf("restore command %q must reference %q", kit.Commands[2], snapID)
	}
}

func TestBuild_NoSnapshotsUsesPlaceholder(t *testing.T) {
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	kit, err := Build(context.Background(), r, &config.Config{}, "sentra.yaml")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if kit.SnapshotCount != 0 || kit.LatestSnapshotID != "" {
		t.Fatalf("empty repo kit = %+v, want zero snapshots", kit)
	}
	if !strings.Contains(kit.Commands[2], "<snapshot-id>") {
		t.Fatalf("restore command %q must use the <snapshot-id> placeholder", kit.Commands[2])
	}
}

func TestRenderMarkdown_NonSecretAndEmptyDashInlined(t *testing.T) {
	kit := Kit{
		GeneratedAt:   time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC),
		ConfigPath:    "sentra.yaml",
		RepoID:        "repo-123",
		RepoCreatedAt: time.Date(2026, 6, 1, 8, 30, 0, 0, time.UTC),
		Bucket:        "sentra-prod",
		// Prefix/Region/Profile/EndpointURL deliberately empty to exercise the
		// inlined empty->"-" path (emptyDash is NOT moved into this package).
		SnapshotCount:    1,
		LatestSnapshotID: "20260624T120000Z-abcdef",
		Commands:         []string{"sentra check --config sentra.yaml"},
	}
	md := RenderMarkdown(kit)
	if !strings.Contains(md, "# Sentra Recovery Kit") {
		t.Fatalf("markdown missing header:\n%s", md)
	}
	if !strings.Contains(md, "- Prefix: -") {
		t.Fatalf("empty prefix must render as '-':\n%s", md)
	}
	if !strings.Contains(md, "intentionally excludes passphrases") {
		t.Fatalf("markdown missing the no-secret disclaimer:\n%s", md)
	}
	for _, forbidden := range []string{"hunter2", "WrappedRepoKey", "wrapped_repo_key", "MAC", "Salt"} {
		if strings.Contains(md, forbidden) {
			t.Fatalf("markdown leaked %q:\n%s", forbidden, md)
		}
	}
}

func TestMarshalJSON_TrailingNewlineAndNoSecrets(t *testing.T) {
	kit, _, _ := buildRealKit(t)
	body, err := MarshalJSON(kit)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if !bytes.HasSuffix(body, []byte("\n")) {
		t.Fatalf("JSON should end with newline, got %q", body)
	}
	var decoded Kit
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if decoded.RepoID != kit.RepoID {
		t.Fatalf("RepoID = %q, want %q", decoded.RepoID, kit.RepoID)
	}
	for _, forbidden := range []string{"hunter2", "wrapped_repo_key", "salt", "\"mac\""} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("recovery kit JSON leaked %q:\n%s", forbidden, body)
		}
	}
}
