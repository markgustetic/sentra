package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// policyHookFixture builds a runnable policy named "hooked". hooks
// receives the test dir so hook commands can reference paths inside
// it. Returns (deps, the shared store, the test dir).
func policyHookFixture(t *testing.T, hooks func(dir string) config.PolicyHooks) (PolicyDeps, *blobstore.Memory, string) {
	t.Helper()
	dir := t.TempDir()
	chDir(t, dir)
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["hooked"] = config.PolicyConfig{
		Paths:    []string{src},
		Schedule: config.PolicySchedule{Cadence: "manual"},
		Hooks:    hooks(dir),
	}
	writePolicyConfigFile(t, dir, &cfg)

	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	r.Close()

	deps := PolicyDeps{
		RepoDeps: RepoDeps{
			NewStore: func(context.Context, *config.Config) (blobstore.Store, error) {
				return store, nil
			},
			Passphrase: func() ([]byte, error) { return []byte("hunter2"), nil },
			Stdout:     &bytes.Buffer{},
		},
	}
	return deps, store, dir
}

// TestPolicyRun_BeforeHookOutputIsCaptured: the before hook completes
// before any snapshot is taken — a dump written by the hook into the
// source tree is part of the snapshot.
func TestPolicyRun_BeforeHookOutputIsCaptured(t *testing.T) {
	deps, store, _ := policyHookFixture(t, func(dir string) config.PolicyHooks {
		return config.PolicyHooks{
			Before: "echo hook-output > " + filepath.Join(dir, "src", "dump.txt"),
		}
	})
	cmd := NewPolicy(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"run", "hooked"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots: %v err=%v", snaps, err)
	}
	m, err := r.LoadSnapshot(context.Background(), snaps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, fe := range m.Tree {
		if fe.Path == "dump.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("before-hook output missing from snapshot tree: %+v", m.Tree)
	}
}

// TestPolicyRun_FailingBeforeHookAbortsAndFiresOnFailure: a failing
// before hook must abort the run (no snapshot — the hook exists so
// the backup captures its output) and fire the on_failure hook.
func TestPolicyRun_FailingBeforeHookAbortsAndFiresOnFailure(t *testing.T) {
	var marker string
	deps, store, _ := policyHookFixture(t, func(dir string) config.PolicyHooks {
		marker = filepath.Join(dir, "failed.marker")
		return config.PolicyHooks{
			Before:    "exit 7",
			OnFailure: "touch " + marker,
		}
	})
	cmd := NewPolicy(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"run", "hooked"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("failing before hook must fail the run")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("on_failure hook did not run: %v", err)
	}
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 0 {
		t.Errorf("no snapshot should exist after an aborted run, got %d", len(snaps))
	}
}

// TestPolicyRun_FailureWebhookPostsFromEnvURL: the webhook URL lives
// in an ENV VAR (only its NAME is in sentra.yaml — no secrets in
// config); on failure the run POSTs a {policy, status, error} JSON.
func TestPolicyRun_FailureWebhookPostsFromEnvURL(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.Store(string(body))
	}))
	defer srv.Close()
	t.Setenv("SENTRA_TEST_WEBHOOK", srv.URL)

	deps, _, _ := policyHookFixture(t, func(string) config.PolicyHooks {
		return config.PolicyHooks{
			Before:              "exit 3",
			OnFailureWebhookEnv: "SENTRA_TEST_WEBHOOK",
		}
	})
	cmd := NewPolicy(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"run", "hooked"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("run must fail")
	}
	body, _ := got.Load().(string)
	if !strings.Contains(body, `"hooked"`) || !strings.Contains(body, `"failed"`) {
		t.Errorf("webhook payload missing policy/status: %q", body)
	}
}

// TestPolicyAdd_ReplacePreservesHooks: hooks are config-authored (no
// CLI flag manages them), so `policy add --replace` must carry the
// existing policy's hooks forward — silently deleting a PagerDuty
// notifier because someone added a path is how alerts die.
func TestPolicyAdd_ReplacePreservesHooks(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["nightly"] = config.PolicyConfig{
		Paths:    []string{"/old"},
		Schedule: config.PolicySchedule{Cadence: "manual"},
		Hooks: config.PolicyHooks{
			Before:              "pg_dump > dump.sql",
			OnFailureWebhookEnv: "SENTRA_ALERT_URL",
		},
	}
	writePolicyConfigFile(t, dir, &cfg)

	out := &bytes.Buffer{}
	cmd := NewPolicy(PolicyDeps{RepoDeps: RepoDeps{Stdout: out}})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"add", "nightly", "--path", "/new", "--schedule", "manual", "--replace"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, err := config.Load(filepath.Join(dir, "sentra.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p := got.Policies["nightly"]
	if len(p.Paths) != 1 || p.Paths[0] != "/new" {
		t.Fatalf("replace did not apply: %+v", p.Paths)
	}
	if p.Hooks.Before != "pg_dump > dump.sql" || p.Hooks.OnFailureWebhookEnv != "SENTRA_ALERT_URL" {
		t.Errorf("replace dropped the hand-authored hooks: %+v", p.Hooks)
	}
}
