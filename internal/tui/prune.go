package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

type pruneStage int

const (
	prunePreview pruneStage = iota
	pruneRunning
	pruneDone
)

// pruneConfirmID ties the typed-confirm modal back to this flow.
const pruneConfirmID = "prune-apply"

type pruneDoneMsg struct {
	deleted int
	stats   repo.GCStats
	err     error
}

func (pruneDoneMsg) opResult() {}

// PruneView shows the retention preview and, after a TYPED confirmation
// ("prune"), deletes the dropped snapshots and runs GC. Mirrors the CLI
// prune --apply sequence exactly: DeleteSnapshot per drop, then GC with
// the keep-set (GC's live set is derived from the store under its lock;
// keepIDs only marks the deliberate-prune path).
type PruneView struct {
	deps      Deps
	stage     pruneStage
	decisions []repo.RetentionDecision
	keep      []string
	drop      []string
	loadErr   string

	result pruneDoneMsg
	width  int
}

func NewPruneView(deps Deps) PruneView {
	v := PruneView{deps: deps}
	if deps.Repo == nil {
		v.loadErr = "no repository configured"
		return v
	}
	ctx, cancel := context.WithTimeout(depsCtx(deps), hydrateTimeout)
	defer cancel()
	snaps, err := deps.Repo.ListSnapshots(ctx)
	if err != nil {
		v.loadErr = err.Error()
		return v
	}
	policy := repo.RetentionPolicy{}
	if deps.Config != nil {
		policy = repo.RetentionPolicy{
			KeepLast:    deps.Config.Retention.KeepLast,
			KeepDaily:   deps.Config.Retention.KeepDaily,
			KeepWeekly:  deps.Config.Retention.KeepWeekly,
			KeepMonthly: deps.Config.Retention.KeepMonthly,
		}
	}
	v.decisions = repo.PlanRetentionExplain(snaps, policy)
	for _, d := range v.decisions {
		if d.Keep {
			v.keep = append(v.keep, d.Snapshot.ID)
		} else {
			v.drop = append(v.drop, d.Snapshot.ID)
		}
	}
	return v
}

func (PruneView) Init() tea.Cmd { return nil }

func (v PruneView) Title() string { return "Prune" }

func (v PruneView) ShortHelp() []key.Binding {
	if v.stage == prunePreview && len(v.drop) > 0 {
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "prune…"))}
	}
	return nil
}

func (v PruneView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case pruneDoneMsg:
		v.stage = pruneDone
		v.result = msg
		return v, nil

	case confirmedMsg:
		if msg.id != pruneConfirmID || v.stage != prunePreview {
			return v, nil
		}
		return v.startPrune()

	case tea.KeyMsg:
		if v.stage == prunePreview && msg.Type == tea.KeyEnter && len(v.drop) > 0 {
			body := fmt.Sprintf("This deletes %d snapshot(s) and reclaims their unique chunks.\nChunks still referenced by kept snapshots are never touched.", len(v.drop))
			modal := NewTypedConfirmModal("Confirm prune", body, "prune", pruneConfirmID, 80, 24)
			return v, func() tea.Msg { return pushModalMsg{modal: modal} }
		}
		if v.stage == pruneDone && msg.Type == tea.KeyEnter {
			fresh := NewPruneView(v.deps)
			fresh.width = v.width
			return fresh, nil
		}
		return v, nil
	}
	return v, nil
}

func (v PruneView) startPrune() (tea.Model, tea.Cmd) {
	v.stage = pruneRunning
	r := v.deps.Repo
	drop := append([]string(nil), v.drop...)
	keep := append([]string(nil), v.keep...)
	start := startOpMsg{
		name: "prune",
		run: func(ctx context.Context) tea.Msg {
			deleted := 0
			for _, id := range drop {
				if err := r.DeleteSnapshot(ctx, id); err != nil {
					return pruneDoneMsg{deleted: deleted, err: err}
				}
				deleted++
			}
			keepIDs := make(map[string]bool, len(keep))
			for _, id := range keep {
				keepIDs[id] = true
			}
			stats, err := r.GC(ctx, keepIDs)
			return pruneDoneMsg{deleted: deleted, stats: stats, err: err}
		},
	}
	return v, func() tea.Msg { return start }
}

func (v PruneView) View() string {
	if v.loadErr != "" {
		return ui.Danger.Render(v.loadErr)
	}
	var b strings.Builder
	switch v.stage {
	case pruneRunning:
		b.WriteString(ui.Primary.Render("Pruning…"))
	case pruneDone:
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Prune failed"))
			b.WriteString("\n\n" + v.result.err.Error())
		} else {
			b.WriteString(ui.Success.Render("Prune complete"))
			b.WriteString(fmt.Sprintf("\n\n  deleted snapshots  %d\n  reclaimed blobs    %d\n  reclaimed bytes    %s\n  live blobs         %d",
				v.result.deleted, v.result.stats.DeletedBlobs,
				ui.FormatBytes(v.result.stats.DeletedBytes), v.result.stats.LiveBlobs))
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ recompute"))
	default:
		b.WriteString(ui.Primary.Render("Retention preview"))
		b.WriteString(fmt.Sprintf("  %s\n\n", ui.Muted.Render(
			fmt.Sprintf("keep %d · drop %d", len(v.keep), len(v.drop)))))
		for _, d := range v.decisions {
			verdict := ui.Success.Render("keep")
			reason := strings.Join(d.Reasons, ", ")
			if !d.Keep {
				verdict = ui.Danger.Render("drop")
				if reason == "" {
					reason = "not selected by retention policy"
				}
			}
			b.WriteString(fmt.Sprintf("  %s  %s  %s\n", d.Snapshot.ID, verdict, ui.Muted.Render(reason)))
		}
		if len(v.drop) > 0 {
			b.WriteString("\n" + ui.Muted.Render("⏎ prune (typed confirmation required)"))
		} else {
			b.WriteString("\n" + ui.Muted.Render("nothing to prune"))
		}
	}
	return b.String()
}
