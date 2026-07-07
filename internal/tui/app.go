// Package tui is the Bubbletea-based dashboard launched by `sentra ui`
// (and by bare `sentra` with no subcommand). It is deliberately a thin
// shell over the internal/repo and internal/agent packages: every view
// is a pure model that renders data the deps layer hands it, and never
// owns its own goroutines beyond the agent stream channel.
//
// The shell is a persistent sidebar rail beside a content pane, with
// a ctrl+p command palette, a status bar of contextual key hints, and
// a stack of modal overlays. Views register in a command Registry
// that drives both the rail and the palette, so adding a view is one
// registration rather than parallel edits to every chrome renderer.
// App owns all overlay and focus state: keys route modals-first, then
// palette, then global bindings, then the focused region — a view can
// never trap input, because the trapping layers live above it.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/setup"
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

	// Config is the resolved sentra configuration. Operation flows
	// read retention limits and walker options from it. May be nil
	// (tests, unconfigured installs) — flows must fall back to
	// config.Defaults() semantics when absent.
	Config *config.Config

	// Ctx is the parent context for all TUI-driven I/O. NewApp
	// derives a cancellable child from this and threads the child
	// back into every sub-view's Deps via DepsForChildren — so when
	// the user presses 'q' the App's cleanup cancels every in-flight
	// blobstore call. Nil falls back to context.Background() so tests
	// using `Deps{}` keep working.
	Ctx context.Context

	// ConfigPath is the absolute path to the sentra.yaml the TUI was
	// launched against. Flows that rewrite config (setup, policy edits,
	// schedule install, recovery kit) need the on-disk location to write
	// back to; it is plain data, never a resolved secret. Empty when the
	// TUI runs against an in-memory/unconfigured repo (tests).
	ConfigPath string

	// NewStore builds a blobstore.Store from an arbitrary config. The
	// sync flow uses it to open the *destination* store, which differs
	// from the repo's own source store, so we take a factory rather than
	// a live handle. It is a call-time function value — invoked only when
	// a flow runs — and resolves no secrets itself. May be nil in tests.
	NewStore func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)

	// Actions is the agent action registry (prune_snapshot, add_to_ignore,
	// flag_secret, none). The agent-apply flow looks up and runs a handler
	// through it after the user confirms a recommendation. Read-only by
	// default: nothing here executes without an explicit confirm. May be
	// nil (agent-apply then reports "no action registry configured").
	Actions *action.Registry

	// SaveKeyringPassphrase re-saves a rotated passphrase to the OS
	// keyring after the password flow changes it, so the user isn't
	// prompted on the next open. It is a call-time function value that
	// receives the new passphrase bytes only at rotation time — the bytes
	// are never retained in Deps. May be nil when no keyring is wired
	// (the password flow then skips the keyring update).
	SaveKeyringPassphrase func(cfg *config.Config, pass []byte) error

	// SetupEffects is the headless side-effecting seam the setup wizard
	// view drives (AWS CLI checks, login/SSO, bucket prep, repo init,
	// keyring save). Production wires it to setup.DefaultEffects(); tests
	// inject fakes. It holds no secrets — every method receives its inputs
	// at call time. May be nil when the wizard view isn't reachable (a
	// repo that's already configured and unlocked never needs it).
	SetupEffects setup.Effects

	// InitialView names the registered command the App should land on at
	// launch, instead of the dashboard. runUI sets it to "setup" for a
	// first-run (no sentra.yaml) and "unlock" for a configured-but-locked
	// repo. Empty (or an unknown id) starts on the dashboard. Plain routing
	// data, never a secret.
	InitialView string
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

	// contentW/contentH are the interior dimensions of the content
	// panel's *text* region — the terminal minus all chrome (sidebar,
	// gap, panel border+padding, title/status rows). resize() computes
	// them once and View() sizes the panel frame to them explicitly, so
	// the frame is budget-sized by construction rather than inheriting
	// whatever width the active view happened to render. That's what
	// makes the resize budget load-bearing: a size-ignoring view (e.g.
	// the Dashboard) can no longer mask an off-by-N budget bug.
	contentW int
	contentH int

	// opRunning names the in-flight operation ("" when idle); opCancel
	// cancels its context. One mutating operation at a time — the
	// repo's advisory lock would reject a second anyway; failing fast
	// here gives the user a clear modal instead of a lock error.
	opRunning string
	opCancel  context.CancelFunc

	ctx    context.Context
	cancel context.CancelFunc
}

// NewApp constructs the shell with all 17 Phase 3 views registered:
// the original read-only views (dashboard, snapshots, diff), the
// async-check views (check, doctor), the management views
// (recovery-kit, policies, schedule), the agent view (which now also
// hosts agent-apply in place, so it gets no separate id), the direct
// data operations (backup, restore, prune, sync, password), the
// "Settings" category (setup, settings), and the unlock gate.
//
// unlock is a startup gate, not a navigable operation: it is present
// in the views slice (so InitialView can land on it) but excluded from
// the command registry, so it never appears in the sidebar or palette.
// Deps semantics (nil-tolerant, cancellable ctx) are unchanged from
// the previous implementation.
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
		{id: "check", model: NewCheckView(deps)},
		{id: "doctor", model: NewDoctorView(deps)},
		{id: "recovery-kit", model: NewRecoveryKitView(deps)},
		{id: "policies", model: NewPoliciesView(deps)},
		{id: "schedule", model: NewScheduleView(deps)},
		{id: "agent", model: NewAgentView(deps)},
		{id: "backup", model: NewBackupView(deps)},
		{id: "restore", model: NewRestoreView(deps)},
		{id: "prune", model: NewPruneView(deps)},
		{id: "sync", model: NewSyncView(deps)},
		{id: "password", model: NewPasswordView(deps)},
		{id: "unlock", model: NewUnlockView(deps)},
		{id: "settings", model: NewSettingsView(deps)},
		{id: "setup", model: NewSetupWizardView(deps)},
	}
	// The direct data operations form the "Operations" category in the
	// rail and palette; every read-only/management view defaults to
	// "Views". Policies carries destructive add/remove/run actions and
	// was registered under Operations back in Part 2 — kept here for
	// consistency with that earlier decision.
	categories := map[string]string{
		"backup": "Operations", "restore": "Operations", "prune": "Operations",
		"sync": "Operations", "password": "Operations", "policies": "Operations",
		"settings": "Settings", "setup": "Settings",
	}
	// hiddenFromRail lists view ids that are reachable only via InitialView
	// routing (startup gates), never from the sidebar/palette. unlock is a
	// login screen, not a navigable operation, so it must not clutter the
	// rail or the command palette.
	hiddenFromRail := map[string]bool{"unlock": true}
	for _, v := range views {
		if hiddenFromRail[v.id] {
			continue // startup gate — renderable via InitialView, not navigable
		}
		title := v.id
		if t, ok := v.model.(interface{ Title() string }); ok {
			title = t.Title()
		}
		cat := categories[v.id]
		if cat == "" {
			cat = "Views"
		}
		registry.Add(Command{ID: v.id, Title: title, Category: cat})
	}

	keys := newGlobalKeymap()

	// InitialView lets runUI land the App on a non-dashboard view (the
	// first-run wizard or the unlock gate). An empty or unknown id leaves
	// active at 0, so the dashboard stays the default landing screen.
	active := 0
	if deps.InitialView != "" {
		for i, v := range views {
			if v.id == deps.InitialView {
				active = i
				break
			}
		}
	}
	// Landing on a non-dashboard view (wizard/unlock) focuses the content
	// pane so keystrokes reach it immediately; the default dashboard landing
	// keeps focus on the sidebar rail.
	focus := focusSidebar
	if active != 0 {
		focus = focusContent
	}
	sidebar := NewSidebar(registry, sidebarWidth, minHeight)
	if !hiddenFromRail[views[active].id] {
		sidebar.Select(views[active].id) // don't select a hidden startup gate
	}

	return App{
		deps:     deps,
		registry: registry,
		keys:     keys,
		views:    views,
		active:   active,
		focus:    focus,
		sidebar:  sidebar,
		palette:  NewPalette(registry, minWidth, minHeight),
		status:   NewStatusBar(keys, minWidth),
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Deps returns the App's dependency set. Exported for cross-package
// wiring tests (internal/cli) that need to assert runUI threaded the
// right values in; production code inside the tui package reads the
// unexported field directly.
func (m App) Deps() Deps { return m.deps }

// appCtx returns the App-scoped context operations derive from.
func (m App) appCtx() context.Context {
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

// Init batches every view's Init at once, not just the active view's,
// so a view the user hasn't visited yet is already initialized by the
// time they switch to it.
//
// Note: views still hydrate their data *synchronously* during NewApp
// (e.g. NewSnapshots / NewRestoreView / NewPruneView do a blocking
// ListSnapshots before the first frame), so these Init cmds are
// effectively no-ops today — the loading isn't deferred. Moving that
// hydration onto async, non-blocking tea.Cmds is a future item; when it
// lands, this batching starts carrying real background loads.
func (m App) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.views))
	for _, v := range m.views {
		cmds = append(cmds, v.model.Init())
	}
	return tea.Batch(cmds...)
}

// repoReadyMsg is emitted by the unlock flow once it has opened the repository
// with a verified passphrase. The App rebuilds its views against the now-live
// repo (they were constructed with a nil Repo on the launch path) and switches
// to the dashboard, so the configured-but-locked landing screen is replaced by
// the real dashboard exactly once, at unlock time.
type repoReadyMsg struct {
	repo   *repo.Repo
	config *config.Config
}

// Update handles shell-owned messages (size, navigation, modal
// results) here and splits the rest by kind: key messages go through
// routeKey's focus rules so exactly one region sees each keystroke,
// while everything else is broadcast to all views — a data load must
// land even when its view isn't focused.
func (m App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Terminal operation results clear the guard regardless of type.
	if res, ok := msg.(opResultMsg); ok {
		_ = res
		m.opRunning = ""
		if m.opCancel != nil {
			m.opCancel()
			m.opCancel = nil
		}
		return m.broadcast(msg)
	}

	switch msg := msg.(type) {
	case repoReadyMsg:
		// Rebuild the whole shell against the unlocked repo. Reusing NewApp
		// keeps view registration in one place (it changes as views are
		// added) rather than duplicating the slice here. We carry over the
		// resolved config, drop the InitialView so the rebuilt App lands on
		// the dashboard, and replay the last WindowSizeMsg so layout is
		// identical to a normal launch.
		nd := m.deps
		nd.Repo = msg.repo
		if msg.config != nil {
			nd.Config = msg.config
		}
		nd.InitialView = ""
		rebuilt := NewApp(nd)
		if m.width > 0 {
			sized, _ := rebuilt.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
			rebuilt = sized.(App)
		}
		return rebuilt, rebuilt.Init()

	case tea.WindowSizeMsg:
		return m.resize(msg), nil

	case startOpMsg:
		if m.opRunning != "" {
			m.modals = append(m.modals, NewErrorModal(
				fmt.Errorf("%s is already in progress", m.opRunning),
				"One operation at a time: wait for it to finish or cancel it with esc.",
				m.width, m.height))
			// Tell the rejected flow so it leaves its optimistic running
			// stage instead of hanging there forever.
			name := msg.name
			return m, func() tea.Msg { return opRejectedMsg{name: name} }
		}
		opCtx, cancel := context.WithCancel(m.appCtx())
		m.opRunning = msg.name
		m.opCancel = cancel
		run := msg.run
		return m, func() tea.Msg { return run(opCtx) }

	case opRejectedMsg:
		// Not an opResultMsg (no guard to clear) — just route it to the
		// flows so the rejected one resets.
		return m.broadcast(msg)

	case cancelOpMsg:
		if m.opCancel != nil {
			m.opCancel()
		}
		return m, nil

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
				break
			}
		}
		return m, nil

	case pushModalMsg:
		m.modals = append(m.modals, msg.modal.SetSize(m.width, m.height))
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
		// The "confirm-quit" branch is unreachable in Phase 1: nothing
		// pushes a quit-confirm modal yet (quit is unconditional). It's
		// kept wired because Phase 2's operation guard will push exactly
		// this modal — "quit while a backup is running?" — and route the
		// confirmed result back here to tear down and exit.
		if msg.id == "confirm-quit" {
			m.cleanup()
			return m, tea.Quit
		}
		// Every other confirmation belongs to a flow (e.g. prune's typed
		// "prune" gate): forward it to every view so the owning flow can
		// act on its id. Without this, popping the modal here silently
		// discards the confirmation — the flow that pushed the modal
		// never learns the user confirmed, and a destructive op that
		// should have started never does.
		return m.broadcast(msg)

	case tea.KeyMsg:
		return m.routeKey(msg)
	}
	// Non-key messages (view data loads, agent stream) go to every
	// view: background loads must land even when the view isn't
	// focused.
	return m.broadcast(msg)
}

// tooSmall reports whether the terminal is below the minimum layout
// size. It mirrors View()'s guard condition: width == 0 means no
// WindowSizeMsg has arrived yet (headless / first frame), which we treat
// as "not too small" so tests and the pre-size frame route keys
// normally.
func (m App) tooSmall() bool {
	return m.width > 0 && (m.width < minWidth || m.height < minHeight)
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

	// Below the minimum size, View() shows the resize-hint guard and
	// hides every overlay. But an overlay left open (e.g. a palette the
	// user opened before shrinking the terminal) is still live in state,
	// so without this gate its Update would keep eating keystrokes the
	// user can't see the effect of ('q' typed into an invisible palette
	// instead of quitting). While too small, only quit keys act; we
	// deliberately do NOT close or clear the overlays — they reappear
	// intact when the terminal is resized back up.
	if m.tooSmall() {
		if key.Matches(msg, m.keys.Quit) {
			m.cleanup()
			return m, tea.Quit
		}
		return m, nil
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
	case key.Matches(msg, m.keys.Help):
		return m.pushHelpModal(), nil
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

// pushHelpModal builds the `?` key-reference modal from the live
// keymap and the active view's ShortHelp, rather than a hardcoded
// string — the review caught the status bar advertising "?" while
// nothing handled it, and deriving the text from the bindings keeps
// the help from drifting the same way.
func (m App) pushHelpModal() App {
	var b strings.Builder
	writeBinding := func(kb key.Binding) {
		fmt.Fprintf(&b, "%-8s %s\n", kb.Help().Key, kb.Help().Desc)
	}
	for _, kb := range m.keys.shortHelp() {
		writeBinding(kb)
	}
	if vh, ok := m.views[m.active].model.(viewShortHelper); ok {
		for _, kb := range vh.ShortHelp() {
			writeBinding(kb)
		}
	}
	w, h := m.width, m.height
	if w == 0 || h == 0 {
		// No WindowSizeMsg yet (headless tests): fall back to the
		// minimum-supported size so the modal still centers sanely.
		w, h = minWidth, minHeight
	}
	body := strings.TrimRight(b.String(), "\n")
	m.modals = append(m.modals, NewInfoModal("Keys", body, w, h))
	return m
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
	// Horizontal chrome around the content text: the sidebar rail
	// (sidebarWidth), the 1-col gap View joins between rail and panel,
	// and ui.Panel's border. In this lipgloss version Style.Width sets
	// the content+padding box and adds only the border (1 each side)
	// outside it — the Padding(0,1) lives *inside* the Width we pass —
	// so the rendered panel block is exactly contentW+2 wide, and the
	// whole content row is sidebarWidth + gap(1) + (contentW+2). For an
	// exact fit to the terminal that must equal msg.Width, hence the
	// budget below is sidebarWidth + gap(1) + border(2) = -3, not -5.
	// (The prior -5 under-filled by 2 columns; harmless visually but it
	// meant the budget wasn't the binding constraint, so the overflow
	// test couldn't detect drift.)
	//
	// Vertical: Style.Height sets content+padding rows and adds the
	// border (1 top + 1 bottom = 2). With title bar (1) + status bar (1)
	// + panel border (2) that's msg.Height - 4.
	//
	// View() sizes the panel to exactly contentW×contentH, so the frame
	// is (contentW+2) wide within the row and the whole row is pinned to
	// msg.Width. TestApp_NoOverflowAtMinSize checks this at 80×20: bump
	// the width budget (e.g. -3 → -1) and the panel block grows to 82,
	// overflowing, and the test fails.
	contentW := msg.Width - sidebarWidth - 3 // gap(1) + panel border(2); padding is inside Width
	contentH := msg.Height - 4               // title(1) + status(1) + panel border(2)
	if contentW < 1 {
		contentW = 1
	}
	if contentH < 1 {
		contentH = 1
	}
	m.contentW, m.contentH = contentW, contentH
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

// View renders the topmost overlay when one is up (modal stack, then
// palette), otherwise the standard chrome: title bar, rail+content
// row, status bar. Overlays replace the whole frame instead of
// compositing over it — lipgloss has no z-axis, and lipgloss.Place
// gives the visual effect of a centered dialog without a hand-rolled
// cell-level blitter. The too-small guard comes first so a shrunken
// terminal shows a resize hint rather than a mangled layout.
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

	// Pin the rail to sidebarWidth. bubbles/list renders each row at its
	// natural content width, so m.sidebar.View() comes back only as wide
	// as its longest label — leaving the layout width non-deterministic
	// and, worse, giving the content row so much slack that the resize
	// budget stops being the binding constraint (the whole point of the
	// budget). Forcing the rail to sidebarWidth makes the content row
	// exactly sidebarWidth + gap(1) + (contentW+2), so the budget is what
	// determines overflow — see resize() and TestApp_NoOverflowAtMinSize.
	rail := lipgloss.NewStyle().Width(sidebarWidth).Render(m.sidebar.View())
	body := m.views[m.active].model.View()
	contentStyle := ui.Panel
	if m.focus == focusContent {
		contentStyle = ui.PanelFocused
	}
	// Size the panel's text region to the resize budget explicitly.
	// Width/Height fix the box at contentW×contentH (padding inside,
	// border outside), so the panel no longer inherits whatever width
	// the active view rendered — a size-ignoring view (e.g. the
	// Dashboard) can't mask an off-by-N budget, and the rendered block
	// is exactly (contentW+2)×(contentH+2). We deliberately do NOT clamp
	// with MaxWidth: it composes with the border oddly and would pin the
	// frame regardless of the budget, defeating the load-bearing
	// property the resize arithmetic is supposed to have. Views render
	// within the inner size they were handed via the synthetic
	// WindowSizeMsg; TestApp_NoOverflowAtMinSize pins the whole frame to
	// 80×20 as the backstop.
	content := contentStyle.
		Width(m.contentW).Height(m.contentH).
		Render(body)
	row := lipgloss.JoinHorizontal(lipgloss.Top, rail, " ", content)

	var viewKeys []key.Binding
	if vh, ok := m.views[m.active].model.(viewShortHelper); ok {
		viewKeys = vh.ShortHelp()
	}
	bottom := m.status.View(m.deps.RepoName, viewKeys, m.opRunning)

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
