package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

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
	// width/height are the content-pane dimensions from the App's
	// synthetic WindowSizeMsg; zero until the first one arrives
	// (headless tests), in which case rendering falls back to the
	// min-terminal budget so output stays deterministic. height is
	// what lets the hero graph grow to fill a tall terminal.
	width  int
	height int
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
		d.width, d.height = msg.Width, msg.Height
	case opResultMsg:
		recs := d.data.RecCount
		d.data = hydrateDashboardData(d.deps)
		d.data.RecCount = recs
	}
	return d, nil
}

// graphGradient / meterGradient are the btop-style neon ramps painted over
// the (glyph-only) braille graph and savings meter when the terminal is
// truecolor. The graph ramps hot→cool up its height (pink peak → aqua base);
// the meter ramps aqua→pink left→right. Both are pure flourish: the braille
// heights and the meter fill carry the meaning, so under NO_COLOR / 256-color
// / the Ascii test profile the panels fall back to a flat theme color and stay
// legible. Hexes are the theme's dark neon variants (see ui.theme).
var (
	graphGradient = []string{"#FF6BDD", "#CB8CFF", "#5CEBFF"}
	meterGradient = []string{"#5CEBFF", "#CB8CFF", "#FF6BDD"}
)

// View lays the dashboard out btop-style: a full-width, terminal-tall braille
// "activity" graph on top, then a stats row of two panels (repo summary with a
// gradient savings meter, and the last snapshot). The graph is the hero and
// absorbs all the vertical slack, so it grows with the terminal.
//
// The body is sized to exactly the content pane the App forwards (see
// dims): the App wraps it in a fixed-height panel that pads short content but
// does NOT clip tall content, so overshooting height would overflow the frame
// (TestApp_NoOverflowAtMinSize is the backstop).
func (d Dashboard) View() string {
	availW, availH := d.dims()

	// Reserve the stats row; the hero takes the rest.
	const statsBlock = 6 // 4 content lines + panel border
	heroBlock := max(availH-statsBlock, 5)

	hero := d.renderHero(availW-2, heroBlock)

	// Split the stats row so the two panels' borders exactly fill the hero
	// width — an uneven split (interior is odd at the 80-col minimum) avoids a
	// 1-column gap that JoinVertical would pad with trailing whitespace.
	inner := availW - 4 // two panel borders
	leftW := (inner + 1) / 2
	rightW := inner - leftW
	stats := lipgloss.JoinHorizontal(lipgloss.Top,
		d.renderRepoPanel(leftW), d.renderLastPanel(rightW))

	return lipgloss.JoinVertical(lipgloss.Left, hero, stats)
}

// dims resolves the interior width/height the dashboard may draw into. The
// App forwards its own panel Width/Height; the drawable text region is that
// minus the panel's padding (pickerContentWidth). Before the first
// WindowSizeMsg (headless tests, goldens) we mirror App.resize's min-terminal
// budget so output stays deterministic.
func (d Dashboard) dims() (w, h int) {
	fw, fh := d.width, d.height
	if fw <= 0 {
		fw = minWidth - sidebarWidth - 3 // App.resize's contentW at min size
	}
	if fh <= 0 {
		fh = minHeight - 4 // App.resize's contentH at min size
	}
	return pickerContentWidth(fw), fh
}

// dashPanel frames one panel at the given lipgloss Width (content + padding).
func dashPanel(w int, body string) string {
	return ui.Panel.Width(w).Render(body)
}

// renderHero draws the activity graph panel: a title row (label + peak size),
// the gradient braille area graph of per-snapshot logical bytes, and a footer
// (backup cadence + covered date span). panelW is the panel's lipgloss Width;
// block is its total height including the border.
//
// Logical size — not NewBytes — is the series: the question the graph answers
// is "how is my data growing", and a mostly-deduplicated snapshot would render
// as a misleading dip if we plotted upload deltas.
func (d Dashboard) renderHero(panelW, block int) string {
	textW := panelW - contentPanelHPad
	graphRows := max(block-4, 2) // -border(2) -title(1) -footer(1)
	snaps := d.data.Snaps

	if len(snaps) == 0 {
		// Keep the exact same title + graphRows + footer scaffold as the
		// populated case so the panel height is identical; a short hint
		// sits on the middle graph row.
		area := make([]string, graphRows)
		area[graphRows/2] = ui.Muted.Render(
			truncateToWidth("run a backup to see activity here", textW))
		body := ui.Subtle.Render("activity") + "\n" +
			strings.Join(area, "\n") + "\n" +
			ui.Muted.Render("no backups yet")
		return dashPanel(panelW, body)
	}

	// Snaps is newest-first; the graph reads left→right in time.
	values := make([]int64, len(snaps))
	var peak int64
	for i, s := range snaps {
		values[len(snaps)-1-i] = s.Stats.Bytes
		if s.Stats.Bytes > peak {
			peak = s.Stats.Bytes
		}
	}

	title := spread(textW,
		ui.Subtle.Render("activity"),
		ui.Muted.Render("peak "+ui.FormatBytes(peak)))

	cadence := "1 backup"
	if len(snaps) > 1 {
		cadence = fmt.Sprintf("%d backups · ~%s apart",
			len(snaps), formatApproxDuration(avgSnapshotInterval(snaps)))
	}
	oldest := snaps[len(snaps)-1].CreatedAt.UTC()
	newest := snaps[0].CreatedAt.UTC()
	footer := spread(textW,
		ui.Subtle.Render(cadence),
		ui.Muted.Render(oldest.Format("2006-01-02")+" → "+newest.Format("2006-01-02")))

	body := title + "\n" + renderGraph(values, textW, graphRows) + "\n" + footer
	return dashPanel(panelW, body)
}

// renderRepoPanel summarizes the repo: name + snapshot count, the dedup
// shrink (logical → stored), a gradient savings meter, and the agent's
// pending-finding status folded in as the last line.
func (d Dashboard) renderRepoPanel(colW int) string {
	name := d.deps.RepoName
	if name == "" {
		name = "(unnamed)"
	}
	textW := colW - contentPanelHPad
	saved := savingsFrac(d.data.TotalBytes, d.data.UploadedBytes)

	head := spread(textW,
		ui.Primary.Render(truncateToWidth(name, textW-9)),
		ui.Muted.Render(fmt.Sprintf("%d snaps", d.data.SnapshotCount)))
	shrink := ui.Subtle.Render(truncateToWidth(fmt.Sprintf("%s → %s",
		ui.FormatBytes(d.data.TotalBytes), ui.FormatBytes(d.data.UploadedBytes)), textW))
	meter := styledGauge(saved, 10) + ui.Muted.Render(fmt.Sprintf(" %d%% saved", int(saved*100+0.5)))

	agent := ui.Success.Render("✓ no findings")
	if d.data.RecCount > 0 {
		agent = ui.Warn.Render(fmt.Sprintf("⚠ %d findings pending", d.data.RecCount))
	}

	body := head + "\n" + shrink + "\n" + meter + "\n" + agent
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

// renderLastPanel shows the most-recent snapshot at a glance — ID, when and
// what tag, file count, and how much data that snapshot actually pushed (its
// NewBytes delta) — or an empty-state line when the repo has no snapshots.
func (d Dashboard) renderLastPanel(colW int) string {
	title := ui.Subtle.Render("last snapshot")
	if d.data.LastSnap == nil {
		body := title + "\n" + ui.Muted.Render("no snapshots yet") + "\n\n"
		return dashPanel(colW, body)
	}
	s := d.data.LastSnap
	tag := s.Tag
	if tag == "" {
		tag = "(untagged)"
	}
	// Composed lines are truncated, not wrapped: a long tag or ID wrapping
	// would push this panel a row taller than its row-mate and stagger the
	// grid. The month-name date (no year) keeps the when—tag line inside the
	// interior of an 80-col terminal; the snapshots table has full timestamps.
	when := s.CreatedAt.UTC().Format("Jan 02 15:04") + " — " + tag
	textW := colW - contentPanelHPad
	body := fmt.Sprintf("%s\n%s\n%s\n%s",
		ui.Primary.Render(truncateToWidth(s.ID, textW)),
		ui.Subtle.Render(truncateToWidth(when, textW)),
		ui.Subtle.Render(fmt.Sprintf("%d files", s.Stats.Files)),
		ui.Muted.Render("+"+ui.FormatBytes(s.Stats.NewBytes)+" new"),
	)
	return dashPanel(colW, body)
}

// renderGraph turns a value series into the braille area graph, painting the
// btop vertical gradient over it on truecolor terminals and falling back to a
// flat theme color otherwise (both stripped to plain braille under Ascii, so
// goldens stay stable). Rows are colored top→bottom, so tall peaks glow hot
// (pink) and the baseline sits cool (aqua).
func renderGraph(values []int64, w, h int) string {
	rows := ui.BrailleGraph(values, w, h)
	if lipgloss.ColorProfile() == termenv.TrueColor {
		grad := ui.GradientColors(graphGradient, len(rows))
		for i, ln := range rows {
			rows[i] = lipgloss.NewStyle().Foreground(lipgloss.Color(grad[i])).Render(ln)
		}
	} else {
		for i, ln := range rows {
			rows[i] = ui.Subtle.Render(ln)
		}
	}
	return strings.Join(rows, "\n")
}

// styledGauge renders ui.Gauge with the btop meter treatment: on truecolor the
// filled cells carry the aqua→pink gradient and the track is muted; elsewhere
// the whole bar is a flat success color. The fill length is the meaning; color
// is flourish.
func styledGauge(frac float64, width int) string {
	bar := ui.Gauge(frac, width)
	if lipgloss.ColorProfile() != termenv.TrueColor {
		return ui.Success.Render(bar)
	}
	runes := []rune(bar)
	filled := 0
	for _, r := range runes {
		if r == '█' {
			filled++
		}
	}
	grad := ui.GradientColors(meterGradient, filled)
	var b strings.Builder
	gi := 0
	for _, r := range runes {
		if r == '█' {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(grad[gi])).Render("█"))
			gi++
		} else {
			b.WriteString(ui.Muted.Render(string(r)))
		}
	}
	return b.String()
}

// spread lays a left and a right fragment on one row exactly w cells wide,
// padding the middle with spaces. Fragments may be pre-styled — lipgloss.Width
// measures visible cells only, so the padding math ignores ANSI. When the two
// fragments already fill (or overflow) the row, a single space still separates
// them.
func spread(w int, left, right string) string {
	pad := max(w-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", pad) + right
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
