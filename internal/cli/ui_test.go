package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	"github.com/markgustetic/sentra/internal/setup"
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
	// "S E N T R A" is the centered header logo shown on every screen (the old
	// left-aligned "✦ sentra" became the spaced-caps wordmark).
	if !strings.Contains(view, "S E N T R A") {
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

// TestRunUI_SetupRoutingMatrix drives the full cross product of
// (ConfigExists x PassphraseAvailable x forceSetup) rather than only the new
// forceSetup cases. The same class of bug — one launch condition silently
// stealing another's route — has shipped here before, so the regression rows
// (forceSetup=false) are as load-bearing as the new ones.
func TestRunUI_SetupRoutingMatrix(t *testing.T) {
	const passphrase = "hunter2"

	tests := []struct {
		name            string
		configExists    bool
		passphraseAvail bool
		forceSetup      bool
		wantInitialView string
		wantReconfigure bool
	}{
		{"first run", false, false, false, "setup", false},
		{"first run, forced", false, false, true, "setup", false},
		{"configured and locked", true, false, false, "unlock", false},
		{"configured and locked, forced", true, false, true, "setup", true},
		{"configured and unlocked", true, true, false, "", false},
		{"configured and unlocked, forced", true, true, true, "setup", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			chDir(t, dir)

			var passFile string
			if tc.configExists {
				writeBackupConfigFile(t, ".") // keyring off, no env source
			}
			if tc.passphraseAvail {
				passFile = filepath.Join(dir, "pass.txt")
				if err := os.WriteFile(passFile, []byte(passphrase+"\n"), 0o600); err != nil {
					t.Fatalf("write passphrase file: %v", err)
				}
			}

			// Initialize whenever a passphrase source exists, not only for the
			// dashboard row. Only that row opens the repo, but the launch probe
			// resolves the passphrase on every configured row, and an
			// uninitialized store is a needless way for an unrelated row to fail.
			store := blobstore.NewMemory()
			if tc.passphraseAvail {
				r, err := repo.Init(context.Background(), store, []byte(passphrase))
				if err != nil {
					t.Fatalf("repo init: %v", err)
				}
				if err := r.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
			}

			var captured tui.App
			deps := UIDeps{
				RepoDeps: RepoDeps{
					NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
						return store, nil
					},
					// probeLaunchState (the routing decision) resolves its own
					// non-interactive passphrase via config.Resolve and never calls
					// this hook — it is openRepoForConfig, reached only on the
					// dashboard branch (configured+unlocked+!forceSetup, i.e. the
					// "configured and unlocked" row), that calls it to actually open
					// the repo. Gate strictly on that exact combination rather than
					// on passFile != "" (every configured+unlocked row has a
					// passFile, forced or not): a future regression that let
					// forceSetup's "outranks the lock gate" path also open the repo
					// must fail loudly here, not resolve quietly because a file
					// happened to be present.
					PassphraseWithConfig: func(_ *config.Config) ([]byte, error) {
						// Naming the condition keeps the guard readable: the
						// De Morgan'd inverse (!a || !b || c) obscures which
						// single row is the legitimate caller.
						dashboardRow := tc.configExists && tc.passphraseAvail && !tc.forceSetup
						if !dashboardRow {
							t.Fatal("interactive passphrase resolver must not run on the launch path")
							return nil, nil
						}
						return config.Resolve(config.ResolveOptions{PassphraseFile: passFile})
					},
				},
				Run:            func(app tui.App) error { captured = app; return nil },
				PassphraseFile: func() string { return passFile },
			}

			cmd := NewUI(deps)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{})
			// NewUI always passes forceSetup=false; exercise the forced path
			// through runUI directly, which is what `sentra setup` will call.
			var err error
			if tc.forceSetup {
				err = runUI(cmd, deps, configFileName, true)
			} else {
				err = cmd.Execute()
			}
			if err != nil {
				t.Fatalf("launch: %v", err)
			}

			d := captured.Deps()
			if d.InitialView != tc.wantInitialView {
				t.Errorf("InitialView = %q, want %q", d.InitialView, tc.wantInitialView)
			}
			if d.Reconfigure != tc.wantReconfigure {
				t.Errorf("Reconfigure = %v, want %v", d.Reconfigure, tc.wantReconfigure)
			}
			// Every row except the true dashboard launch (empty InitialView) must
			// carry a nil Repo — the wizard/unlock views own opening the repo
			// themselves. This is a structural backstop on top of the
			// PassphraseWithConfig gate above: even if some future change made a
			// non-dashboard route resolve a passphrase and open a repo some other
			// way, a live Repo here would still catch it.
			wantNilRepo := tc.wantInitialView != ""
			if wantNilRepo && d.Repo != nil {
				t.Errorf("Repo = %v, want nil for a non-dashboard launch (InitialView %q)", d.Repo, d.InitialView)
			}
		})
	}
}

// TestRunUI_SetupPrefillPrecedence is a RULE, not a case: it sweeps every
// combination of the three optional pre-fill sources and asserts one ordering
// throughout — on-disk config > setup draft > seed > blank.
//
// Testing the rule matters because the sources were added one at a time and
// each arrived with a guard of its own. The draft is the newest: `sentra setup`
// used to read it in the deleted CLI wizard's loadSetupConfigForWizard, and
// without a reader here a failed provision leaves a .setup-draft on disk that
// nothing ever consumes. It slots BELOW a real config (an operator's committed
// file always wins) and ABOVE the seed (a resumable in-progress run is more
// specific than `sentra local`'s generic MinIO coordinates).
func TestRunUI_SetupPrefillPrecedence(t *testing.T) {
	for _, cfgExists := range []bool{false, true} {
		for _, draftExists := range []bool{false, true} {
			for _, seedSet := range []bool{false, true} {
				name := fmt.Sprintf("config=%v/draft=%v/seed=%v", cfgExists, draftExists, seedSet)
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					chDir(t, dir)

					// Every source carries a distinct bucket, so the winner is
					// identifiable from tui.Deps.Config alone.
					want := "" // blank config → zero-value bucket
					if seedSet {
						want = "from-seed"
					}
					if draftExists {
						want = "from-draft"
						draft := &config.Config{}
						draft.Repo.S3.Bucket = "from-draft"
						if err := setup.NewEngine(nil).WriteDraft(configFileName, draft); err != nil {
							t.Fatalf("write draft: %v", err)
						}
					}
					if cfgExists {
						want = "from-config"
						body := "repo:\n  s3:\n    bucket: from-config\n"
						if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(body), 0o600); err != nil {
							t.Fatalf("write config: %v", err)
						}
					}

					var seed *config.Config
					if seedSet {
						seed = &config.Config{}
						seed.Repo.S3.Bucket = "from-seed"
					}

					var captured tui.App
					deps := UIDeps{
						RepoDeps: RepoDeps{
							NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
								return blobstore.NewMemory(), nil
							},
							PassphraseWithConfig: func(_ *config.Config) ([]byte, error) {
								t.Fatal("interactive passphrase resolver must not run on the launch path")
								return nil, nil
							},
						},
						Run:             func(app tui.App) error { captured = app; return nil },
						SetupSeedConfig: seed,
					}
					cmd := NewUI(deps)
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					// This test calls runUI directly rather than cmd.Execute(), so
					// cobra never marks the "config" flag Changed. Pin it explicitly
					// so config discovery (added after this test) doesn't reroute
					// cfgPath away from the cwd path the draft above was written
					// to — this test's subject is source precedence, not discovery.
					if err := cmd.Flags().Set("config", configFileName); err != nil {
						t.Fatal(err)
					}
					// forceSetup so the config-present rows reach the wizard
					// instead of the unlock gate.
					if err := runUI(cmd, deps, configFileName, true); err != nil {
						t.Fatalf("launch: %v", err)
					}

					d := captured.Deps()
					if d.InitialView != "setup" {
						t.Fatalf("InitialView = %q, want setup", d.InitialView)
					}
					if got := d.Config.Repo.S3.Bucket; got != want {
						t.Errorf("wizard pre-filled from bucket %q, want %q "+
							"(config > draft > seed > blank)", got, want)
					}
				})
			}
		}
	}
}

// TestDefaultUIRunner_NonTTYNamesANonInteractiveAlternative: `go test` runs
// with a non-TTY stdout, so this exercises the real refusal path.
//
// The message reaches `sentra setup` too, not just `sentra ui` — setup is a
// launcher for the same TUI — and someone who typed `setup` is trying to
// configure a repository. Pointing them at `sentra ui` restates the thing that
// just refused to run; `sentra init` is the flow that actually does the job
// without a terminal.
func TestDefaultUIRunner_NonTTYNamesANonInteractiveAlternative(t *testing.T) {
	err := DefaultUIRunner(tui.NewApp(tui.Deps{}))
	if err == nil {
		t.Fatal("a non-TTY stdout must refuse to launch the TUI")
	}
	if !strings.Contains(err.Error(), "sentra init") {
		t.Errorf("refusal must name a non-interactive alternative, got: %v", err)
	}
	if !strings.Contains(err.Error(), "requires a terminal") {
		t.Errorf("refusal must still say why, got: %v", err)
	}
}

// TestRunUI_EmptyConfigPathNormalizes: `--config ""` must mean the default
// sentra.yaml, not the current directory. The deleted runSetup opened with
// `if cfgPath == "" { cfgPath = configFileName }`; runUI never had the
// equivalent, so filepath.Abs("") resolves to the cwd and the wizard would hand
// a DIRECTORY to every config-writing flow. Fixing it in runUI covers
// `sentra ui` too.
func TestRunUI_EmptyConfigPathNormalizes(t *testing.T) {
	chDir(t, t.TempDir())
	// Derive the expectation from the process cwd, not from t.TempDir(): on
	// macOS the temp dir is reached through a /var -> /private/var symlink, so
	// the two spellings differ and only the cwd matches what Abs produces.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

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
	// This test calls runUI directly rather than cmd.Execute(), so cobra
	// never marks the "config" flag Changed — but the scenario under test
	// (a literal `--config ""`) IS an explicit flag on the real CLI path, so
	// pin Changed here too. Otherwise config discovery (added after this
	// test) sees an "untouched default" in this empty cwd and reroutes to
	// the XDG home path, which is a different, also-legitimate behavior
	// this test isn't about.
	if err := cmd.Flags().Set("config", ""); err != nil {
		t.Fatal(err)
	}
	if err := runUI(cmd, deps, "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	want := filepath.Join(cwd, configFileName)
	if got := captured.Deps().ConfigPath; got != want {
		t.Errorf("ConfigPath = %q, want %q — an empty --config must mean the default file", got, want)
	}
}

// TestRunUI_ThreadsPassphraseFileToTUI: the setup wizard resolves
// --passphrase-file itself (then SENTRA_PASSPHRASE) so it can skip its entry
// stage rather than prompt for a secret the operator already configured. That
// only works if runUI hands the flag's value down — the wizard has no other way
// to see it, and a dropped field degrades silently into the exact
// initialize-under-the-wrong-passphrase bug the resolution exists to prevent.
//
// The path is threaded, never the file's contents.
func TestRunUI_ThreadsPassphraseFileToTUI(t *testing.T) {
	for _, forceSetup := range []bool{false, true} {
		t.Run(fmt.Sprintf("forceSetup=%v", forceSetup), func(t *testing.T) {
			chDir(t, t.TempDir())
			// Named for the path it is, not the secret it is not: `passFile`
			// trips gosec's G101 hardcoded-credential heuristic.
			const wantPath = "/tmp/does-not-need-to-exist"

			var captured tui.App
			deps := UIDeps{
				RepoDeps: RepoDeps{
					NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
						return blobstore.NewMemory(), nil
					},
				},
				Run:            func(app tui.App) error { captured = app; return nil },
				PassphraseFile: func() string { return wantPath },
			}
			cmd := NewUI(deps)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := runUI(cmd, deps, configFileName, forceSetup); err != nil {
				t.Fatalf("launch: %v", err)
			}
			if got := captured.Deps().PassphraseFile; got != wantPath {
				t.Errorf("tui.Deps.PassphraseFile = %q, want %q", got, wantPath)
			}
		})
	}
}

// TestRunUI_UnreadableSetupDraftDegradesToNextSource: a corrupt draft is a
// stale convenience artifact, not a reason to refuse the wizard. If it were
// fatal the operator would be stranded — the only in-product way to clear the
// draft is to finish a setup run, which is exactly what the error would block.
func TestRunUI_UnreadableSetupDraftDegradesToNextSource(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	draftPath := setup.NewEngine(nil).DraftPath(configFileName)
	if err := os.WriteFile(draftPath, []byte("repo:\n  s3:\n   :::not yaml\n"), 0o600); err != nil {
		t.Fatalf("write corrupt draft: %v", err)
	}
	seed := &config.Config{}
	seed.Repo.S3.Bucket = "from-seed"

	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return blobstore.NewMemory(), nil
			},
		},
		Run:             func(app tui.App) error { captured = app; return nil },
		SetupSeedConfig: seed,
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	// This test calls runUI directly rather than cmd.Execute(), so cobra
	// never marks the "config" flag Changed. Pin it so config discovery
	// can't reroute cfgPath away from the cwd path the corrupt draft above
	// was written next to — otherwise the wizard's draft lookup would miss
	// the draft entirely (wrong directory) and this test would pass for
	// the wrong reason: an absent draft, not a corrupt one.
	if err := cmd.Flags().Set("config", configFileName); err != nil {
		t.Fatal(err)
	}
	if err := runUI(cmd, deps, configFileName, true); err != nil {
		t.Fatalf("a corrupt setup draft must not fail the launch: %v", err)
	}
	if got := captured.Deps().Config.Repo.S3.Bucket; got != "from-seed" {
		t.Errorf("pre-filled from %q, want from-seed — a bad draft must fall through", got)
	}
}

// TestRunUI_ForcedSetupPrefersOnDiskConfigOverSeed guards the seed condition.
// forceSetup makes initial=="setup" reachable WITH a config present, so the
// SetupSeedConfig override must additionally require !ConfigExists — otherwise
// a seeded caller (sentra local's MinIO coordinates) would silently outrank the
// operator's real config on a forced reconfigure.
func TestRunUI_ForcedSetupPrefersOnDiskConfigOverSeed(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, ".")

	// config.Config.Repo and .Repo.S3 are ANONYMOUS nested structs, so there is
	// no config.RepoConfig/config.S3Config to compose a literal from. Build the
	// seed by field assignment.
	seed := &config.Config{}
	seed.Repo.S3.Bucket = "seeded-not-wanted"

	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return blobstore.NewMemory(), nil
			},
			PassphraseWithConfig: func(_ *config.Config) ([]byte, error) {
				t.Fatal("interactive passphrase resolver must not run on the launch path")
				return nil, nil
			},
		},
		Run:             func(app tui.App) error { captured = app; return nil },
		SetupSeedConfig: seed,
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := runUI(cmd, deps, configFileName, true); err != nil {
		t.Fatalf("launch: %v", err)
	}
	d := captured.Deps()
	if d.Config.Repo.S3.Bucket == "seeded-not-wanted" {
		t.Error("forced setup over an existing config must use the on-disk config, not SetupSeedConfig")
	}
}

// The headline behavior: `sentra` from a directory with no sentra.yaml
// routes to the first-run wizard TARGETING the user-level config path, so
// completing setup once makes bare `sentra` work from anywhere after.
func TestUI_FirstRunFromAnywhereTargetsHomeConfig(t *testing.T) {
	xdg := t.TempDir()
	chDir(t, t.TempDir()) // empty cwd: no ./sentra.yaml
	t.Setenv("XDG_CONFIG_HOME", xdg)

	deps, captured := uiFixture(t, "hunter2")
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	d := captured.Deps()
	want := filepath.Join(xdg, "sentra", "sentra.yaml")
	if d.ConfigPath != want {
		t.Errorf("ConfigPath = %q, want discovered home path %q", d.ConfigPath, want)
	}
	if d.InitialView != "setup" {
		t.Errorf("InitialView = %q, want \"setup\" (first run)", d.InitialView)
	}
}
