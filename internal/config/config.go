// Package config holds the parsed sentra.yaml document and the
// passphrase resolver that the CLI commands use to construct a *Repo.
//
// The schema mirrors docs/plans/2026-05-02-sentra-design.md ("sentra.yaml"
// section). Defaults are embedded at the top of Load so a missing file or
// missing fields produce a sensible runnable config without surprises.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config is the typed view of a sentra.yaml document. Field tags use
// koanf so we can use the lowercase / snake_case YAML keys without
// fighting the JSON tag conventions used elsewhere in the codebase.
type Config struct {
	Repo struct {
		S3 struct {
			Bucket      string `koanf:"bucket"`
			Prefix      string `koanf:"prefix"`
			Region      string `koanf:"region"`
			Profile     string `koanf:"profile"`
			EndpointURL string `koanf:"endpoint_url"`
		} `koanf:"s3"`
	} `koanf:"repo"`

	Agent struct {
		Provider         string `koanf:"provider"`
		Model            string `koanf:"model"`
		MaxFindingsToLLM int    `koanf:"max_findings_to_llm"`
	} `koanf:"agent"`

	Backup struct {
		IgnoreFile    string `koanf:"ignore_file"`
		ExcludeCaches bool   `koanf:"exclude_caches"`
		// Concurrency caps the walker/upload worker count during
		// backup. 0 means one worker per logical CPU.
		Concurrency int `koanf:"concurrency"`
		// MaxUploadRate caps backup upload bandwidth in bytes per
		// second (paced at the blobstore layer). 0 means unlimited.
		MaxUploadRate int64 `koanf:"max_upload_rate"`
	} `koanf:"backup"`

	Retention struct {
		KeepLast    int `koanf:"keep_last"`
		KeepDaily   int `koanf:"keep_daily"`
		KeepWeekly  int `koanf:"keep_weekly"`
		KeepMonthly int `koanf:"keep_monthly"`
	} `koanf:"retention"`

	// Policies contains named backup policies. Each policy is
	// non-secret local configuration: source paths, optional tags,
	// schedule metadata, and post-backup maintenance preferences.
	Policies map[string]PolicyConfig `koanf:"policies"`

	// Passphrase contains optional passphrase-resolution settings.
	// Stored under "passphrase" in the YAML; never carries the
	// passphrase itself. Recognised keys: use_keyring (bool).
	Passphrase struct {
		UseKeyring bool `koanf:"use_keyring"`
	} `koanf:"passphrase"`

	// UI contains optional presentation settings for the TUI. Stored under
	// "ui" in the YAML; carries no secrets.
	//
	// HideSplash is negated deliberately. Go's zero value for bool is false,
	// so a sentra.yaml written before this field existed loads as "don't
	// hide" — the welcome splash shows by default, with no migration and no
	// pointer field.
	UI struct {
		HideSplash bool `koanf:"hide_splash"`
	} `koanf:"ui"`
}

// PolicyConfig is the typed view of one entry under sentra.yaml's
// `policies:` map.
type PolicyConfig struct {
	Paths       []string          `koanf:"paths"`
	Tags        []string          `koanf:"tags"`
	Schedule    PolicySchedule    `koanf:"schedule"`
	AfterBackup PolicyAfterBackup `koanf:"after_backup"`
}

// PolicySchedule describes when a named policy should run. Validation
// of supported cadences and clock values lives in internal/policy so
// config loading stays a pure parse/overlay step.
type PolicySchedule struct {
	Cadence string `koanf:"cadence"`
	At      string `koanf:"at"`
	Weekday string `koanf:"weekday"`
}

// PolicyAfterBackup controls optional maintenance after a policy run.
// Prune is a string so the CLI can distinguish "", "off", "dry-run",
// and "apply" explicitly.
type PolicyAfterBackup struct {
	Check bool   `koanf:"check"`
	Prune string `koanf:"prune"`
}

// envPrefix is the namespace for SENTRA_* env-var overrides. Sub-keys
// nest with double-underscore to avoid colliding with single underscores
// in YAML key names like "max_findings_to_llm". So the env override for
// repo.s3.bucket is SENTRA_REPO__S3__BUCKET.
const envPrefix = "SENTRA_"

// envDelim separates nested keys in env-var names. Two underscores so a
// single underscore inside a YAML key (e.g. "endpoint_url") is preserved
// as part of the leaf name rather than treated as a path separator.
const envDelim = "__"

// koanfDelim is the delimiter koanf uses internally for path lookups.
// "." is the conventional choice and matches the YAML structure.
const koanfDelim = "."

// Defaults returns a Config populated with sensible zero-value
// overrides. Used both by Load (when the file is missing) and as the
// base for the YAML overlay (so a partial file still lands the
// documented defaults for unspecified fields).
func Defaults() Config {
	var c Config
	c.Backup.IgnoreFile = ".sentraignore"
	c.Backup.ExcludeCaches = true
	c.Agent.MaxFindingsToLLM = 50
	c.Retention.KeepLast = 10
	c.Retention.KeepDaily = 7
	c.Retention.KeepWeekly = 4
	c.Retention.KeepMonthly = 6
	c.Policies = map[string]PolicyConfig{}
	return c
}

// Load reads a sentra.yaml document from path, overlays SENTRA_* env
// variables, and returns the merged Config. This is the *resolved* view:
// what this process should act on right now.
//
// A missing path returns Defaults() and a nil error — that's the
// "haven't run sentra init yet" path. Any other I/O or parse error
// surfaces with a wrapped error so callers can show a helpful message.
//
// Env-var overlay: SENTRA_<key path joined with __>. So
// "repo.s3.bucket" maps to SENTRA_REPO__S3__BUCKET. The double-
// underscore separator avoids ambiguity with single underscores
// inside leaf keys (e.g. "max_findings_to_llm").
//
// Do NOT pair Load with Write to edit one field — the overlay would be
// rendered back into the file, making a transient override permanent.
// Update exists for that; see its doc comment.
func Load(path string) (*Config, error) {
	return load(path, true)
}

// loadOnDisk reads sentra.yaml *without* the env overlay: Defaults() plus
// whatever the file says, and nothing else. It answers "what does the file
// claim?" rather than Load's "what should this process do?".
//
// Defaults are still seeded, so a partial file rewritten from this base
// renders its omitted keys at the documented defaults rather than as zero
// values. This is the base every rewrite must build on; see Update.
func loadOnDisk(path string) (*Config, error) {
	return load(path, false)
}

// load is the shared body of Load and loadOnDisk. The only difference between
// the two is whether the SENTRA_* overlay is applied, so keeping one
// implementation means the default-seeding and error wrapping can't drift.
//
// A missing file is not an error: the koanf tree is built from defaults (plus
// env, when requested) exactly as if an empty document had been parsed.
func load(path string, withEnv bool) (*Config, error) {
	out := Defaults()

	fileExists := true
	info, err := os.Stat(path)
	switch {
	case err == nil && info.IsDir():
		return nil, fmt.Errorf("config: %s is a directory, not a file", path)
	case err != nil && errors.Is(err, os.ErrNotExist):
		fileExists = false
	case err != nil:
		return nil, fmt.Errorf("config: stat %s: %w", path, err)
	}

	k := koanf.New(koanfDelim)
	// Seed defaults so partial files still produce a runnable config.
	if err := loadDefaults(k); err != nil {
		return nil, err
	}
	if fileExists {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("config: load %s: %w", path, err)
		}
	}
	if withEnv {
		// Env overlay: read all SENTRA_* vars, lowercase, strip prefix,
		// translate __ to . to nest into the koanf tree.
		if err := loadEnv(k); err != nil {
			return nil, fmt.Errorf("config: load env: %w", err)
		}
	}
	if err := k.Unmarshal("", &out); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}
	return &out, nil
}

// loadDefaults seeds k with the documented defaults so partial YAML
// files (or pure-env-overlay calls) still produce sensible output.
func loadDefaults(k *koanf.Koanf) error {
	def := Defaults()
	// Map of dot-path → value. We deliberately use the explicit key
	// list rather than reflecting over the struct so that omitting a
	// default here is a visible diff, not a silent zeroing.
	m := map[string]any{
		"backup.ignore_file":        def.Backup.IgnoreFile,
		"backup.exclude_caches":     def.Backup.ExcludeCaches,
		"agent.max_findings_to_llm": def.Agent.MaxFindingsToLLM,
		"retention.keep_last":       def.Retention.KeepLast,
		"retention.keep_daily":      def.Retention.KeepDaily,
		"retention.keep_weekly":     def.Retention.KeepWeekly,
		"retention.keep_monthly":    def.Retention.KeepMonthly,
		"policies":                  def.Policies,
	}
	if err := k.Load(rawMapProvider{m: m}, nil); err != nil {
		return fmt.Errorf("config: seed defaults: %w", err)
	}
	return nil
}

// reservedEnv lists SENTRA_* env-var names that must NOT bleed into
// the koanf overlay. Their semantics are owned by code outside the
// config schema (e.g. the passphrase resolver), and routing them
// through the schema breaks unmarshal — SENTRA_PASSPHRASE would map
// to the "passphrase" path, which is a struct (object), not a string.
//
// The transform function returns "" for these names; koanf treats an
// empty key as "skip", so the value never reaches the tree.
var reservedEnv = map[string]bool{
	"SENTRA_PASSPHRASE":      true,
	"SENTRA_PASSPHRASE_FILE": true,
}

// loadEnv layers SENTRA_* env vars on top of the koanf tree. The env
// provider strips envPrefix and translates envDelim ("__") to "." so
// SENTRA_REPO__S3__BUCKET becomes repo.s3.bucket.
//
// Reserved names (see reservedEnv) are filtered out so they don't
// collide with config-schema keys.
func loadEnv(k *koanf.Koanf) error {
	provider := env.Provider(envPrefix, koanfDelim, func(s string) string {
		// Drop reserved names entirely — they belong to other code
		// paths (passphrase resolver, etc.) and would crash unmarshal
		// if routed through the schema.
		if reservedEnv[s] {
			return ""
		}
		// Strip prefix, lowercase, replace double-underscore separator
		// with the koanf delim. Anything outside the prefix is dropped
		// upstream by env.Provider, but be defensive.
		s = strings.TrimPrefix(s, envPrefix)
		s = strings.ToLower(s)
		s = strings.ReplaceAll(s, envDelim, koanfDelim)
		return s
	})
	return k.Load(provider, nil)
}

// rawMapProvider is the smallest possible koanf.Provider that returns
// an in-memory map. Used to seed Defaults without a custom file.
type rawMapProvider struct {
	m map[string]any
}

// ReadBytes is unsupported. koanf only calls this when a parser is
// passed, which we don't.
func (rawMapProvider) ReadBytes() ([]byte, error) {
	return nil, errors.New("config: rawMapProvider does not support ReadBytes")
}

// Read returns the seeded map directly to koanf. The keys are dot-
// separated and koanf flattens them into the nested tree using its
// configured delimiter.
func (p rawMapProvider) Read() (map[string]any, error) {
	out := make(map[string]any, len(p.m))
	for k, v := range p.m {
		out[k] = v
	}
	return out, nil
}
