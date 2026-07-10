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

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/tui"
)

// uiFixture builds a UIDeps wired to an in-memory repo. The runner
// stub captures the App that would have been launched and exits
// immediately so the test doesn't need a real terminal.
func uiFixture(t *testing.T, passphrase string) (UIDeps, *tui.App) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(passphrase))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return store, nil
			},
			Passphrase: func() ([]byte, error) { return []byte(passphrase), nil },
		},
		Provider: nil, // agent view will show the placeholder
		Run: func(app tui.App) error {
			captured = app
			return nil
		},
	}
	return deps, &captured
}

// TestUI_LaunchesApp verifies that the cobra command opens the repo
// and hands a constructed App to the Run hook. We don't actually
// run a Bubbletea program — that requires a TTY.
func TestUI_LaunchesApp(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, captured := uiFixture(t, "hunter2")
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// runUI now launches with the welcome splash on by default (no
	// ui.hide_splash in this fixture's config), so the very first View()
	// is the splash overlay rather than the frame. Dismiss it with a
	// keystroke — same as any real launch — before inspecting the frame.
	m, _ := captured.Update(tea.KeyMsg{Type: tea.KeyEnter})
	view := m.(tui.App).View()
	if !strings.Contains(view, "sentra") {
		t.Errorf("captured app's view did not contain brand: %s", view)
	}
}

// TestUI_PropagatesRunError ensures errors from Run() bubble out as
// the cobra command's exit code path.
func TestUI_PropagatesRunError(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, _ := uiFixture(t, "hunter2")
	deps.Run = func(_ tui.App) error {
		return errors.New("tea boom")
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error from execute")
	}
	if !strings.Contains(err.Error(), "tea boom") {
		t.Errorf("expected tea boom in error, got %v", err)
	}
}

// TestUI_PassesProviderToApp verifies the Provider deps reach the
// constructed App via tui.Deps. We pass a non-nil llm.Provider and
// assert the captured App's agent view does NOT show the placeholder
// (since a provider is configured).
func TestUI_PassesProviderToApp(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	// A non-interactive passphrase source so launch routing lands on the
	// dashboard (not the unlock gate); otherwise the agent-tab assertion
	// below would render the unlock view and vacuously pass.
	t.Setenv("SENTRA_PASSPHRASE", "hunter2")
	deps, captured := uiFixture(t, "hunter2")
	deps.Provider = &llm.FakeProvider{}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Switch to the agent tab — when a provider is configured the
	// agent placeholder ("ANTHROPIC_API_KEY") should be absent.
	updated, _ := captured.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	view := updated.(tui.App).View()
	if strings.Contains(view, "ANTHROPIC_API_KEY") {
		t.Errorf("agent view showed configure hint despite provider being wired: %s", view)
	}
}

// TestRoot_BareSentraLaunchesUI ensures invoking root with no
// subcommand falls through to the ui command rather than printing
// the help text. We register a tiny ui stub that records its
// invocation; no other subcommands are wired so any non-flag arg
// would be unmatched too — but bare invocation specifically must
// reach our ui handler.
func TestRoot_BareSentraLaunchesUI(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, _ := uiFixture(t, "hunter2")
	uiCalled := false
	deps.Run = func(_ tui.App) error {
		uiCalled = true
		return nil
	}
	root := NewRoot("v", "c", "d")
	root.AddCommand(NewUI(deps))
	SetUIAsDefault(root, deps)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(io.Discard)
	root.SetArgs([]string{})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !uiCalled {
		t.Errorf("bare sentra invocation did not launch ui")
	}
}

// TestRunUI_ThreadsNewDepsFields proves runUI populates the four Unit-1
// Deps fields from UIDeps: the store factory, the action registry, the
// keyring saver, and an absolute ConfigPath. No secret is threaded — the
// func values are call-time hooks and ConfigPath is plain data.
func TestRunUI_ThreadsNewDepsFields(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, ".")

	deps, captured := uiFixture(t, "hunter2")
	var saveKeyringCalled bool
	deps.Actions = action.NewDefaultRegistry()
	deps.SavePassphrase = func(_ *config.Config, _ []byte) error {
		saveKeyringCalled = true
		return nil
	}

	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	// Relative --config so we can assert runUI absolutizes it.
	cmd.SetArgs([]string{"--config", "sentra.yaml"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	d := captured.Deps()
	wantAbs, err := filepath.Abs("sentra.yaml")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	if d.ConfigPath != wantAbs {
		t.Errorf("Deps.ConfigPath = %q, want absolutized %q", d.ConfigPath, wantAbs)
	}
	if d.NewStore == nil {
		t.Error("Deps.NewStore not threaded")
	}
	if d.Actions == nil {
		t.Error("Deps.Actions not threaded")
	}
	if d.SaveKeyringPassphrase == nil {
		t.Fatal("Deps.SaveKeyringPassphrase not threaded")
	}
	if err := d.SaveKeyringPassphrase(nil, nil); err != nil || !saveKeyringCalled {
		t.Error("Deps.SaveKeyringPassphrase is not the func passed via UIDeps")
	}
}

// TestRunUI_ThreadsSetupEffects proves runUI constructs setup.DefaultEffects()
// and threads it into tui.Deps when UIDeps carries no explicit override. The
// effects seam holds no secrets — it is a call-time interface of func hooks.
func TestRunUI_ThreadsSetupEffects(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, ".")

	deps, captured := uiFixture(t, "hunter2")
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if captured.Deps().SetupEffects == nil {
		t.Error("Deps.SetupEffects not threaded from runUI")
	}
}

// TestRunUI_MissingConfigLaunchesFirstRunWizard: with no sentra.yaml present,
// runUI must NOT try to open a repo (there is none). It launches the TUI on the
// setup wizard with a nil Repo, so the first-run experience is the wizard.
func TestRunUI_MissingConfigLaunchesFirstRunWizard(t *testing.T) {
	chDir(t, t.TempDir()) // empty dir: no sentra.yaml
	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				t.Fatal("NewStore must not be called on the first-run path")
				return nil, nil
			},
			PassphraseWithConfig: func(_ *config.Config) ([]byte, error) {
				t.Fatal("passphrase must not be resolved on the first-run path")
				return nil, nil
			},
		},
		Run: func(app tui.App) error { captured = app; return nil },
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	d := captured.Deps()
	if d.InitialView != "setup" {
		t.Errorf("InitialView = %q, want setup", d.InitialView)
	}
	if d.Repo != nil {
		t.Error("first-run App must carry a nil Repo")
	}
}

// TestRunUI_SeedsWizardConfigOnFirstRun: on the first-run path (no sentra.yaml),
// a non-nil UIDeps.SetupSeedConfig must reach the wizard as tui.Deps.Config so
// the setup wizard pre-fills its S3 fields — WITHOUT writing any config file
// (it stays first-run; the wizard writes on completion). The canonical caller
// is `sentra local`, which seeds MinIO coordinates.
func TestRunUI_SeedsWizardConfigOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir) // empty dir: no sentra.yaml → first run

	seed := &config.Config{}
	seed.Repo.S3.EndpointURL = "http://localhost:9000"
	seed.Repo.S3.Bucket = "sentra-test"
	seed.Repo.S3.Region = "us-east-1"

	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				t.Fatal("NewStore must not be called on the first-run path")
				return nil, nil
			},
		},
		SetupSeedConfig: seed,
		Run:             func(app tui.App) error { captured = app; return nil },
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	d := captured.Deps()
	if d.InitialView != "setup" {
		t.Errorf("InitialView = %q, want setup", d.InitialView)
	}
	if d.Config == nil {
		t.Fatal("wizard Deps.Config is nil; seed did not reach the wizard")
	}
	if d.Config.Repo.S3.EndpointURL != "http://localhost:9000" {
		t.Errorf("seed endpoint: got %q, want http://localhost:9000", d.Config.Repo.S3.EndpointURL)
	}
	if d.Config.Repo.S3.Bucket != "sentra-test" {
		t.Errorf("seed bucket: got %q, want sentra-test", d.Config.Repo.S3.Bucket)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "sentra.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("first-run seed must NOT write sentra.yaml, stat err=%v", statErr)
	}
}

// TestRunUI_PassphraseFileRoutesToDashboard: sentra.yaml exists with keyring
// off and no SENTRA_PASSPHRASE, but a --passphrase-file supplies a valid
// non-interactive passphrase source. The launch probe must honor that file
// (exactly as every other command's read path does) and route to the DASHBOARD
// with a live repo — NOT dead-end on the unlock gate, which can't read the file.
func TestRunUI_PassphraseFileRoutesToDashboard(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, ".") // keyring off, no env source

	const passphrase = "hunter2"
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(passphrase))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	passFile := filepath.Join(dir, "pass.txt")
	if err := os.WriteFile(passFile, []byte(passphrase+"\n"), 0o600); err != nil {
		t.Fatalf("write passphrase file: %v", err)
	}

	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return store, nil
			},
			// The read path (openRepoForConfig) uses PassphraseWithConfig, and
			// production wires it to config.Resolve with the --passphrase-file
			// path. Model that here so the dashboard branch can open the repo.
			PassphraseWithConfig: func(_ *config.Config) ([]byte, error) {
				return config.Resolve(config.ResolveOptions{PassphraseFile: passFile})
			},
		},
		// The launch probe must read the SAME file source the read path uses.
		PassphraseFile: func() string { return passFile },
		Run:            func(app tui.App) error { captured = app; return nil },
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	d := captured.Deps()
	if d.InitialView != "" {
		t.Errorf("InitialView = %q, want \"\" (dashboard) — --passphrase-file should route past the unlock gate", d.InitialView)
	}
	if d.Repo == nil {
		t.Error("dashboard App must carry a live Repo when --passphrase-file supplies the passphrase")
	}
}

// TestRunUI_ConfigPresentButLockedLaunchesUnlockView: sentra.yaml exists but no
// non-interactive passphrase source can supply the secret (keyring off, no env,
// no file, and the launch path passes NO interactive prompt). runUI must land
// on the unlock view rather than erroring or blocking on huh.
func TestRunUI_ConfigPresentButLockedLaunchesUnlockView(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, ".") // config with a bucket, keyring off

	// Enforce the "no env" precondition rather than assuming it. config.Resolve
	// treats an empty SENTRA_PASSPHRASE as unset, so this neutralizes an
	// ambient one — `just` loads the repo's .env (justfile: dotenv-load), which
	// exports SENTRA_PASSPHRASE and would otherwise make the launch path find a
	// passphrase and open the repo instead of landing on the unlock view.
	t.Setenv("SENTRA_PASSPHRASE", "")

	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return store, nil
			},
			// PassphraseWithConfig is the INTERACTIVE resolver (it prompts).
			// runUI must not call it on the launch path — a huh/tty prompt
			// there is exactly what Phase 3 forbids.
			PassphraseWithConfig: func(_ *config.Config) ([]byte, error) {
				t.Fatal("interactive passphrase resolver must not run on the launch path")
				return nil, nil
			},
		},
		Run: func(app tui.App) error { captured = app; return nil },
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	d := captured.Deps()
	if d.InitialView != "unlock" {
		t.Errorf("InitialView = %q, want unlock", d.InitialView)
	}
	if d.Repo != nil {
		t.Error("locked App must carry a nil Repo until the user unlocks")
	}
	if d.NewStore == nil {
		t.Error("unlock view needs NewStore threaded to open the repo")
	}
}

// TestRunUI_SplashFollowsConfig proves runUI reads ui.hide_splash and threads
// the build identity, on the dashboard path.
func TestRunUI_SplashFollowsConfig(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, ".")
	t.Setenv("SENTRA_PASSPHRASE", "hunter2")

	deps, captured := uiFixture(t, "hunter2")
	deps.Version = "v1.2.0"
	deps.Commit = "a1b2c3d4"

	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	d := captured.Deps()
	if !d.ShowSplash {
		t.Error("a config without ui.hide_splash must launch with the splash on")
	}
	if d.Version != "v1.2.0" || d.Commit != "a1b2c3d4" {
		t.Errorf("build identity not threaded: %q %q", d.Version, d.Commit)
	}
}

// TestRunUI_HideSplashDisablesSplash: the persisted opt-out wins.
func TestRunUI_HideSplashDisablesSplash(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, ".")
	t.Setenv("SENTRA_PASSPHRASE", "hunter2")

	// Rewrite the config with the splash suppressed.
	cfg, err := config.Load("sentra.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.UI.HideSplash = true
	if err := config.Write("sentra.yaml", cfg); err != nil {
		t.Fatalf("write: %v", err)
	}

	deps, captured := uiFixture(t, "hunter2")
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if captured.Deps().ShowSplash {
		t.Error("ui.hide_splash: true must disable the splash")
	}
}

// TestRunUI_FirstRunShowsSplash: no config on disk, so the default applies.
func TestRunUI_FirstRunShowsSplash(t *testing.T) {
	chDir(t, t.TempDir()) // empty dir: no sentra.yaml
	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return blobstore.NewMemory(), nil
			},
		},
		Run: func(app tui.App) error { captured = app; return nil },
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !captured.Deps().ShowSplash {
		t.Error("first run (no config) must show the splash")
	}
}
