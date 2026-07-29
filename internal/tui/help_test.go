package tui

import (
	"strings"
	"testing"

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
