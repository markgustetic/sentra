package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// policiesDeps writes a sentra.yaml with two named policies to a temp dir
// and returns Deps wired with ConfigPath pointing at it (plus an optional
// repo for the RUN flow). The view hydrates by loading ConfigPath, mirror-
// ing how PruneView hydrates from the repo.
func policiesDeps(t *testing.T, r *repo.Repo) (Deps, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.yaml")
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "b"
	cfg.Policies["alpha"] = config.PolicyConfig{
		Paths:    []string{"/data/alpha"},
		Tags:     []string{"nightly"},
		Schedule: config.PolicySchedule{Cadence: "daily", At: "03:00"},
		AfterBackup: config.PolicyAfterBackup{
			Check: true,
			Prune: "off",
		},
	}
	cfg.Policies["beta"] = config.PolicyConfig{
		Paths:    []string{"/data/beta"},
		Schedule: config.PolicySchedule{Cadence: "manual"},
	}
	if err := config.Write(path, &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return Deps{Repo: r, Config: &cfg, ConfigPath: path}, path
}

func TestPoliciesView_HydratesFromConfigPath(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	if len(v.names) != 2 || v.names[0] != "alpha" || v.names[1] != "beta" {
		t.Fatalf("names = %v, want [alpha beta] (sorted)", v.names)
	}
	out := v.View()
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("picker must list both policies:\n%s", out)
	}
}

func TestPoliciesView_MissingConfigPathShowsPlaceholder(t *testing.T) {
	v := NewPoliciesView(Deps{})
	if v.loadErr == "" {
		t.Fatal("empty deps must set a load error")
	}
	if !strings.Contains(v.View(), "no config") {
		t.Errorf("view must surface the missing-config placeholder:\n%s", v.View())
	}
}

func TestPoliciesView_InlineDetailShowsSelectedPolicy(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	// Selection starts at index 0 (alpha); its schedule + tag render inline.
	out := v.View()
	if !strings.Contains(out, "daily@03:00") {
		t.Errorf("detail must show alpha's schedule shorthand:\n%s", out)
	}
	if !strings.Contains(out, "/data/alpha") {
		t.Errorf("detail must show alpha's path:\n%s", out)
	}
	// Down moves selection to beta; its manual schedule renders.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
	v = m.(PoliciesView)
	if v.selected != 1 {
		t.Fatalf("selected = %d, want 1 after down", v.selected)
	}
	if out := v.View(); !strings.Contains(out, "/data/beta") {
		t.Errorf("detail must follow selection to beta:\n%s", out)
	}
}

func TestPoliciesView_RemoveRequiresConfirm(t *testing.T) {
	deps, path := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	// Pressing 'd' pushes a simple ConfirmModal and does NOT touch the file.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	v = m.(PoliciesView)
	if cmd == nil {
		t.Fatal("d must request a confirmation modal")
	}
	msg := cmd()
	push, ok := msg.(pushModalMsg)
	if !ok {
		t.Fatalf("expected pushModalMsg, got %#v", msg)
	}
	if _, ok := push.modal.(ConfirmModal); !ok {
		t.Fatalf("remove must use the simple ConfirmModal, got %T", push.modal)
	}
	// File is untouched: alpha still present.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Policies["alpha"]; !ok {
		t.Fatal("remove must not delete before confirmation")
	}
}

func TestPoliciesView_RemoveConfirmedRewritesConfigAndReloads(t *testing.T) {
	deps, path := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	// selected == 0 == alpha. Arm the modal, then confirm.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	v = m.(PoliciesView)
	m, cmd := v.Update(confirmedMsg{id: policyRemoveConfirmID})
	v = m.(PoliciesView)
	// The write is done synchronously in a plain tea.Cmd (no op guard).
	if cmd != nil {
		if _, ok := cmd().(startOpMsg); ok {
			t.Fatal("remove must NOT take the op guard (config-only edit)")
		}
	}
	// alpha is gone from disk and from the reloaded view.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Policies["alpha"]; ok {
		t.Fatal("confirmed remove must delete alpha from sentra.yaml")
	}
	if len(v.names) != 1 || v.names[0] != "beta" {
		t.Fatalf("view names after remove = %v, want [beta]", v.names)
	}
}
