package config

import (
	"errors"
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

// TestRender_EmitsUIHideSplash pins the new ui: section into the rendered file
// so a config rewrite (setup, policy edit) round-trips the operator's choice.
func TestRender_EmitsUIHideSplash(t *testing.T) {
	var cfg Config
	cfg.UI.HideSplash = true
	body := string(Render(&cfg))
	for _, want := range []string{"ui:", "hide_splash: true"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered config missing %q:\n%s", want, body)
		}
	}
}

// TestLoad_MissingUISectionDefaultsToSplashOn is the reason the field is named
// HideSplash rather than ShowSplash: bool's zero value is false, so a config
// written before this field existed must load as "don't hide" — splash on.
func TestLoad_MissingUISectionDefaultsToSplashOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentra.yaml")
	legacy := "repo:\n  s3:\n    bucket: \"b\"\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UI.HideSplash {
		t.Error("a config with no ui: section must load HideSplash=false (splash shows)")
	}
}

// TestUpdate_DoesNotPersistEnvOverrides is the regression test for the bug
// Update exists to prevent: a field edit made while SENTRA_* is set must
// rewrite only the field it names. Reproduced from the field report —
// `SENTRA_REPO__S3__BUCKET=ephemeral-env-bucket sentra`, then flip the
// cosmetic splash toggle, and sentra.yaml's bucket was silently replaced.
func TestUpdate_DoesNotPersistEnvOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentra.yaml")
	var cfg Config
	cfg.Repo.S3.Bucket = "real-bucket"
	cfg.Repo.S3.Region = "us-west-2"
	if err := Write(path, &cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}

	t.Setenv("SENTRA_REPO__S3__BUCKET", "ephemeral-env-bucket")
	t.Setenv("SENTRA_REPO__S3__REGION", "eu-central-1")

	if err := Update(path, func(c *Config) error {
		c.UI.HideSplash = true
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := loadOnDisk(path)
	if err != nil {
		t.Fatalf("loadOnDisk: %v", err)
	}
	if got.Repo.S3.Bucket != "real-bucket" {
		t.Errorf("Update persisted the env bucket: got %q, want real-bucket", got.Repo.S3.Bucket)
	}
	if got.Repo.S3.Region != "us-west-2" {
		t.Errorf("Update persisted the env region: got %q, want us-west-2", got.Repo.S3.Region)
	}
	if !got.UI.HideSplash {
		t.Error("Update did not persist the field it was asked to change")
	}
}

// TestUpdate_MutateErrorLeavesFileUntouched keeps a rejected edit (e.g. policy
// add hitting a duplicate name) from truncating or rewriting sentra.yaml.
func TestUpdate_MutateErrorLeavesFileUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentra.yaml")
	var cfg Config
	cfg.Repo.S3.Bucket = "real-bucket"
	if err := Write(path, &cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	sentinel := errors.New("policy already exists")
	err = Update(path, func(c *Config) error {
		c.Repo.S3.Bucket = "clobbered"
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Update error: got %v, want %v", err, sentinel)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("a failed mutate rewrote the file:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestUpdate_SeedsDefaultsForPartialFile proves a hand-written partial
// sentra.yaml survives a field edit with its omitted keys rendered at their
// documented defaults rather than as zero values.
func TestUpdate_SeedsDefaultsForPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentra.yaml")
	if err := os.WriteFile(path, []byte("repo:\n  s3:\n    bucket: \"b\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, func(c *Config) error {
		c.UI.HideSplash = true
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := loadOnDisk(path)
	if err != nil {
		t.Fatalf("loadOnDisk: %v", err)
	}
	if got.Retention.KeepLast != 10 {
		t.Errorf("KeepLast: got %d, want the default 10 (not a zero value)", got.Retention.KeepLast)
	}
	if got.Repo.S3.Bucket != "b" {
		t.Errorf("Bucket: got %q, want b", got.Repo.S3.Bucket)
	}
}

// TestWrite_RoundTripsHideSplash proves the toggle survives Write -> Load.
func TestWrite_RoundTripsHideSplash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentra.yaml")
	var cfg Config
	cfg.Repo.S3.Bucket = "b"
	cfg.UI.HideSplash = true
	if err := Write(path, &cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.UI.HideSplash {
		t.Error("HideSplash did not round-trip through Write -> Load")
	}
}

// The user-level fallback (~/.config/sentra/sentra.yaml) may be the first
// thing ever written there; Write must create the directory rather than
// fail on a fresh machine, and must create it private.
func TestWrite_CreatesMissingParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentra", "nested", "sentra.yaml")
	cfg := Defaults()
	if err := Write(path, &cfg); err != nil {
		t.Fatalf("Write into missing dir: %v", err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat created dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("config dir perms = %o, want 0700", got)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("round-trip Load: %v", err)
	}
}
