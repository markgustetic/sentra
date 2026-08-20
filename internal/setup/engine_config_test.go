package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

// WriteConfig writes the plan's config to cfgPath via config.Write.
func TestEngineWriteConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sentra.yaml")
	var p Plan
	p.Config.Repo.S3.Bucket = "example-bucket"
	p.Config.Repo.S3.Region = "us-east-1"
	if err := NewEngine(fakeEffects{}).WriteConfig(cfgPath, &p); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if loaded.Repo.S3.Bucket != "example-bucket" {
		t.Fatalf("bucket = %q, want example-bucket", loaded.Repo.S3.Bucket)
	}
}

// DraftPath is a dotfile sibling of cfgPath suffixed .setup-draft.
func TestEngineDraftPath(t *testing.T) {
	got := NewEngine(fakeEffects{}).DraftPath("/tmp/sub/sentra.yaml")
	want := filepath.Join("/tmp/sub", ".sentra.yaml.setup-draft")
	if got != want {
		t.Fatalf("DraftPath = %q, want %q", got, want)
	}
}

// WriteDraft then RemoveDraft round-trips; RemoveDraft on a missing draft is
// a best-effort no-op.
func TestEngineWriteAndRemoveDraft(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sentra.yaml")
	e := NewEngine(fakeEffects{})
	var cfg config.Config
	cfg.Repo.S3.Bucket = "example-bucket"
	if err := e.WriteDraft(cfgPath, &cfg); err != nil {
		t.Fatalf("WriteDraft: %v", err)
	}
	draft := e.DraftPath(cfgPath)
	if _, err := os.Stat(draft); err != nil {
		t.Fatalf("draft not written: %v", err)
	}
	e.RemoveDraft(cfgPath)
	if _, err := os.Stat(draft); !os.IsNotExist(err) {
		t.Fatalf("draft still present after RemoveDraft: err=%v", err)
	}
	// Second RemoveDraft on the now-missing draft must not panic or error.
	e.RemoveDraft(cfgPath)
}

// A first run from a random directory targets ~/.config/sentra/, which may
// not exist yet — the resumability draft must not be the thing that fails.
func TestWriteDraft_CreatesMissingParentDirs(t *testing.T) {
	e := NewEngine(nil)
	cfgPath := filepath.Join(t.TempDir(), "sentra", "sentra.yaml")
	cfg := config.Defaults()
	if err := e.WriteDraft(cfgPath, &cfg); err != nil {
		t.Fatalf("WriteDraft into missing dir: %v", err)
	}
	if _, err := os.Stat(e.DraftPath(cfgPath)); err != nil {
		t.Errorf("draft not written: %v", err)
	}
}
