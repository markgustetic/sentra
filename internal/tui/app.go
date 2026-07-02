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
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
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

// viewEntry pairs a registered command ID with its model. Order is
// sidebar order.
type viewEntry struct {
	id    string
	model tea.Model
}

// focusArea tracks which region owns plain keystrokes.
type focusArea int

const (
	focusSidebar focusArea = iota
	focusContent
)

// minWidth/minHeight guard tiny terminals: below these we render a
// resize hint instead of a broken layout.
const (
	minWidth     = 80
	minHeight    = 20
	sidebarWidth = 18
)

// viewShortHelper is the optional part of the view contract; views
// without extra keys return nil.
type viewShortHelper interface{ ShortHelp() []key.Binding }

// App is the root model: layout (title bar, sidebar, content, status
// bar), focus, overlays (palette, modal stack), and the command
// registry that drives navigation.
type App struct {
	deps     Deps
	registry *Registry
	keys     globalKeymap

	views  []viewEntry
	active int
	focus  focusArea

	sidebar Sidebar
	palette Palette
	status  StatusBar

	paletteOpen bool
	modals      []Modal

	width  int
	height int

	cancel context.CancelFunc
}

// NewApp constructs the shell with the 5 v1 views registered. Deps
// semantics (nil-tolerant, cancellable ctx) are unchanged from the
// previous implementation.
func NewApp(deps Deps) App {
	parent := deps.Ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	deps.Ctx = ctx

	registry := NewRegistry()
	views := []viewEntry{
		{id: "dashboard", model: NewDashboard(deps)},
		{id: "snapshots", model: NewSnapshots(deps)},
		{id: "diff", model: NewDiff(deps)},
		{id: "agent", model: NewAgentView(deps)},
		{id: "operations", model: NewOperations(deps)},
	}
	for _, v := range views {
		title := v.id
		if t, ok := v.model.(interface{ Title() string }); ok {
			title = t.Title()
		}
		registry.Add(Command{ID: v.id, Title: title, Category: "Views"})
	}

	keys := newGlobalKeymap()
	return App{
		deps:     deps,
		registry: registry,
		keys:     keys,
		views:    views,
		active:   0,
		focus:    focusSidebar,
		sidebar:  NewSidebar(registry, sidebarWidth, minHeight),
		palette:  NewPalette(registry, minWidth, minHeight),
		status:   NewStatusBar(keys, minWidth),
		cancel:   cancel,
	}
}

func (m App) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.views))
	for _, v := range m.views {
		cmds = append(cmds, v.model.Init())
	}
	return tea.Batch(cmds...)
}

func (m App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.resize(msg), nil

	case badgeMsg:
		m.registry.SetBadge(msg.id, msg.badge)
		m.sidebar.Refresh()
		return m, nil

	case activateMsg:
		m.paletteOpen = false
		for i, v := range m.views {
			if v.id == msg.id {
				m.active = i
				m.sidebar.Select(msg.id)
				m.focus = focusContent
			}
		}
		return m, nil

	case dismissModalMsg:
		if n := len(m.modals); n > 0 {
			m.modals = m.modals[:n-1]
		}
		return m, nil

	case confirmedMsg:
		if n := len(m.modals); n > 0 {
			m.modals = m.modals[:n-1]
		}
		if msg.id == "confirm-quit" {
			m.cleanup()
			return m, tea.Quit
		}
		return m, nil

	case tea.KeyMsg:
		return m.routeKey(msg)
	}
	// Non-key messages (view data loads, agent stream) go to every
	// view: background loads must land even when the view isn't
	// focused.
	return m.broadcast(msg)
}

// routeKey implements the focus rules: modals first, palette second,
// then global bindings, then the focused region.
func (m App) routeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C always quits, even under overlays — a stuck modal must
	// never trap the terminal.
	if msg.Type == tea.KeyCtrlC {
		m.cleanup()
		return m, tea.Quit
	}

	if n := len(m.modals); n > 0 {
		var cmd tea.Cmd
		m.modals[n-1], cmd = m.modals[n-1].Update(msg)
		return m, cmd
	}

	if m.paletteOpen {
		if msg.Type == tea.KeyEsc {
			m.paletteOpen = false
			return m, nil
		}
		var cmd tea.Cmd
		m.palette, cmd = m.palette.Update(msg)
		return m, cmd
	}

	switch {
	case key.Matches(msg, m.keys.Palette):
		m.paletteOpen = true
		m.palette.Reset()
		return m, nil
	case key.Matches(msg, m.keys.Focus):
		if m.focus == focusSidebar {
			m.focus = focusContent
		} else {
			m.focus = focusSidebar
		}
		return m, nil
	case key.Matches(msg, m.keys.Quit):
		m.cleanup()
		return m, tea.Quit
	}

	// Number keys jump straight to the nth view.
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		if n := int(msg.Runes[0] - '1'); n >= 0 && n < len(m.views) {
			m.active = n
			m.sidebar.Select(m.views[n].id)
			m.focus = focusContent
			return m, nil
		}
	}

	if m.focus == focusSidebar {
		var cmd tea.Cmd
		m.sidebar, cmd = m.sidebar.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.views[m.active].model, cmd = m.views[m.active].model.Update(msg)
	return m, cmd
}

// broadcast forwards a non-key message to every view.
func (m App) broadcast(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmds := make([]tea.Cmd, 0, len(m.views))
	for i := range m.views {
		var c tea.Cmd
		m.views[i].model, c = m.views[i].model.Update(msg)
		cmds = append(cmds, c)
	}
	return m, tea.Batch(cmds...)
}

// resize recomputes layout regions and forwards content-pane sizes to
// views as a synthetic WindowSizeMsg so their existing size handling
// keeps working unchanged.
func (m App) resize(msg tea.WindowSizeMsg) App {
	m.width, m.height = msg.Width, msg.Height
	contentW := msg.Width - sidebarWidth - 3 // rail + border + gap
	contentH := msg.Height - 4               // title bar + status bar + borders
	if contentW < 1 {
		contentW = 1
	}
	if contentH < 1 {
		contentH = 1
	}
	m.sidebar.SetSize(sidebarWidth, contentH)
	m.palette.SetSize(msg.Width, msg.Height)
	m.status.SetWidth(msg.Width)
	for i := range m.modals {
		m.modals[i] = m.modals[i].SetSize(msg.Width, msg.Height)
	}
	inner := tea.WindowSizeMsg{Width: contentW, Height: contentH}
	for i := range m.views {
		m.views[i].model, _ = m.views[i].model.Update(inner)
	}
	return m
}

func (m App) View() string {
	if m.width > 0 && (m.width < minWidth || m.height < minHeight) {
		hint := fmt.Sprintf("terminal too small (%dx%d)\nneed at least %dx%d",
			m.width, m.height, minWidth, minHeight)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			ui.Subtle.Render(hint))
	}
	if n := len(m.modals); n > 0 {
		return m.modals[n-1].View()
	}
	if m.paletteOpen {
		return m.palette.View()
	}

	title := ui.TitleBar.Render("✦ sentra") + "  " +
		ui.Muted.Render(m.deps.RepoName)

	rail := m.sidebar.View()
	body := m.views[m.active].model.View()
	contentStyle := ui.Panel
	if m.focus == focusContent {
		contentStyle = ui.PanelFocused
	}
	content := contentStyle.Render(body)
	row := lipgloss.JoinHorizontal(lipgloss.Top, rail, " ", content)

	var viewKeys []key.Binding
	if vh, ok := m.views[m.active].model.(viewShortHelper); ok {
		viewKeys = vh.ShortHelp()
	}
	bottom := m.status.View(m.deps.RepoName, viewKeys, "")

	return lipgloss.JoinVertical(lipgloss.Left, title, row, bottom)
}

// cleanup cancels the app-scoped context and releases sub-view
// resources (unchanged semantics from the previous shell).
func (m App) cleanup() {
	if m.cancel != nil {
		m.cancel()
	}
	type cleaner interface{ Cleanup() }
	for _, v := range m.views {
		if c, ok := v.model.(cleaner); ok {
			c.Cleanup()
		}
	}
}
