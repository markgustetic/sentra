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

func TestPoliciesView_AddOpensInlineForm(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)
	if v.stage != policiesForm {
		t.Fatalf("stage = %v, want policiesForm after 'a'", v.stage)
	}
	if !strings.Contains(v.View(), "New policy") {
		t.Errorf("form view must show the new-policy header:\n%s", v.View())
	}
}

func TestPoliciesView_AddConfirmedWritesPolicyAndReloads(t *testing.T) {
	deps, path := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)
	// Type a name, tab to path, type a path.
	v = typeIntoPolicies(t, v, "gamma")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PoliciesView)
	v = typeIntoPolicies(t, v, "/data/gamma")
	// Enter on the form arms the simple confirm modal.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PoliciesView)
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("form enter must push a confirm modal, got %#v", cmd())
	}
	if _, ok := push.modal.(ConfirmModal); !ok {
		t.Fatalf("add must use the simple ConfirmModal, got %T", push.modal)
	}
	// Confirm: config.Write happens, view reloads, gamma is present.
	m, _ = v.Update(confirmedMsg{id: policyAddConfirmID})
	v = m.(PoliciesView)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := cfg.Policies["gamma"]
	if !ok || len(p.Paths) != 1 || p.Paths[0] != "/data/gamma" {
		t.Fatalf("gamma not written correctly: %+v", cfg.Policies["gamma"])
	}
	if v.stage != policiesList {
		t.Fatalf("stage after add = %v, want policiesList", v.stage)
	}
}

func TestPoliciesView_AddRejectsInvalidPolicy(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)
	// Name only, no path: policy.Validate rejects (needs >=1 path). Enter
	// must surface the error and NOT push a confirm modal.
	v = typeIntoPolicies(t, v, "noPaths")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PoliciesView)
	if cmd != nil {
		t.Fatalf("invalid policy must not push a modal, got %#v", cmd())
	}
	if v.form.err == "" {
		t.Fatal("invalid policy must set a form error")
	}
}

func typeIntoPolicies(t *testing.T, v PoliciesView, s string) PoliciesView {
	t.Helper()
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(PoliciesView)
	}
	return v
}
