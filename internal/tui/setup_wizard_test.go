package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
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

func TestSetupWizard_BackendEnterOpensDetails(t *testing.T) {
	v := NewSetupWizardView(Deps{Config: &config.Config{}})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // choose AWS S3 (cursor 0)
	v = m.(SetupWizardView)
	if v.stage != stageDetails {
		t.Fatalf("stage = %v, want stageDetails", v.stage)
	}
	if !strings.Contains(v.View(), "S3 bucket") {
		t.Fatalf("details stage must prompt for the bucket, got:\n%s", v.View())
	}
}

func TestSetupWizard_DetailsRejectsInvalidBucket(t *testing.T) {
	v := setupAtDetails(t, 0)           // AWS backend
	v = setupTypeField(v, "UPPER_CASE") // invalid: not DNS-compatible
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stageDetails {
		t.Fatalf("invalid bucket must keep the view on details, got stage %v", v.stage)
	}
	if v.detailErr == "" {
		t.Fatal("invalid bucket must set a detail error")
	}
}

func TestSetupWizard_AWSDetailsAdvancesToActions(t *testing.T) {
	v := setupAtDetails(t, 0)
	v = setupTypeField(v, "my-sentra-bucket")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stageActions {
		t.Fatalf("valid AWS bucket must advance to stageActions, got %v", v.stage)
	}
	if v.plan.Config.Repo.S3.Bucket != "my-sentra-bucket" {
		t.Fatalf("bucket not captured into plan: %q", v.plan.Config.Repo.S3.Bucket)
	}
}

func TestSetupWizard_CompatibleDetailsAdvancesToPassphrase(t *testing.T) {
	v := setupAtDetails(t, 1) // S3-compatible backend
	v = setupTypeField(v, "existing-bucket")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stagePassphrase {
		t.Fatalf("S3-compatible skips actions, want stagePassphrase, got %v", v.stage)
	}
	if v.plan.Backend != setup.BackendS3Compatible {
		t.Fatalf("plan.Backend = %v, want S3-compatible", v.plan.Backend)
	}
}

// setupAtDetails drives the wizard to stageDetails on the given backend
// cursor (0=AWS, 1=S3-compatible).
func setupAtDetails(t *testing.T, backendCursor int) SetupWizardView {
	t.Helper()
	v := NewSetupWizardView(Deps{Config: &config.Config{}})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	v.backendCursor = backendCursor
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return m.(SetupWizardView)
}

// setupTypeField types s into the focused details field (bucket by default).
func setupTypeField(v SetupWizardView, s string) SetupWizardView {
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(SetupWizardView)
	}
	return v
}
