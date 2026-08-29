package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
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
	for _, want := range []string{"Backup", "Maintenance"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing %q", want)
		}
	}
	if got := len(app.views); got != 20 {
		t.Fatalf("views = %d, want 20 (6 rail views + 14 hidden: diff, check, doctor, recovery-kit, policies, schedule, jobs, restore, prune, sync, password, unlock, connect, setup)", got)
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
	// "S E N T R A" is the centered header logo; "test-repo" is the repo name,
	// which now lives on the status bar rather than the header.
	for _, want := range []string{"S E N T R A", "Dashboard", "Snapshots", "Maintenance", "test-repo"} {
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

// TestApp_SidebarScrollSwitchesViewLive checks the rail switches the shown view
// as the cursor scrolls over items, while keeping focus on the rail so you can
// keep scrolling.
func TestApp_SidebarScrollSwitchesViewLive(t *testing.T) {
	app := newTestApp(t)
	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyDown}) // cursor → Snapshots
	if cmd == nil {
		t.Fatal("scrolling the rail should emit a preview cmd")
	}
	m, _ = m.(App).Update(cmd()) // deliver navPreviewMsg
	if got := m.(App).active; got != 1 {
		t.Fatalf("active = %d, want 1 (snapshots) — the view should follow the cursor", got)
	}
	if m.(App).focus != focusSidebar {
		t.Fatal("live scroll must keep focus on the rail")
	}
}

func TestApp_NumberKeyJumpsToView(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	if got := m.(App).active; got != 3 {
		t.Fatalf("active = %d, want 3 (maintenance)", got)
	}
}

func TestApp_PaletteOpensFiltersAndActivates(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.(App).paletteOpen {
		t.Fatal("ctrl+p should open the palette")
	}
	for _, r := range "main" {
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
	if app2.views[app2.active].id != "maintenance" {
		t.Fatalf("active view = %s, want maintenance", app2.views[app2.active].id)
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

// TestApp_QuitOnQ asserts that pressing `q` no longer quits outright: it pops a
// "Quit sentra?" confirm, and only a confirmed result returns tea.QuitMsg.
// ctrl+c stays an instant force-quit (TestApp_CtrlCQuitsUnderModal).
func TestApp_QuitOnQ(t *testing.T) {
	app := newTestApp(t)
	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	app = m.(App)
	assertNoQuit(t, cmd) // q alone must not quit
	if len(app.modals) != 1 {
		t.Fatalf("q must pop a quit-confirm modal, modals=%d", len(app.modals))
	}
	if !strings.Contains(app.View(), "Quit sentra") {
		t.Errorf("modal must ask about quitting:\n%s", app.View())
	}

	// Confirm: enter routes to the modal, which emits confirmedMsg{confirm-quit};
	// the App then tears down and returns tea.QuitMsg.
	m, cmd = app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on the confirm modal must emit a command")
	}
	_, quitCmd := m.(App).Update(cmd())
	if quitCmd == nil {
		t.Fatal("confirming quit must emit a command")
	}
	if _, ok := quitCmd().(tea.QuitMsg); !ok {
		t.Errorf("confirming quit must return tea.QuitMsg, got %T", quitCmd())
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

// TestApp_SynthwaveBannerAtTallSize: on a terminal tall enough (>=
// bannerMinHeight) the full synthwave banner heads every page. The frame must
// still be EXACTLY the terminal height with no line overflowing — the banner is
// reserved in resize()'s content budget, so a miscount would over/underflow —
// and the block SENTRA wordmark and the rail must both render (banner AND
// content, not banner instead of content).
func TestApp_SynthwaveBannerAtTallSize(t *testing.T) {
	r := newFlowRepo(t)
	app := NewApp(Deps{RepoName: "test-repo", Repo: r})
	const w, h = 80, 40
	m, _ := app.Update(tea.WindowSizeMsg{Width: w, Height: h})
	a := m.(App)

	out := a.View()
	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Fatalf("frame is %d lines, want exactly %d:\n%s", len(lines), h, out)
	}
	for i, line := range lines {
		if lw := lipgloss.Width(line); lw > w {
			t.Errorf("line %d overflows: width %d > %d: %q", i, lw, w, line)
		}
	}
	// The sun is the banner's fingerprint (its logotype text matches the
	// one-line fallback, so the sun scene is what distinguishes them). The
	// solid band is a distinctive, mostly-non-blank sun row.
	if !strings.Contains(out, bannerSunArt[2]) {
		t.Errorf("tall terminal must show the synthwave banner (its sun):\n%s", out)
	}
	if !strings.Contains(out, "S E N T R A") {
		t.Errorf("the banner must still carry the SENTRA logotype:\n%s", out)
	}
	if !strings.Contains(out, "Snapshots") {
		t.Errorf("the banner must not crowd out the rail/content:\n%s", out)
	}
}

// TestApp_HeaderFallsBackWhenShort: below bannerMinHeight the header collapses
// to the one-line ✦ S E N T R A ✦ logo so the shell stays usable — the block
// banner must NOT appear, and the frame stays exactly the terminal height.
func TestApp_HeaderFallsBackWhenShort(t *testing.T) {
	r := newFlowRepo(t)
	app := NewApp(Deps{RepoName: "test-repo", Repo: r})
	const w, h = 80, 30 // below bannerMinHeight (32)
	m, _ := app.Update(tea.WindowSizeMsg{Width: w, Height: h})
	a := m.(App)

	out := a.View()
	if lines := strings.Split(out, "\n"); len(lines) != h {
		t.Fatalf("frame is %d lines, want exactly %d:\n%s", len(lines), h, out)
	}
	if !strings.Contains(out, "S E N T R A") {
		t.Errorf("a short terminal must show the one-line logo:\n%s", out)
	}
	if strings.Contains(out, bannerSunArt[2]) {
		t.Errorf("a short terminal must NOT show the synthwave banner (no sun):\n%s", out)
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

	// Press '3' to jump to Snapshots (now the third rail item, after Dashboard
	// and Backup); focus must move to content.
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	a := m.(App)
	if a.active != 2 {
		t.Fatalf("active = %d, want 2 (snapshots)", a.active)
	}
	if a.focus != focusContent {
		t.Fatalf("focus = %v, want focusContent", a.focus)
	}
	beforeCursor := a.views[2].model.(Snapshots).cursor()

	// A Down key must reach the table and advance its cursor.
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyDown})
	a = m.(App)
	if got := a.views[2].model.(Snapshots).cursor(); got == beforeCursor {
		t.Fatalf("content Down did not advance table cursor (stayed %d)", got)
	}

	// Tab toggles focus back to the sidebar.
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a = m.(App)
	if a.focus != focusSidebar {
		t.Fatalf("tab did not return focus to sidebar (focus=%v)", a.focus)
	}

	// Now a Down key must move the sidebar highlight, not the table.
	tableBefore := a.views[2].model.(Snapshots).cursor()
	sidebarBefore := a.sidebar.list.Index()
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyDown})
	a = m.(App)
	if got := a.sidebar.list.Index(); got == sidebarBefore {
		t.Errorf("sidebar-focused Down did not move rail highlight (stayed %d)", got)
	}
	if got := a.views[2].model.(Snapshots).cursor(); got != tableBefore {
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

	got := a.views[2].model.(Snapshots).tbl.Height() // snapshots is the 3rd view (dashboard, backup, snapshots)
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

	// '4' jumps to the 4th rail view (maintenance, index 3) — rail selects index 3.
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	a := m.(App)
	if got := a.sidebar.list.Index(); got != 3 {
		t.Fatalf("after '4', sidebar index = %d, want 3", got)
	}

	// Open the palette and activate "settings" — rail follows.
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = m.(App)
	for _, r := range "sett" {
		m, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		a = m.(App)
	}
	m, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("palette enter should emit an activate cmd")
	}
	m, _ = m.(App).Update(cmd()) // deliver activateMsg
	a = m.(App)
	// Look the index up rather than hardcode it: the rail's order is a product
	// decision that moves (backup was promoted to sit under snapshots).
	want := -1
	for i, v := range a.views {
		if v.id == "settings" {
			want = i
		}
	}
	if got := a.sidebar.list.Index(); got != want {
		t.Fatalf("after activating settings, sidebar index = %d, want %d", got, want)
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
// broadcast to every view, not just the active one. A recorder stub is
// installed under a hidden view id; the Dashboard stays active, and a
// synthetic message must still reach the stub — proving broadcast()
// forwards to all views.
type broadcastProbeMsg struct{}

type broadcastRecorder struct{ got *bool }

func (r broadcastRecorder) Init() tea.Cmd { return nil }
func (r broadcastRecorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(broadcastProbeMsg); ok {
		*r.got = true
	}
	return r, nil
}
func (r broadcastRecorder) View() string { return "" }

func TestApp_BroadcastReachesInactiveView(t *testing.T) {
	app := newTestApp(t)
	var got bool
	installView(t, &app, "sync", broadcastRecorder{got: &got})

	if app.active != 0 {
		t.Fatalf("expected dashboard active, got %d", app.active)
	}
	_, _ = app.Update(broadcastProbeMsg{})
	if !got {
		t.Error("inactive view did not receive the broadcast message")
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

// TestApp_DepsCarryNewFields: Unit-1 plumbing. Deps must carry the
// store/config-path/keyring fields through NewApp so the ported
// operation flows (sync, password, setup) can reach them.
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
	app := NewApp(Deps{
		ConfigPath:            "/abs/path/sentra.yaml",
		NewStore:              newStore,
		SaveKeyringPassphrase: saveKeyring,
	})

	if app.deps.ConfigPath != "/abs/path/sentra.yaml" {
		t.Errorf("Deps.ConfigPath not carried: got %q", app.deps.ConfigPath)
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

// TestApp_AllViewsRegistered pins the views slice after the six-view rail
// simplification: every view — rail and hidden alike — is present exactly
// once. Rail/palette membership itself is pinned by
// TestApp_RailShowsExactlySixViews; this guards the routable set.
func TestApp_AllViewsRegistered(t *testing.T) {
	app := newTestApp(t)

	want := []string{
		"dashboard", "backup", "snapshots", "maintenance", "settings", "help",
		"diff", "check", "doctor", "recovery-kit", "policies", "schedule", "jobs",
		"restore", "prune", "sync", "password", "unlock", "connect", "setup",
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
}

// TestApp_InitialViewUnlockLandsContentFocusedAndHidden: the unlock gate is
// reachable only via Deps.InitialView (never the sidebar/palette per
// TestApp_RailShowsExactlySixViews). Landing on it must still focus content
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

// TestApp_StartupGateHidesNavChrome: on a first-run/unlock gate (setup or
// unlock with a nil repo) there is no repo behind any other view, so the shell
// hides its navigation chrome. The rail's other view titles must not render,
// ctrl+p must not open the palette, and a number key must not jump views —
// every non-quit key belongs to the gate view.
func TestApp_StartupGateHidesNavChrome(t *testing.T) {
	for _, gate := range []string{"setup", "unlock", "connect"} {
		t.Run(gate, func(t *testing.T) {
			app := NewApp(Deps{RepoName: "x", InitialView: gate}) // Repo nil
			sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			app = sized.(App)
			if !app.inStartupGate() {
				t.Fatalf("%s with a nil repo must be a startup gate", gate)
			}
			// No rail: the sidebar's other view titles must not render.
			out := app.View()
			for _, title := range []string{"Snapshots", "Backup", "Dashboard"} {
				if strings.Contains(out, title) {
					t.Errorf("gate view must hide the rail, but found %q:\n%s", title, out)
				}
			}
			// ctrl+p must not open the palette.
			m, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
			if m.(App).paletteOpen {
				t.Error("ctrl+p must not open the palette in a startup gate")
			}
			// A number key must not jump views.
			m, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
			if got := m.(App).active; got != app.active {
				t.Errorf("number key jumped active %d→%d in a gate; nav must be suppressed", app.active, got)
			}
		})
	}
}

// TestApp_LiveRepoShowsRailAndNav is the regression guard: with a live repo
// (the normal dashboard) the shell is NOT a startup gate, so the rail renders
// and ctrl+p / number-key navigation still work exactly as before.
func TestApp_LiveRepoShowsRailAndNav(t *testing.T) {
	r := newFlowRepo(t)
	app := NewApp(Deps{RepoName: "x", Repo: r})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = sized.(App)
	if app.inStartupGate() {
		t.Fatal("a live-repo dashboard must not be a startup gate")
	}
	if out := app.View(); !strings.Contains(out, "Snapshots") {
		t.Fatalf("live-repo shell must render the rail (Snapshots):\n%s", out)
	}
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.(App).paletteOpen {
		t.Fatal("ctrl+p must open the palette with a live repo")
	}
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if got := m.(App).active; got != 1 {
		t.Fatalf("number key must jump to view 1 (backup), got %d", got)
	}
}

// TestApp_NoOverflowAtGateMinSize is the gate-layout twin of
// TestApp_NoOverflowAtMinSize: with the rail dropped, the content panel fills
// the full width, so at 80x20 the frame is still exactly 20 lines, no line
// exceeds 80, and the panel's border rows sit at exactly 80.
func TestApp_NoOverflowAtGateMinSize(t *testing.T) {
	app := NewApp(Deps{RepoName: "test-repo", InitialView: "setup"}) // Repo nil → gate
	m, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	a := m.(App)
	if !a.inStartupGate() {
		t.Fatal("precondition: setup with a nil repo must be a startup gate")
	}
	out := a.View()
	lines := strings.Split(out, "\n")
	if len(lines) != 20 {
		t.Fatalf("gate view is %d lines, want exactly 20:\n%s", len(lines), out)
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("gate line %d overflows: width %d > 80: %q", i, w, line)
		}
	}
	borderRows := 0
	for i, line := range lines {
		if strings.ContainsAny(line, "╭╮╰╯│") {
			if w := lipgloss.Width(line); w != 80 {
				t.Errorf("gate panel border row %d width %d, want exactly 80: %q", i, w, line)
			}
			borderRows++
		}
	}
	if borderRows == 0 {
		t.Fatal("found no content-panel border rows to check; gate layout changed?")
	}
}

// liveRepoApp builds a shell over a real in-memory repo, sized so nav chrome
// is live (not a startup gate). Used by the keyboard-ownership tests below.
func liveRepoApp(t *testing.T) App {
	t.Helper()
	app := NewApp(Deps{RepoName: "x", Repo: newFlowRepo(t)})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return sized.(App)
}

// TestApp_ContentTextInputOwnsKeyboard is the exact empirical repro: with a
// live repo (NOT a startup gate) the password view holds content focus on its
// masked-input stage, so the passphrase field must own every rune. Before the
// fix, routeKey applied its single-rune globals and the number-key view jump
// first: '1' jumped to the dashboard, 'q' quit the app, and 'A' (rune 16 past
// '1', with 17 views) jumped to setup — none reached the input.
func TestApp_ContentTextInputOwnsKeyboard(t *testing.T) {
	app := liveRepoApp(t)

	m, _ := app.Update(activateMsg{id: "password"})
	app = m.(App)
	if got := app.views[app.active].id; got != "password" {
		t.Fatalf("active view = %q, want password", got)
	}
	if app.focus != focusContent {
		t.Fatalf("focus = %v, want focusContent", app.focus)
	}
	passwordIdx := app.active

	// Feed the three runes the globals used to steal. Each must reach the
	// passphrase field: never quit, never change the active view.
	for _, r := range []rune{'1', 'q', 'A'} {
		var cmd tea.Cmd
		m, cmd = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		app = m.(App)
		assertNoQuit(t, cmd)
		if got := app.active; got != passwordIdx {
			t.Fatalf("rune %q changed active %d→%d; the input must own it", r, passwordIdx, got)
		}
	}
	if got := app.views[app.active].model.(PasswordView).newPass.Value(); got != "1qA" {
		t.Fatalf("passphrase field = %q, want %q — runes did not reach the input", got, "1qA")
	}
}

// TestApp_SetupWizardDetailsStageOwnsKeyboard: the wizard's details stage
// focuses a text input (the bucket field), so while it holds content focus the
// globals/number-jump must not fire — the bucket field owns '1', 'q', and 'A'.
func TestApp_SetupWizardDetailsStageOwnsKeyboard(t *testing.T) {
	app := liveRepoApp(t)

	// Advance a wizard from the backend stage to the details stage (enter),
	// then install it so the setup slot sits on a text-input stage.
	wiz, _ := NewSetupWizardView(app.deps).Update(tea.KeyMsg{Type: tea.KeyEnter})
	installView(t, &app, "setup", wiz.(SetupWizardView))

	m, _ := app.Update(activateMsg{id: "setup"})
	app = m.(App)
	if got := app.views[app.active].model.(SetupWizardView).stage; got != stageDetails {
		t.Fatalf("precondition: wizard stage = %v, want stageDetails", got)
	}
	setupIdx := app.active

	for _, r := range []rune{'1', 'q', 'A'} {
		var cmd tea.Cmd
		m, cmd = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		app = m.(App)
		assertNoQuit(t, cmd)
		if got := app.active; got != setupIdx {
			t.Fatalf("rune %q changed active in the details stage", r)
		}
	}
	if got := app.views[app.active].model.(SetupWizardView).fields[setupFieldBucket].Value(); got != "1qA" {
		t.Fatalf("bucket field = %q, want %q — runes did not reach the input", got, "1qA")
	}
}

// TestApp_SetupWizardPassphraseStageOwnsKeyboard: the passphrase stage focuses
// a masked input, so a passphrase containing 'q' / digits must land in it.
func TestApp_SetupWizardPassphraseStageOwnsKeyboard(t *testing.T) {
	app := liveRepoApp(t)

	wiz := NewSetupWizardView(app.deps)
	wiz.stage = stagePassphrase
	wiz.newPass.Focus()
	wiz.confirmPass.Blur()
	installView(t, &app, "setup", wiz)

	m, _ := app.Update(activateMsg{id: "setup"})
	app = m.(App)
	setupIdx := app.active

	for _, r := range []rune{'1', 'q', 'A'} {
		var cmd tea.Cmd
		m, cmd = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		app = m.(App)
		assertNoQuit(t, cmd)
		if got := app.active; got != setupIdx {
			t.Fatalf("rune %q changed active in the passphrase stage", r)
		}
	}
	if got := app.views[app.active].model.(SetupWizardView).newPass.Value(); got != "1qA" {
		t.Fatalf("passphrase field = %q, want %q — runes did not reach the input", got, "1qA")
	}
}

// TestApp_UnlockViewOwnsKeyboard: the unlock gate is a single masked input, so
// it captures text whenever it holds content focus (here forced with a live
// repo so the startup-gate path is not what's being exercised).
func TestApp_UnlockViewOwnsKeyboard(t *testing.T) {
	app := liveRepoApp(t)

	m, _ := app.Update(activateMsg{id: "unlock"})
	app = m.(App)
	if got := app.views[app.active].id; got != "unlock" {
		t.Fatalf("active view = %q, want unlock", got)
	}
	unlockIdx := app.active

	for _, r := range []rune{'1', 'q', 'A'} {
		var cmd tea.Cmd
		m, cmd = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		app = m.(App)
		assertNoQuit(t, cmd)
		if got := app.active; got != unlockIdx {
			t.Fatalf("rune %q changed active in the unlock view", r)
		}
	}
	if got := app.views[app.active].model.(UnlockView).input.Value(); got != "1qA" {
		t.Fatalf("unlock input = %q, want %q — runes did not reach the input", got, "1qA")
	}
}

// TestApp_NonInputViewKeepsGlobals is the regression guard: a content-focused
// view that does NOT capture text (snapshots) must still honor the globals —
// 'q' quits, '1' jumps to view 0, ctrl+p opens the palette.
func TestApp_NonInputViewKeepsGlobals(t *testing.T) {
	app := liveRepoApp(t)

	m, _ := app.Update(activateMsg{id: "snapshots"})
	app = m.(App)
	if app.focus != focusContent {
		t.Fatalf("focus = %v, want focusContent", app.focus)
	}

	// 'q' still reaches the shell — now as a quit-confirm modal rather than an
	// instant quit (ctrl+c stays the instant force-quit).
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if len(m.(App).modals) != 1 {
		t.Fatalf("expected 'q' to pop a quit-confirm modal, modals=%d", len(m.(App).modals))
	}

	// '1' still jumps to view 0 (dashboard).
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if got := m.(App).active; got != 0 {
		t.Fatalf("'1' did not jump to dashboard: active = %d", got)
	}

	// ctrl+p still opens the palette.
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.(App).paletteOpen {
		t.Fatal("ctrl+p must still open the palette on a non-input view")
	}
}

// TestApp_GateRoutesQuitRuneToView is the gate regression: on a first-run
// wizard gate every key except ctrl+c belongs to the view, so 'q' (a valid
// passphrase character) must reach the wizard's input, not quit — while ctrl+c
// remains the escape hatch.
func TestApp_GateRoutesQuitRuneToView(t *testing.T) {
	app := NewApp(Deps{InitialView: "setup"}) // Repo nil → startup gate
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = sized.(App)
	if !app.inStartupGate() {
		t.Fatal("precondition: setup with a nil repo must be a startup gate")
	}

	// enter advances backend → details (routes to the view in a gate).
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = m.(App)
	if got := app.views[app.active].model.(SetupWizardView).stage; got != stageDetails {
		t.Fatalf("precondition: wizard stage = %v, want stageDetails", got)
	}

	// 'q' must reach the bucket field, not quit.
	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	assertNoQuit(t, cmd)
	app = m.(App)
	if got := app.views[app.active].model.(SetupWizardView).fields[setupFieldBucket].Value(); got != "q" {
		t.Fatalf("'q' did not reach the wizard bucket field in a gate: %q", got)
	}

	// ctrl+c is still the always-quit escape hatch, even in a gate.
	_, cmd = app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected a quit cmd from ctrl+c in a gate")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg from ctrl+c, got %T", cmd())
	}
}

// TestApp_DepsCarrySplashFields: the splash is opt-in via Deps so the whole
// existing suite (which constructs Deps{}) keeps rendering the normal frame.
func TestApp_DepsCarrySplashFields(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", ShowSplash: true, Version: "v1.2.0", Commit: "a1b2c3d4"})
	if !app.deps.ShowSplash {
		t.Error("Deps.ShowSplash not carried through NewApp")
	}
	if app.deps.Version != "v1.2.0" || app.deps.Commit != "a1b2c3d4" {
		t.Errorf("version/commit not carried: %q %q", app.deps.Version, app.deps.Commit)
	}
	if NewApp(Deps{RepoName: "x"}).deps.ShowSplash {
		t.Error("Deps{} must default ShowSplash to false")
	}
}

// splashApp builds a sized App with the splash armed.
func splashApp(t *testing.T) App {
	t.Helper()
	app := NewApp(Deps{RepoName: "x", ShowSplash: true, Version: "v1.2.0", Commit: "a1b2c3d4ef"})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return sized.(App)
}

func TestApp_SplashRendersThenAutoDismisses(t *testing.T) {
	app := splashApp(t)
	// The splash covers the frame from the very first paint, before the reveal
	// has drawn a single letter.
	if strings.Contains(app.View(), "Dashboard") {
		t.Fatalf("the splash must cover the frame at frame 0:\n%s", app.View())
	}
	app = advanceSplash(app, splashFramesTo(splashRevealDone))
	if splashBlocks(app.View()) == 0 {
		t.Fatalf("splash wordmark not rendered once revealed:\n%s", app.View())
	}
	m, _ := app.Update(splashDoneMsg{})
	app = m.(App)
	if app.splashActive {
		t.Error("splashDoneMsg must retire the splash")
	}
	if !strings.Contains(app.View(), "Dashboard") {
		t.Errorf("normal frame should render after the splash:\n%s", app.View())
	}
}

// The dismissing key is CONSUMED: it must not reach the active view.
//
// Asserting the wordmark is absent would now pass vacuously — frame 0 has no
// letters either, so the assertion could not tell a dismissed splash from a
// freshly drawn one. Assert the frame behind actually renders instead.
func TestApp_SplashDismissedByAnyKeyAndConsumed(t *testing.T) {
	app := splashApp(t)
	before := app.active
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	app = m.(App)
	if app.splashActive {
		t.Error("any key must dismiss the splash")
	}
	if !strings.Contains(app.View(), "Dashboard") {
		t.Errorf("the frame must render once the splash is dismissed:\n%s", app.View())
	}
	if app.active != before {
		t.Errorf("the dismissing key must be consumed, not routed (active %d -> %d)", before, app.active)
	}
}

func TestApp_CtrlCQuitsDuringSplash(t *testing.T) {
	app := splashApp(t)
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c must quit even while the splash is up")
	}
}

// Deps{} leaves the splash off, which is what keeps every other App test in this
// file rendering the normal frame. Assert on splashActive and the frame itself:
// "no wordmark" would hold vacuously even if the splash WERE on, since frame 0
// draws none of it.
func TestApp_NoSplashByDefault(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = sized.(App)
	if app.splashActive {
		t.Error("Deps{} must not show the splash")
	}
	if app.Init() != nil && app.splashFrame != 0 {
		t.Error("Deps{} must not arm the splash frame tick")
	}
	if !strings.Contains(app.View(), "Dashboard") {
		t.Errorf("Deps{} must render the normal frame:\n%s", app.View())
	}
}

func TestApp_TooSmallBeatsSplash(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", ShowSplash: true})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
	out := sized.(App).View()
	if !strings.Contains(out, "terminal too small") {
		t.Errorf("the too-small guard must outrank the splash:\n%s", out)
	}
}

func TestApp_VersionLine(t *testing.T) {
	tests := []struct {
		name    string
		version string
		commit  string
		want    string
	}{
		{name: "version and short commit", version: "v1.2.0", commit: "a1b2c3d4ef", want: "v1.2.0 · a1b2c3d"},
		{name: "commit none is omitted", version: "dev", commit: "none", want: "dev"},
		{name: "empty commit is omitted", version: "dev", commit: "", want: "dev"},
		{name: "no version renders nothing", version: "", commit: "abc", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := NewApp(Deps{Version: tt.version, Commit: tt.commit})
			if got := app.versionLine(); got != tt.want {
				t.Errorf("versionLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestApp_RepoReadyDoesNotReplaySplash: the splash is a launch moment, not a
// per-App-construction one. repoReadyMsg rebuilds the whole shell via NewApp,
// which re-seeds splashActive from Deps.ShowSplash — so unless the rebuild
// clears it, unlocking (or finishing first-run setup) covers the fresh
// dashboard with the splash a second time and swallows the user's next
// keystroke dismissing it.
func TestApp_RepoReadyDoesNotReplaySplash(t *testing.T) {
	r := newFlowRepo(t)
	cfg := config.Defaults()
	app := NewApp(Deps{RepoName: "x", InitialView: "unlock", ShowSplash: true})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = sized.(App)

	// The launch splash had its moment and the user dismissed it.
	dismissed, _ := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = dismissed.(App)
	if app.splashActive {
		t.Fatal("precondition: a key must dismiss the launch splash")
	}

	m, _ := app.Update(repoReadyMsg{repo: r, config: &cfg})
	next := m.(App)
	if next.splashActive {
		t.Error("the splash must not replay over the dashboard after unlock")
	}
	if !strings.Contains(next.View(), "Dashboard") {
		t.Errorf("post-unlock frame must be the dashboard, not the splash:\n%s", next.View())
	}
	if next.deps.ShowSplash {
		t.Error("rebuilt Deps must not carry ShowSplash, or Init() re-arms the tick")
	}
}

// splashFramesTo returns the frame index at which elapsed >= d.
func splashFramesTo(d time.Duration) int {
	n := 0
	for time.Duration(n)*splashFrameInterval < d {
		n++
	}
	return n
}

// advanceSplash drives n frame ticks through the App.
func advanceSplash(app App, n int) App {
	for i := 0; i < n; i++ {
		m, _ := app.Update(splashFrameMsg{})
		app = m.(App)
	}
	return app
}

// splashBlocks counts the block-glyph cells (█) in a rendered frame — the reveal
// signal for the big wordmark, which cascades one letter at a time so the count
// grows monotonically from zero to its full value.
func splashBlocks(view string) int { return strings.Count(view, "█") }

// TestSplashRevealIsProgressive: the block wordmark cascades in letter by letter,
// so frame 0 shows none of it, a mid frame shows some, and the final frame shows
// strictly more than the mid frame.
func TestSplashRevealIsProgressive(t *testing.T) {
	app := splashApp(t) // width 100 → the big block wordmark

	if got := splashBlocks(app.View()); got != 0 {
		t.Errorf("frame 0 must show no wordmark blocks, got %d:\n%s", got, app.View())
	}

	// Four letters in: lettersAt + 3 steps has revealed s, e, n, t.
	mid := splashBlocks(advanceSplash(app, splashFramesTo(splashLettersAt+3*splashLetterStep)).View())
	if mid == 0 {
		t.Error("a mid-reveal frame must show some blocks")
	}

	full := advanceSplash(app, splashFramesTo(splashRevealDone))
	fullBlocks := splashBlocks(full.View())
	if fullBlocks <= mid {
		t.Errorf("the reveal must be progressive: mid=%d full=%d", mid, fullBlocks)
	}
	if !strings.Contains(full.View(), "Encrypted, deduplicated") {
		t.Errorf("final frame must show the tagline:\n%s", full.View())
	}
}

// TestSplashGeometryIsConstant is the load-bearing one. lipgloss.Place centers
// the lockup, so if a hidden letter or line simply were not drawn, the block
// would grow and the wordmark would slide across the screen as it revealed.
// Every frame must occupy exactly the box the final frame does.
func TestSplashGeometryIsConstant(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", ShowSplash: true, Version: "v1.2.0", Commit: "a1b2c3d4"})
	// width/height 0 => renderSplash returns the raw body, not the centered frame.
	wantW, wantH := 0, 0
	for f := 0; f <= splashFramesTo(splashRevealDone); f++ {
		body := advanceSplash(app, f).renderSplash()
		w, h := lipgloss.Width(body), lipgloss.Height(body)
		if f == 0 {
			wantW, wantH = w, h
			continue
		}
		if w != wantW || h != wantH {
			t.Fatalf("frame %d geometry %dx%d, want %dx%d — the lockup shifts as it reveals",
				f, w, h, wantW, wantH)
		}
	}
}

// TestSplashFrameMsgIgnoredOnceDismissed: a tick already in flight when the user
// skips must not resurrect the animation or re-arm another tick.
func TestSplashFrameMsgIgnoredOnceDismissed(t *testing.T) {
	app := splashApp(t)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = m.(App)
	if app.splashActive {
		t.Fatal("precondition: the key must dismiss the splash")
	}
	before := app.splashFrame
	m2, cmd := app.Update(splashFrameMsg{})
	app = m2.(App)
	if cmd != nil {
		t.Error("a stale frame tick must not re-arm another")
	}
	if app.splashFrame != before {
		t.Error("a stale frame tick must not advance the frame")
	}
}

// TestApp_EnterOnAlreadyActiveRailItemKeepsRailUsable pins the bug where the
// dashboard appeared frozen. The rail lands highlighting the view that is
// already open, so Enter activates it: m.active and Select are no-ops, but the
// handler still moved focus to the content pane. Nothing visibly happened, and
// because Dashboard.Update ignores every message, ↑/↓ silently stopped driving
// the rail — the app looked dead after a single keystroke.
//
// Activating the view you are already on must not steal focus from the rail.
func TestApp_EnterOnAlreadyActiveRailItemKeepsRailUsable(t *testing.T) {
	app := newTestApp(t)
	if app.focus != focusSidebar || app.active != 0 {
		t.Fatalf("precondition: want rail focus on view 0, got focus=%v active=%d", app.focus, app.active)
	}

	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on the rail should emit an activate cmd")
	}
	m, _ = m.(App).Update(cmd()) // deliver activateMsg{dashboard}
	app = m.(App)

	if app.active != 0 {
		t.Errorf("active view changed to %d; enter on the current item navigates nowhere", app.active)
	}
	if app.focus != focusSidebar {
		t.Error("activating the already-active view must not move focus off the rail")
	}

	// The rail must still respond, or the UI is dead.
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyDown})
	app = m.(App)
	if got := app.sidebar.list.Index(); got != 1 {
		t.Errorf("rail index after down = %d, want 1 — rail navigation is dead", got)
	}
}

// Activating a DIFFERENT view still hands the keyboard to the content pane.
func TestApp_ActivatingADifferentViewFocusesContent(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyDown}) // highlight backup (2nd rail item)
	app = m.(App)
	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should emit an activate cmd")
	}
	m, _ = m.(App).Update(cmd())
	app = m.(App)
	if app.active != 1 {
		t.Fatalf("active = %d, want 1 (backup)", app.active)
	}
	if app.focus != focusContent {
		t.Error("activating a different view must move focus to content")
	}
}

// TestApp_ScrollPreviewThenEnterDivesIn reproduces the real runtime sequence the
// two tests above skip by discarding the Down cmd: scrolling the rail delivers a
// navPreviewMsg that makes the highlighted view active BEFORE Enter arrives. The
// activate handler must still hand the keyboard to the content pane.
//
// The regressed guard (i != m.active) saw the previewed view already active and
// swallowed the Enter, so diving into any view you scrolled to silently did
// nothing — the "hitting return doesn't go into the item" report.
func TestApp_ScrollPreviewThenEnterDivesIn(t *testing.T) {
	app := newTestApp(t)

	// Scroll down one and DELIVER the preview cmd, exactly as the Bubbletea
	// runtime does — this is the step the older tests dropped.
	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd == nil {
		t.Fatal("scrolling the rail should emit a preview cmd")
	}
	m, _ = m.(App).Update(cmd()) // deliver navPreviewMsg → active follows the cursor
	app = m.(App)
	if app.focus != focusSidebar {
		t.Fatalf("precondition: preview keeps focus on the rail, got %v", app.focus)
	}
	previewed := app.active
	if previewed == 0 {
		t.Fatal("precondition: preview should have moved active off the dashboard")
	}

	// Enter must now dive into the previewed (interactive) view.
	m, cmd = app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on the rail should emit an activate cmd")
	}
	m, _ = m.(App).Update(cmd()) // deliver activateMsg
	app = m.(App)
	if app.active != previewed {
		t.Fatalf("active = %d, want %d (the previewed view)", app.active, previewed)
	}
	if app.focus != focusContent {
		t.Fatal("enter must dive into the previewed view — focus should move to content")
	}
}

// TestApp_ArrowsNeverDead is the rule behind two "the UI froze" reports.
//
// Activating a view moves focus to the content pane, and ↑/↓ then go only to
// that view. Eight views never use arrows (dashboard, check, doctor, backup,
// prune, sync, password, unlock) and six more use them only when they have rows.
// In those states the arrows were silently dropped AND the rail stopped
// navigating, so the only way out was `tab` — a status-bar hint nobody reads.
//
// The rule: an arrow key must never do nothing. If the focused view cannot use
// it, the rail takes it, and focus follows so that enter stays coherent.
func TestApp_ArrowsNeverDead(t *testing.T) {
	r := newFlowRepo(t)

	// Dashboard: a static readout that never uses arrows.
	app := NewApp(Deps{RepoName: "x", Repo: r})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = m.(App)
	app.focus = focusContent // as if the user had activated it

	before := app.sidebar.list.Index()
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyDown})
	app = m.(App)
	if app.sidebar.list.Index() != before+1 {
		t.Errorf("down on an inert view must move the rail: index %d -> %d",
			before, app.sidebar.list.Index())
	}
	if app.focus != focusSidebar {
		t.Error("focus must follow the arrow back to the rail, or enter targets the wrong pane")
	}
}

// A view that CAN use the arrows still gets them, and the rail stays put.
func TestApp_ArrowsReachAViewThatUsesThem(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)
	seedSnapshotReal(t, r)

	app := NewApp(Deps{RepoName: "x", Repo: r})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = m.(App)
	for i, v := range app.views {
		if v.id == "snapshots" {
			app.active = i
		}
	}
	app.focus = focusContent
	railBefore := app.sidebar.list.Index()

	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyDown})
	app = m.(App)

	if app.focus != focusContent {
		t.Error("a view that consumes arrows must keep focus")
	}
	if app.sidebar.list.Index() != railBefore {
		t.Error("the rail must not move when the view consumed the arrow")
	}
	snaps := app.views[app.active].model.(Snapshots)
	if snaps.tbl.Cursor() != 1 {
		t.Errorf("snapshots table cursor = %d, want 1", snaps.tbl.Cursor())
	}
}

// The same view with NO rows cannot use the arrow, so the rail takes it.
func TestApp_ArrowsFallBackWhenAViewHasNoRows(t *testing.T) {
	r := newFlowRepo(t) // zero snapshots, exactly the reported repo state
	app := NewApp(Deps{RepoName: "x", Repo: r})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = m.(App)
	for i, v := range app.views {
		if v.id == "snapshots" {
			app.active = i
		}
	}
	app.focus = focusContent
	before := app.sidebar.list.Index()

	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyDown})
	app = m.(App)
	if app.sidebar.list.Index() == before {
		t.Error("an empty snapshots table cannot use the arrow; the rail must take it")
	}
}

// Backup sits directly under Dashboard in the rail, ahead of the read-only
// views: taking a backup is the thing an operator reaches for most, and
// registration order IS rail order.
func TestApp_BackupSitsUnderDashboard(t *testing.T) {
	app := newTestApp(t)
	want := []string{"dashboard", "backup", "snapshots"}
	for i, id := range want {
		if app.views[i].id != id {
			t.Fatalf("rail position %d = %q, want %q", i, app.views[i].id, id)
		}
	}
}

// The picker is reached through the shell, so the arrow routing added for the
// frozen-rail bug must hand ↑/↓ to it rather than to the nav rail.
func TestApp_ArrowsReachTheBackupFolderPicker(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = m.(App)
	for i, v := range app.views {
		if v.id == "backup" {
			app.active = i
		}
	}
	app.focus = focusContent
	railBefore := app.sidebar.list.Index()

	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyDown})
	app = m.(App)

	if app.sidebar.list.Index() != railBefore {
		t.Error("the picker consumes arrows; the rail must not move")
	}
	if app.focus != focusContent {
		t.Error("focus must stay on the picker")
	}
	if got := app.views[app.active].model.(BackupView).picker.cursor; got != 1 {
		t.Errorf("picker cursor = %d, want 1 — the arrow never arrived", got)
	}
}

// activate puts the App on view id with the content pane focused.
func focusView(t *testing.T, app App, id string) App {
	t.Helper()
	for i, v := range app.views {
		if v.id == id {
			app.active = i
			app.focus = focusContent
			return app
		}
	}
	t.Fatalf("no view %q", id)
	return app
}

// TestApp_EscapeLeavesATextField pins the original trap: a text field on Backup
// or Password used to swallow every key but ctrl+c. esc must always be a way back
// to the rail — now directly, with no leave confirm.
func TestApp_EscapeLeavesATextField(t *testing.T) {
	r := newFlowRepo(t)

	t.Run("backup tag field", func(t *testing.T) {
		app := focusView(t, sizedApp(t, r), "backup")
		m, _ := app.Update(tea.KeyMsg{Type: tea.KeyTab}) // into the tag field
		app = m.(App)
		if !app.contentCapturesText() {
			t.Fatal("precondition: the tag field must capture text")
		}
		m, _ = app.Update(tea.KeyMsg{Type: tea.KeyEsc}) // esc leaves straight to the rail
		app = m.(App)
		if len(app.modals) != 0 {
			t.Errorf("esc must not pop a confirm, modals=%d", len(app.modals))
		}
		if app.focus != focusSidebar {
			t.Error("esc must return focus to the rail")
		}
	})

	t.Run("password field", func(t *testing.T) {
		app := focusView(t, sizedApp(t, r), "password")
		if !app.contentCapturesText() {
			t.Fatal("precondition: the passphrase field must capture text")
		}
		m, _ := app.Update(tea.KeyMsg{Type: tea.KeyEsc})
		app = m.(App)
		if len(app.modals) != 0 {
			t.Errorf("esc must not pop a confirm, modals=%d", len(app.modals))
		}
		if app.focus != focusSidebar {
			t.Error("esc must return focus to the rail")
		}
	})
}

// A view that means something by esc still gets it. Cancelling a running backup
// must not be turned into "go back to the rail".
func TestApp_EscapeStillReachesAViewThatConsumesIt(t *testing.T) {
	r := newFlowRepo(t)
	app := NewApp(Deps{RepoName: "x", Repo: r})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = focusView(t, m.(App), "backup")

	// Drive the view into its running stage, where esc cancels the op.
	bv := app.views[app.active].model.(BackupView)
	bv.stage = backupRunning
	app.views[app.active].model = bv

	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEsc})
	app = m.(App)
	if app.focus != focusContent {
		t.Error("a view that consumes esc must keep focus")
	}
	if cmd == nil {
		t.Fatal("esc during a running backup must still emit cancelOpMsg")
	}
	if _, ok := cmd().(cancelOpMsg); !ok {
		t.Error("esc during a running backup must cancel the op, not leave the view")
	}
}

// ctrl+p is a control chord no text field needs, so the palette must open even
// while typing. The status bar advertises it; it has to be true.
func TestApp_PaletteOpensWhileTypingInAField(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", Repo: newFlowRepo(t)})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = focusView(t, m.(App), "password")
	if !app.contentCapturesText() {
		t.Fatal("precondition: password captures text")
	}
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if !m.(App).paletteOpen {
		t.Error("ctrl+p must open the palette even while a text field has focus")
	}
}

// The status bar must not advertise keys the current state swallows.
func TestApp_StatusBarTellsTheTruthWhileTyping(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", Repo: newFlowRepo(t)})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	app = focusView(t, m.(App), "password")

	out := app.View()
	for _, promise := range []string{"esc", "ctrl+p", "ctrl+c"} {
		if !strings.Contains(out, promise) {
			t.Errorf("status bar must advertise %q while typing:\n%s", promise, lastLine(out))
		}
	}
	for _, lie := range []string{"tab focus", "? help", "q quit"} {
		if strings.Contains(out, lie) {
			t.Errorf("status bar must not advertise %q — the field swallows it:\n%s", lie, lastLine(out))
		}
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// The bar must not print the same hint twice: Snapshots advertises "esc back"
// in its own ShortHelp, and the shell used to append its own on top.
func TestApp_StatusBarDoesNotDuplicateEsc(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", Repo: newFlowRepo(t)})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 130, Height: 30})
	app = focusView(t, m.(App), "snapshots")
	bar := lastLine(app.View())
	if n := strings.Count(bar, "esc back"); n != 1 {
		t.Errorf("status bar shows %d 'esc back' hints, want 1:\n%s", n, bar)
	}
}

// In a startup gate every key routes into the gate view, so 'q' is typed into
// the unlock passphrase field, not a quit. Only ctrl+c quits. The gate status
// bar must say so — it advertised "q quit", a key that does not work there.
func TestApp_GateStatusBarAdvertisesCtrlCNotQ(t *testing.T) {
	for _, initial := range []string{"unlock", "setup"} {
		t.Run(initial, func(t *testing.T) {
			app := NewApp(Deps{RepoName: "x", InitialView: initial}) // nil repo → gate
			m, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
			app = m.(App)
			if !app.inStartupGate() {
				t.Fatalf("precondition: %q must be a startup gate", initial)
			}
			bar := lastLine(app.View())
			if !strings.Contains(bar, "ctrl+c") {
				t.Errorf("gate bar must advertise ctrl+c to quit:\n%s", bar)
			}
			if strings.Contains(bar, "q quit") {
				t.Errorf("gate bar must not advertise 'q quit' — q is typed into the field:\n%s", bar)
			}
		})
	}
}

// sizedApp builds an App on repo r with a real window size.
func sizedApp(t *testing.T, r *repo.Repo) App {
	t.Helper()
	app := NewApp(Deps{RepoName: "x", Repo: r})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m.(App)
}

// Escaping a data-entry screen returns straight to the rail — no confirm. The
// only guarded action left in the shell is quit.
func TestApp_EscFromDataEntryScreenReturnsToRail(t *testing.T) {
	app := focusView(t, sizedApp(t, newFlowRepo(t)), "backup") // backupConfigure = data entry
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyEsc})
	app = m.(App)
	if len(app.modals) != 0 {
		t.Errorf("esc must not pop a confirm modal, modals=%d", len(app.modals))
	}
	if app.focus != focusSidebar {
		t.Error("esc on a data-entry screen must return focus to the rail")
	}
}

// A read-only screen escapes to the rail the same way — proving the data-entry
// special-case is gone, not just bypassed.
func TestApp_EscFromReadOnlyScreenReturnsToRail(t *testing.T) {
	app := focusView(t, sizedApp(t, newFlowRepo(t)), "snapshots")
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyEsc})
	app = m.(App)
	if len(app.modals) != 0 {
		t.Error("a read-only screen must not confirm on esc")
	}
	if app.focus != focusSidebar {
		t.Error("a read-only screen must escape straight to the rail")
	}
}

// Escaping a running operation cancels it immediately — no confirm modal.
func TestApp_EscDuringRunningOpCancelsImmediately(t *testing.T) {
	app := focusView(t, sizedApp(t, newFlowRepo(t)), "backup")
	// Simulate a running backup: the view is in its running stage and the App
	// holds the op guard with a cancel hook we can observe.
	bv := app.views[app.active].model.(BackupView)
	bv.stage = backupRunning
	app.views[app.active].model = bv
	app.opRunning = "backup"
	canceled := false
	app.opCancel = func() { canceled = true }

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyEsc})
	app = m.(App)
	if len(app.modals) != 0 {
		t.Errorf("esc during a running op must not pop a modal, modals=%d", len(app.modals))
	}
	if !canceled {
		t.Error("esc during a running op must cancel it immediately")
	}
}

// TestApp_DataViewsRefreshAfterBackup locks the wiring the per-view reload
// tests can't reach (a view cannot test the shell): a completed backup,
// broadcast through the App as an opResultMsg, must land on the dashboard and
// snapshots views so a snapshot taken this session appears without a restart.
func TestApp_DataViewsRefreshAfterBackup(t *testing.T) {
	r := newFlowRepo(t)
	app := NewApp(Deps{Repo: r, RepoName: "test"})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = m.(App)

	seedTaggedSnaps(t, r, "nightly") // a backup lands in the repo

	m, cmd := app.Update(backupDoneMsg{}) // the flow's terminal op result
	app = m.(App)
	for _, msg := range execCmds(t, cmd) { // run the async view reloads
		m, _ = app.Update(msg)
		app = m.(App)
	}

	var dash Dashboard
	var snaps Snapshots
	for _, v := range app.views {
		switch model := v.model.(type) {
		case Dashboard:
			dash = model
		case Snapshots:
			snaps = model
		}
	}
	if dash.data.SnapshotCount != 1 {
		t.Errorf("dashboard did not refresh through the shell: count = %d, want 1", dash.data.SnapshotCount)
	}
	if len(snaps.snaps) != 1 {
		t.Errorf("snapshots did not refresh through the shell: got %d, want 1", len(snaps.snaps))
	}
}

// TestApp_ConnectRegisteredAsView: the connect gate is a registered view
// so InitialView routing can land on it.
func TestApp_ConnectRegisteredAsView(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	found := false
	for _, v := range app.views {
		if v.id == "connect" {
			found = true
		}
	}
	if !found {
		t.Fatal("connect view not registered in NewApp")
	}
}

// TestApp_ConnectHiddenFromRail: connect is a startup gate like unlock —
// it must never appear in the rail/palette surface.
func TestApp_ConnectHiddenFromRail(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if strings.Contains(m.(App).View(), "Connect") {
		t.Fatal("connect gate leaked into the rail")
	}
}

// TestApp_ConnectLaunchHidesRail: when the connect gate is the InitialView,
// the shell hides the rail and devotes the frame to the gate, just like
// unlock/setup. The gate view owns the full width, and the shell's global
// navigation (number jumps, tab, palette) is suppressed.
func TestApp_ConnectLaunchHidesRail(t *testing.T) {
	app := NewApp(Deps{
		RepoName:     "x",
		InitialView:  "connect",
		ConnectError: errors.New("session expired"),
	})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := m.(App).View()
	// The gate view is active and renders its content (the error message).
	if !strings.Contains(view, "Repository unreachable") {
		t.Fatal("connect gate not rendering; expected 'Repository unreachable'")
	}
	// The rail is hidden — no Dashboard title (rail shows navigable views).
	if strings.Contains(view, "Dashboard") {
		t.Fatal("rail leaked into view when connect gate is active startup gate")
	}
}
