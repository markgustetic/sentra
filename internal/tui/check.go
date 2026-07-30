package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

type checkStage int

const (
	checkIdle checkStage = iota
	checkRunning
	checkDone
)

// checkDoneMsg carries the integrity report back to the flow. This is a
// READ-ONLY load, so it is NOT an opResultMsg — Check does not take the
// mutating-op guard and can run alongside a backup.
type checkDoneMsg struct {
	report repo.CheckReport
	err    error
}

// CheckView runs repo.Check asynchronously (a repo with many blobs can
// take a moment to list) and renders the integrity report. It replaces
// the Phase 1 Operations view, which did the same read synchronously in
// its constructor and blocked the first frame.
type CheckView struct {
	deps   Deps
	stage  checkStage
	spin   spinner.Model
	result checkDoneMsg
	width  int
	// deepMode arms --read-data for the next run: 0 = presence only,
	// 1 = full deep verify (every chunk downloaded and re-hashed),
	// 2 = 10% deterministic sample — the S3-egress lever for big
	// repos, mirroring `check --read-data-subset`.
	deepMode int
}

// checkDeepModes is the 'd' cycle length.
const checkDeepModes = 3

func NewCheckView(deps Deps) CheckView {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return CheckView{deps: deps, spin: s}
}

func (CheckView) Init() tea.Cmd { return nil }

func (v CheckView) Title() string { return "Check" }

func (v CheckView) ShortHelp() []key.Binding {
	switch v.stage {
	case checkRunning:
		return nil
	default:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "run check")),
			key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "verify depth")),
		}
	}
}

func (v CheckView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case checkDoneMsg:
		v.stage = checkDone
		v.result = msg
		return v, nil

	case spinner.TickMsg:
		if v.stage == checkRunning {
			var cmd tea.Cmd
			v.spin, cmd = v.spin.Update(msg)
			return v, cmd
		}
		return v, nil

	case tea.KeyMsg:
		if msg.String() == "d" && v.stage != checkRunning {
			v.deepMode = (v.deepMode + 1) % checkDeepModes
			return v, nil
		}
		if msg.Type == tea.KeyEnter && v.stage != checkRunning && v.deps.Repo != nil {
			v.stage = checkRunning
			r := v.deps.Repo
			opts := repo.CheckOptions{ReadData: v.deepMode > 0}
			if v.deepMode == 2 {
				opts.ReadDataSubset = 0.10
			}
			ctx := ctxOrBackground(v.deps.Ctx)
			run := func() tea.Msg {
				report, err := r.Check(ctx, opts)
				return checkDoneMsg{report: report, err: err}
			}
			return v, tea.Batch(v.spin.Tick, run)
		}
		return v, nil
	}
	return v, nil
}

func (v CheckView) View() string {
	if v.deps.Repo == nil {
		return ui.Muted.Render("no repository configured")
	}
	switch v.stage {
	case checkRunning:
		return v.spin.View() + " running integrity check…"
	case checkDone:
		return v.renderReport()
	default:
		mode := "presence check (fast; d cycles verify depth)"
		switch v.deepMode {
		case 1:
			mode = "deep verify armed — every chunk downloaded and re-hashed"
		case 2:
			mode = "deep verify (10% sample) armed — bounded S3 egress"
		}
		return ui.Primary.Render("Repository integrity check") + "\n" +
			ui.Muted.Render("  mode: "+mode) + "\n\n" +
			ui.ActionLine("run the integrity check", "")
	}
}

func (v CheckView) renderReport() string {
	if v.result.err != nil {
		return ui.Danger.Render("Check failed") + "\n\n" + v.result.err.Error()
	}
	rep := v.result.report
	var b strings.Builder
	// Healthy() is the repo's own verdict — it already folds in
	// missing blobs, manifest issues, deep-verify corruption, and
	// lock state, so the view can't drift from the CLI's judgment.
	healthy := rep.Healthy()
	status := ui.Success.Render("● healthy")
	if !healthy {
		status = ui.Danger.Render("● issues found")
	}
	fmt.Fprintf(&b, "%s  %s\n\n", ui.Primary.Render("Integrity report"), status)
	fmt.Fprintf(&b, "  snapshots        %d\n", rep.Snapshots)
	fmt.Fprintf(&b, "  files            %d\n", rep.Files)
	fmt.Fprintf(&b, "  data blobs       %d  (%s)\n", rep.DataBlobs, ui.FormatBytes(rep.DataBytes))
	fmt.Fprintf(&b, "  referenced blobs %d\n", rep.ReferencedBlobs)
	fmt.Fprintf(&b, "  orphan bytes     %s\n", ui.FormatBytes(rep.OrphanBytes))
	if rep.ReadDataBlobs > 0 {
		fmt.Fprintf(&b, "  deep-verified    %d chunk(s)\n", rep.ReadDataBlobs)
	}
	if n := len(rep.CorruptBlobs); n > 0 {
		fmt.Fprintf(&b, "\n  %s  %d corrupt blob(s)\n", ui.Danger.Render("✗"), n)
	}
	if n := len(rep.MissingBlobs); n > 0 {
		fmt.Fprintf(&b, "\n  %s  %d missing blob(s)\n", ui.Danger.Render("✗"), n)
	}
	if n := len(rep.ManifestIssues); n > 0 {
		fmt.Fprintf(&b, "  %s  %d manifest issue(s)\n", ui.Danger.Render("✗"), n)
	}
	if rep.Lock != nil && (rep.Lock.Stale || rep.Lock.Unreadable) {
		fmt.Fprintf(&b, "  %s\n", ui.Warn.Render("⚠ advisory lock is stale or unreadable"))
	}
	fmt.Fprintf(&b, "\n%s", ui.ActionLine("run the check again", ""))
	return b.String()
}
