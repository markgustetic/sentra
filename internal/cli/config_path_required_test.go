package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/tui"
)

// The rule under test: an explicit --config names a file that must exist.
//
// config.Load tolerates a missing file on purpose — discovery and env-only
// loads depend on it — so a typo'd --config used to load Defaults(): read
// commands reported the empty config as if it were real ("No policies
// configured", exit 0; "repo.s3.bucket not set in sentra.yaml — edit the
// file" for a file that was never there), and the config.Update paths
// authored a new file at the typo. The guard therefore lives in the CLI, in
// front of every load, not in config.Load — which init and the wizard still
// need tolerant.

func missingConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "nope", "sentra.yaml")
}

func TestPolicyList_ExplicitMissingConfigFails(t *testing.T) {
	chDir(t, t.TempDir())
	missing := missingConfigPath(t)

	out := &bytes.Buffer{}
	cmd := NewPolicy(PolicyDeps{RepoDeps: RepoDeps{Stdout: out}})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"list", "--config", missing})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("policy list --config %s: want an error, got nil with output %q", missing, out.String())
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error must name the missing path %s: %v", missing, err)
	}
	if strings.Contains(out.String(), "No policies") {
		t.Errorf("a missing explicit config must not read as an empty one: %q", out.String())
	}
}

// The write path is the dangerous half: config.Update rebases on the file
// as it exists on disk, and "does not exist" rebased to Defaults() and
// wrote a brand-new sentra.yaml wherever the typo pointed.
func TestPolicyAdd_ExplicitMissingConfigWritesNothing(t *testing.T) {
	chDir(t, t.TempDir())
	missing := missingConfigPath(t)

	cmd := NewPolicy(PolicyDeps{RepoDeps: RepoDeps{Stdout: io.Discard}})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"add", "nightly", "--path", t.TempDir(), "--config", missing})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("policy add --config %s: want an error naming the path, got %v", missing, err)
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Errorf("policy add authored %s at the typo'd path", missing)
	}
}

// --dst-config is always explicit, so the same rule applies: a destination
// config that does not exist is an operator error, not an empty repo.
func TestSync_ExplicitMissingDstConfigFails(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	deps, _, _ := syncFixture(t, dir, "hunter2")
	missing := filepath.Join(dir, "absent", "dst.yaml")

	cmd := NewSync(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--dst-config", missing})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("sync --dst-config %s: want an error naming the path, got %v", missing, err)
	}
}

// The discovery path keeps config.Load's tolerance: with no --config, no
// ./sentra.yaml and no home config, commands still see Defaults(). "No
// config" is what routes a first run to the wizard, and `policy list` has
// nothing to report there rather than nothing to load.
func TestPolicyList_DiscoveryToleratesNoConfig(t *testing.T) {
	chDir(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	out := &bytes.Buffer{}
	cmd := NewPolicy(PolicyDeps{RepoDeps: RepoDeps{Stdout: out}})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("policy list with no config anywhere: %v", err)
	}
	if !strings.Contains(out.String(), "No policies configured") {
		t.Errorf("output = %q, want the empty-policies line", out.String())
	}
}

// sentra ui hosts the first-run wizard — the one flow that creates the
// file — so an explicit --config there may name a path that does not exist
// yet: the launch lands on the wizard, which writes back to that path.
// TestSetup_CustomConfigPath covers the same exemption for sentra setup.
func TestUI_ExplicitMissingConfigLandsOnWizard(t *testing.T) {
	chDir(t, t.TempDir())
	var captured tui.App
	cmd := NewUI(launcherDeps(t, &captured))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--config", "custom.yaml"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ui --config custom.yaml (absent): %v", err)
	}
	if got := captured.Deps().InitialView; got != "setup" {
		t.Errorf("InitialView = %q, want setup", got)
	}
	if got := captured.Deps().ConfigPath; !strings.HasSuffix(got, "custom.yaml") {
		t.Errorf("ConfigPath = %q, want it to end in custom.yaml", got)
	}
}
