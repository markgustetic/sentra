package cli

import (
	"bytes"
	"context"
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

// initFixture builds an InitDeps that uses a fresh in-memory store
// and a static passphrase. The store is shared across the deps so
// tests can inspect it after the command runs.
func initFixture(t *testing.T, passphrase string) (InitDeps, *blobstore.Memory, *bytes.Buffer) {
	t.Helper()
	store := blobstore.NewMemory()
	out := &bytes.Buffer{}
	deps := InitDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) {
			return []byte(passphrase), nil
		},
		Stdout: out,
	}
	return deps, store, out
}

// chDir cds the test process into dir and restores the previous wd
// on cleanup. The init command works against the current directory,
// so tests need a stable cwd.
func chDir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// TestInit_FreshDir creates sentra.yaml and the encrypted config blob
// in the injected store. After running, Open(memory, passphrase)
// should succeed.
func TestInit_FreshDir(t *testing.T) {
	chDir(t, t.TempDir())
	deps, store, _ := initFixture(t, "hunter2")

	cmd := NewInit(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--bucket", "test-bucket"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// sentra.yaml must exist locally.
	if _, err := os.Stat("sentra.yaml"); err != nil {
		t.Fatalf("sentra.yaml not created: %v", err)
	}

	// The config blob must exist in the in-memory store, and it must
	// open with the passphrase we injected.
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
}

// TestInit_RefusesExisting refuses to clobber a pre-existing
// sentra.yaml without --force. The on-disk repo state would be
// orphaned by re-init, so the safety guard is critical.
func TestInit_RefusesExisting(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte("repo: {}\n"), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	deps, _, _ := initFixture(t, "hunter2")

	cmd := NewInit(deps)
	cmd.SetOut(io.Discard)
	errBuf := &bytes.Buffer{}
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error on existing sentra.yaml, got nil")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "exists") && !strings.Contains(msg, "force") {
		t.Errorf("expected refusal mentioning exists/force, got %v", err)
	}
}

// TestInit_ForceOverwrites with --force replaces an existing
// sentra.yaml *and* re-bootstraps the repo. After force-init with a
// new passphrase, only the new passphrase should open the repo.
func TestInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte("# stale\n"), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	deps, store, _ := initFixture(t, "newpass")

	cmd := NewInit(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--force", "--bucket", "test-bucket"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Old passphrase must NOT open the new repo.
	if _, err := repo.Open(context.Background(), store, []byte("oldpass")); err == nil {
		t.Fatal("expected wrong-passphrase error opening with stale pass")
	} else if !errors.Is(err, repo.ErrWrongPassphrase) {
		t.Fatalf("expected ErrWrongPassphrase, got %v", err)
	}
	// New passphrase opens.
	r, err := repo.Open(context.Background(), store, []byte("newpass"))
	if err != nil {
		t.Fatalf("Open with new pass: %v", err)
	}
	r.Close()
}

// TestInit_PrintsSummary asserts the user gets some confirmation
// output. The exact wording is loose; the important thing is the
// run isn't silent.
func TestInit_PrintsSummary(t *testing.T) {
	chDir(t, t.TempDir())
	deps, _, out := initFixture(t, "hunter2")

	cmd := NewInit(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--bucket", "test-bucket"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := out.String()
	if !strings.Contains(strings.ToLower(got), "init") &&
		!strings.Contains(strings.ToLower(got), "sentra.yaml") {
		t.Errorf("expected init summary in output, got %q", got)
	}
}

// TestInit_RegisteredOnRoot verifies the command shows up under the
// root command's children (so users see it in `sentra --help`).
func TestInit_RegisteredOnRoot(t *testing.T) {
	deps, _, _ := initFixture(t, "x")
	root := NewRoot("v", "c", "d")
	root.AddCommand(NewInit(deps))
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "init" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("init command not registered on root")
	}
}

func TestRenderConfigYAML_IncludesPolicies(t *testing.T) {
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["home"] = config.PolicyConfig{
		Paths: []string{"~/Documents"},
		Tags:  []string{"home", "daily"},
		Schedule: config.PolicySchedule{
			Cadence: "daily",
			At:      "03:00",
		},
		AfterBackup: config.PolicyAfterBackup{
			Check: true,
			Prune: "dry-run",
		},
	}

	body := renderConfigYAML(&cfg)
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

// TestInit_RequiresBucketFlag refuses to run with no sentra.yaml and
// no --bucket flag. Without this guard, `sentra init` against a
// production S3 store has no path that produces a working config —
// the user has no way to specify the bucket.
func TestInit_RequiresBucketFlag(t *testing.T) {
	chDir(t, t.TempDir())
	// Custom deps without injecting a bucket — production wiring would
	// fail when newS3Store sees an empty bucket. We use a NewStore that
	// enforces the same precondition so the test fails with a clear error.
	store := blobstore.NewMemory()
	deps := InitDeps{
		NewStore: func(_ context.Context, cfg *config.Config) (blobstore.Store, error) {
			if cfg.Repo.S3.Bucket == "" {
				return nil, errors.New("repo.s3.bucket not set")
			}
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte("hunter2"), nil },
		Stdout:     io.Discard,
	}
	cmd := NewInit(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --bucket is not provided, got nil")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "bucket") {
		t.Errorf("error should mention bucket, got %v", err)
	}
}

// TestInit_AcceptsBucketFlag passes flag-driven values through to the
// store factory and persists them to sentra.yaml on success.
func TestInit_AcceptsBucketFlag(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	store := blobstore.NewMemory()
	var captured *config.Config
	deps := InitDeps{
		NewStore: func(_ context.Context, cfg *config.Config) (blobstore.Store, error) {
			captured = cfg
			if cfg.Repo.S3.Bucket == "" {
				return nil, errors.New("repo.s3.bucket not set")
			}
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte("hunter2"), nil },
		Stdout:     io.Discard,
	}
	cmd := NewInit(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--bucket", "my-bucket",
		"--endpoint-url", "http://localhost:9000",
		"--region", "us-east-1",
		"--prefix", "sentra/",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if captured == nil {
		t.Fatal("NewStore not called")
	}
	if captured.Repo.S3.Bucket != "my-bucket" {
		t.Errorf("bucket not passed: got %q", captured.Repo.S3.Bucket)
	}
	if captured.Repo.S3.EndpointURL != "http://localhost:9000" {
		t.Errorf("endpoint not passed: got %q", captured.Repo.S3.EndpointURL)
	}
	if captured.Repo.S3.Region != "us-east-1" {
		t.Errorf("region not passed: got %q", captured.Repo.S3.Region)
	}
	if captured.Repo.S3.Prefix != "sentra/" {
		t.Errorf("prefix not passed: got %q", captured.Repo.S3.Prefix)
	}

	// sentra.yaml must contain the flag values so subsequent commands
	// pick them up via config.Load.
	body, err := os.ReadFile(filepath.Join(dir, "sentra.yaml"))
	if err != nil {
		t.Fatalf("read sentra.yaml: %v", err)
	}
	cfg, err := config.Load(filepath.Join(dir, "sentra.yaml"))
	if err != nil {
		t.Fatalf("Load(sentra.yaml): %v\nbody:\n%s", err, body)
	}
	if cfg.Repo.S3.Bucket != "my-bucket" {
		t.Errorf("persisted bucket: got %q, want my-bucket", cfg.Repo.S3.Bucket)
	}
	if cfg.Repo.S3.EndpointURL != "http://localhost:9000" {
		t.Errorf("persisted endpoint: got %q", cfg.Repo.S3.EndpointURL)
	}
	if cfg.Repo.S3.Region != "us-east-1" {
		t.Errorf("persisted region: got %q", cfg.Repo.S3.Region)
	}
	if cfg.Repo.S3.Prefix != "sentra/" {
		t.Errorf("persisted prefix: got %q", cfg.Repo.S3.Prefix)
	}
}

// TestInit_EnvPassphraseSkipsPrompt verifies the production wiring's
// contract: when SENTRA_PASSPHRASE is set, the passphrase resolver
// short-circuits to that value and never invokes the interactive
// prompt. We assert this by passing a panicking-prompt fake — if the
// prompt fires, the test would crash.
//
// This test stands in for "scripts can run sentra non-interactively
// when SENTRA_PASSPHRASE is set", which is the user-facing contract
// of issue I1.
func TestInit_EnvPassphraseSkipsPrompt(t *testing.T) {
	chDir(t, t.TempDir())
	t.Setenv("SENTRA_PASSPHRASE", "from-env-1234")
	store := blobstore.NewMemory()
	deps := InitDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		// Production builds the Passphrase callback by calling
		// config.Resolve under the hood. We exercise the same wiring
		// here: a Resolve call that would fall through to the prompt
		// if env weren't set must NOT call the prompt when env IS set.
		Passphrase: func() ([]byte, error) {
			return config.Resolve(config.ResolveOptions{
				Prompt: func() ([]byte, error) {
					panic("prompt should not be called when SENTRA_PASSPHRASE is set")
				},
			})
		},
		Stdout: io.Discard,
	}
	cmd := NewInit(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--bucket", "test"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

// TestInit_ForceMergesExistingConfig with --force re-bootstraps the
// repo using the existing sentra.yaml as the base for the store
// settings (so the user doesn't have to re-pass every flag). New
// flag values still override.
func TestInit_ForceUsesFlagsOverYAML(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	// Pre-existing sentra.yaml says bucket-A; flag says bucket-B.
	body := "repo:\n  s3:\n    bucket: bucket-A\n    region: us-west-2\n"
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	store := blobstore.NewMemory()
	var captured *config.Config
	deps := InitDeps{
		NewStore: func(_ context.Context, cfg *config.Config) (blobstore.Store, error) {
			captured = cfg
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte("hunter2"), nil },
		Stdout:     io.Discard,
	}
	cmd := NewInit(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--force", "--bucket", "bucket-B"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if captured.Repo.S3.Bucket != "bucket-B" {
		t.Errorf("flag should override yaml bucket: got %q", captured.Repo.S3.Bucket)
	}
	// Region from yaml should still be there since the flag was not set.
	if captured.Repo.S3.Region != "us-west-2" {
		t.Errorf("yaml region should be preserved: got %q", captured.Repo.S3.Region)
	}
}
