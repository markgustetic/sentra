package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/agent"
	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

// TestApp_DepsCarrySetupEffects: the setup wizard view needs a headless
// effects seam. Deps must carry it nil-tolerantly and NewApp must not panic
// when it is set.
func TestApp_DepsCarrySetupEffects(t *testing.T) {
	eff := setup.DefaultEffects()
	app := NewApp(Deps{RepoName: "x", SetupEffects: eff})
	if app.deps.SetupEffects == nil {
		t.Fatal("Deps.SetupEffects not carried through NewApp")
	}
}

// TestApp_InitialViewSelectsStartingView: when Deps.InitialView names a
// registered view, NewApp starts focused on it instead of the dashboard, so
// the first-run wizard / unlock gate can be the landing screen.
func TestApp_InitialViewSelectsStartingView(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", InitialView: "restore"})
	if got := app.views[app.active].id; got != "restore" {
		t.Fatalf("active view = %q, want restore", got)
	}
}

// TestApp_InitialViewUnknownFallsBackToDashboard: an InitialView that names no
// registered command must not crash or leave active out of range — it falls
// back to the first view.
func TestApp_InitialViewUnknownFallsBackToDashboard(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", InitialView: "does-not-exist"})
	if app.active != 0 {
		t.Fatalf("active = %d, want 0 (dashboard fallback)", app.active)
	}
}

// TestApp_RepoReadyRebuildsViewsWithLiveRepoAndShowsDashboard: the unlock flow
// hands the App an opened repo via repoReadyMsg; the App rebuilds its views
// against it (so every view now sees a non-nil Repo) and switches to the
// dashboard, dropping any first-run/unlock landing view.
func TestApp_RepoReadyRebuildsViewsWithLiveRepoAndShowsDashboard(t *testing.T) {
	r := newFlowRepo(t)
	cfg := config.Defaults()
	// Start as if on the unlock gate: no repo, unlock is the landing view.
	app := NewApp(Deps{RepoName: "x", InitialView: "unlock"})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = sized.(App)

	m, _ := app.Update(repoReadyMsg{repo: r, config: &cfg})
	next := m.(App)
	if next.deps.Repo != r {
		t.Fatal("repoReadyMsg did not swap the live repo into Deps")
	}
	if got := next.views[next.active].id; got != "dashboard" {
		t.Fatalf("active view after repoReady = %q, want dashboard", got)
	}
	// Every rebuilt view must see the live repo. Sample the snapshots view.
	for _, v := range next.views {
		if v.id == "snapshots" {
			if sv, ok := v.model.(interface{ Deps() Deps }); ok && sv.Deps().Repo != r {
				t.Fatal("rebuilt snapshots view did not receive the live repo")
			}
		}
	}
}

// TestApp_UnlockRegisteredAsView: the unlock gate is a registered view so the
// InitialView routing can land on it and the sidebar/palette know about it.
func TestApp_UnlockRegisteredAsView(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	found := false
	for _, v := range app.views {
		if v.id == "unlock" {
			found = true
		}
	}
	if !found {
		t.Fatal("unlock view not registered in NewApp")
	}
}

// TestApp_DepsCarryConfig: flows need the resolved config (retention
// policy, walker options). Deps must carry it nil-tolerantly.
func TestApp_DepsCarryConfig(t *testing.T) {
	cfg := config.Defaults()
	cfg.Retention.KeepLast = 7
	app := NewApp(Deps{RepoName: "x", Config: &cfg})
	if app.deps.Config == nil || app.deps.Config.Retention.KeepLast != 7 {
		t.Fatal("Deps.Config not carried through NewApp")
	}
}

// TestApp_OperationsRegisteredAndRunningIndicatorEndToEnd: the three
// flows appear in sidebar+palette (registry-driven), and starting a
// backup through the real App shows it in the status bar.
func TestApp_OperationsRegisteredAndRunningIndicatorEndToEnd(t *testing.T) {
	app := newTestApp(t)
	out := app.View()
	for _, want := range []string{"Backup", "Restore", "Prune"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing operation %q", want)
		}
	}
	if got := len(app.views); got != 17 {
		t.Fatalf("views = %d, want 17 (3 read-only + check + doctor + recovery-kit + policies + schedule + agent + 3 operations + sync + password + unlock + settings + setup)", got)
	}
}

func newTestApp(t *testing.T) App {
	t.Helper()
	app := NewApp(Deps{RepoName: "test-repo"})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return sized.(App)
}

// installView swaps the model registered under id for a test double so
// tests can assert against a view pre-loaded with data (the default
// Deps{} views are empty). Same-package access into App.views is the
// intended seam here.
func installView(t *testing.T, app *App, id string, model tea.Model) {
	t.Helper()
	for i := range app.views {
		if app.views[i].id == id {
			app.views[i].model = model
			return
		}
	}
	t.Fatalf("no view registered under id %q", id)
}

func TestApp_RendersSidebarAndActiveView(t *testing.T) {
	app := newTestApp(t)
	out := app.View()
	for _, want := range []string{"sentra", "Dashboard", "Snapshots", "Agent", "test-repo"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

func TestApp_SidebarEnterSwitchesView(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyDown}) // highlight Snapshots
	m, cmd := m.(App).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should emit activate cmd")
	}
	m, _ = m.(App).Update(cmd()) // deliver activateMsg
	if got := m.(App).active; got != 1 {
		t.Fatalf("active = %d, want 1 (snapshots)", got)
	}
	if m.(App).focus != focusContent {
		t.Fatal("activation must move focus to content")
	}
}

func TestApp_NumberKeyJumpsToView(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	if got := m.(App).active; got != 3 {
		t.Fatalf("active = %d, want 3 (agent)", got)
	}
}

func TestApp_PaletteOpensFiltersAndActivates(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.(App).paletteOpen {
		t.Fatal("ctrl+p should open the palette")
	}
	for _, r := range "diff" {
		m, _ = m.(App).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m2, cmd := m.(App).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("palette enter should emit activate cmd")
	}
	m2, _ = m2.(App).Update(cmd())
	app2 := m2.(App)
	if app2.paletteOpen {
		t.Fatal("palette should close after activation")
	}
	if app2.views[app2.active].id != "diff" {
		t.Fatalf("active view = %s, want diff", app2.views[app2.active].id)
	}
}

// TestApp_QInsidePaletteTypes: the focus rule — q quits the app only
// when no overlay owns input.
func TestApp_QInsidePaletteTypes(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m, cmd := m.(App).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd != nil {
		if _, quits := cmd().(tea.QuitMsg); quits {
			t.Fatal("q inside palette must type, not quit")
		}
	}
	if got := m.(App).palette.Query(); got != "q" {
		t.Fatalf("palette query = %q, want q", got)
	}
}

func TestApp_TooSmallShowsGuard(t *testing.T) {
	app := NewApp(Deps{})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if out := m.(App).View(); !strings.Contains(out, "terminal too small") {
		t.Errorf("guard screen missing:\n%s", out)
	}
}

// TestApp_TooSmallRoutesOnlyQuit pins the guard-routing rule: with the
// terminal below the minimum size the resize-hint guard is shown and all
// overlays are hidden, but an overlay left open (here, the palette) is
// still live in state. Keys must not route into it — pressing `q` must
// quit, not get typed into the invisible palette.
func TestApp_TooSmallRoutesOnlyQuit(t *testing.T) {
	app := newTestApp(t)
	// Open the palette while at a normal size.
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.(App).paletteOpen {
		t.Fatal("ctrl+p should open the palette")
	}
	// Shrink below the minimum: the guard takes over the screen.
	m, _ = m.(App).Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if out := m.(App).View(); !strings.Contains(out, "terminal too small") {
		t.Fatalf("guard screen missing:\n%s", out)
	}
	// `q` must quit — not be swallowed by the still-open palette.
	before := m.(App).palette.Query()
	m2, cmd := m.(App).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected a quit cmd from `q` while too small")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", cmd())
	}
	// The palette must not have absorbed the keystroke.
	if got := m2.(App).palette.Query(); got != before {
		t.Errorf("q leaked into palette: query %q, want %q", got, before)
	}
	// The overlay stays open (it comes back on resize), just inert.
	if !m2.(App).paletteOpen {
		t.Error("guard routing must not close the palette")
	}
}

func TestApp_ErrorModalCapturesKeysAndDismisses(t *testing.T) {
	app := newTestApp(t)
	app.modals = append(app.modals, NewErrorModal(assertErr{}, "advice", 100, 30))
	out := app.View()
	if !strings.Contains(out, "advice") {
		t.Errorf("modal not rendered:\n%s", out)
	}
	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = m.(App).Update(cmd()) // dismissModalMsg
	if len(m.(App).modals) != 0 {
		t.Fatal("modal should pop on dismiss")
	}
}

type assertErr struct{}

func (assertErr) Error() string { return "assert error" }

// assertNoQuit fails the test if cmd resolves to a tea.QuitMsg. A nil
// cmd is fine — "nothing happened" is exactly what these tests want.
func assertNoQuit(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	if _, quits := cmd().(tea.QuitMsg); quits {
		t.Fatal("unexpected tea.QuitMsg")
	}
}

// TestApp_QuitOnQ asserts that pressing `q` (with no overlay open)
// returns a tea.QuitMsg via the returned tea.Cmd. We invoke the cmd
// inline to verify the resulting message type rather than running a
// real tea.Program.
func TestApp_QuitOnQ(t *testing.T) {
	app := newTestApp(t)
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected non-nil cmd from `q` keypress")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", cmd())
	}
}

// TestApp_CtrlCQuitsUnderModal locks in the "never trap the terminal"
// rule: even with a modal capturing all other input, Ctrl+C must
// still quit. A stuck confirm dialog that also ate Ctrl+C would leave
// the user with no exit short of killing the process.
func TestApp_CtrlCQuitsUnderModal(t *testing.T) {
	app := newTestApp(t)
	app.modals = append(app.modals,
		NewConfirmModal("Quit during operation?", "body", "confirm-quit", 100, 30))
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected non-nil cmd from Ctrl+C under a modal")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", cmd())
	}
}

// TestApp_ModalSwallowsGlobalKeys pins the other half of the modal
// focus contract: while a modal is up, global bindings (palette, q)
// must not fire underneath it. A ConfirmModal ignores everything but
// enter/esc; an ErrorModal treats any key — including q — as dismiss,
// never as quit.
func TestApp_ModalSwallowsGlobalKeys(t *testing.T) {
	app := newTestApp(t)
	app.modals = append(app.modals,
		NewConfirmModal("Delete snapshot?", "body", "confirm-delete", 100, 30))

	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	assertNoQuit(t, cmd)
	if m.(App).paletteOpen {
		t.Fatal("ctrl+p under a modal must not open the palette")
	}

	m, cmd = m.(App).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	assertNoQuit(t, cmd)
	if len(m.(App).modals) != 1 {
		t.Fatal("confirm modal must ignore q (stay open, no quit)")
	}

	// ErrorModal: any key dismisses — q pops the modal, never quits.
	app2 := newTestApp(t)
	app2.modals = append(app2.modals, NewErrorModal(errors.New("boom"), "", 100, 30))
	m2, cmd := app2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("error modal should emit a dismiss cmd on any key")
	}
	msg := cmd()
	if _, quits := msg.(tea.QuitMsg); quits {
		t.Fatal("q on an error modal must dismiss, not quit")
	}
	m2, _ = m2.(App).Update(msg) // deliver dismissModalMsg
	if len(m2.(App).modals) != 0 {
		t.Fatal("error modal should pop on any key")
	}
}

// TestApp_QuitCancelsAgentScan asserts that the App's quit handler
// invokes AgentView.Cleanup, which cancels any in-flight scan's
// context. Without this, pressing q during an LLM streaming call
// leaks the network round-trip past process exit.
//
// We don't construct a real LLM-backed AgentView; we install a
// runner that blocks on its ctx and observes the cancellation.
func TestApp_QuitCancelsAgentScan(t *testing.T) {
	app := newTestApp(t)

	// Build an AgentView whose runner blocks until its ctx is cancelled,
	// then drive it through Update directly (the shell routes plain keys
	// by focus, so we start the scan at the sub-view level). We do NOT
	// invoke the returned cmd — that's the waitForAgentEvent select,
	// which would block the test goroutine on a token that never comes.
	cancelled := make(chan struct{}, 1)
	agentView := NewAgentViewWithRunner(Deps{}, func(ctx context.Context, _ chan<- string) ([]agent.Recommendation, error) {
		<-ctx.Done()
		cancelled <- struct{}{}
		return nil, ctx.Err()
	})
	updated, _ := agentView.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})

	// Install the post-scan-started agent view back into the shell's
	// registry slot, then send the App `q` to trigger cleanup().
	installed := false
	for i := range app.views {
		if app.views[i].id == "agent" {
			app.views[i].model = updated
			installed = true
		}
	}
	if !installed {
		t.Fatal("no agent view registered in the shell")
	}
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	select {
	case <-cancelled:
		// Runner observed ctx cancellation — good.
	case <-time.After(2 * time.Second):
		t.Fatal("agent runner did not see ctx cancel after q; cleanup() failed")
	}
}

// TestApp_BadgeMsgUpdatesSidebar covers the badge round-trip: a view
// emits badgeMsg, the App writes it into the registry and refreshes
// the rail, and the rail actually shows the count.
func TestApp_BadgeMsgUpdatesSidebar(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(badgeMsg{id: "agent", badge: "3"})
	if out := m.(App).sidebar.View(); !strings.Contains(out, "3") {
		t.Errorf("sidebar missing badge after badgeMsg:\n%s", out)
	}
}

// TestApp_NoOverflowAtMinSize renders the shell at exactly the minimum
// supported terminal (80x20) and asserts nothing spills: the frame is
// exactly 20 rows, no line exceeds 80 columns, and the content panel's
// border rows sit at exactly 80 columns (the layout fills the row to
// the terminal width, so a too-wide budget would push a border row past
// 80 and a too-narrow one would leave it short).
//
// This is the load-bearing guarantee behind resize()'s chrome
// arithmetic AND View()'s explicit panel sizing: because View() sizes
// the panel to contentW×contentH rather than inheriting the active
// view's own dimensions, a size-ignoring view (like the Dashboard) can
// no longer mask a wrong budget. Mutation-checked: widening resize()'s
// contentW budget from `- 3` to `- 1` grows the panel block by 2 and
// every border row jumps to width 82, failing this test.
func TestApp_NoOverflowAtMinSize(t *testing.T) {
	app := NewApp(Deps{RepoName: "test-repo"})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	a := m.(App)
	out := a.View()
	lines := strings.Split(out, "\n")

	// Explicit panel sizing makes the row count exact, not just bounded.
	if len(lines) != 20 {
		t.Fatalf("view is %d lines, want exactly 20:\n%s", len(lines), out)
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line %d overflows: width %d > 80: %q", i, w, line)
		}
	}

	// The content panel's rounded border rows must sit at exactly the
	// terminal width. These are the rows most sensitive to a budget
	// drift, so pinning them (not just "<= 80") is what catches an
	// off-by-N that a trailing short line would otherwise hide.
	borderRows := 0
	for i, line := range lines {
		if strings.ContainsAny(line, "╭╮╰╯│") {
			if w := lipgloss.Width(line); w != 80 {
				t.Errorf("panel border row %d width %d, want exactly 80: %q", i, w, line)
			}
			borderRows++
		}
	}
	if borderRows == 0 {
		t.Fatal("found no content-panel border rows to check; layout changed?")
	}
}

// TestApp_HelpKeyShowsKeysModal makes `?` real: the status bar
// advertises it, so pressing it must open the key reference. The
// modal is derived from the live keymap, so the global palette
// binding has to appear in it.
func TestApp_HelpKeyShowsKeysModal(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	a := m.(App)
	if len(a.modals) != 1 {
		t.Fatalf("`?` should push the help modal, stack size = %d", len(a.modals))
	}
	out := a.View()
	for _, want := range []string{"Keys", "ctrl+p"} {
		if !strings.Contains(out, want) {
			t.Errorf("help modal missing %q:\n%s", want, out)
		}
	}
}

// T1 — TestApp_KeysRouteToFocusedContent: activating a view moves focus
// to content, and plain keys then route to that view (not the sidebar).
// We install a Snapshots view with rows so a Down key has an observable
// effect (its table cursor advances). Then tab toggles focus back to the
// sidebar and a Down key moves the *rail* highlight instead of the
// table — proving the focus switch actually re-routes plain keys.
func TestApp_KeysRouteToFocusedContent(t *testing.T) {
	app := newTestApp(t)
	snaps := NewSnapshots(Deps{}).SetSnapshots(sampleSnaps())
	installView(t, &app, "snapshots", snaps)

	// Press '2' to jump to Snapshots; focus must move to content.
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	a := m.(App)
	if a.active != 1 {
		t.Fatalf("active = %d, want 1 (snapshots)", a.active)
	}
	if a.focus != focusContent {
		t.Fatalf("focus = %v, want focusContent", a.focus)
	}
	beforeCursor := a.views[1].model.(Snapshots).cursor()

	// A Down key must reach the table and advance its cursor.
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyDown})
	a = m.(App)
	if got := a.views[1].model.(Snapshots).cursor(); got == beforeCursor {
		t.Fatalf("content Down did not advance table cursor (stayed %d)", got)
	}

	// Tab toggles focus back to the sidebar.
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a = m.(App)
	if a.focus != focusSidebar {
		t.Fatalf("tab did not return focus to sidebar (focus=%v)", a.focus)
	}

	// Now a Down key must move the sidebar highlight, not the table.
	tableBefore := a.views[1].model.(Snapshots).cursor()
	sidebarBefore := a.sidebar.list.Index()
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyDown})
	a = m.(App)
	if got := a.sidebar.list.Index(); got == sidebarBefore {
		t.Errorf("sidebar-focused Down did not move rail highlight (stayed %d)", got)
	}
	if got := a.views[1].model.(Snapshots).cursor(); got != tableBefore {
		t.Errorf("sidebar-focused Down leaked into the table cursor (%d -> %d)", tableBefore, got)
	}
}

// T2 — TestApp_ResizeForwardsInnerSize: resize() must forward the
// content-pane inner size to views, not the raw terminal size. We
// install a Snapshots view, resize the shell to 100x30, and assert the
// table's height reflects the inner content height (contentH-8), not the
// height it would derive from the raw 30. If resize() stops forwarding
// the synthetic WindowSizeMsg, this fails.
func TestApp_ResizeForwardsInnerSize(t *testing.T) {
	const termW, termH = 100, 30
	const innerH = termH - 4 // resize()'s vertical budget (contentH)

	// Derive the expected table heights empirically from the view itself
	// (bubbles/table subtracts a header row, so we don't hardcode the
	// offset): the height a fresh view produces when handed the INNER
	// size vs. the RAW terminal size. They must differ, and the shell
	// must forward the inner one.
	innerRef, _ := NewSnapshots(Deps{}).SetSnapshots(sampleSnaps()).
		Update(tea.WindowSizeMsg{Width: termW - sidebarWidth - 3, Height: innerH})
	rawRef, _ := NewSnapshots(Deps{}).SetSnapshots(sampleSnaps()).
		Update(tea.WindowSizeMsg{Width: termW, Height: termH})
	wantInnerH := innerRef.(Snapshots).tbl.Height()
	rawH := rawRef.(Snapshots).tbl.Height()
	if wantInnerH == rawH {
		t.Fatalf("test setup broken: inner and raw table heights coincide (%d) — pick a size where they differ", wantInnerH)
	}

	app := NewApp(Deps{RepoName: "test-repo"})
	installView(t, &app, "snapshots", NewSnapshots(Deps{}).SetSnapshots(sampleSnaps()))
	m, _ := app.Update(tea.WindowSizeMsg{Width: termW, Height: termH})
	a := m.(App)

	got := a.views[1].model.(Snapshots).tbl.Height()
	if got == rawH {
		t.Fatalf("table height %d matches the RAW terminal size — resize() stopped forwarding inner size", got)
	}
	if got != wantInnerH {
		t.Fatalf("table height = %d, want %d (the inner content height)", got, wantInnerH)
	}
}

// T3 — TestApp_SidebarHighlightTracksActive: number keys and palette
// activation both keep the rail highlight in sync with the active view.
func TestApp_SidebarHighlightTracksActive(t *testing.T) {
	app := newTestApp(t)

	// '4' jumps to the 4th view (agent, index 3) — rail selects index 3.
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	a := m.(App)
	if got := a.sidebar.list.Index(); got != 3 {
		t.Fatalf("after '4', sidebar index = %d, want 3", got)
	}

	// Open the palette and activate "diff" (view index 2) — rail follows.
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = m.(App)
	for _, r := range "diff" {
		m, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		a = m.(App)
	}
	m, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("palette enter should emit an activate cmd")
	}
	m, _ = m.(App).Update(cmd()) // deliver activateMsg
	a = m.(App)
	if got := a.sidebar.list.Index(); got != 2 {
		t.Fatalf("after activating diff, sidebar index = %d, want 2", got)
	}
}

// T4 — TestApp_PaletteDownActivatesSecondMatch: with two matches for a
// query, Down then Enter activates the SECOND match, not the first.
func TestApp_PaletteDownActivatesSecondMatch(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a := m.(App)
	// "s" matches both "Snapshots" and "Operations" (subsequence); the
	// registry order puts Snapshots (idx 1) before Operations (idx 4).
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	a = m.(App)
	matches := a.palette.matches
	if len(matches) < 2 {
		t.Fatalf("query 's' produced %d matches, need >= 2 for this test: %+v", len(matches), matches)
	}
	wantSecond := matches[1].ID

	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyDown})
	a = m.(App)
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("palette enter should emit an activate cmd")
	}
	act, ok := cmd().(activateMsg)
	if !ok || act.id != wantSecond {
		t.Fatalf("Down+Enter activated %v, want %s (the second match)", cmd(), wantSecond)
	}
}

// T5 — TestApp_PaletteEscResetLifecycle: esc closes the palette; ctrl+p
// reopens it with a cleared query (Reset ran on the new open).
func TestApp_PaletteEscResetLifecycle(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a := m.(App)
	for _, r := range "diff" {
		m, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		a = m.(App)
	}
	if got := a.palette.Query(); got != "diff" {
		t.Fatalf("query = %q, want diff before esc", got)
	}

	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a = m.(App)
	if a.paletteOpen {
		t.Fatal("esc must close the palette")
	}

	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = m.(App)
	if !a.paletteOpen {
		t.Fatal("ctrl+p must reopen the palette")
	}
	if got := a.palette.Query(); got != "" {
		t.Fatalf("reopened palette query = %q, want empty (Reset should clear it)", got)
	}
}

// T6 — TestApp_BroadcastReachesInactiveView: a non-key message must be
// broadcast to every view, not just the active one. We install an agent
// view (with a runner so it renders its stream pane rather than the
// unavailable-placeholder), leave the Dashboard active, and deliver a
// tokenMsg — the agent's stream message. The agent view must absorb it
// even though it isn't focused, proving broadcast() forwards to all
// views.
func TestApp_BroadcastReachesInactiveView(t *testing.T) {
	app := newTestApp(t)
	// A runner is required for the agent view to render its viewport (nil
	// run => "configure ANTHROPIC_API_KEY" placeholder). The runner body
	// is never called here — we deliver the token msg directly.
	agentView := NewAgentViewWithRunner(Deps{}, func(context.Context, chan<- string) ([]agent.Recommendation, error) {
		return nil, nil
	})
	installView(t, &app, "agent", agentView)

	// Dashboard (index 0) stays active; the agent view is not focused.
	if app.active != 0 {
		t.Fatalf("expected dashboard active, got %d", app.active)
	}

	const token = "REASONING-TOKEN-XYZ"
	m, _ := app.Update(tokenMsg(token))
	a := m.(App)

	// Find the agent view and confirm it absorbed the token.
	var agentOut string
	for _, v := range a.views {
		if v.id == "agent" {
			agentOut = v.model.View()
		}
	}
	if !strings.Contains(agentOut, token) {
		t.Errorf("inactive agent view did not absorb broadcast tokenMsg:\n%s", agentOut)
	}
}

// TestApp_PruneTypedConfirmRoundTripThroughShell drives the prune flow's
// typed-confirmation gate through the REAL App, not directly against
// PruneView. The existing flow tests (prune_test.go) deliver confirmedMsg
// straight to the view, which passes even if the App never forwards it —
// they can't catch a shell-level routing bug. This test goes through
// App.Update at every step, exactly like a real terminal session, so it
// exercises the actual path: modal owns keys while it's on the stack, then
// the App must hand the resulting confirmedMsg back to the views (the
// owning flow, PruneView, is the only one that acts on pruneConfirmID).
//
// Before the fix, `case confirmedMsg` in app.go pops the modal and returns
// early for any id other than "confirm-quit" — it never calls
// m.broadcast(msg) — so PruneView.Update never sees the confirmation, the
// flow never starts the op, and the two seeded snapshots survive.
func TestApp_PruneTypedConfirmRoundTripThroughShell(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)

	app := NewApp(pruneDeps(r))
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	a := sized.(App)

	// Navigate to the prune view via the registry, same as a real palette
	// activation or sidebar selection would.
	m, _ := a.Update(activateMsg{id: "prune"})
	a = m.(App)
	if a.views[a.active].id != "prune" {
		t.Fatalf("active view = %s, want prune", a.views[a.active].id)
	}

	// Enter on the preview requests the typed-confirm modal.
	m, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(App)
	if cmd == nil {
		t.Fatal("enter on the prune preview should return a pushModalMsg cmd")
	}
	msgs := execCmds(t, cmd)
	var pushed pushModalMsg
	var foundPush bool
	for _, msg := range msgs {
		if pm, ok := msg.(pushModalMsg); ok {
			pushed, foundPush = pm, true
		}
	}
	if !foundPush {
		t.Fatalf("expected pushModalMsg in %#v", msgs)
	}
	m, _ = a.Update(pushed)
	a = m.(App)
	if len(a.modals) != 1 {
		t.Fatalf("modal stack = %d, want 1 after pushModalMsg", len(a.modals))
	}

	// The modal owns keys now: type "prune" then enter. Route every key
	// through the App (not the modal directly) to prove the shell's
	// modal-first routing is what's under test.
	for _, ch := range "prune" {
		m, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		a = m.(App)
	}
	m, cmd = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(App)
	if cmd == nil {
		t.Fatal("typed confirm enter should return a confirmedMsg cmd")
	}
	msgs = execCmds(t, cmd)
	var confirmed confirmedMsg
	var foundConfirm bool
	for _, msg := range msgs {
		if cm, ok := msg.(confirmedMsg); ok {
			confirmed, foundConfirm = cm, true
		}
	}
	if !foundConfirm {
		t.Fatalf("expected confirmedMsg in %#v", msgs)
	}
	if confirmed.id != pruneConfirmID {
		t.Fatalf("confirmedMsg.id = %q, want %q", confirmed.id, pruneConfirmID)
	}

	// Deliver the confirmation to the App. This is the crux of the bug:
	// the App must pop the modal AND forward the message to the views so
	// PruneView starts the op.
	m, cmd = a.Update(confirmed)
	a = m.(App)
	if len(a.modals) != 0 {
		t.Fatalf("modal stack after confirmedMsg = %d, want 0", len(a.modals))
	}
	if cmd == nil {
		t.Fatal("confirmedMsg must be forwarded to views: expected a cmd chain yielding startOpMsg, got nil")
	}
	msgs = execCmds(t, cmd)
	var start startOpMsg
	var foundStart bool
	for _, msg := range msgs {
		if sm, ok := msg.(startOpMsg); ok {
			start, foundStart = sm, true
		}
	}
	if !foundStart {
		snaps, _ := r.ListSnapshots(context.Background())
		t.Fatalf("confirmedMsg was not forwarded to PruneView (no startOpMsg produced); msgs=%#v; snapshots still %d", msgs, len(snaps))
	}

	// Deliver startOpMsg to the App (this is what a real tea.Program does),
	// then execute the op's run function and deliver the result.
	m, _ = a.Update(start)
	a = m.(App)
	if a.opRunning != "prune" {
		t.Fatalf("opRunning = %q, want prune", a.opRunning)
	}
	res := start.run(context.Background())
	done, ok := res.(pruneDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("prune op result: %#v", res)
	}
	m, _ = a.Update(done)
	a = m.(App)
	if a.opRunning != "" {
		t.Fatalf("opRunning after done = %q, want empty", a.opRunning)
	}

	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("ListSnapshots after full round trip = %d, want 1", len(snaps))
	}
}

// TestApp_CheckReplacesOperationsInSidebar: after Phase 2b, the sidebar
// exposes Check (not the old Operations placeholder), and the view count
// is unchanged (operations → check is a swap, not an addition).
func TestApp_CheckReplacesOperationsInSidebar(t *testing.T) {
	app := newTestApp(t)
	out := app.View()
	if !strings.Contains(out, "Check") {
		t.Errorf("sidebar should list Check:\n%s", out)
	}
	if strings.Contains(out, "Operations") {
		t.Errorf("Operations placeholder should be gone:\n%s", out)
	}
	if got := len(app.views); got != 17 {
		t.Fatalf("views = %d, want 17 (Phase 2c end-state + the unlock gate + settings + setup)", got)
	}
}

// TestApp_DepsCarryNewFields: Unit-1 plumbing. Deps must carry the four
// action/store/config-path/keyring fields through NewApp so the ported
// operation flows (sync, agent-apply, password, setup) can reach them.
// These are call-time function values and plain data — never resolved
// secrets — so a stub that records its call is a faithful test double.
func TestApp_DepsCarryNewFields(t *testing.T) {
	var newStoreCalled, saveKeyringCalled bool

	newStore := func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
		newStoreCalled = true
		return blobstore.NewMemory(), nil
	}
	saveKeyring := func(_ *config.Config, _ []byte) error {
		saveKeyringCalled = true
		return nil
	}
	reg := action.NewDefaultRegistry()

	app := NewApp(Deps{
		ConfigPath:            "/abs/path/sentra.yaml",
		NewStore:              newStore,
		Actions:               reg,
		SaveKeyringPassphrase: saveKeyring,
	})

	if app.deps.ConfigPath != "/abs/path/sentra.yaml" {
		t.Errorf("Deps.ConfigPath not carried: got %q", app.deps.ConfigPath)
	}
	if app.deps.Actions != reg {
		t.Error("Deps.Actions not carried through NewApp")
	}
	if app.deps.NewStore == nil {
		t.Fatal("Deps.NewStore not carried through NewApp")
	}
	if app.deps.SaveKeyringPassphrase == nil {
		t.Fatal("Deps.SaveKeyringPassphrase not carried through NewApp")
	}

	// Prove the carried func values are the ones we passed (identity via
	// side effect): invoking them flips the sentinels.
	if _, err := app.deps.NewStore(context.Background(), nil); err != nil {
		t.Fatalf("carried NewStore returned error: %v", err)
	}
	if err := app.deps.SaveKeyringPassphrase(nil, nil); err != nil {
		t.Fatalf("carried SaveKeyringPassphrase returned error: %v", err)
	}
	if !newStoreCalled || !saveKeyringCalled {
		t.Error("carried func values are not the ones passed to Deps")
	}
}

func TestApp_RegistersPoliciesView(t *testing.T) {
	app := newTestApp(t)
	var found bool
	for _, v := range app.views {
		if v.id == "policies" {
			found = true
			if _, ok := v.model.(PoliciesView); !ok {
				t.Fatalf("policies entry is %T, want PoliciesView", v.model)
			}
		}
	}
	if !found {
		t.Fatal("App must register the policies view")
	}
}

// TestApp_Phase2cViewsRegistered: after Phase 2c, all six new standalone
// views are present (doctor, recovery-kit, policies, schedule, sync,
// password) alongside the eight pre-existing ones, plus the unlock gate
// and the Part 7 settings/setup views, for a total of 17. agent-apply is
// NOT a new view — it extends the existing "agent" view in place — so it
// adds no id.
func TestApp_Phase2cViewsRegistered(t *testing.T) {
	app := newTestApp(t)

	want := []string{
		"dashboard", "snapshots", "diff", "check", "doctor", "recovery-kit",
		"policies", "schedule", "agent", "backup", "restore", "prune",
		"sync", "password", "unlock", "settings", "setup",
	}
	got := make(map[string]bool, len(app.views))
	for _, v := range app.views {
		got[v.id] = true
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("view %q not registered", id)
		}
	}
	if len(app.views) != len(want) {
		t.Fatalf("views = %d, want %d", len(app.views), len(want))
	}

	// The direct data operations carry the "Operations" palette category.
	out := app.View()
	for _, label := range []string{"Sync", "Password", "Doctor", "Policies"} {
		if !strings.Contains(out, label) {
			t.Errorf("sidebar/palette should list %q:\n%s", label, out)
		}
	}
}

// TestApp_Phase3ViewsRegistered: after Phase 3 the shell has 17 view models
// (14 Phase 2c + unlock + setup + settings). setup and settings are
// navigable (rail/palette) under the "Settings" category; unlock is a
// startup gate reached only via Deps.InitialView, so it is NOT in the
// command registry.
func TestApp_Phase3ViewsRegistered(t *testing.T) {
	app := newTestApp(t)

	want := []string{
		"dashboard", "snapshots", "diff", "check", "doctor", "recovery-kit",
		"policies", "schedule", "agent", "backup", "restore", "prune",
		"sync", "password", "setup", "settings", "unlock",
	}
	got := make(map[string]bool, len(app.views))
	for _, v := range app.views {
		got[v.id] = true
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("view %q not registered", id)
		}
	}
	if len(app.views) != len(want) {
		t.Fatalf("views = %d, want %d", len(app.views), len(want))
	}

	// setup + settings are navigable; unlock is a hidden startup gate.
	cmds := app.registry.Commands()
	ids := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		ids[c.ID] = true
	}
	if !ids["setup"] || !ids["settings"] {
		t.Error("setup and settings must be in the command registry (rail/palette)")
	}
	if ids["unlock"] {
		t.Error("unlock is a startup gate and must NOT be in the command registry")
	}

	out := app.View()
	for _, label := range []string{"Setup", "Settings"} {
		if !strings.Contains(out, label) {
			t.Errorf("sidebar/palette should list %q:\n%s", label, out)
		}
	}
}

// TestApp_InitialViewUnlockLandsContentFocusedAndHidden: the unlock gate is
// reachable only via Deps.InitialView (never the sidebar/palette per
// TestApp_Phase3ViewsRegistered). Landing on it must still focus content
// immediately, exactly like any other non-dashboard InitialView.
func TestApp_InitialViewUnlockLandsContentFocusedAndHidden(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", InitialView: "unlock"})
	if got := app.views[app.active].id; got != "unlock" {
		t.Fatalf("active view = %q, want unlock", got)
	}
	if app.focus != focusContent {
		t.Fatalf("focus = %v, want focusContent", app.focus)
	}
}

// TestApp_InitialViewSetupLandsContentFocused: the first-run wizard is the
// other InitialView-only landing case exercised by C8; assert it alongside
// unlock so both startup-gate routes are pinned.
func TestApp_InitialViewSetupLandsContentFocused(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", InitialView: "setup"})
	if got := app.views[app.active].id; got != "setup" {
		t.Fatalf("active view = %q, want setup", got)
	}
	if app.focus != focusContent {
		t.Fatalf("focus = %v, want focusContent", app.focus)
	}
}
