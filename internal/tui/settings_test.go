package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
)

// TestSettingsView_RendersConfigSummaryNoSecrets: the view shows the
// non-secret repo/config identity (bucket, prefix, config path, keyring
// flag) and its two entries, and never renders anything secret.
func TestSettingsView_RendersConfigSummary(t *testing.T) {
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "my-bucket"
	cfg.Repo.S3.Prefix = "team/backups"
	cfg.Passphrase.UseKeyring = true
	v := NewSettingsView(Deps{
		Config:     &cfg,
		RepoName:   "my-bucket",
		ConfigPath: "/home/u/sentra.yaml",
	})
	out := v.View()
	for _, want := range []string{
		"my-bucket", "team/backups", "/home/u/sentra.yaml",
		"Re-run setup", "Change passphrase",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("settings view missing %q:\n%s", want, out)
		}
	}
}

// TestSettingsView_NoConfigPlaceholder: with a nil config the view still
// renders (no crash) and shows a placeholder plus the two entries.
func TestSettingsView_NoConfigPlaceholder(t *testing.T) {
	v := NewSettingsView(Deps{})
	out := v.View()
	if !strings.Contains(out, "no configuration loaded") {
		t.Errorf("expected no-config placeholder:\n%s", out)
	}
	if !strings.Contains(out, "Re-run setup") || !strings.Contains(out, "Change passphrase") {
		t.Errorf("entries missing under nil config:\n%s", out)
	}
}

// TestSettingsView_EnterActivatesNavigateTarget: selecting a navigate
// entry and pressing Enter emits activateMsg for ITS target, so the shell
// switches views. Rows are found by target rather than position — the
// entry order is presentation, not contract.
func TestSettingsView_EnterActivatesNavigateTarget(t *testing.T) {
	for _, target := range []string{"setup", "password"} {
		t.Run(target, func(t *testing.T) {
			v := NewSettingsView(Deps{Config: ptrDefaults()})
			idx := -1
			for i, e := range v.entries {
				if e.kind == entryNavigate && e.targetID == target {
					idx = i
					break
				}
			}
			if idx < 0 {
				t.Fatalf("no navigate entry for %q", target)
			}
			for i := 0; i < idx; i++ {
				m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
				v = m.(SettingsView)
			}
			_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if cmd == nil {
				t.Fatalf("Enter on %s entry returned no command", target)
			}
			msg := cmd()
			act, ok := msg.(activateMsg)
			if !ok || act.id != target {
				t.Fatalf("got %#v, want activateMsg{%s}", msg, target)
			}
		})
	}
}

// TestSettingsView_TitleAndCursorClamp: Title is stable and the cursor never
// leaves the [0,len(entries)-1] range regardless of key spam. The Settings
// view now has three entries (Task 5 added the splash toggle), so the
// down-spam clamp is 2, not the original two-entry view's 1.
func TestSettingsView_TitleAndCursorClamp(t *testing.T) {
	v := NewSettingsView(Deps{Config: ptrDefaults()})
	if v.Title() != "Settings" {
		t.Fatalf("Title = %q, want Settings", v.Title())
	}
	for i := 0; i < 5; i++ {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyUp})
		v = m.(SettingsView)
	}
	if v.cursor != 0 {
		t.Fatalf("cursor after up-spam = %d, want 0", v.cursor)
	}
	for i := 0; i < len(v.entries)+2; i++ {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
		v = m.(SettingsView)
	}
	if v.cursor != len(v.entries)-1 {
		t.Fatalf("cursor after down-spam = %d, want %d", v.cursor, len(v.entries)-1)
	}
}

func ptrDefaults() *config.Config {
	c := config.Defaults()
	return &c
}

// TestApp_SetupAndSettingsRegistered: both new views are registered in the
// shell (sidebar + palette are registry-driven off the same slice). This
// task only inserts the two views into the existing slice — it does not
// touch active/focus/sidebar selection (that block is owned by the final
// registration task) — so this asserts presence plus the resulting count.
func TestApp_SetupAndSettingsRegistered(t *testing.T) {
	app := NewApp(Deps{RepoName: "test-repo"})
	have := map[string]bool{}
	for _, v := range app.views {
		have[v.id] = true
	}
	for _, id := range []string{"setup", "settings"} {
		if !have[id] {
			t.Errorf("view %q not registered", id)
		}
	}
	if got := len(app.views); got != 19 {
		t.Fatalf("views = %d, want 19 (six rail views + thirteen hidden)", got)
	}
}

// settingsWithConfig writes a real config file and returns a view bound to it.
func settingsWithConfig(t *testing.T) (SettingsView, string, *config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sentra.yaml")
	cfg := &config.Config{}
	cfg.Repo.S3.Bucket = "b"
	if err := config.Write(path, cfg); err != nil {
		t.Fatal(err)
	}
	return NewSettingsView(Deps{Config: cfg, ConfigPath: path}), path, cfg
}

// cursorTo moves the settings cursor onto the splash toggle row.
func cursorTo(v SettingsView, kind settingsEntryKind) SettingsView {
	for i, e := range v.entries {
		if e.kind == kind {
			v.cursor = i
		}
	}
	return v
}

func TestSettings_ToggleSplashPersists(t *testing.T) {
	v, path, cfg := settingsWithConfig(t)
	v = cursorTo(v, entryToggleSplash)

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SettingsView)

	if !cfg.UI.HideSplash {
		t.Error("toggling must flip the in-memory config after a successful write")
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !got.UI.HideSplash {
		t.Error("toggling must persist hide_splash to disk")
	}
	if !strings.Contains(v.View(), "[off]") {
		t.Errorf("view should show the splash as off:\n%s", v.View())
	}
}

// TestSettings_ToggleSplashKeepsEnvOverridesOutOfFile is the regression test
// for the reported bug: run the TUI under SENTRA_REPO__S3__BUCKET, flip the
// purely cosmetic splash toggle, and sentra.yaml's bucket was rewritten to the
// ephemeral env value. A display-only action must never touch the repo config.
func TestSettings_ToggleSplashKeepsEnvOverridesOutOfFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentra.yaml")
	onDisk := &config.Config{}
	onDisk.Repo.S3.Bucket = "real-bucket"
	onDisk.Repo.S3.Region = "us-west-2"
	if err := config.Write(path, onDisk); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SENTRA_REPO__S3__BUCKET", "ephemeral-env-bucket")
	t.Setenv("SENTRA_REPO__S3__REGION", "eu-central-1")

	// deps.Config is the *resolved* config, exactly as internal/cli/ui.go
	// wires it — env overlay and all.
	resolved, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if resolved.Repo.S3.Bucket != "ephemeral-env-bucket" {
		t.Fatalf("fixture is not exercising the env overlay: bucket = %q", resolved.Repo.S3.Bucket)
	}

	v := cursorTo(NewSettingsView(Deps{Config: resolved, ConfigPath: path}), entryToggleSplash)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SettingsView)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(body), "ephemeral-env-bucket") {
		t.Errorf("toggling the splash persisted the env bucket into sentra.yaml:\n%s", body)
	}
	if strings.Contains(string(body), "eu-central-1") {
		t.Errorf("toggling the splash persisted the env region into sentra.yaml:\n%s", body)
	}
	if !strings.Contains(string(body), "real-bucket") {
		t.Errorf("toggling the splash dropped the real bucket:\n%s", body)
	}
	// The field the user actually asked to change still lands.
	if !strings.Contains(string(body), "hide_splash: true") {
		t.Errorf("toggle did not persist hide_splash:\n%s", body)
	}
	if !resolved.UI.HideSplash {
		t.Error("in-memory config must reflect the toggle after a successful write")
	}
	if !strings.Contains(v.View(), "[off]") {
		t.Errorf("view should show the splash as off:\n%s", v.View())
	}
}

// TestSettings_ToggleSplashNegatesResolvedState: with SENTRA_UI__HIDE_SPLASH
// set, the label reflects the env value, so the toggle has to negate *that*.
// Negating the on-disk value instead would leave the toggle visibly stuck.
func TestSettings_ToggleSplashNegatesResolvedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentra.yaml")
	onDisk := &config.Config{}
	onDisk.Repo.S3.Bucket = "b"
	onDisk.UI.HideSplash = false
	if err := config.Write(path, onDisk); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SENTRA_UI__HIDE_SPLASH", "true")

	resolved, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !resolved.UI.HideSplash {
		t.Fatalf("fixture is not exercising the env overlay: HideSplash = false")
	}

	v := cursorTo(NewSettingsView(Deps{Config: resolved, ConfigPath: path}), entryToggleSplash)
	if !strings.Contains(v.View(), "[off]") {
		t.Fatalf("precondition: env override should render the splash as off:\n%s", v.View())
	}

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SettingsView)

	if resolved.UI.HideSplash {
		t.Error("toggling from the env-hidden state must turn the splash back on")
	}
	if !strings.Contains(v.View(), "[on]") {
		t.Errorf("the toggle must visibly move, not stick:\n%s", v.View())
	}
}

func TestSettings_ToggleSplashDisabledWithoutConfig(t *testing.T) {
	v := cursorTo(NewSettingsView(Deps{}), entryToggleSplash)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SettingsView)
	if !strings.Contains(v.View(), "available after setup") {
		t.Errorf("no config: the toggle must render a disabled hint:\n%s", v.View())
	}
}

// A failed write must not desync the in-memory config from disk.
func TestSettings_ToggleSplashWriteErrorKeepsMemory(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("a read-only directory does not block writes for root")
	}
	cfg := &config.Config{}
	// config.Write now creates a missing parent directory (config
	// discovery's home-path fallback needs that), so a merely-missing
	// directory no longer makes Write fail. Use a read-only parent
	// instead: MkdirAll is a no-op on an existing directory, but the
	// WriteFile inside it still needs write permission.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("restore perms for cleanup: %v", err)
		}
	})
	bad := filepath.Join(dir, "sentra.yaml")
	v := cursorTo(NewSettingsView(Deps{Config: cfg, ConfigPath: bad}), entryToggleSplash)

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SettingsView)

	if cfg.UI.HideSplash {
		t.Error("a failed write must leave the in-memory config unchanged")
	}
	if !strings.Contains(v.View(), "could not save") {
		t.Errorf("a write error should surface inline:\n%s", v.View())
	}
}

// Navigation entries still work.
func TestSettings_NavigateEntryStillEmitsActivate(t *testing.T) {
	v := cursorTo(NewSettingsView(Deps{}), entryNavigate)
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("a navigate entry must emit a command")
	}
	if _, ok := cmd().(activateMsg); !ok {
		t.Error("a navigate entry must emit activateMsg")
	}
}

// TestSettings_ForgetKeyringEntry: the TUI face of `password forget` —
// a confirmed forget deletes the OS keyring entry and turns
// passphrase.use_keyring off in sentra.yaml, without touching the repo.
func TestSettings_ForgetKeyringEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.yaml")
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "b"
	cfg.Passphrase.UseKeyring = true
	if err := config.Write(path, &cfg); err != nil {
		t.Fatal(err)
	}
	deleted := false
	v := NewSettingsView(Deps{
		Config:     &cfg,
		ConfigPath: path,
		DeleteKeyringPassphrase: func(*config.Config) (bool, error) {
			deleted = true
			return true, nil
		},
	})

	// Walk the cursor to the forget entry (identified by label, not index).
	idx := -1
	for i, e := range v.entries {
		if strings.Contains(strings.ToLower(e.label), "forget") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no forget-keyring entry in settings: %+v", v.entries)
	}
	for range idx {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
		v = m.(SettingsView)
	}
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SettingsView)
	if cmd == nil {
		t.Fatal("forget must push a confirm modal")
	}
	if _, ok := cmd().(pushModalMsg); !ok {
		t.Fatalf("expected pushModalMsg, got %#v", cmd())
	}

	m, _ = v.Update(confirmedMsg{id: settingsForgetConfirmID})
	v = m.(SettingsView)
	if !deleted {
		t.Error("confirm must call the keyring delete seam")
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Passphrase.UseKeyring {
		t.Error("forget must persist passphrase.use_keyring: false")
	}
}

// The management views that left the rail — policies, schedule, recovery
// kit — must each keep a launcher here, alongside the setup and password
// entries that always lived in Settings. This pins the fold: a view hidden
// from the rail with no launcher would be unreachable.
func TestSettings_NavigateEntriesCoverDemotedViews(t *testing.T) {
	v := NewSettingsView(Deps{})
	got := map[string]bool{}
	for _, e := range v.entries {
		if e.kind == entryNavigate {
			got[e.targetID] = true
		}
	}
	for _, want := range []string{"setup", "password", "policies", "schedule", "recovery-kit"} {
		if !got[want] {
			t.Errorf("settings has no navigate entry for %q", want)
		}
	}
}
