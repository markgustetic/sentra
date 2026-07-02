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
)

func newTestApp(t *testing.T) App {
	t.Helper()
	app := NewApp(Deps{RepoName: "test-repo"})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return sized.(App)
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
// supported terminal (80x20) and asserts nothing spills: every line
// fits the width and the frame fits the height. This is the guarantee
// behind resize()'s chrome arithmetic — if the budget constants drift
// from the real border/padding/gap widths, this fails instead of the
// layout silently wrapping.
func TestApp_NoOverflowAtMinSize(t *testing.T) {
	app := NewApp(Deps{RepoName: "test-repo"})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	out := m.(App).View()
	lines := strings.Split(out, "\n")
	if len(lines) > 20 {
		t.Fatalf("view is %d lines, want <= 20:\n%s", len(lines), out)
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line %d overflows: width %d > 80: %q", i, w, line)
		}
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
