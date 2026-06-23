// Package tui is the Bubbletea-based dashboard launched by `sentra ui`
// (and by bare `sentra` with no subcommand). It is deliberately a thin
// shell over the internal/repo and internal/agent packages: every view
// is a pure model that renders data the deps layer hands it, and never
// owns its own goroutines beyond the agent stream channel.
//
// The parent App owns the active view, the window size, and a small
// help toggle. Each sub-model handles its own internal state — App
// just routes keys and renders top/bottom chrome.
package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// View identifies which sub-model the parent App is currently
// rendering. The order is the navigation order users see in the top
// bar — keep them stable.
type View int

const (
	// ViewDashboard is the home screen: repo summary panels and a
	// recent-activity glance. Default on launch.
	ViewDashboard View = iota
	// ViewSnapshots is the sortable snapshot list with detail drill-in.
	ViewSnapshots
	// ViewDiff is the three-column added/removed/changed view.
	ViewDiff
	// ViewAgent is the streaming agent reasoning + recommendations
	// split. Pressing `s` inside it kicks off a scan.
	ViewAgent
	// ViewOperations shows repository health, integrity, and cleanup
	// signals from repo.Check.
	ViewOperations
)

// Deps is the wiring App needs to construct the sub-views. Every
// field is optional: a Deps{} produces a TUI that still renders
// (with empty-repo placeholders) so tests don't need a real backend.
//
// Repo, when nil, makes the dashboard / snapshots / diff views show
// "no repo configured" placeholders rather than crashing on data
// access. Provider can be nil too — the agent view renders a
// "configure ANTHROPIC_API_KEY" hint instead of a streaming pane.
type Deps struct {
	// Repo is the opened repository. Sub-models read snapshot lists,
	// manifests, and diff results from it. May be nil for tests.
	Repo *repo.Repo

	// Provider is the LLM provider for the agent view. May be nil
	// when the user hasn't configured an API key — the agent view
	// renders a placeholder instead of a stream pane.
	Provider llm.Provider

	// RepoName is the human-readable name shown in the top bar. We
	// pass it explicitly rather than reading from the repo's config
	// because the user-facing label often comes from sentra.yaml's
	// bucket field, not from anything inside the repo struct.
	RepoName string

	// Ctx is the parent context for all TUI-driven I/O. NewApp
	// derives a cancellable child from this and threads the child
	// back into every sub-view's Deps via DepsForChildren — so when
	// the user presses 'q' the App's cleanup cancels every in-flight
	// blobstore call. Nil falls back to context.Background() so tests
	// using `Deps{}` keep working.
	Ctx context.Context
}

// App is the parent Bubbletea model. It owns the active view and
// global window dimensions; sub-models own their own internal state.
type App struct {
	active View
	width  int
	height int
	help   bool

	dashboard  tea.Model
	snapshots  tea.Model
	diff       tea.Model
	agent      tea.Model
	operations tea.Model

	// cancel cancels the App-scoped context that was derived from
	// deps.Ctx in NewApp. Sub-views derive per-call timeouts from
	// that App-scoped context, so calling cancel() on quit
	// terminates every in-flight blobstore call rather than letting
	// it drain to its own per-call timeout.
	cancel context.CancelFunc
}

// NewApp constructs the App with each sub-view pre-built from deps.
// Sub-views are built eagerly so switching between them is a tab-key
// away with no construction cost; for the v1 view set this is cheap
// (each view is a few KB of state).
//
// NewApp wraps deps.Ctx (or context.Background() if nil) in a
// cancellable child and stores the cancel func on the returned App.
// Every sub-view receives Deps with that same cancellable Ctx, so
// when the App's cleanup runs every in-flight derived context fires
// ctx.Err() at once.
func NewApp(deps Deps) App {
	parent := deps.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	deps.Ctx = ctx

	return App{
		active:     ViewDashboard,
		dashboard:  NewDashboard(deps),
		snapshots:  NewSnapshots(deps),
		diff:       NewDiff(deps),
		agent:      NewAgentView(deps),
		operations: NewOperations(deps),
		cancel:     cancel,
	}
}

// Init is the Bubbletea entry point. We forward to whichever sub-
// view is initially active so it can kick off any background work
// (the agent view, for instance, primes nothing here — it spawns the
// agent only on user request).
func (m App) Init() tea.Cmd {
	// Aggregate Init cmds from each sub-view. Some views may want to
	// schedule a tick or a stream-drain at boot; collecting them here
	// keeps the wiring honest.
	return tea.Batch(
		m.dashboard.Init(),
		m.snapshots.Init(),
		m.diff.Init(),
		m.agent.Init(),
		m.operations.Init(),
	)
}

// Update routes messages to the active sub-view. Top-level keys
// (view switch, quit, help) are intercepted before delegation so they
// work regardless of which view is focused. Window-size events are
// broadcast to all sub-views so panel widths stay consistent on
// resize without forcing a full rebuild.
func (m App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Forward the size to every sub-view so they can self-size.
		var cmds []tea.Cmd
		var c tea.Cmd
		m.dashboard, c = m.dashboard.Update(msg)
		cmds = append(cmds, c)
		m.snapshots, c = m.snapshots.Update(msg)
		cmds = append(cmds, c)
		m.diff, c = m.diff.Update(msg)
		cmds = append(cmds, c)
		m.agent, c = m.agent.Update(msg)
		cmds = append(cmds, c)
		m.operations, c = m.operations.Update(msg)
		cmds = append(cmds, c)
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		// Quit keys take precedence so a panicked sub-view can't
		// trap the user. Both `q` and Ctrl+C produce QuitMsg —
		// matches conventional terminal app expectations. Before
		// quitting, cancel any in-flight agent scan so the LLM call
		// doesn't outlive the TUI process.
		if msg.Type == tea.KeyCtrlC {
			m.cleanup()
			return m, tea.Quit
		}
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'q':
				m.cleanup()
				return m, tea.Quit
			case '?':
				m.help = !m.help
				return m, nil
			case 'd':
				m.active = ViewDashboard
				return m, nil
			case 's':
				m.active = ViewSnapshots
				return m, nil
			case 'D':
				m.active = ViewDiff
				return m, nil
			case 'a':
				m.active = ViewAgent
				return m, nil
			case 'o':
				m.active = ViewOperations
				return m, nil
			}
		}
	}

	// Anything else: delegate to the active view. Sub-models handle
	// their own arrow / enter / esc routing.
	return m.delegate(msg)
}

// delegate forwards msg to the currently-active sub-view and writes
// the returned model back into the App struct field by field. Sub-
// models in Bubbletea return tea.Model rather than their concrete
// type, so we re-store the interface value directly.
func (m App) delegate(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.active {
	case ViewDashboard:
		m.dashboard, cmd = m.dashboard.Update(msg)
	case ViewSnapshots:
		m.snapshots, cmd = m.snapshots.Update(msg)
	case ViewDiff:
		m.diff, cmd = m.diff.Update(msg)
	case ViewAgent:
		m.agent, cmd = m.agent.Update(msg)
	case ViewOperations:
		m.operations, cmd = m.operations.Update(msg)
	}
	return m, cmd
}

// View renders the full screen: top bar (brand + tabs + repo
// stats), the active sub-view body, and a bottom hint bar. The
// bottom bar is one of two strings — minimal hint when help is off,
// full key list when toggled on.
func (m App) View() string {
	top := m.renderTopBar()
	body := m.activeView()
	bottom := m.renderBottomBar()

	// JoinVertical with no styling — sub-views own their own borders
	// and padding (via ui.Panel etc.). This keeps the parent layout
	// dumb and lets each sub-view choose its own visual frame.
	return lipgloss.JoinVertical(lipgloss.Left, top, body, bottom)
}

// renderTopBar formats the brand + tab line + repo stats. We don't
// truncate based on width — terminals smaller than ~60 cols are out
// of scope for v1 and will line-wrap on the user's emulator.
func (m App) renderTopBar() string {
	brand := ui.Primary.Render("sentra")
	tabs := m.renderTabs()
	return brand + "  " + tabs
}

// renderTabs renders the four-view nav. The active view is rendered
// in Primary (the brand color); inactive tabs are Muted so they're
// visually backgrounded.
func (m App) renderTabs() string {
	tabSpec := []struct {
		key   string
		label string
		view  View
	}{
		{"d", "dashboard", ViewDashboard},
		{"s", "snapshots", ViewSnapshots},
		{"D", "diff", ViewDiff},
		{"a", "agent", ViewAgent},
		{"o", "operations", ViewOperations},
	}
	parts := make([]string, 0, len(tabSpec))
	for _, t := range tabSpec {
		label := "[" + t.key + "]" + t.label
		if t.view == m.active {
			parts = append(parts, ui.Primary.Render(label))
		} else {
			parts = append(parts, ui.Muted.Render(label))
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Left, joinSpaces(parts)...)
}

// activeView returns the rendered body of whichever sub-view is
// currently focused. A view returning the empty string still leaves
// a small body block — better than the screen jumping when a
// sub-view briefly has nothing to show.
func (m App) activeView() string {
	switch m.active {
	case ViewDashboard:
		return m.dashboard.View()
	case ViewSnapshots:
		return m.snapshots.View()
	case ViewDiff:
		return m.diff.View()
	case ViewAgent:
		return m.agent.View()
	case ViewOperations:
		return m.operations.View()
	}
	return ""
}

// renderBottomBar renders the global hint line. When help is toggled
// off (default), the bar lists the most-used keys: arrows / enter /
// esc. When toggled on, it lists every top-level key. Sub-views
// don't get to inject their own hints into this bar — that keeps the
// bottom row a stable screen anchor.
func (m App) renderBottomBar() string {
	if m.help {
		return ui.Subtle.Render(
			"d:dashboard  s:snapshots  D:diff  a:agent  o:operations  ?:help  q/^C:quit  ↑/↓:navigate  ⏎:select  esc:back",
		)
	}
	return ui.Subtle.Render("?:help  q:quit")
}

// cleanup releases resources held by sub-views and cancels the
// App-scoped context so any goroutine blocked on a deps.Ctx-derived
// blobstore call wakes up with ctx.Canceled rather than draining its
// per-call timeout. Idempotent — quit-and-cancel followed by another
// signal-driven cancel is a no-op the second time.
func (m App) cleanup() {
	if m.cancel != nil {
		m.cancel()
	}
	type cleaner interface{ Cleanup() }
	// AgentView.Cleanup is a value-receiver method, so the AgentView
	// value stored in m.agent's tea.Model interface satisfies the
	// cleaner interface directly. Sub-views without Cleanup are
	// quietly skipped.
	if c, ok := m.agent.(cleaner); ok {
		c.Cleanup()
	}
}

// joinSpaces inserts a two-space separator between elements. Used by
// the tab line. Pulled out into a helper so resizes don't have to
// recompute the formatter each call.
func joinSpaces(parts []string) []string {
	if len(parts) == 0 {
		return parts
	}
	out := make([]string, 0, 2*len(parts)-1)
	for i, p := range parts {
		if i > 0 {
			out = append(out, "  ")
		}
		out = append(out, p)
	}
	return out
}
