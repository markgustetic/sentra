package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRender_IncludesPolicies(t *testing.T) {
	cfg := Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["home"] = PolicyConfig{
		Paths: []string{"~/Documents"},
		Tags:  []string{"home", "daily"},
		Schedule: PolicySchedule{
			Cadence: "daily",
			At:      "03:00",
		},
		AfterBackup: PolicyAfterBackup{
			Check: true,
			Prune: "dry-run",
		},
	}

	body := string(Render(&cfg))
	for _, want := range []string{
		"policies:",
		"  home:",
		"    paths:",
		"      - \"~/Documents\"",
		"    tags:",
		"      - \"home\"",
		"      - \"daily\"",
		"    schedule:",
		"      cadence: \"daily\"",
		"      at: \"03:00\"",
		"    after_backup:",
		"      check: true",
		"      prune: \"dry-run\"",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "hunter2") {
		t.Fatalf("rendered config must not contain passphrase-looking fixture:\n%s", body)
	}
}

func TestRender_PreservesCustomAgent(t *testing.T) {
	cfg := Defaults()
	cfg.Agent.Provider = "openai"
	cfg.Agent.Model = "gpt-4o"

	body := string(Render(&cfg))
	if !strings.Contains(body, "gpt-4o") {
		t.Errorf("rendered config dropped custom agent.model:\n%s", body)
	}
	if !strings.Contains(body, "openai") {
		t.Errorf("rendered config dropped custom agent.provider:\n%s", body)
	}
	if strings.Contains(body, "claude-sonnet-4-6") {
		t.Errorf("rendered config kept the hardcoded default model despite a custom value:\n%s", body)
	}
}

func TestRender_DefaultsAgentWhenUnset(t *testing.T) {
	cfg := Defaults()
	body := string(Render(&cfg))
	if !strings.Contains(body, "anthropic") {
		t.Errorf("expected default provider anthropic:\n%s", body)
	}
	if !strings.Contains(body, "claude-sonnet-4-6") {
		t.Errorf("expected default model claude-sonnet-4-6:\n%s", body)
	}
}

// TestWrite_RoundTripsThroughLoad proves Write+Load is a faithful round
// trip and that the file lands at 0o600 (never group/world readable — it
// names the bucket/region).
func TestWrite_RoundTripsThroughLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.yaml")
	cfg := Defaults()
	cfg.Repo.S3.Bucket = "b"
	cfg.Policies["home"] = PolicyConfig{
		Paths:    []string{"/data"},
		Schedule: PolicySchedule{Cadence: "manual"},
	}
	if err := Write(path, &cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := got.Policies["home"]
	if !ok || len(p.Paths) != 1 || p.Paths[0] != "/data" {
		t.Fatalf("round-trip policy mismatch: %+v", got.Policies)
	}
}
