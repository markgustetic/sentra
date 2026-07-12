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
	// UploadedBytes is the sum of sealed-blob bytes actually pushed
	// (per-snapshot NewBytes). Against TotalBytes it is what dedup +
	// compression bought; on tiny repos it can EXCEED TotalBytes
	// because AEAD sealing and zstd framing cost more than dedup
	// saves — savings rendering must clamp, not go negative.
	UploadedBytes int64
	// LastSnap is the most-recent snapshot, or nil for fresh repos.
	LastSnap *repo.SnapshotInfo
	// RecCount is the count of pending agent recommendations from
	// the most-recent scan. Zero is fine and renders as "0".
	RecCount int
	// Snaps is the full snapshot series, newest-first as ListSnapshots
	// returns it. The timeline sparkline and cadence stats derive from
	// it; the aggregates above are kept precomputed so panels that
	// only need a number don't re-walk the slice per frame.
	Snaps []repo.SnapshotInfo
}

// Dashboard is the home view: four panels showing repo summary, last
// snapshot, agent state, and the timeline sparkline of snapshot sizes.
type Dashboard struct {
	deps Deps
	data DashboardData
	// width is the content-pane width from the App's synthetic
	// WindowSizeMsg; zero until the first one arrives (headless
	// tests), in which case rendering falls back to the min-terminal
	// budget so output stays deterministic.
	width int
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

// InertContent marks the Dashboard as a passive readout: its Update handles no
// keys, so activating it from the rail must not move the focus border into its
// pane — there is nothing to do there, and it would read as Enter doing nothing.
// Focus stays on the rail so scrolling continues. See App.contentFocusable.
func (Dashboard) InertContent() bool { return true }

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
		Snaps:         snaps,
	}
	for _, s := range snaps {
		d.TotalBytes += s.Stats.Bytes
		d.UploadedBytes += s.Stats.NewBytes
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
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width = msg.Width
	case opResultMsg:
		recs := d.data.RecCount
		d.data = hydrateDashboardData(d.deps)
		d.data.RecCount = recs
	}
	return d, nil
}

// View renders the four panels. Layout is two rows of two panels;
// each panel is wrapped in ui.Panel at a shared column width so the
// grid stays aligned regardless of which panel has the longest line.
func (d Dashboard) View() string {
	colW := d.colWidth()
	row1 := lipgloss.JoinHorizontal(lipgloss.Top,
		d.renderRepoPanel(colW), d.renderLastPanel(colW))
	row2 := lipgloss.JoinHorizontal(lipgloss.Top,
		d.renderAgentPanel(colW), d.renderTimelinePanel(colW))
	return lipgloss.JoinVertical(lipgloss.Left, row1, row2) + "\n"
}

// colWidth is each panel's lipgloss Width (content + padding). The
// width the App forwards is its own panel Width, whose Padding(0,1)
// sits *inside* it — the drawable region is pickerContentWidth of it.
// Two panels per row, each adding a border (2) outside their Width,
// must fit that region: 2*(colW+2) <= drawable, hence (drawable-4)/2.
// Without a WindowSizeMsg yet we mirror App.resize's min-terminal
// budget so headless tests and goldens render the same frame a real
// 80-col terminal would.
func (d Dashboard) colWidth() int {
	w := d.width
	if w <= 0 {
		w = minWidth - sidebarWidth - 3 // App.resize's contentW at min size
	}
	return max((pickerContentWidth(w)-4)/2, 20)
}

// dashPanel frames one panel at the shared column width.
func dashPanel(colW int, body string) string {
	return ui.Panel.Width(colW).Render(body)
}

// renderRepoPanel shows the repo's name, snapshot count, logical size,
// and what dedup + compression bought: uploaded (stored) bytes plus a
// savings gauge. The repo name doubles as the panel title so the user
// always sees which repo is being summarized.
func (d Dashboard) renderRepoPanel(colW int) string {
	name := d.deps.RepoName
	if name == "" {
		name = "(unnamed)"
	}
	saved := savingsFrac(d.data.TotalBytes, d.data.UploadedBytes)
	textW := colW - contentPanelHPad
	body := fmt.Sprintf("%s\n%s\n%s\n%s %s",
		ui.Primary.Render(truncateToWidth(name, textW)),
		ui.Subtle.Render(truncateToWidth(fmt.Sprintf("%d snapshots · %s",
			d.data.SnapshotCount, ui.FormatBytes(d.data.TotalBytes)), textW)),
		ui.Subtle.Render(ui.FormatBytes(d.data.UploadedBytes)+" stored"),
		ui.Success.Render(ui.Gauge(saved, 10)),
		ui.Muted.Render(fmt.Sprintf("%d%% saved", int(saved*100+0.5))),
	)
	return dashPanel(colW, body)
}

// savingsFrac is the fraction of logical bytes the store never had to
// hold — dedup plus compression. Clamped at zero: sealing overhead on
// a tiny repo can push uploaded past logical, and "-12% saved" would
// read as a bug rather than the physics it is.
func savingsFrac(total, uploaded int64) float64 {
	if total <= 0 {
		return 0
	}
	f := 1 - float64(uploaded)/float64(total)
	if f < 0 {
		f = 0
	}
	return f
}

// renderLastPanel shows the most-recent snapshot at a glance — ID,
// when and what tag, file count, and how much data that snapshot
// actually pushed (its NewBytes delta) — or an empty-state line when
// the repo has no snapshots. The "no snapshots yet" copy is what
// users see on a freshly-initialized repo.
func (d Dashboard) renderLastPanel(colW int) string {
	title := ui.Subtle.Render("last snapshot")
	if d.data.LastSnap == nil {
		body := title + "\n" + ui.Muted.Render("no snapshots yet")
		return dashPanel(colW, body)
	}
	s := d.data.LastSnap
	tag := s.Tag
	if tag == "" {
		tag = "(untagged)"
	}
	// Composed lines are truncated, not wrapped: a long tag or ID
	// wrapping would push this panel a row taller than its row-mate
	// and stagger the grid. The month-name date (no year) keeps the
	// when—tag line inside the 24-cell interior of an 80-col terminal;
	// the snapshots table is where full ISO timestamps live.
	when := s.CreatedAt.UTC().Format("Jan 02 15:04") + " — " + tag
	textW := colW - contentPanelHPad
	body := fmt.Sprintf("%s\n%s\n%s\n%s%s",
		title,
		ui.Primary.Render(truncateToWidth(s.ID, textW)),
		ui.Subtle.Render(truncateToWidth(when, textW)),
		ui.Subtle.Render(fmt.Sprintf("%d files", s.Stats.Files)),
		ui.Muted.Render(" · +"+ui.FormatBytes(s.Stats.NewBytes)),
	)
	return dashPanel(colW, body)
}

// renderAgentPanel shows the agent's pending-recommendation badge.
// Zero recommendations is rendered with the Success color (nothing
// to do, all clear); any non-zero count is Warn so it visually
// stands out as work to inspect.
func (d Dashboard) renderAgentPanel(colW int) string {
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
	return dashPanel(colW, body)
}

// renderTimelinePanel graphs per-snapshot logical bytes oldest→newest
// as a block sparkline, with the cadence ("how often do I back up")
// and the covered date span beneath it. Size, not NewBytes, is the
// series: the question this graph answers is "how is my data growing",
// and a mostly-deduplicated snapshot would render as a misleading dip.
func (d Dashboard) renderTimelinePanel(colW int) string {
	title := ui.Subtle.Render("timeline")
	snaps := d.data.Snaps
	if len(snaps) == 0 {
		body := title + "\n" + ui.Muted.Render("no backups graphed yet")
		return dashPanel(colW, body)
	}

	// Snaps is newest-first; the graph reads left→right in time.
	values := make([]int64, len(snaps))
	for i, s := range snaps {
		values[len(snaps)-1-i] = s.Stats.Bytes
	}
	textW := colW - contentPanelHPad
	spark := ui.Sparkline(values, textW)

	cadence := "1 backup"
	if len(snaps) > 1 {
		cadence = fmt.Sprintf("%d backups · ~%s apart",
			len(snaps), formatApproxDuration(avgSnapshotInterval(snaps)))
	}
	oldest := snaps[len(snaps)-1].CreatedAt.UTC()
	newest := snaps[0].CreatedAt.UTC()
	span := oldest.Format("2006-01-02") + " → " + newest.Format("2006-01-02")

	body := fmt.Sprintf("%s\n%s\n%s\n%s",
		title,
		ui.Primary.Render(spark),
		ui.Subtle.Render(truncateToWidth(cadence, textW)),
		ui.Muted.Render(truncateToWidth(span, textW)),
	)
	return dashPanel(colW, body)
}

// avgSnapshotInterval is the mean gap between consecutive snapshots:
// total span over n-1 gaps. Fewer than two snapshots have no gaps and
// return zero; callers render a count instead of a cadence then.
// snaps is newest-first (ListSnapshots order).
func avgSnapshotInterval(snaps []repo.SnapshotInfo) time.Duration {
	if len(snaps) < 2 {
		return 0
	}
	span := snaps[0].CreatedAt.Sub(snaps[len(snaps)-1].CreatedAt)
	if span < 0 {
		return 0
	}
	return span / time.Duration(len(snaps)-1)
}

// formatApproxDuration renders a cadence in the single coarse unit an
// operator would say out loud ("about every 26 hours"), never a
// composite like 26h3m12s. Hours run to 47h before switching to days
// so a drifting nightly backup reads as "26h", not a falsely-tidy "1d".
func formatApproxDuration(dur time.Duration) string {
	switch {
	case dur < time.Minute:
		return "<1m"
	case dur < time.Hour:
		return fmt.Sprintf("%dm", int(dur.Minutes()))
	case dur < 48*time.Hour:
		return fmt.Sprintf("%dh", int(dur.Hours()))
	default:
		return fmt.Sprintf("%dd", int(dur.Hours()/24))
	}
}
