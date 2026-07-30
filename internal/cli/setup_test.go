package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/tui"
)

// launcherDeps builds UIDeps for the `sentra setup` launcher. PassphraseWithConfig
// is a t.Fatal on purpose: setup always passes forceSetup=true, which always
// takes runUI's launch-without-opening-a-repo branch, so the interactive
// resolver must never be reached. A huh prompt firing there would fight the
// tea.Program the launcher is about to start for os.Stdin.
func launcherDeps(t *testing.T, captured *tui.App) UIDeps {
	t.Helper()
	return UIDeps{
		RepoDeps: RepoDeps{
			Stdout: io.Discard,
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return blobstore.NewMemory(), nil
			},
			PassphraseWithConfig: func(_ *config.Config) ([]byte, error) {
				t.Fatal("interactive passphrase resolver must not run on the launch path")
				return nil, nil
			},
		},
		Run: func(app tui.App) error { *captured = app; return nil },
	}
}

// TestSetup_LaunchesWizardOnFirstRun: the launcher's whole job is landing the
// TUI on the wizard. No config present is the plain case.
func TestSetup_LaunchesWizardOnFirstRun(t *testing.T) {
	chDir(t, t.TempDir())
	var captured tui.App
	cmd := NewSetup(launcherDeps(t, &captured))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := captured.Deps().InitialView; got != "setup" {
		t.Errorf("InitialView = %q, want setup", got)
	}
}

// TestSetup_RejectsForceFlag: --force was removed when the wizard's review
// stage became the overwrite gate. Pinned so it cannot quietly return; a
// silently-accepted --force would read as "confirmed" to a scripted caller
// while the TUI still waits at the review prompt.
func TestSetup_RejectsForceFlag(t *testing.T) {
	chDir(t, t.TempDir())
	var captured tui.App
	cmd := NewSetup(launcherDeps(t, &captured))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--force"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("setup --force must fail: the flag no longer exists")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("want an unknown-flag error, got: %v", err)
	}
}

// TestSetup_IAMPolicySubcommandStillRegistered: `setup iam-policy` prints a
// least-privilege policy for an arbitrary bucket and is deliberately
// CLI-only. It must survive the launcher rewrite.
func TestSetup_IAMPolicySubcommandStillRegistered(t *testing.T) {
	var captured tui.App
	cmd := NewSetup(launcherDeps(t, &captured))
	for _, sub := range cmd.Commands() {
		if sub.Name() == "iam-policy" {
			return
		}
	}
	t.Fatal("setup iam-policy must stay registered under the launcher")
}

// TestSetup_CustomConfigPath: --config must reach runUI, so the wizard writes
// back to the file the operator named.
func TestSetup_CustomConfigPath(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	var captured tui.App
	cmd := NewSetup(launcherDeps(t, &captured))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--config", "custom.yaml"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := captured.Deps().ConfigPath; !strings.HasSuffix(got, "custom.yaml") {
		t.Errorf("ConfigPath = %q, want it to end in custom.yaml", got)
	}
}

// TestSetupIAMPolicy_PrintsLeastPrivilegePolicy drives the subcommand's own
// flag plumbing end to end. setup.WriteIAMPolicy has its own golden test, but
// nothing below the CLI wires --bucket/--prefix into it, so this stays here.
func TestSetupIAMPolicy_PrintsLeastPrivilegePolicy(t *testing.T) {
	out := &bytes.Buffer{}
	var captured tui.App
	deps := launcherDeps(t, &captured)
	deps.Stdout = out
	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"iam-policy", "--bucket", "sentra-prod", "--prefix", "backups/"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var policy setupIAMPolicyDocument
	if err := json.Unmarshal(out.Bytes(), &policy); err != nil {
		t.Fatalf("decode policy: %v\n%s", err, out.String())
	}
	if policy.Version != "2012-10-17" {
		t.Fatalf("policy version: got %q", policy.Version)
	}
	got := out.String()
	for _, want := range []string{
		"arn:aws:s3:::sentra-prod",
		"arn:aws:s3:::sentra-prod/backups/*",
		"s3:PutBucketEncryption",
		"s3:GetObject",
		`"backups/*"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("policy missing %q:\n%s", want, got)
		}
	}
}

// TestSetupIAMPolicy_RejectsInvalidBucket: the subcommand validates before
// printing, so an operator cannot copy a policy naming a bucket S3 will refuse.
func TestSetupIAMPolicy_RejectsInvalidBucket(t *testing.T) {
	var captured tui.App
	cmd := NewSetup(launcherDeps(t, &captured))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"iam-policy", "--bucket", "Bad_Bucket"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected invalid bucket error, got nil")
	}
	if !strings.Contains(err.Error(), "lowercase") {
		t.Errorf("error should explain bucket naming, got %v", err)
	}
}

func TestSetup_RegisteredOnRoot(t *testing.T) {
	var captured tui.App
	root := NewRoot("v", "c", "d")
	root.AddCommand(NewSetup(launcherDeps(t, &captured)))
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "setup" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("setup command not registered on root")
	}
}
