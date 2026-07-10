package tui

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/markgustetic/sentra/internal/config"
)

// Golden frame snapshots.
//
// Two of this project's real bugs were visible only in rendered output and
// invisible to every assertion we had: the setup wizard silently prefilled
// `AWS profile  sentra` on an S3-compatible target, and the splash's wordmark
// would have slid across the screen as it revealed. Both were caught by a human
// dumping frames to a terminal. These snapshots make CI do that dumping, and
// turn any change in what the TUI draws into a reviewable diff.
//
// Regenerate after an intentional change:
//
//	go test ./internal/tui/ -run TestGolden -update
//
// Then READ the diff. A golden that changed for a reason you cannot articulate
// is a bug you are about to commit.
//
// Frames are plain text: unit tests run under lipgloss's Ascii color profile,
// which emits no ANSI at all. That is also why selection is carried by the "▍"
// marker rather than by color — see ui.SelectRow.

const goldenW, goldenH = 80, 24

// hermeticAWS severs every input DefaultPlan reads from the machine. Without it
// these goldens would encode whatever lives in the developer's ~/.aws/config —
// they would pass locally, encode a real profile name, and fail in CI.
func hermeticAWS(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "no-such-aws-config"))
}

func goldenWizard(t *testing.T, cfg *config.Config) SetupWizardView {
	t.Helper()
	hermeticAWS(t)
	v := NewSetupWizardView(Deps{Config: cfg, ConfigPath: "/repo/sentra.yaml"})
	m, _ := v.Update(tea.WindowSizeMsg{Width: goldenW, Height: goldenH})
	return m.(SetupWizardView)
}

// wizardWithAmbientProfile builds a wizard on a machine that HAS an AWS profile,
// picks backendCursor, and advances to the details stage. It asserts the profile
// really was inferred first — a golden of a condition that did not occur proves
// nothing.
func wizardWithAmbientProfile(t *testing.T, backendCursor int) SetupWizardView {
	t.Helper()
	hermeticAWS(t)
	t.Setenv("AWS_PROFILE", "ambient-profile")

	v := NewSetupWizardView(Deps{Config: &config.Config{}, ConfigPath: "/repo/sentra.yaml"})
	m, _ := v.Update(tea.WindowSizeMsg{Width: goldenW, Height: goldenH})
	v = m.(SetupWizardView)
	if got := v.fields[setupFieldProfile].Value(); got != "ambient-profile" {
		t.Fatalf("precondition: DefaultPlan should have inferred a profile, got %q", got)
	}

	v.backendCursor = backendCursor
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return m.(SetupWizardView)
}

func TestGoldenWizard(t *testing.T) {
	t.Run("backend_select", func(t *testing.T) {
		v := goldenWizard(t, &config.Config{})
		golden.RequireEqual(t, []byte(v.View()))
	})

	t.Run("backend_locked_by_endpoint", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Repo.S3.EndpointURL = "http://localhost:9000"
		v := goldenWizard(t, cfg)
		golden.RequireEqual(t, []byte(v.View()))
	})

	t.Run("details_aws", func(t *testing.T) {
		v := goldenWizard(t, &config.Config{})
		v.backendCursor = 0
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
		v = m.(SetupWizardView)
		v.fieldCursor = setupFieldRegion
		golden.RequireEqual(t, []byte(v.View()))
	})

	t.Run("details_s3_compatible", func(t *testing.T) {
		v := goldenWizard(t, &config.Config{})
		v.backendCursor = 1
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
		v = m.(SetupWizardView)
		golden.RequireEqual(t, []byte(v.View()))
	})

	// These two snapshot the DANGEROUS condition: an AWS profile is present in
	// the ambient environment, so DefaultPlan infers one. A hermetic golden
	// cannot catch the profile leak, because with no ambient profile there is
	// nothing to leak — it would render the "default" placeholder either way.
	//
	// AWS must keep the inferred profile; S3-compatible must drop it. If either
	// invariant breaks, the rendered profile row changes and the golden fails.
	t.Run("details_aws_keeps_ambient_profile", func(t *testing.T) {
		golden.RequireEqual(t, []byte(wizardWithAmbientProfile(t, 0).View()))
	})

	t.Run("details_s3_compatible_drops_ambient_profile", func(t *testing.T) {
		golden.RequireEqual(t, []byte(wizardWithAmbientProfile(t, 1).View()))
	})

	t.Run("details_print_iam_armed", func(t *testing.T) {
		v := goldenWizard(t, &config.Config{})
		v.backendCursor = 0
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
		v = m.(SetupWizardView)
		v.printIAM = true
		v.fieldCursor = v.detailFieldCount()
		golden.RequireEqual(t, []byte(v.View()))
	})

	t.Run("actions", func(t *testing.T) {
		v := goldenWizard(t, &config.Config{})
		v.backendCursor = 0
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
		v = m.(SetupWizardView)
		v.stage = stageActions
		golden.RequireEqual(t, []byte(v.View()))
	})

	t.Run("passphrase", func(t *testing.T) {
		v := goldenWizard(t, &config.Config{})
		v.stage = stagePassphrase
		golden.RequireEqual(t, []byte(v.View()))
	})
}

func TestGoldenSplash(t *testing.T) {
	frames := map[string]int{
		"frame_00_glyph_only": 0,
		"frame_09_mid_reveal": splashFramesTo(splashLettersAt + 3*splashLetterStep),
		"frame_24_complete":   splashFramesTo(splashRevealDone),
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) {
			app := NewApp(Deps{RepoName: "golden", ShowSplash: true, Version: "v1.2.0", Commit: "a1b2c3d4"})
			m, _ := app.Update(tea.WindowSizeMsg{Width: goldenW, Height: goldenH})
			app = advanceSplash(m.(App), frame)
			golden.RequireEqual(t, []byte(app.renderSplash()))
		})
	}
}

func TestGoldenSettings(t *testing.T) {
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "golden-bucket"
	cfg.Repo.S3.Region = "us-east-1"
	v := NewSettingsView(Deps{Config: &cfg, ConfigPath: "/repo/sentra.yaml"})
	m, _ := v.Update(tea.WindowSizeMsg{Width: goldenW, Height: goldenH})
	golden.RequireEqual(t, []byte(m.(SettingsView).View()))
}
