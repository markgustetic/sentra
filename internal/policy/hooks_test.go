package policy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

// A hook runs in the operator's shell with the operator's environment,
// but a policy run is also the process holding the repo passphrase and
// the failure-webhook URL — and its stdout is a timer log under
// ~/Library/Logs. Neither the command line (PGPASSWORD=… inline) nor
// SENTRA_* may reach the hook's output or its child environment.

func TestRunHook_EchoesOnlyTheLabel(t *testing.T) {
	var out bytes.Buffer
	script := "PGPASSWORD=hunter2 true"
	if err := RunHook(context.Background(), &out, "before", script); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if strings.Contains(out.String(), "hunter2") || strings.Contains(out.String(), "PGPASSWORD") {
		t.Fatalf("hook command line leaked into the log:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "hook before: running") {
		t.Fatalf("label line missing:\n%s", out.String())
	}
}

// TestRunHook_ScrubsSecretsFromEnvironment: the rule is every SENTRA_*
// variable plus the configured webhook env var, whatever its name; the
// rest of the environment (PATH, HOME, the operator's own PG* vars)
// passes through unchanged so the hook still works.
func TestRunHook_ScrubsSecretsFromEnvironment(t *testing.T) {
	t.Setenv("SENTRA_PASSPHRASE", "hunter2")
	t.Setenv("SENTRA_REPO__S3__BUCKET", "b")
	t.Setenv("MY_ALERT_URL", "https://hooks.example/secret-token")
	t.Setenv("PGHOST", "db.local")

	dump := filepath.Join(t.TempDir(), "env.txt")
	var out bytes.Buffer
	err := RunHook(context.Background(), &out, "before", "env > "+dump, "MY_ALERT_URL")
	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	raw, err := os.ReadFile(dump) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	env := string(raw)
	for _, leaked := range []string{"SENTRA_PASSPHRASE", "SENTRA_REPO__S3__BUCKET", "MY_ALERT_URL", "hunter2", "secret-token"} {
		if strings.Contains(env, leaked) {
			t.Errorf("hook environment carries %s", leaked)
		}
	}
	for _, kept := range []string{"PGHOST=db.local", "PATH="} {
		if !strings.Contains(env, kept) {
			t.Errorf("hook environment lost %s", kept)
		}
	}
}

// TestFireFailureHooks_OnFailureHookIsScrubbedToo: the on_failure
// command has the webhook env var name in hand — it must be dropped
// there as well, not only for before/after.
func TestFireFailureHooks_OnFailureHookIsScrubbedToo(t *testing.T) {
	t.Setenv("SENTRA_PASSPHRASE", "hunter2")
	t.Setenv("MY_ALERT_URL", "")
	dump := filepath.Join(t.TempDir(), "env.txt")
	var out bytes.Buffer
	FireFailureHooks(context.Background(), &out, "p", config.PolicyHooks{OnFailure: "env > " + dump, OnFailureWebhookEnv: "MY_ALERT_URL"}, errors.New("boom"))
	raw, err := os.ReadFile(dump) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SENTRA_PASSPHRASE") || strings.Contains(string(raw), "MY_ALERT_URL") {
		t.Fatalf("on_failure hook environment carries a secret:\n%s", raw)
	}
}

func TestHookEnv(t *testing.T) {
	in := []string{"PATH=/bin", "SENTRA_PASSPHRASE=x", "sentra_lower=keep", "HOOK_URL=u", "SENTRA_=empty", "HOME=/h"}
	got := HookEnv(in, "HOOK_URL")
	want := "PATH=/bin sentra_lower=keep HOME=/h"
	if strings.Join(got, " ") != want {
		t.Fatalf("HookEnv: got %q, want %q", strings.Join(got, " "), want)
	}
	// An empty webhook name drops only SENTRA_*; nothing else is
	// mistaken for it.
	if got := HookEnv([]string{"=odd", "A=1"}, ""); strings.Join(got, " ") != "=odd A=1" {
		t.Fatalf("HookEnv with no webhook name: got %q", got)
	}
}
