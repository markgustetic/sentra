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
	"time"

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

	// ShowSplash gates the launch splash. runUI sets it from the config's
	// ui.hide_splash; the zero value (false) keeps it off, so tests that build
	// a bare Deps{} render the normal frame.
	ShowSplash bool

	// Version and Commit identify the build on the splash. They are plain
	// display data, threaded from cmd/sentra. Commit may be the goreleaser
	// placeholder "none", in which case it is omitted from the rendered line.
	Version string
	Commit  string

	// preload is the App's one shared snapshot-list load, set by NewApp before
	// it constructs the views (see initialSnapshots). nil in tests that build a
	// view directly, which then load fresh — unchanged behavior.
	preload *snapshotPreload
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

// textCapturer is implemented by views whose current stage routes raw runes
// into a text input. While such a view holds content focus, the shell must not
// apply its single-rune global bindings (quit 'q', help '?', the number-key
// view jump) or 'tab' — those characters belong to the input. Only ctrl+c
// (handled before anything else) and the modal stack outrank it.
type textCapturer interface{ CapturesText() bool }

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

	// splashActive is true while the launch splash covers the frame. It is
	// seeded from Deps.ShowSplash and cleared by the tick or any keystroke.
	splashActive bool

	// splashFrame counts elapsed reveal frames. It is the animation's only
	// state: renderSplash derives every stage from it, so a frame is
	// reproducible and the view never reads the clock.
	splashFrame int

	// animFrame counts ambient chrome-animation ticks (the steady-state neon
	// breathe). Like splashFrame it is the animation's whole state, so a frame
	// is a pure function of it and never reads the clock. It advances from
	// launch via uiTick; the chrome reads it in View to color the title, the
	// focused border, and the active nav item.
	animFrame int

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

// NewApp constructs the shell with all 19 views — 18 navigable commands plus
// the unlock startup gate: the original read-only views (dashboard, snapshots,
// files, diff), the async-check views (check, doctor), the management views
// (recovery-kit, policies, schedule), the agent view (which now also hosts
// agent-apply in place, so it gets no separate id), the direct data operations
// (backup, restore, prune, sync, password), the "Settings" category (setup,
// settings), the Help directory of the other screens, and the unlock gate.
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

	// One shared snapshot-list load for every view that needs the list at
	// construction (dashboard, snapshots, diff, restore, prune) — five separate
	// ListSnapshots at launch collapse into one. Bounded so a slow store can't
	// stall startup; each view still falls back gracefully on error.
	if deps.Repo != nil {
		loadCtx, loadCancel := context.WithTimeout(ctx, 20*time.Second)
		var pre snapshotPreload
		pre.snaps, pre.err = deps.Repo.ListSnapshots(loadCtx)
		loadCancel()
		deps.preload = &pre
	}

	registry := NewRegistry()
	views := []viewEntry{
		{id: "dashboard", model: NewDashboard(deps)},
		// Backup sits directly under Dashboard, heading the rail: taking one is
		// the thing an operator reaches for most, so it comes before the
		// read-only views. The Category field still files it under "Operations"
		// in the palette; the rail renders registration order, not category groups.
		{id: "backup", model: NewBackupView(deps)},
		{id: "snapshots", model: NewSnapshots(deps)},
		{id: "files", model: NewFilesView(deps)},
		{id: "diff", model: NewDiff(deps)},
		{id: "check", model: NewCheckView(deps)},
		{id: "stats", model: NewStatsView(deps)},
		{id: "doctor", model: NewDoctorView(deps)},
		{id: "recovery-kit", model: NewRecoveryKitView(deps)},
		{id: "policies", model: NewPoliciesView(deps)},
		{id: "schedule", model: NewScheduleView(deps)},
		{id: "agent", model: NewAgentView(deps)},
		{id: "restore", model: NewRestoreView(deps)},
		{id: "prune", model: NewPruneView(deps)},
		{id: "sync", model: NewSyncView(deps)},
		{id: "password", model: NewPasswordView(deps)},
		{id: "unlock", model: NewUnlockView(deps)},
		{id: "settings", model: NewSettingsView(deps)},
		{id: "setup", model: NewSetupWizardView(deps)},
		// Help sits last so it renders at the BOTTOM of the rail: it is the
		// screen you reach for when you do not know which of the others you
		// want, not one you visit in the course of a backup.
		{id: "help", model: NewHelpView(registry)},
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
		registry.Add(Command{ID: v.id, Title: title, Category: cat,
			Description: viewDescriptions[v.id]})
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
		deps:         deps,
		registry:     registry,
		keys:         keys,
		views:        views,
		active:       active,
		focus:        focus,
		sidebar:      sidebar,
		palette:      NewPalette(registry, minWidth, minHeight),
		status:       NewStatusBar(keys, minWidth),
		ctx:          ctx,
		cancel:       cancel,
		splashActive: deps.ShowSplash,
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
	cmds := make([]tea.Cmd, 0, len(m.views)+2)
	for _, v := range m.views {
		cmds = append(cmds, v.model.Init())
	}
	if m.splashActive {
		cmds = append(cmds, splashTick())
	}
	// Kick the ambient chrome-animation clock. It re-arms itself each frame (see
	// uiFrameMsg in Update), so this single tick keeps the shell breathing for
	// the whole session.
	cmds = append(cmds, uiTick())
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

// The launch splash reveals itself in stages, then holds. Any keystroke
// dismisses it sooner, so splashDuration is a ceiling, not a wait.
//
// The reveal repaints once per splashFrameInterval; the hold that follows arms a
// single tick and draws nothing, so a still image costs no frames.
const (
	splashDuration      = 4200 * time.Millisecond
	splashFrameInterval = 60 * time.Millisecond

	// Reveal timeline, measured from the first frame.
	splashGlyphSettled = 200 * time.Millisecond  // ✦ finishes twinkling in
	splashLettersAt    = 300 * time.Millisecond  // first wordmark letter
	splashLetterStep   = 80 * time.Millisecond   // cadence between letters
	splashTaglineAt    = 1050 * time.Millisecond // both tagline lines
	splashRevealDone   = 1400 * time.Millisecond // version line appears

	// Animation cadence. Unlike the reveal timeline (one-shot), these drive the
	// living neon: the gradient flows down the wordmark, each letter flashes
	// white as it lands, and the glyph pulses. The splash keeps ticking for its
	// whole life so the motion never freezes.
	splashFlowStep    = 110 * time.Millisecond // gradient advances one row per step
	splashFlashDur    = 150 * time.Millisecond // a just-revealed line stays white this long
	splashGlyphPulse  = 130 * time.Millisecond // glyph color cadence
	splashShimmerStep = 45 * time.Millisecond  // tagline/version shimmer crest advances one cell
)

// splashFrameMsg advances the reveal by one frame.
type splashFrameMsg struct{}

// splashDoneMsg retires the launch splash when the hold expires.
type splashDoneMsg struct{}

// splashTick arms the next reveal frame.
func splashTick() tea.Cmd {
	return tea.Tick(splashFrameInterval, func(time.Time) tea.Msg { return splashFrameMsg{} })
}

// splashElapsed converts the frame counter into wall time. Rendering reads this
// rather than the clock so every frame is a pure function of splashFrame.
func (m App) splashElapsed() time.Duration {
	return time.Duration(m.splashFrame) * splashFrameInterval
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
	case splashFrameMsg:
		// A tick already in flight when the user skips must not resurrect the
		// animation, advance the frame, or arm another tick.
		if !m.splashActive {
			return m, nil
		}
		m.splashFrame++
		if m.splashElapsed() >= splashDuration {
			// Time's up — retire the splash. Unlike the old still-hold, we keep
			// ticking right up to the end so the neon flows and pulses for the
			// whole duration rather than freezing once the letters land.
			return m, func() tea.Msg { return splashDoneMsg{} }
		}
		return m, splashTick()

	case splashDoneMsg:
		m.splashActive = false
		return m, nil

	case uiFrameMsg:
		// Advance the ambient chrome clock and re-arm. This runs for the whole
		// session (chrome is hidden behind the splash/overlays but the counter
		// keeps ticking, so the breathe is already in motion when they clear).
		m.animFrame++
		return m, uiTick()

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
		// The splash is a launch moment, and the launch already had it. NewApp
		// re-seeds splashActive from ShowSplash and Init() re-arms the tick, so
		// leaving it set would cover the freshly unlocked dashboard a second
		// time and eat the user's next keystroke dismissing it.
		nd.ShowSplash = false
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
			if v.id != msg.id {
				continue
			}
			m.active = i
			m.sidebar.Select(msg.id)
			// Enter commits the rail's live preview: hand the keyboard to the
			// content pane so the user can act on the view they scrolled to.
			//
			// We deliberately do NOT gate this on "is this a different view than
			// m.active" — live rail preview (navPreviewMsg) already made the
			// highlighted view active during the scroll, so that guard saw the
			// previewed view as "already active" and silently swallowed Enter,
			// stranding the user on the rail. Instead gate on whether the view can
			// use focus at all: a passive readout (the Dashboard handles no keys)
			// keeps focus on the rail, since moving the focus border onto an inert
			// pane would only look like Enter did nothing — and the rail stays live
			// because the ↑/↓ fallback (see routeKey) drives it regardless of focus.
			// Every interactive view takes focus, restoring the pre-preview behavior.
			if m.contentFocusable(i) {
				m.focus = focusContent
			}
			break
		}
		return m.showActive()

	case navPreviewMsg:
		// Live rail scroll: show the highlighted view but keep focus on the rail
		// (the list already moved its own selection, so don't touch it) so the
		// user can keep scrolling through screens. showActive lets a lazy view
		// (Files) begin loading as it scrolls into view; eager views ignore it.
		for i, v := range m.views {
			if v.id == msg.id {
				m.active = i
				break
			}
		}
		return m.showActive()

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
		// Quit is the one action guarded by a confirmation: 'q' pops this modal
		// and only a confirmed result tears down and exits. ctrl+c stays an
		// unconditional force-quit, so a hung UI is never trapped.
		if msg.id == quitConfirmID {
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

// inStartupGate reports whether the App is sitting on a first-run/unlock
// gate: a setup or unlock view with no repo behind it. In that state every
// other view is dead (they all read from a repo that doesn't exist yet), so
// the shell hides its navigation chrome — the rail, the palette, and the
// number/tab jumps — and devotes the whole frame to the gate view.
func (m App) inStartupGate() bool {
	id := m.views[m.active].id
	return m.deps.Repo == nil && (id == "setup" || id == "unlock")
}

// contentCapturesText reports whether the content pane is focused on a view
// whose current stage is routing raw runes into a text input (see
// textCapturer). When true, the shell hands the keystroke to that view instead
// of applying its single-rune globals or the number-key view jump — otherwise
// no text field in the TUI could accept 'q', a digit, '?', or tab.
func (m App) contentCapturesText() bool {
	if m.focus != focusContent {
		return false
	}
	tc, ok := m.views[m.active].model.(textCapturer)
	return ok && tc.CapturesText()
}

// inertContent is implemented by a view that has nothing to interact with when
// its pane is focused — the Dashboard is a static readout whose Update ignores
// every key. Activating such a view from the rail must not move the focus border
// into its content pane; focus stays on the rail so scrolling keeps working.
//
// This exists because live rail preview (navPreviewMsg) makes the highlighted
// view active BEFORE Enter, so the activate handler can no longer distinguish
// "Enter on the view I scrolled to" (dive in) from "Enter on the passive
// Dashboard I launched on" (stay) by comparing indices — both have i == m.active.
// The one durable difference is the view itself, which it declares here. Every
// view that omits this method is focusable by default.
type inertContent interface{ InertContent() bool }

// contentFocusable reports whether activating view i should hand it the keyboard.
// Only a view that declares itself inert (the Dashboard) keeps focus on the rail.
func (m App) contentFocusable(i int) bool {
	ic, ok := m.views[i].model.(inertContent)
	return !ok || !ic.InertContent()
}

// arrowConsumer is implemented by a view that can use ↑/↓ in its CURRENT state:
// a table with rows, a cursor over entries, a scrollable viewport. It is
// state-dependent, not static — Snapshots consumes arrows only when it has
// snapshots and its detail page is closed.
//
// Views that never implement it (dashboard, check, doctor, prune, …) never take
// the arrows, and the shell hands those keys to the nav rail instead.
type arrowConsumer interface{ ConsumesArrows() bool }

// contentConsumesArrows reports whether the active view would do something with
// an up/down key right now.
func (m App) contentConsumesArrows() bool {
	ac, ok := m.views[m.active].model.(arrowConsumer)
	return ok && ac.ConsumesArrows()
}

// isArrowKey is up/down only. Left/right belong to the views that use them (the
// wizard's auth-method selector) and never navigate the rail.
func isArrowKey(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyUp || msg.Type == tea.KeyDown
}

// escapeConsumer is implemented by a view that means something by esc in its
// CURRENT state — cancel a running op, close the snapshot detail, step back a
// wizard stage. When the focused view does not, the shell takes esc and returns
// focus to the rail.
//
// Without this, a text field trapped the keyboard: on Backup's tag field and on
// Password, esc, tab and ctrl+p were all swallowed and only ctrl+c escaped,
// which quits the whole app.
type escapeConsumer interface{ ConsumesEscape() bool }

// contentConsumesEscape reports whether the active view would use esc right now.
func (m App) contentConsumesEscape() bool {
	ec, ok := m.views[m.active].model.(escapeConsumer)
	return ok && ec.ConsumesEscape()
}

// quitConfirmID tags the quit-confirmation modal so the confirmedMsg handler
// knows a confirmed result means "exit". It is the sole confirmation in the
// shell: esc steps back to the rail (or cancels a running op) without one.
const quitConfirmID = "confirm-quit"

// pushConfirmModal puts a y/n ConfirmModal on the stack sized to the terminal.
// enter emits confirmedMsg{id}; the modal's own esc dismisses it (see
// ConfirmModal.Update), so backing out is free.
func (m App) pushConfirmModal(title, body, id string) App {
	m.modals = append(m.modals, NewConfirmModal(title, body, id, m.width, m.height))
	return m
}

// advertisesKey reports whether any of the view's own hints already bind k, so
// the shell does not print the same hint a second time.
func advertisesKey(viewKeys []key.Binding, k string) bool {
	for _, b := range viewKeys {
		for _, bound := range b.Keys() {
			if bound == k {
				return true
			}
		}
	}
	return false
}

// tabConsumer is implemented by a view that moves focus BETWEEN its own controls
// with tab — Backup's folder picker and tag field. Without it the shell's focus
// toggle steals the key and the view's second control is unreachable.
//
// A view capturing text already receives tab (see textCapturer); this is for the
// views that need it while NOT capturing text.
type tabConsumer interface{ ConsumesTab() bool }

// contentConsumesTab reports whether the focused view uses tab internally.
func (m App) contentConsumesTab() bool {
	if m.focus != focusContent {
		return false
	}
	tc, ok := m.views[m.active].model.(tabConsumer)
	return ok && tc.ConsumesTab()
}

// statusGlobals is the set of shell keys that actually reach the shell right
// now. The bar must never promise a key the current state swallows: it used to
// offer "tab focus · ? help · q quit" while a passphrase was being typed, and
// not one of them worked.
func (m App) statusGlobals(viewKeys []key.Binding) []key.Binding {
	if m.inStartupGate() {
		// Every key routes into the gate view, so 'q' is typed into the unlock
		// passphrase field, not a quit. Only ctrl+c quits — advertise that.
		return []key.Binding{m.keys.ForceQuit}
	}
	var g []key.Binding
	// Offer esc only when the shell will act on it AND the view is not already
	// advertising it — Snapshots lists "esc back" itself, and the bar rendered
	// it twice.
	if m.focus == focusContent && !m.contentConsumesEscape() && !advertisesKey(viewKeys, "esc") {
		g = append(g, m.keys.Back)
	}
	g = append(g, m.keys.Palette)
	if m.contentCapturesText() {
		// 'q', '?' and tab all reach the text field instead of the shell.
		return append(g, m.keys.ForceQuit)
	}
	if !m.contentConsumesTab() {
		g = append(g, m.keys.Focus)
	}
	return append(g, m.keys.Help, m.keys.Quit)
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

	// The launch splash owns the first keystroke: any key dismisses it and the
	// key is consumed, so it never falls through to a view, modal, or nav
	// binding. ctrl+c (checked above) still quits.
	if m.splashActive {
		m.splashActive = false
		return m, nil
	}

	if n := len(m.modals); n > 0 {
		var cmd tea.Cmd
		m.modals[n-1], cmd = m.modals[n-1].Update(msg)
		return m, cmd
	}

	// The palette is an overlay: while open it owns the keyboard ahead of any
	// view, including one capturing text. It therefore sits ABOVE the
	// text-capture branch — below it, a palette opened over a text field could
	// never receive a keystroke.
	if m.paletteOpen {
		if msg.Type == tea.KeyEsc {
			m.paletteOpen = false
			return m, nil
		}
		var cmd tea.Cmd
		m.palette, cmd = m.palette.Update(msg)
		return m, cmd
	}

	// ctrl+p is a control chord no text field needs, so the palette opens even
	// while typing. A startup gate still swallows it: every other view is dead
	// behind a nil repo, so offering navigation there would strand the operator.
	if !m.inStartupGate() && key.Matches(msg, m.keys.Palette) {
		m.paletteOpen = true
		m.palette.Reset()
		return m, nil
	}

	// esc is the shell's escape hatch. A view that means something by it keeps
	// it — cancelling a running backup, closing the snapshot detail, stepping
	// back a wizard stage. Otherwise esc returns focus to the rail.
	//
	// This must sit ABOVE the text-capture branch, or a text field swallows it:
	// Backup's tag field and Password used to trap the keyboard entirely, with
	// ctrl+c (which quits the app) the only way out. Startup gates keep esc —
	// the wizard uses it to restart, and there is no rail to return to.
	if msg.Type == tea.KeyEsc && !m.inStartupGate() && m.focus == focusContent {
		switch {
		case m.opRunning != "":
			// esc cancels the running op in place — no confirm. The only guarded
			// action is quit; everything else steps back cheaply. ctrl+c still
			// force-quits if the operator wants out entirely.
			if m.opCancel != nil {
				m.opCancel()
			}
			return m, nil
		case m.contentConsumesEscape():
			// The view means something by esc itself — close a detail, step back
			// a wizard stage. Let it handle the key (fall through below).
		default:
			m.focus = focusSidebar
			return m, nil
		}
	}

	// A startup gate, or a content-focused view capturing text, owns the rest of
	// the keyboard. Route every key straight to the active view so a character
	// its textinput needs — a digit, 'q' or 'A' in a passphrase, '?', tab between
	// fields — is never swallowed by a nav binding or the single-rune quit. In a
	// gate this also suppresses the number/tab jumps, since every other view is
	// dead behind a nil repo.
	if m.inStartupGate() || m.contentCapturesText() {
		var cmd tea.Cmd
		m.views[m.active].model, cmd = m.views[m.active].model.Update(msg)
		return m, cmd
	}

	switch {
	case key.Matches(msg, m.keys.Focus):
		// A view that moves focus between its OWN controls with tab keeps the
		// key; esc is how the operator leaves such a view for the rail.
		if m.contentConsumesTab() {
			var cmd tea.Cmd
			m.views[m.active].model, cmd = m.views[m.active].model.Update(msg)
			return m, cmd
		}
		if m.focus == focusSidebar {
			m.focus = focusContent
		} else {
			m.focus = focusSidebar
		}
		return m, nil
	case key.Matches(msg, m.keys.Help):
		return m.pushHelpModal(), nil
	case key.Matches(msg, m.keys.Quit):
		// 'q' asks before quitting; only a confirmed result exits (see the
		// quitConfirmID branch in Update). ctrl+c remains an instant force-quit.
		return m.pushConfirmModal(
			"Quit sentra?",
			"You'll return to your shell. Any unsaved work on this screen is discarded.",
			quitConfirmID), nil
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

	// An arrow key must never do nothing. Eight views never use ↑/↓ at all, and
	// six more use them only when they have rows — and in those states the key
	// was silently dropped while the rail, no longer focused, had also stopped
	// responding. The app looked frozen, twice, to a real user.
	//
	// So: if the focused view cannot use this arrow, the rail takes it. Focus
	// follows, because otherwise the highlight would move in a pane that enter
	// does not target.
	if isArrowKey(msg) && !m.contentConsumesArrows() {
		m.focus = focusSidebar
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

// viewShownMsg tells the now-active view it is on screen. A view that defers
// heavy loading (e.g. Files, which fetches a whole manifest) hydrates lazily on
// first display instead of eagerly at startup; views that load in their
// constructor or Init simply ignore it.
type viewShownMsg struct{}

// showActive notifies the active view it is displayed, returning any load
// command it emits. Called whenever the App changes which view is active (rail
// commit or live preview).
func (m App) showActive() (tea.Model, tea.Cmd) {
	var c tea.Cmd
	m.views[m.active].model, c = m.views[m.active].model.Update(viewShownMsg{})
	return m, c
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
	// border (1 top + 1 bottom = 2). The header is titleRows(height) tall
	// — one line normally, the full synthwave banner on a tall terminal —
	// so with status bar (1) + panel border (2) the content budget is
	// msg.Height - titleRows - 3. The banner only engages above the common
	// test height, so at 80×20 titleRows is 1 and this stays msg.Height-4.
	//
	// View() sizes the panel to exactly contentW×contentH, so the frame
	// is (contentW+2) wide within the row and the whole row is pinned to
	// msg.Width. TestApp_NoOverflowAtMinSize checks this at 80×20: bump
	// the width budget (e.g. -3 → -1) and the panel block grows to 82,
	// overflowing, and the test fails.
	contentW := msg.Width - sidebarWidth - 3 // gap(1) + panel border(2); padding is inside Width
	if m.inStartupGate() {
		// A startup gate hides the rail (see View()), so the content fills the
		// full width and the only horizontal chrome is the panel border(2).
		// Drop the rail(sidebarWidth) + gap(1) from the budget.
		contentW = msg.Width - 2
	}
	contentH := msg.Height - titleRows(msg.Height) - 3 // header + status(1) + panel border(2)
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
// View renders the frame and paints the deep-space ground behind it (a no-op on
// non-truecolor terminals — see paintBackground). Splitting the composition into
// viewFrame keeps every early-return path (too-small, splash, modal, palette,
// normal) covered by one paint pass.
func (m App) View() string {
	return paintBackground(m.viewFrame())
}

// headerView renders the top-of-screen brand: the full synthwave banner when
// the terminal is tall enough (titleRows > 1), otherwise the one-line logo. The
// repo name is NOT repeated here — the status bar's left already carries it — so
// the header stays pure brand. Its row count MUST equal titleRows(m.height),
// which resize() reserves in the content budget, or the frame over/underflows.
//
// Both forms live off the ambient clock (animColor / the banner's frame are
// pure in animFrame, so they stay reproducible and vanish under Ascii): the
// one-line logo breathes its neon, and the banner's sun shimmers for a few
// rounds then settles (the wordmark and grid are static). Like the splash, all
// of it is color over fixed glyphs, so it survives the Ascii profile the tests
// render under.
func (m App) headerView() string {
	if titleRows(m.height) > 1 && m.width > 0 {
		return synthwaveBanner(m.width, m.animFrame)
	}
	logo := lipgloss.NewStyle().Foreground(animColor(animBrand, m.animFrame)).Bold(true).
		Render("✦  S E N T R A  ✦")
	if m.width > 0 {
		return lipgloss.Place(m.width, 1, lipgloss.Center, lipgloss.Center, logo)
	}
	return logo
}

func (m App) viewFrame() string {
	if m.width > 0 && (m.width < minWidth || m.height < minHeight) {
		hint := fmt.Sprintf("terminal too small (%dx%d)\nneed at least %dx%d",
			m.width, m.height, minWidth, minHeight)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			ui.Subtle.Render(hint))
	}
	if m.splashActive {
		return m.renderSplash()
	}
	if n := len(m.modals); n > 0 {
		return m.modals[n-1].View()
	}
	if m.paletteOpen {
		return m.palette.View()
	}

	title := m.headerView()

	body := m.views[m.active].model.View()
	contentStyle := ui.Panel.BorderForeground(animColor(animIdle, m.animFrame))
	if m.focus == focusContent {
		contentStyle = ui.PanelFocused.BorderForeground(animColor(animFocus, m.animFrame))
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

	// The row is the rail + gap + content, EXCEPT in a startup gate, where the
	// rail is hidden and the gate view owns the full width (see inStartupGate).
	// Pin the rail to sidebarWidth: bubbles/list renders each row at its
	// natural content width, so m.sidebar.View() comes back only as wide as its
	// longest label — leaving the layout width non-deterministic and, worse,
	// giving the content row so much slack that the resize budget stops being
	// the binding constraint (the whole point of the budget). Forcing the rail
	// to sidebarWidth makes the content row exactly sidebarWidth + gap(1) +
	// (contentW+2), so the budget is what determines overflow — see resize()
	// and TestApp_NoOverflowAtMinSize.
	row := content
	if !m.inStartupGate() {
		// withFrame breathes the active nav item's neon in step with the border.
		rail := lipgloss.NewStyle().Width(sidebarWidth).Render(m.sidebar.withFrame(m.animFrame).View())
		row = lipgloss.JoinHorizontal(lipgloss.Top, rail, " ", content)
	}

	var viewKeys []key.Binding
	if vh, ok := m.views[m.active].model.(viewShortHelper); ok {
		viewKeys = vh.ShortHelp()
	}
	// The bar only ever promises keys that reach the shell in the current state.
	bottom := m.status.ViewWith(m.deps.RepoName, viewKeys, m.statusGlobals(viewKeys), m.opRunning)

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

// renderSplash draws the centered launch lockup at the current reveal frame:
// the brand glyph twinkles in, the letter-spaced wordmark cascades left to
// right, then the tagline and the build identity. It is drawn into the full
// terminal rectangle; before the first WindowSizeMsg the dimensions are zero, so
// we fall back to the unplaced body.
//
// Every stage that has not appeared yet still occupies its exact final cells, as
// blanks. Hidden is not absent, and that matters in two different ways.
//
// lipgloss.Place centers each LINE independently, so a line's position is a
// function of its own width. Rendering only the revealed letters would grow the
// wordmark from one cell to sixteen and slide it leftward across the screen on
// every frame; padding unrevealed letters to spaces pins it. The blank tagline
// and version lines are the defensive half: they keep the line count and the
// un-placed body's geometry (the m.width == 0 path) invariant across frames.
func (m App) renderSplash() string {
	elapsed := m.splashElapsed()
	glyph := lipgloss.NewStyle().Foreground(splashGlyphColor(elapsed)).Bold(true).Background(splashBg)

	const (
		tagline1 = "Encrypted, deduplicated, agent-aware backups"
		tagline2 = "for S3-compatible storage"
	)

	// The block wordmark is 56 cells wide and the shell never renders below
	// minWidth (80) — the too-small guard catches narrower terminals first — so
	// it always fits. renderSplash is reached with m.width == 0 only before the
	// first WindowSizeMsg (and in the geometry test), where the big form is still
	// the right thing to measure.
	lines := []string{glyph.Render(splashGlyphAt(elapsed)), ""}
	lines = append(lines, splashBigWordmarkLines(elapsed)...)
	lines = append(lines, "")
	if elapsed >= splashTaglineAt {
		since := elapsed - splashTaglineAt
		lines = append(lines,
			splashTextLine(tagline1, elapsed, since),
			splashTextLine(tagline2, elapsed, since))
	} else {
		lines = append(lines, splashBlank(tagline1), splashBlank(tagline2))
	}
	if v := m.versionLine(); v != "" {
		lines = append(lines, "")
		if elapsed >= splashRevealDone {
			lines = append(lines, splashTextLine(v, elapsed, elapsed-splashRevealDone))
		} else {
			lines = append(lines, splashBlank(v))
		}
	}

	body := strings.Join(lines, "\n")
	if m.width == 0 || m.height == 0 {
		return body
	}
	// Fill the whole splash with the deep-space ground: WithWhitespaceBackground
	// paints every padded cell Place adds around the centered body, and each body
	// glyph already carries splashBg, so the frame reads as neon on deep space —
	// the mock's hero — with no default-bg gaps.
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body,
		lipgloss.WithWhitespaceBackground(splashBg))
}

// splashGlyphAt twinkles the brand glyph in: a point, a spark, then the star.
// Shape carries the animation rather than color, so it survives the Ascii color
// profile that unit tests and NO_COLOR terminals render under.
func splashGlyphAt(elapsed time.Duration) string {
	switch {
	case elapsed < splashGlyphSettled/3:
		return "·"
	case elapsed < 2*splashGlyphSettled/3:
		return "✧"
	default:
		return "✦"
	}
}

// splashBlank reserves exactly the cells a line will occupy once it appears,
// painted with the deep-space bg so a not-yet-revealed line reads as ground, not
// a default-bg strip. The width is unchanged, so geometry stays pinned.
func splashBlank(s string) string {
	return splashBgStyle.Render(strings.Repeat(" ", lipgloss.Width(s)))
}

// versionLine renders "version · shortcommit". The commit is dropped when it
// is empty or the goreleaser placeholder "none" (a plain `go build`), and it
// is truncated to the conventional 7 characters.
func (m App) versionLine() string {
	v := strings.TrimSpace(m.deps.Version)
	c := strings.TrimSpace(m.deps.Commit)
	if v == "" {
		return ""
	}
	if c == "" || c == "none" {
		return v
	}
	if len(c) > 7 {
		c = c[:7]
	}
	return v + " · " + c
}
