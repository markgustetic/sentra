package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TestHelp_EveryCommandDescribed states the RULE, not an example: every
// navigable view describes itself, and viewDescriptions carries no key that is
// not a registered command. A view added later cannot ship undescribed, and a
// view removed later cannot leave a stale entry behind — which is exactly how
// a help screen rots.
func TestHelp_EveryCommandDescribed(t *testing.T) {
	app := NewApp(Deps{RepoName: "test-repo"})
	registered := make(map[string]bool)
	for _, c := range app.registry.Commands() {
		registered[c.ID] = true
		if strings.TrimSpace(c.Description) == "" {
			t.Errorf("command %q has no description; add one to viewDescriptions in help.go", c.ID)
		}
	}
	for id := range viewDescriptions {
		if !registered[id] {
			t.Errorf("viewDescriptions has stale key %q: no such registered command", id)
		}
	}
}

// TestHelp_DescriptionsFitTheNarrowestPane: a description that wraps would push
// the Help view past the height the shell budgeted it, and the content panel
// does not truncate an over-tall view — it overflows the frame. The budget is
// derived, not hardcoded, so it tracks a future change to minWidth or
// sidebarWidth.
func TestHelp_DescriptionsFitTheNarrowestPane(t *testing.T) {
	// At the minimum terminal size the content pane's text region is
	// minWidth - sidebarWidth - gap(1) - panel border(2); the panel's
	// Padding(0,1) takes another 2, and the Help view indents the
	// description helpDescIndent columns.
	budget := minWidth - sidebarWidth - 3 - 2 - helpDescIndent
	for id, desc := range viewDescriptions {
		if w := lipgloss.Width(desc); w > budget {
			t.Errorf("description for %q is %d columns, budget is %d: %q", id, w, budget, desc)
		}
	}
}

// helpTestRegistry builds a registry of n described commands: cmd0..cmd(n-1),
// titled "View 0".. and described "does thing 0"..
func helpTestRegistry(n int) *Registry {
	r := NewRegistry()
	for i := 0; i < n; i++ {
		r.Add(Command{
			ID:          fmt.Sprintf("cmd%d", i),
			Title:       fmt.Sprintf("View %d", i),
			Category:    "Views",
			Description: fmt.Sprintf("does thing %d", i),
		})
	}
	return r
}

// TestHelpView_ListsCommandsInRailOrder: the view renders each command's title
// and description, in registration order — the same order the rail draws.
func TestHelpView_ListsCommandsInRailOrder(t *testing.T) {
	v := NewHelpView(helpTestRegistry(3))
	out := v.View()
	for i := 0; i < 3; i++ {
		if !strings.Contains(out, fmt.Sprintf("View %d", i)) {
			t.Errorf("missing title for cmd%d:\n%s", i, out)
		}
		if !strings.Contains(out, fmt.Sprintf("does thing %d", i)) {
			t.Errorf("missing description for cmd%d:\n%s", i, out)
		}
	}
	first, last := strings.Index(out, "View 0"), strings.Index(out, "View 2")
	if first < 0 || last < 0 || first > last {
		t.Errorf("entries are not in registration order:\n%s", out)
	}
}

// TestHelpView_TitleAndCursorClamp: Title is stable and the cursor never leaves
// [0, len(entries)-1] regardless of key spam.
func TestHelpView_TitleAndCursorClamp(t *testing.T) {
	v := NewHelpView(helpTestRegistry(3))
	if v.Title() != "Help" {
		t.Fatalf("Title = %q, want Help", v.Title())
	}
	for i := 0; i < 5; i++ {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyUp})
		v = m.(HelpView)
	}
	if v.cursor != 0 {
		t.Fatalf("cursor after up-spam = %d, want 0", v.cursor)
	}
	for i := 0; i < 5; i++ {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
		v = m.(HelpView)
	}
	if v.cursor != 2 {
		t.Fatalf("cursor after down-spam = %d, want 2", v.cursor)
	}
}

// TestHelpView_EnterActivatesHighlighted: enter emits activateMsg for the
// command under the cursor, which is how the shell already navigates.
func TestHelpView_EnterActivatesHighlighted(t *testing.T) {
	v := NewHelpView(helpTestRegistry(3))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
	v = m.(HelpView)
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter returned no command")
	}
	act, ok := cmd().(activateMsg)
	if !ok || act.id != "cmd1" {
		t.Fatalf("got %#v, want activateMsg{cmd1}", cmd())
	}
}

// TestHelpView_EmptyRegistryDoesNotPanic: NewApp builds the view models BEFORE
// it populates the registry, and a bare Deps{} test may pass nil. Neither may
// panic on render or on enter.
func TestHelpView_EmptyRegistryDoesNotPanic(t *testing.T) {
	for name, v := range map[string]HelpView{
		"nil registry":   NewHelpView(nil),
		"empty registry": NewHelpView(NewRegistry()),
	} {
		t.Run(name, func(t *testing.T) {
			_ = v.View()
			if _, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
				t.Error("enter on an empty list must emit nothing")
			}
		})
	}
}

// TestHelpView_WindowFitsHeightAndKeepsCursorVisible: 18 entries at two lines
// each cannot fit the 16-row content budget of an 80x20 terminal. The view must
// draw a window that stays within the budget AND still shows the cursor — the
// content panel overflows the frame rather than truncating an over-tall view.
func TestHelpView_WindowFitsHeightAndKeepsCursorVisible(t *testing.T) {
	const height = 16
	v := NewHelpView(helpTestRegistry(18))
	m, _ := v.Update(tea.WindowSizeMsg{Width: 59, Height: height})
	v = m.(HelpView)

	for i := 0; i < 17; i++ { // walk to the last entry
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown})
		v = m.(HelpView)
	}
	out := v.View()
	if got := len(strings.Split(out, "\n")); got > height {
		t.Errorf("rendered %d lines, budget is %d:\n%s", got, height, out)
	}
	if !strings.Contains(out, "View 17") {
		t.Errorf("cursor entry scrolled out of the window:\n%s", out)
	}
	if strings.Contains(out, "View 0") {
		t.Errorf("window did not scroll: the first entry is still drawn:\n%s", out)
	}
}

// TestApp_HelpIsLastRailEntry: the Help entry sits at the BOTTOM of the rail.
// Registration order is rail order, so this pins the position, not just the
// presence.
func TestApp_HelpIsLastRailEntry(t *testing.T) {
	app := NewApp(Deps{RepoName: "test-repo"})
	cmds := app.registry.Commands()
	if len(cmds) == 0 {
		t.Fatal("no commands registered")
	}
	if got := cmds[len(cmds)-1].ID; got != "help" {
		t.Errorf("last rail entry = %q, want help", got)
	}
	if got := app.views[len(app.views)-1].id; got != "help" {
		t.Errorf("last view = %q, want help", got)
	}
}

// TestApp_ActivateHelpRendersDescriptions: activating Help from the shell
// switches the content pane to it and the frame shows other views' descriptions.
// A view-level test cannot catch this — key routing and activation live in App.
func TestApp_ActivateHelpRendersDescriptions(t *testing.T) {
	app := NewApp(Deps{RepoName: "test-repo"})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	app = m.(App)

	m, _ = app.Update(activateMsg{id: "help"})
	app = m.(App)

	if got := app.views[app.active].id; got != "help" {
		t.Fatalf("active view = %q, want help", got)
	}
	out := app.View()
	if !strings.Contains(out, "What each screen does") {
		t.Errorf("help header missing from the frame:\n%s", out)
	}
	if !strings.Contains(out, viewDescriptions["backup"]) {
		t.Errorf("backup's description missing from the frame:\n%s", out)
	}
}
