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

type statsStage int

const (
	statsIdle statsStage = iota
	statsRunning
	statsDone
)

// statsDoneMsg carries the storage report back to the view. READ-ONLY
// load — not an opResultMsg, so it can run alongside a backup.
type statsDoneMsg struct {
	report repo.RepoStats
	err    error
}

// StatsView is the TUI face of `sentra stats`: dedup factor, logical
// vs stored bytes, and each snapshot's unique (unshared) footprint.
// Same async run-on-enter shape as CheckView — Stats loads every
// manifest, which can take a moment against S3.
type StatsView struct {
	deps   Deps
	stage  statsStage
	spin   spinner.Model
	result statsDoneMsg
	width  int
}

func NewStatsView(deps Deps) StatsView {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return StatsView{deps: deps, spin: s}
}

func (StatsView) Init() tea.Cmd { return nil }

func (v StatsView) Title() string { return "Stats" }

func (v StatsView) ShortHelp() []key.Binding {
	if v.stage == statsRunning {
		return nil
	}
	return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "compute stats"))}
}

func (v StatsView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case statsDoneMsg:
		v.stage = statsDone
		v.result = msg
		return v, nil

	case spinner.TickMsg:
		if v.stage == statsRunning {
			var cmd tea.Cmd
			v.spin, cmd = v.spin.Update(msg)
			return v, cmd
		}
		return v, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyEnter && v.stage != statsRunning && v.deps.Repo != nil {
			v.stage = statsRunning
			r := v.deps.Repo
			ctx := ctxOrBackground(v.deps.Ctx)
			run := func() tea.Msg {
				report, err := r.Stats(ctx)
				return statsDoneMsg{report: report, err: err}
			}
			return v, tea.Batch(v.spin.Tick, run)
		}
		return v, nil
	}
	return v, nil
}

func (v StatsView) View() string {
	if v.deps.Repo == nil {
		return ui.Muted.Render("no repository configured")
	}
	switch v.stage {
	case statsRunning:
		return v.spin.View() + " computing storage stats…"
	case statsDone:
		return v.renderReport()
	default:
		return ui.Primary.Render("Storage & deduplication") + "\n\n" +
			ui.ActionLine("compute repository stats", "")
	}
}

func (v StatsView) renderReport() string {
	if v.result.err != nil {
		return ui.Danger.Render("Stats failed") + "\n\n" + humanizeErr(v.result.err)
	}
	rep := v.result.report
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ui.Primary.Render("Storage report"))
	fmt.Fprintf(&b, "  snapshots      %d\n", rep.Snapshots)
	fmt.Fprintf(&b, "  logical bytes  %s\n", ui.FormatBytes(rep.LogicalBytes))
	fmt.Fprintf(&b, "  stored bytes   %s (sealed)\n", ui.FormatBytes(rep.StoredBytes))
	fmt.Fprintf(&b, "  unique chunks  %d\n", rep.UniqueChunks)
	fmt.Fprintf(&b, "  dedup factor   %.2fx\n", rep.DedupFactor())
	if len(rep.PerSnapshot) > 0 {
		fmt.Fprintf(&b, "\n%s\n", ui.Subtle.Render("  per snapshot — unique = reclaimable if pruned alone"))
		for _, s := range rep.PerSnapshot {
			fmt.Fprintf(&b, "    %s  %s unique\n", s.ID, ui.FormatBytes(s.UniqueBytes))
		}
	}
	fmt.Fprintf(&b, "\n%s", ui.ActionLine("recompute", ""))
	return b.String()
}
