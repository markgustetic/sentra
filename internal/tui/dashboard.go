package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// DashboardData is the read-only snapshot of repo state the
// dashboard renders. Pulling it into a struct (rather than reaching
// into deps.Repo from View) lets tests hydrate the model with
// canned data and avoids a per-frame round-trip to the blobstore.
//
// A v1 dashboard is intentionally static: it shows what was known at
// load time. A future iteration can refresh on a tick or on an
// "after backup completed" event.
type DashboardData struct {
	// SnapshotCount is the total number of snapshots in the repo.
	SnapshotCount int
	// TotalBytes is the sum of plaintext bytes across all snapshots
	// (the human-friendly "how big is this repo" number).
	TotalBytes int64
	// LastSnap is the most-recent snapshot, or nil for fresh repos.
	LastSnap *repo.SnapshotInfo
	// RecCount is the count of pending agent recommendations from
	// the most-recent scan. Zero is fine and renders as "0".
	RecCount int
}

// Dashboard is the home view: four panels showing repo summary, last
// snapshot, agent state, and a placeholder for the timeline sparkline
// (real implementation deferred — see Future below).
type Dashboard struct {
	deps Deps
	data DashboardData
}

// NewDashboard returns a hydrated Dashboard. If deps.Repo is non-nil
// we attempt a one-shot ListSnapshots to populate counts; failures
// are non-fatal (the dashboard renders an empty-state in that case)
// because TUI views must never crash the parent App.
//
// The hydration is synchronous on construction — a future iteration
// could push it into Init() with a tea.Cmd, but for v1 the cost is
// "one ListSnapshots per `sentra ui` invocation" which is fine.
func NewDashboard(deps Deps) Dashboard {
	d := Dashboard{deps: deps}
	d.data = hydrateDashboardData(deps)
	return d
}

// Title names the view in the sidebar, palette, and title bar.
func (Dashboard) Title() string { return "Dashboard" }

// ShortHelp lists the view-specific keys for the status bar.
func (Dashboard) ShortHelp() []key.Binding { return nil }

// SetData replaces the model's data. Tests use this to inject canned
// state; production code calls it after a backup or scan completes
// to refresh the dashboard without rebuilding the model.
func (d Dashboard) SetData(data DashboardData) Dashboard {
	d.data = data
	return d
}

// hydrateDashboardData reads what it can from the repo at
// construction time. Errors are swallowed — the dashboard always
// renders, even on a partially-broken repo. Sub-views that need
// stricter error handling should not use this helper.
func hydrateDashboardData(deps Deps) DashboardData {
	if deps.Repo == nil {
		return DashboardData{}
	}
	// Bound the load to a short timeout. A blobstore that's slow to
	// list shouldn't block the TUI from drawing — if List takes more
	// than a few seconds, we render an empty dashboard and the user
	// can re-open later.
	//
	// Parent is deps.Ctx (App-scoped) so a 'q' quit cancels mid-load.
	ctx, cancel := context.WithTimeout(ctxOrBackground(deps.Ctx), 5*time.Second)
	defer cancel()

	snaps, err := deps.Repo.ListSnapshots(ctx)
	if err != nil {
		return DashboardData{}
	}
	d := DashboardData{
		SnapshotCount: len(snaps),
	}
	for _, s := range snaps {
		d.TotalBytes += s.Stats.Bytes
	}
	if len(snaps) > 0 {
		// ListSnapshots returns newest-first.
		s := snaps[0]
		d.LastSnap = &s
	}
	return d
}

// Init is a no-op — hydration runs in NewDashboard. A future
// iteration could refresh on a timer here.
func (Dashboard) Init() tea.Cmd { return nil }

// Update refreshes the dashboard when an operation completes and is
// otherwise a no-op (the dashboard has no input bindings — App handles
// tab switching).
//
// A completed op (backup, prune, sync, …) is broadcast to every view as
// an opResultMsg; the dashboard re-hydrates so its counts reflect the
// new repo state instead of what was true at launch. Reacting to the
// marker interface rather than each concrete done-message keeps a new
// operation type refreshing the dashboard for free. RecCount comes from
// an agent scan, not ListSnapshots, so it is carried across the refresh.
func (d Dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(opResultMsg); ok {
		recs := d.data.RecCount
		d.data = hydrateDashboardData(d.deps)
		d.data.RecCount = recs
	}
	return d, nil
}

// View renders the four panels. Layout is two rows of two panels;
// each panel is wrapped in ui.Panel so the visual frame matches the
// rest of the TUI.
func (d Dashboard) View() string {
	repoPanel := d.renderRepoPanel()
	lastPanel := d.renderLastPanel()
	agentPanel := d.renderAgentPanel()
	timelinePanel := d.renderTimelinePanel()

	row1 := lipgloss.JoinHorizontal(lipgloss.Top, repoPanel, lastPanel)
	row2 := lipgloss.JoinHorizontal(lipgloss.Top, agentPanel, timelinePanel)
	return lipgloss.JoinVertical(lipgloss.Left, row1, row2) + "\n"
}

// renderRepoPanel shows the repo's name, snapshot count, and total
// bytes. The repo name doubles as the panel title so the user
// always sees which repo is being summarized.
func (d Dashboard) renderRepoPanel() string {
	name := d.deps.RepoName
	if name == "" {
		name = "(unnamed)"
	}
	body := fmt.Sprintf("%s\n%s snapshots\n%s total",
		ui.Primary.Render(name),
		ui.Subtle.Render(fmt.Sprintf("%d", d.data.SnapshotCount)),
		ui.Subtle.Render(ui.FormatBytes(d.data.TotalBytes)),
	)
	return ui.Panel.Render(body)
}

// renderLastPanel shows the most-recent snapshot at a glance, or an
// empty-state line when the repo has no snapshots. The "no snapshots
// yet" copy is what users see on a freshly-initialized repo.
func (d Dashboard) renderLastPanel() string {
	title := ui.Subtle.Render("last snapshot")
	if d.data.LastSnap == nil {
		body := title + "\n" + ui.Muted.Render("no snapshots yet")
		return ui.Panel.Render(body)
	}
	s := d.data.LastSnap
	tag := s.Tag
	if tag == "" {
		tag = "(untagged)"
	}
	body := fmt.Sprintf("%s\n%s\n%s\n%s files",
		title,
		ui.Primary.Render(s.ID),
		ui.Subtle.Render(s.CreatedAt.UTC().Format(time.RFC3339)+" — "+tag),
		ui.Subtle.Render(fmt.Sprintf("%d", s.Stats.Files)),
	)
	return ui.Panel.Render(body)
}

// renderAgentPanel shows the agent's pending-recommendation badge.
// Zero recommendations is rendered with the Success color (nothing
// to do, all clear); any non-zero count is Warn so it visually
// stands out as work to inspect.
func (d Dashboard) renderAgentPanel() string {
	title := ui.Subtle.Render("agent")
	style := ui.Success
	hint := "no pending findings"
	if d.data.RecCount > 0 {
		style = ui.Warn
		hint = "pending review"
	}
	body := fmt.Sprintf("%s\n%s\n%s",
		title,
		style.Render(fmt.Sprintf("%d recommendations", d.data.RecCount)),
		ui.Muted.Render(hint),
	)
	return ui.Panel.Render(body)
}

// renderTimelinePanel is the sparkline placeholder. A real
// implementation would walk snapshot byte sizes and render an inline
// braille sparkline; that's gold-plating for v1 and is deferred.
//
// Future: replace with a proper sparkline of snapshot sizes
// over time. Suggested approach: bucket snapshots by week, render
// each bucket's NewBytes via the unicode block-characters scale.
func (d Dashboard) renderTimelinePanel() string {
	title := ui.Subtle.Render("timeline")
	body := title + "\n" + ui.Muted.Render("sparkline coming soon")
	return ui.Panel.Render(body)
}
