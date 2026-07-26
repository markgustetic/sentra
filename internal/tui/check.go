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
}

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
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "run check"))}
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
		if msg.Type == tea.KeyEnter && v.stage != checkRunning && v.deps.Repo != nil {
			v.stage = checkRunning
			r := v.deps.Repo
			ctx := ctxOrBackground(v.deps.Ctx)
			run := func() tea.Msg {
				report, err := r.Check(ctx, repo.CheckOptions{})
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
		return ui.Primary.Render("Repository integrity check") + "\n\n" +
			ui.ActionLine("run the integrity check", "")
	}
}

func (v CheckView) renderReport() string {
	if v.result.err != nil {
		return ui.Danger.Render("Check failed") + "\n\n" + v.result.err.Error()
	}
	rep := v.result.report
	var b strings.Builder
	healthy := len(rep.MissingBlobs) == 0 && len(rep.ManifestIssues) == 0 &&
		(rep.Lock == nil || (!rep.Lock.Stale && !rep.Lock.Unreadable))
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
