package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

// TestPolicyRun_HooksNeverSeeSentraSecrets: the CLI end of the hook
// scrub. A policy run holds SENTRA_PASSPHRASE and the webhook URL in
// its environment; a before hook that dumps `env` into the timer log
// must see neither, and the log must not echo the hook's own command
// line (where inline credentials live).
func TestPolicyRun_HooksNeverSeeSentraSecrets(t *testing.T) {
	var dump string
	deps, _, _ := policyHookFixture(t, func(dir string) config.PolicyHooks {
		dump = filepath.Join(dir, "env.txt")
		return config.PolicyHooks{
			Before:              "PGPASSWORD=inline-secret env > " + dump,
			OnFailureWebhookEnv: "SENTRA_TEST_ALERT_URL",
		}
	})
	t.Setenv("SENTRA_PASSPHRASE", "env-passphrase")
	t.Setenv("SENTRA_TEST_ALERT_URL", "https://hooks.example/token")
	out := &bytes.Buffer{}
	deps.Stdout = out

	cmd := NewPolicy(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"run", "hooked"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	raw, err := os.ReadFile(dump) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"SENTRA_PASSPHRASE", "env-passphrase", "SENTRA_TEST_ALERT_URL", "hooks.example"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("hook environment carries %s", leaked)
		}
	}
	if strings.Contains(out.String(), "inline-secret") {
		t.Errorf("run output echoed the hook command line:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "hook before: running") {
		t.Errorf("run output missing the hook label line:\n%s", out.String())
	}
}
