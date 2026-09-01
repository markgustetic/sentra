package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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

// TestSetup_ForcesWizardOverExistingConfig is the launcher's ONE distinguishing
// behavior: it passes forceSetup=true. With a config on disk and no resolvable
// passphrase, `sentra ui` routes to the unlock gate; `sentra setup` must reach
// the wizard anyway, because reconfiguring must not demand the passphrase for a
// repo the operator may be replacing. Every other launcher test runs in an empty
// dir, where both values of forceSetup land on "setup" — only this one fails if
// the flag flips.
func TestSetup_ForcesWizardOverExistingConfig(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	// Keyring off and no env/file source, so probeLaunchState reports the repo
	// as locked. `sentra ui` would show the unlock view here.
	body := []byte("repo:\n  s3:\n    bucket: existing-bucket\n")
	if err := os.WriteFile(filepath.Join(dir, configFileName), body, 0o600); err != nil {
		t.Fatalf("write existing config: %v", err)
	}
	t.Setenv("SENTRA_PASSPHRASE", "")

	var captured tui.App
	cmd := NewSetup(launcherDeps(t, &captured))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	d := captured.Deps()
	if d.InitialView != "setup" {
		t.Errorf("InitialView = %q, want setup — the launcher must force the wizard "+
			"past the unlock gate", d.InitialView)
	}
	if !d.Reconfigure {
		t.Error("Reconfigure = false; forcing the wizard over an existing config must " +
			"arm the review stage's overwrite warning")
	}
	if got := d.Config.Repo.S3.Bucket; got != "existing-bucket" {
		t.Errorf("wizard seeded with bucket %q, want the on-disk config's existing-bucket", got)
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
		"s3:PutEncryptionConfiguration",
		"s3:GetObject",
		"s3:ListBucket",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("policy missing %q:\n%s", want, got)
		}
	}
	// A prefix condition on the ListBucket statement denies Sentra's own
	// HeadBucket probes (no s3:prefix in that request context), so the emitted
	// JSON must never carry one.
	if strings.Contains(got, "Condition") {
		t.Fatalf("policy must not contain a Condition:\n%s", got)
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
