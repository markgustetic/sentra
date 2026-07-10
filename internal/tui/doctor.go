package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

type doctorStage int

const (
	doctorIdle doctorStage = iota
	doctorRunning
	doctorDone
)

// doctorStatus is the severity of a single diagnostic row.
type doctorStatus int

const (
	doctorOK doctorStatus = iota
	doctorWarn
	doctorFail
)

// doctorRow is one line of the report: a short label, its status, and an
// optional detail (an error string or an explanatory note).
type doctorRow struct {
	label  string
	status doctorStatus
	detail string
}

// doctorDoneMsg carries the collected rows back to the flow. Like
// checkDoneMsg this is a READ-ONLY result, so it is deliberately NOT an
// opResultMsg — the Doctor view never takes the mutating-op guard and can
// run alongside a backup.
type doctorDoneMsg struct {
	rows    []doctorRow
	healthy bool
}

// DoctorView runs every read-only environment check asynchronously and
// renders ok/warn/fail rows plus a healthy/issues summary. It is the TUI
// analogue of `sentra doctor`: config validity, bucket-name shape, AWS
// identity + bucket inspection (AWS backends only), and repository
// integrity. All checks run in one tea.Cmd off the UI goroutine so a slow
// AWS round-trip never blocks a frame.
type DoctorView struct {
	deps   Deps
	stage  doctorStage
	spin   spinner.Model
	result doctorDoneMsg
	width  int
}

func NewDoctorView(deps Deps) DoctorView {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return DoctorView{deps: deps, spin: s}
}

func (DoctorView) Init() tea.Cmd { return nil }

func (v DoctorView) Title() string { return "Doctor" }

func (v DoctorView) ShortHelp() []key.Binding {
	switch v.stage {
	case doctorRunning:
		return nil
	default:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "run doctor"))}
	}
}

func (v DoctorView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case doctorDoneMsg:
		v.stage = doctorDone
		v.result = msg
		return v, nil

	case spinner.TickMsg:
		if v.stage == doctorRunning {
			var cmd tea.Cmd
			v.spin, cmd = v.spin.Update(msg)
			return v, cmd
		}
		return v, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyEnter && v.stage != doctorRunning {
			v.stage = doctorRunning
			deps := v.deps
			ctx := ctxOrBackground(v.deps.Ctx)
			run := func() tea.Msg {
				return runDoctorChecks(ctx, deps)
			}
			return v, tea.Batch(v.spin.Tick, run)
		}
		return v, nil
	}
	return v, nil
}

// runDoctorChecks performs every read-only diagnostic and returns the
// collected rows plus the overall healthy verdict (healthy == no fail
// rows; warnings do not flip it, matching the cli doctor which counts only
// failures). It is a pure function of deps + ctx so tests drive it through
// the Enter batch without a real terminal.
func runDoctorChecks(ctx context.Context, deps Deps) doctorDoneMsg {
	var rows []doctorRow
	add := func(label string, status doctorStatus, detail string) {
		rows = append(rows, doctorRow{label: label, status: status, detail: detail})
	}

	cfg := deps.Config
	if cfg == nil {
		add("Config loaded", doctorFail, "no sentra.yaml configuration is loaded")
	} else {
		add("Config loaded", doctorOK, "")
	}

	// Bucket-name + AWS legs only make sense with a config. Mirror the cli
	// doctor's gating: AWS identity/inspect run only for a real bucket on
	// the AWS backend (no S3-compatible endpoint override).
	bucketOK := false
	if cfg != nil {
		switch cfg.Repo.S3.Bucket {
		case "":
			add("Bucket configured", doctorFail, "repo.s3.bucket is missing")
		default:
			if err := diag.ValidateBucketName(cfg.Repo.S3.Bucket); err != nil {
				add("Bucket name valid", doctorFail, err.Error())
			} else {
				bucketOK = true
				add("Bucket name valid", doctorOK, "")
			}
		}
	}

	if cfg != nil && bucketOK {
		if cfg.Repo.S3.EndpointURL != "" {
			add("S3-compatible endpoint configured", doctorOK, "")
		} else {
			runDoctorAWSChecks(ctx, cfg, add)
		}
	}

	// Repository integrity. Reuse CheckReport.Healthy() — the same verdict
	// the Check view renders.
	if deps.Repo == nil {
		add("Repository check", doctorWarn, "no repository configured")
	} else {
		report, err := deps.Repo.Check(ctx, repo.CheckOptions{StaleLockAfter: 24 * time.Hour})
		if err != nil {
			add("Repository check", doctorFail, err.Error())
		} else if !report.Healthy() {
			add("Repository check", doctorFail, "integrity check found issues")
		} else {
			add("Repository check healthy", doctorOK, "")
		}
	}

	healthy := true
	for _, row := range rows {
		if row.status == doctorFail {
			healthy = false
			break
		}
	}
	return doctorDoneMsg{rows: rows, healthy: healthy}
}

// runDoctorAWSChecks runs the AWS identity + bucket-inspection legs and
// appends their rows. A failed identity check short-circuits inspection,
// mirroring cli runDoctorAWS.
func runDoctorAWSChecks(ctx context.Context, cfg *config.Config, add func(string, doctorStatus, string)) {
	if err := diag.CheckSDKIdentity(ctx, cfg); err != nil {
		add("AWS identity verified", doctorFail, err.Error())
		return
	}
	add("AWS identity verified", doctorOK, "")

	report, err := diag.Inspect(ctx, cfg)
	if err != nil {
		add("AWS S3 bucket inspected", doctorFail, err.Error())
		return
	}
	if report.BucketAccessible {
		add("Bucket is accessible", doctorOK, "")
	}
	if report.PublicAccessReadable && report.PublicAccessBlocked {
		add("Bucket public access is blocked", doctorOK, "")
	} else if report.PublicAccessReadable {
		add("Bucket public access block is not fully enabled", doctorWarn, "")
	}
	if report.DefaultEncryptionReadable && report.DefaultEncryptionEnabled {
		add("Bucket default encryption is enabled", doctorOK, "")
	} else if report.DefaultEncryptionReadable {
		add("Bucket default encryption is not enabled", doctorWarn, "")
	}
}

func (v DoctorView) View() string {
	switch v.stage {
	case doctorRunning:
		return v.spin.View() + " running diagnostics…"
	case doctorDone:
		return v.renderReport()
	default:
		return ui.Primary.Render("Environment diagnostics") + "\n\n" +
			ui.ActionLine("run diagnostics", "")
	}
}

func (v DoctorView) renderReport() string {
	var b strings.Builder
	status := ui.Success.Render("● healthy")
	if !v.result.healthy {
		status = ui.Danger.Render("● issues found")
	}
	b.WriteString(ui.Primary.Render("Doctor report") + "  " + status + "\n\n")
	for _, row := range v.result.rows {
		mark := ui.Success.Render("ok  ")
		switch row.status {
		case doctorWarn:
			mark = ui.Warn.Render("warn")
		case doctorFail:
			mark = ui.Danger.Render("fail")
		}
		b.WriteString("  " + mark + "  " + row.label + "\n")
		if row.detail != "" {
			b.WriteString("        " + ui.Muted.Render(row.detail) + "\n")
		}
	}
	b.WriteString("\n" + ui.ActionLine("run diagnostics again", ""))
	return b.String()
}
