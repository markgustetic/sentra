package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
)

func TestSetupWizard_InitialStageIsBackendSelect(t *testing.T) {
	cfg := &config.Config{}
	v := NewSetupWizardView(Deps{Config: cfg})
	if v.Title() != "Setup" {
		t.Fatalf("Title() = %q, want %q", v.Title(), "Setup")
	}
	// Feed a window size so the view has render dimensions.
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	out := v.View()
	if !strings.Contains(out, "Storage backend") {
		t.Fatalf("initial view must show the backend selector, got:\n%s", out)
	}
	if !strings.Contains(out, "AWS S3") || !strings.Contains(out, "S3-compatible") {
		t.Fatalf("backend selector must list both backends, got:\n%s", out)
	}
}

func TestSetupWizard_NilEffectsRendersGuard(t *testing.T) {
	// A view with no SetupEffects wired can still render but reports it
	// cannot provision, so a first-run gate never crashes.
	v := NewSetupWizardView(Deps{})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	if got := v.View(); got == "" {
		t.Fatal("View() must never be empty even without SetupEffects")
	}
}
