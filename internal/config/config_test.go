package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureYAML = `
repo:
  s3:
    bucket: my-backups
    prefix: sentra/
    region: us-west-2
    profile: default
    endpoint_url: ""
agent:
  provider: anthropic
  model: claude-sonnet-4-6
  max_findings_to_llm: 50
backup:
  ignore_file: .sentraignore
  exclude_caches: true
retention:
  keep_last: 10
  keep_daily: 7
  keep_weekly: 4
  keep_monthly: 6
`

func writeYAML(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "sentra.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestLoad_Fixture parses a complete fixture and asserts every field
// makes the round trip from YAML to the struct fields.
func TestLoad_Fixture(t *testing.T) {
	path := writeYAML(t, t.TempDir(), fixtureYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Repo.S3.Bucket != "my-backups" {
		t.Errorf("Bucket: got %q, want my-backups", cfg.Repo.S3.Bucket)
	}
	if cfg.Repo.S3.Prefix != "sentra/" {
		t.Errorf("Prefix: got %q", cfg.Repo.S3.Prefix)
	}
	if cfg.Repo.S3.Region != "us-west-2" {
		t.Errorf("Region: got %q", cfg.Repo.S3.Region)
	}
	if cfg.Repo.S3.Profile != "default" {
		t.Errorf("Profile: got %q", cfg.Repo.S3.Profile)
	}
	if cfg.Repo.S3.EndpointURL != "" {
		t.Errorf("EndpointURL: got %q", cfg.Repo.S3.EndpointURL)
	}
	if cfg.Agent.Provider != "anthropic" {
		t.Errorf("Provider: got %q", cfg.Agent.Provider)
	}
	if cfg.Agent.Model != "claude-sonnet-4-6" {
		t.Errorf("Model: got %q", cfg.Agent.Model)
	}
	if cfg.Agent.MaxFindingsToLLM != 50 {
		t.Errorf("MaxFindingsToLLM: got %d", cfg.Agent.MaxFindingsToLLM)
	}
	if cfg.Backup.IgnoreFile != ".sentraignore" {
		t.Errorf("IgnoreFile: got %q", cfg.Backup.IgnoreFile)
	}
	if !cfg.Backup.ExcludeCaches {
		t.Errorf("ExcludeCaches: got false")
	}
	if cfg.Retention.KeepLast != 10 {
		t.Errorf("KeepLast: got %d", cfg.Retention.KeepLast)
	}
	if cfg.Retention.KeepDaily != 7 {
		t.Errorf("KeepDaily: got %d", cfg.Retention.KeepDaily)
	}
	if cfg.Retention.KeepWeekly != 4 {
		t.Errorf("KeepWeekly: got %d", cfg.Retention.KeepWeekly)
	}
	if cfg.Retention.KeepMonthly != 6 {
		t.Errorf("KeepMonthly: got %d", cfg.Retention.KeepMonthly)
	}
}

func TestLoad_Policies(t *testing.T) {
	path := writeYAML(t, t.TempDir(), fixtureYAML+`
policies:
  home:
    paths:
      - ~/Documents
    tags:
      - home
      - daily
    schedule:
      cadence: daily
      at: "03:00"
    after_backup:
      check: true
      prune: dry-run
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.Policies["home"]
	if len(p.Paths) != 1 || p.Paths[0] != "~/Documents" {
		t.Fatalf("paths: %+v", p.Paths)
	}
	if len(p.Tags) != 2 || p.Tags[0] != "home" || p.Tags[1] != "daily" {
		t.Fatalf("tags: %+v", p.Tags)
	}
	if p.Schedule.Cadence != "daily" || p.Schedule.At != "03:00" {
		t.Fatalf("schedule: %+v", p.Schedule)
	}
	if !p.AfterBackup.Check || p.AfterBackup.Prune != "dry-run" {
		t.Fatalf("after_backup: %+v", p.AfterBackup)
	}
}

// TestLoad_EnvOverlay verifies SENTRA_* env vars override fields from the
// YAML file. Documented contract: the env vars use double-underscore as a
// nesting separator (so SENTRA_REPO__S3__BUCKET maps to repo.s3.bucket).
// We start with a fixture that says "my-backups" and override with the env.
func TestLoad_EnvOverlay(t *testing.T) {
	path := writeYAML(t, t.TempDir(), fixtureYAML)
	t.Setenv("SENTRA_REPO__S3__BUCKET", "override-bucket")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Repo.S3.Bucket != "override-bucket" {
		t.Errorf("env override failed: got %q, want override-bucket", cfg.Repo.S3.Bucket)
	}
	// Other fields must still come from the file.
	if cfg.Repo.S3.Region != "us-west-2" {
		t.Errorf("Region clobbered: got %q", cfg.Repo.S3.Region)
	}
}

// TestLoad_Missing returns Defaults() with no error when the file does
// not exist. This is the path used when sentra.yaml hasn't been
// initialised yet.
func TestLoad_Missing(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "no-such-file.yaml"))
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	def := Defaults()
	if cfg.Backup.IgnoreFile != def.Backup.IgnoreFile {
		t.Errorf("IgnoreFile: got %q, want default %q", cfg.Backup.IgnoreFile, def.Backup.IgnoreFile)
	}
	if !cfg.Backup.ExcludeCaches {
		t.Errorf("default ExcludeCaches should be true")
	}
	if cfg.Agent.MaxFindingsToLLM != 50 {
		t.Errorf("MaxFindingsToLLM default: got %d", cfg.Agent.MaxFindingsToLLM)
	}
	if cfg.Retention.KeepLast != 10 {
		t.Errorf("KeepLast default: got %d", cfg.Retention.KeepLast)
	}
	if cfg.Retention.KeepDaily != 7 {
		t.Errorf("KeepDaily default: got %d", cfg.Retention.KeepDaily)
	}
	if cfg.Retention.KeepWeekly != 4 {
		t.Errorf("KeepWeekly default: got %d", cfg.Retention.KeepWeekly)
	}
	if cfg.Retention.KeepMonthly != 6 {
		t.Errorf("KeepMonthly default: got %d", cfg.Retention.KeepMonthly)
	}
}

// TestLoad_Malformed surfaces a parse error for invalid YAML rather
// than silently returning Defaults().
func TestLoad_Malformed(t *testing.T) {
	path := writeYAML(t, t.TempDir(), "this is: : not yaml: : :")
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error from malformed YAML, got nil")
	}
	if !strings.Contains(err.Error(), "yaml") && !strings.Contains(err.Error(), "parse") {
		// Don't be too precious about the exact message — koanf
		// surfaces its underlying yaml.v3 errors which can change.
		t.Logf("error message: %v", err)
	}
}

// TestLoad_IgnoresReservedEnvVars asserts that env vars whose names
// would otherwise collide with reserved keys (notably SENTRA_PASSPHRASE,
// which is the secret-source env, not a config key) are NOT applied to
// the koanf overlay. Without this, koanf would route SENTRA_PASSPHRASE
// to the "passphrase" path, which is a struct (object) — unmarshal
// would fail with `'passphrase' expected a map or struct, got "string"`
// and every command would error out before doing useful work.
func TestLoad_IgnoresReservedEnvVars(t *testing.T) {
	t.Setenv("SENTRA_PASSPHRASE", "hunter2")
	cfg, err := Load(filepath.Join(t.TempDir(), "no-such-file.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := Defaults()
	if cfg.Backup.IgnoreFile != def.Backup.IgnoreFile {
		t.Errorf("IgnoreFile: got %q, want %q", cfg.Backup.IgnoreFile, def.Backup.IgnoreFile)
	}
	if cfg.Agent.MaxFindingsToLLM != def.Agent.MaxFindingsToLLM {
		t.Errorf("MaxFindingsToLLM: got %d, want %d", cfg.Agent.MaxFindingsToLLM, def.Agent.MaxFindingsToLLM)
	}
	// Use_keyring should remain at its zero default — the env var did
	// not (and must not) seed the Passphrase struct.
	if cfg.Passphrase.UseKeyring {
		t.Errorf("UseKeyring should be false, got true")
	}
}

// TestLoad_IgnoresReservedEnvVarsWithFile checks the same blacklist
// path through Load's file branch: even with an actual sentra.yaml on
// disk, SENTRA_PASSPHRASE must not crash unmarshal.
func TestLoad_IgnoresReservedEnvVarsWithFile(t *testing.T) {
	t.Setenv("SENTRA_PASSPHRASE", "hunter2")
	t.Setenv("SENTRA_PASSPHRASE_FILE", "/dev/null")
	path := writeYAML(t, t.TempDir(), fixtureYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Repo.S3.Bucket != "my-backups" {
		t.Errorf("Bucket: got %q, want my-backups", cfg.Repo.S3.Bucket)
	}
}

// TestLoadOnDisk_IgnoresEnvOverlay is the load half of the rewrite-safety
// contract: loadOnDisk reports what sentra.yaml *says*, not what the process
// resolves. Load would return "override-bucket" here; a rewrite based on that
// would bake a transient env var into the file forever.
func TestLoadOnDisk_IgnoresEnvOverlay(t *testing.T) {
	path := writeYAML(t, t.TempDir(), fixtureYAML)
	t.Setenv("SENTRA_REPO__S3__BUCKET", "override-bucket")

	cfg, err := loadOnDisk(path)
	if err != nil {
		t.Fatalf("loadOnDisk: %v", err)
	}
	if cfg.Repo.S3.Bucket != "my-backups" {
		t.Errorf("env overlay leaked into loadOnDisk: got %q, want my-backups", cfg.Repo.S3.Bucket)
	}
	// Defaults must still be seeded — a partial sentra.yaml has to render
	// complete, so an omitted key can't come back as a zero value.
	if cfg.Retention.KeepLast != 10 {
		t.Errorf("KeepLast: got %d, want the seeded default 10", cfg.Retention.KeepLast)
	}
}

// TestLoadOnDisk_Missing mirrors TestLoad_Missing: no file is the pre-init
// path, not an error. Without env, that's exactly Defaults().
func TestLoadOnDisk_Missing(t *testing.T) {
	t.Setenv("SENTRA_REPO__S3__BUCKET", "override-bucket")
	cfg, err := loadOnDisk(filepath.Join(t.TempDir(), "no-such-file.yaml"))
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if cfg.Repo.S3.Bucket != "" {
		t.Errorf("missing file must not inherit the env bucket: got %q", cfg.Repo.S3.Bucket)
	}
	if cfg.Backup.IgnoreFile != Defaults().Backup.IgnoreFile {
		t.Errorf("IgnoreFile: got %q, want the default", cfg.Backup.IgnoreFile)
	}
}

// TestDefaults gives the documented set of defaults. Any change here is
// a user-visible change and should be deliberate.
func TestDefaults(t *testing.T) {
	d := Defaults()
	if d.Backup.IgnoreFile != ".sentraignore" {
		t.Errorf("IgnoreFile: got %q", d.Backup.IgnoreFile)
	}
	if !d.Backup.ExcludeCaches {
		t.Errorf("ExcludeCaches: got false, want true")
	}
	if d.Agent.MaxFindingsToLLM != 50 {
		t.Errorf("MaxFindingsToLLM: got %d", d.Agent.MaxFindingsToLLM)
	}
	if d.Retention.KeepLast != 10 || d.Retention.KeepDaily != 7 ||
		d.Retention.KeepWeekly != 4 || d.Retention.KeepMonthly != 6 {
		t.Errorf("retention defaults wrong: %+v", d.Retention)
	}
	if d.Policies == nil {
		t.Fatal("Policies default map is nil")
	}
	if len(d.Policies) != 0 {
		t.Fatalf("Policies default: got %+v, want empty", d.Policies)
	}
}
