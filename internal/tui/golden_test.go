package tui

import (
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
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

// TestGoldenDashboard snapshots the populated dashboard at a tall size so the
// whole btop-style layout is locked: the activity graph, the storage + last-
// snapshot row, the tags + retention row, and the recent-snapshots table. Data
// is canned with fixed clocks — nothing in the frame derives from time.Now(),
// which is what keeps this golden stable. The growth series is deliberately
// non-monotonic so the sparkline can't degenerate into a flat bar.
func TestGoldenDashboard(t *testing.T) {
	day := func(n int) time.Time {
		return time.Date(2026, 6, n, 3, 30, 0, 0, time.UTC)
	}
	snaps := []repo.SnapshotInfo{ // newest-first, ListSnapshots order
		{ID: "snap-4f9c2ab8e1", CreatedAt: day(9), Tag: "nightly",
			Stats: repo.SnapshotStats{Files: 1382, Bytes: 96 << 20, NewBytes: 3 << 20}},
		{ID: "snap-b71d0c55aa", CreatedAt: day(8), Tag: "nightly",
			Stats: repo.SnapshotStats{Files: 1375, Bytes: 94 << 20, NewBytes: 2 << 20}},
		{ID: "snap-90e3d1f207", CreatedAt: day(7), Tag: "nightly",
			Stats: repo.SnapshotStats{Files: 1391, Bytes: 128 << 20, NewBytes: 41 << 20}},
		{ID: "snap-2c8ba9d640", CreatedAt: day(5), Tag: "weekly",
			Stats: repo.SnapshotStats{Files: 1120, Bytes: 61 << 20, NewBytes: 12 << 20}},
		{ID: "snap-e5f01b3c99", CreatedAt: day(3), Tag: "nightly",
			Stats: repo.SnapshotStats{Files: 1101, Bytes: 58 << 20, NewBytes: 58 << 20}},
	}
	data := DashboardData{
		SnapshotCount: len(snaps),
		LastSnap:      &snaps[0],
		RecCount:      2,
		Snaps:         snaps,
	}
	for _, s := range snaps {
		data.TotalBytes += s.Stats.Bytes
		data.UploadedBytes += s.Stats.NewBytes
	}

	cfg := config.Defaults()
	v := NewDashboard(Deps{RepoName: "golden-repo", Config: &cfg})
	// A tall content pane (well above the 24-row golden frame) so every section
	// renders; the dashboard is snapshotted on its own, not inside the shell.
	m, _ := v.Update(tea.WindowSizeMsg{Width: goldenW - sidebarWidth - 3, Height: 40})
	v = m.(Dashboard).SetData(data)
	golden.RequireEqual(t, []byte(v.View()))
}

// TestGoldenBanner snapshots the synthwave header banner (sun, large SENTRA
// wordmark, grid horizon) at the golden width. It renders plain under the Ascii
// profile, so this locks the SHAPE — the piece a human would eyeball in a
// terminal — and turns any drift in the art into a reviewable diff.
func TestGoldenBanner(t *testing.T) {
	golden.RequireEqual(t, []byte(synthwaveBanner(goldenW, 0)))
}

func TestGoldenSettings(t *testing.T) {
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "golden-bucket"
	cfg.Repo.S3.Region = "us-east-1"
	v := NewSettingsView(Deps{Config: &cfg, ConfigPath: "/repo/sentra.yaml"})
	m, _ := v.Update(tea.WindowSizeMsg{Width: goldenW, Height: goldenH})
	golden.RequireEqual(t, []byte(m.(SettingsView).View()))
}
