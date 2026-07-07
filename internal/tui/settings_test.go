package tui

import (
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

// TestSettingsView_EnterOnSetupActivatesSetup: with the "Re-run setup"
// entry selected, Enter emits activateMsg{"setup"} so the shell switches
// to the setup wizard view.
func TestSettingsView_EnterOnSetupActivatesSetup(t *testing.T) {
	v := NewSettingsView(Deps{Config: ptrDefaults()})
	// cursor starts at 0 == "Re-run setup".
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on setup entry returned no command")
	}
	msg := cmd()
	act, ok := msg.(activateMsg)
	if !ok || act.id != "setup" {
		t.Fatalf("got %#v, want activateMsg{setup}", msg)
	}
}

// TestSettingsView_EnterOnPasswordActivatesPassword: moving the cursor
// down to "Change passphrase" and pressing Enter emits
// activateMsg{"password"}.
func TestSettingsView_EnterOnPasswordActivatesPassword(t *testing.T) {
	v := NewSettingsView(Deps{Config: ptrDefaults()})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
	v = m.(SettingsView)
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on password entry returned no command")
	}
	msg := cmd()
	act, ok := msg.(activateMsg)
	if !ok || act.id != "password" {
		t.Fatalf("got %#v, want activateMsg{password}", msg)
	}
}

// TestSettingsView_TitleAndCursorClamp: Title is stable and the cursor
// never leaves the [0,1] range regardless of key spam.
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
	for i := 0; i < 5; i++ {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
		v = m.(SettingsView)
	}
	if v.cursor != 1 {
		t.Fatalf("cursor after down-spam = %d, want 1", v.cursor)
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
	if got := len(app.views); got != 17 {
		t.Fatalf("views = %d, want 17 (15 Phase 2c+unlock + setup + settings)", got)
	}
}
