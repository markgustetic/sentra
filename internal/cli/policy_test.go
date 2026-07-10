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

func writePolicyConfigFile(t *testing.T, dir string, cfg *config.Config) string {
	t.Helper()
	path := filepath.Join(dir, "sentra.yaml")
	if err := config.Write(path, cfg); err != nil {
		t.Fatalf("write sentra.yaml: %v", err)
	}
	return path
}

// TestPolicyAddRemove_KeepEnvOverridesOutOfFile: `policy add` / `policy remove`
// edit the policies map, so they must leave repo.s3 exactly as the file had it.
// Rewriting the resolved config would bake a transient SENTRA_* override in.
func TestPolicyAddRemove_KeepEnvOverridesOutOfFile(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "real-bucket"
	cfg.Repo.S3.Region = "us-west-2"
	path := writePolicyConfigFile(t, dir, &cfg)

	t.Setenv("SENTRA_REPO__S3__BUCKET", "ephemeral-env-bucket")
	t.Setenv("SENTRA_REPO__S3__REGION", "eu-central-1")

	runPolicyCmd := func(t *testing.T, args ...string) {
		t.Helper()
		out := &bytes.Buffer{}
		cmd := NewPolicy(PolicyDeps{RepoDeps: RepoDeps{Stdout: out}})
		cmd.SetOut(out)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute %v: %v", args, err)
		}
	}
	assertRepoUntouched := func(t *testing.T, step string) {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if strings.Contains(string(body), "ephemeral-env-bucket") {
			t.Errorf("%s persisted the env bucket:\n%s", step, body)
		}
		if strings.Contains(string(body), "eu-central-1") {
			t.Errorf("%s persisted the env region:\n%s", step, body)
		}
		if !strings.Contains(string(body), "real-bucket") {
			t.Errorf("%s dropped the real bucket:\n%s", step, body)
		}
	}

	runPolicyCmd(t, "add", "home", "--path", "~/Documents", "--schedule", "manual")
	assertRepoUntouched(t, "policy add")

	runPolicyCmd(t, "remove", "home")
	assertRepoUntouched(t, "policy remove")

	// The edits themselves still take effect.
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := got.Policies["home"]; ok {
		t.Error("policy remove did not delete the policy")
	}
}

func TestPolicyAdd_WritesConfigPolicy(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	writePolicyConfigFile(t, dir, &cfg)

	out := &bytes.Buffer{}
	cmd := NewPolicy(PolicyDeps{RepoDeps: RepoDeps{Stdout: out}})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"add", "home",
		"--path", "~/Documents",
		"--tag", "home",
		"--tag", "daily",
		"--schedule", "daily@03:00",
		"--check",
		"--prune", "dry-run",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, err := config.Load(filepath.Join(dir, "sentra.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := got.Policies["home"]
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
	if !strings.Contains(out.String(), "Policy added") {
		t.Fatalf("output missing success summary: %q", out.String())
	}
}

func TestPolicyListAndShow(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["home"] = config.PolicyConfig{
		Paths: []string{"~/Documents"},
		Tags:  []string{"home"},
		Schedule: config.PolicySchedule{
			Cadence: "daily",
			At:      "03:00",
		},
		AfterBackup: config.PolicyAfterBackup{Check: true, Prune: "dry-run"},
	}
	writePolicyConfigFile(t, dir, &cfg)

	out := &bytes.Buffer{}
	cmd := NewPolicy(PolicyDeps{RepoDeps: RepoDeps{Stdout: out}})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list execute: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "home") || !strings.Contains(got, "daily@03:00") {
		t.Fatalf("list output missing policy details: %q", got)
	}

	out.Reset()
	cmd = NewPolicy(PolicyDeps{RepoDeps: RepoDeps{Stdout: out}})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"show", "home"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("show execute: %v", err)
	}
	got := out.String()
	for _, want := range []string{"home", "~/Documents", "daily@03:00", "check: true", "prune: dry-run"} {
		if !strings.Contains(got, want) {
			t.Fatalf("show output missing %q: %q", want, got)
		}
	}
}

func TestPolicyRemove_DeletesPolicy(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["home"] = config.PolicyConfig{Paths: []string{"."}}
	writePolicyConfigFile(t, dir, &cfg)

	out := &bytes.Buffer{}
	cmd := NewPolicy(PolicyDeps{RepoDeps: RepoDeps{Stdout: out}})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"remove", "home"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got, err := config.Load(filepath.Join(dir, "sentra.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := got.Policies["home"]; ok {
		t.Fatalf("policy still present: %+v", got.Policies)
	}
	if !strings.Contains(out.String(), "Policy removed") {
		t.Fatalf("output missing removal summary: %q", out.String())
	}
}

func TestPolicyRun_CreatesTaggedSnapshot(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["home"] = config.PolicyConfig{
		Paths: []string{src},
		Tags:  []string{"daily"},
		Schedule: config.PolicySchedule{
			Cadence: "manual",
		},
		AfterBackup: config.PolicyAfterBackup{Check: true},
	}
	writePolicyConfigFile(t, dir, &cfg)

	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	out := &bytes.Buffer{}
	deps := PolicyDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return store, nil
			},
			Passphrase: func() ([]byte, error) {
				return []byte("hunter2"), nil
			},
			Stdout: out,
		},
		Stderr: io.Discard,
	}
	cmd := NewPolicy(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"run", "home"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	r, err = repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snapshots: got %d, want 1", len(snaps))
	}
	if !strings.Contains(snaps[0].Tag, "policy:home") || !strings.Contains(snaps[0].Tag, "daily") {
		t.Fatalf("snapshot tag: got %q", snaps[0].Tag)
	}
	got := out.String()
	if !strings.Contains(got, "Policy run complete") || !strings.Contains(got, "check: healthy") {
		t.Fatalf("output missing run summary: %q", got)
	}
}
