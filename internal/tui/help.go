package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/markgustetic/sentra/internal/ui"
)

// helpDescIndent is how far the Help view indents a description under its
// title. It is also the width budget the description table is checked against
// (see TestHelp_DescriptionsFitTheNarrowestPane), so it lives here rather than
// inline in the renderer.
const helpDescIndent = 4

// viewDescriptions is the one-line "what does this screen do" text for every
// navigable view, keyed by command ID. NewApp copies each entry into the
// registry's Command.Description, so the Help view and the rail render the same
// list in the same order and cannot drift.
//
// Every registered command MUST have an entry, and no entry may name a command
// that is not registered — TestHelp_EveryCommandDescribed states that rule so a
// view added or removed later cannot silently leave the help screen wrong.
//
// Keep each line within the width budget checked by
// TestHelp_DescriptionsFitTheNarrowestPane: a wrapped description would push the
// view past the height the shell budgeted it, and the content panel overflows
// rather than truncates.
//
// gosec's G101 flags this declaration because the "password" key sits next to
// a string literal value — its heuristic for a hardcoded credential. It is a
// false positive: the key is the registered command ID for the passphrase-
// rotation view (see app.go's command table, id "password"; Settings links to
// it as targetID "password"), and every value here is Help-view display text,
// never a secret. The "no secrets in artifacts" invariant is unaffected —
// nothing derived from this map is a passphrase, key, or credential.
// Suppressed narrowly on this declaration only (not gosec package- or
// repo-wide) so a real hardcoded credential elsewhere still trips the linter.
//
//nolint:gosec // G101 false positive: help text keyed by command ID "password", not a credential
var viewDescriptions = map[string]string{
	"dashboard":    "Repo health, last snapshot, and size timeline",
	"backup":       "Snapshot a folder into the repository",
	"snapshots":    "Browse past snapshots and inspect their files",
	"files":        "Latest snapshot's directory layout as a graph",
	"diff":         "Compare two snapshots file by file",
	"check":        "Verify repository integrity end to end",
	"doctor":       "Diagnose config, AWS access, and repo health",
	"recovery-kit": "Print a non-secret kit for disaster recovery",
	"policies":     "Manage named backup policies and run them",
	"schedule":     "Install or remove OS scheduler entries",
	"agent":        "Scan for backup risks and get recommendations",
	"restore":      "Restore a snapshot to a chosen destination",
	"prune":        "Apply retention and reclaim unused storage",
	"sync":         "Replicate this repository to a second bucket",
	"password":     "Rotate the repository passphrase",
	"settings":     "Configuration summary and app preferences",
	"setup":        "Re-run the first-run configuration wizard",
}

// helpHeaderRows is the header line plus the blank line under it. The entry
// window is sized against the height left over, so the rendered block never
// exceeds the budget the shell handed us.
const helpHeaderRows = 2

// HelpView lists every navigable screen with a one-line description of what it
// does, in rail order, and jumps to the highlighted one on enter. It answers
// "what is this screen for"; the `?` modal answers "which keys work here",
// which is a different question and stays where it is.
//
// It reads the registry at RENDER time rather than snapshotting at
// construction, because NewApp builds every view model before it populates the
// registry — a snapshot taken in the constructor would always be empty. Reading
// live also means a badge update or a newly registered view needs no
// invalidation here.
//
// It performs no I/O, opens no repository, and takes no operation guard, so it
// needs no Deps — hence the constructor signature differs from its siblings'.
type HelpView struct {
	registry *Registry
	cursor   int
	width    int
	height   int
}

func NewHelpView(registry *Registry) HelpView {
	return HelpView{registry: registry}
}

func (HelpView) Init() tea.Cmd { return nil }

func (HelpView) Title() string { return "Help" }

// ConsumesArrows: the entry cursor is always present, so up/down always belong
// to this view while it holds content focus.
func (HelpView) ConsumesArrows() bool { return true }

func (HelpView) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "entry")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "open")),
	}
}

// entries returns the navigable commands in rail order. A nil registry yields
// none rather than panicking: tests construct bare views, and the App builds
// this one before the registry is filled.
func (v HelpView) entries() []Command {
	if v.registry == nil {
		return nil
	}
	return v.registry.Commands()
}

func (v HelpView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
		return v, nil

	case tea.KeyMsg:
		cmds := v.entries()
		switch msg.Type {
		case tea.KeyUp:
			if v.cursor > 0 {
				v.cursor--
			}
		case tea.KeyDown:
			if v.cursor < len(cmds)-1 {
				v.cursor++
			}
		case tea.KeyEnter:
			if v.cursor >= 0 && v.cursor < len(cmds) {
				id := cmds[v.cursor].ID
				return v, func() tea.Msg { return activateMsg{id: id} }
			}
		}
		return v, nil
	}
	return v, nil
}

func (v HelpView) View() string {
	cmds := v.entries()
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ui.Primary.Render("What each screen does"))
	start, end := v.window(len(cmds))
	indent := strings.Repeat(" ", helpDescIndent)
	for i := start; i < end; i++ {
		// SelectRow's label must be UNSTYLED — styling it here would embed an
		// ANSI reset that terminates the row style mid-line. The description is
		// styled separately, on its own line.
		fmt.Fprintf(&b, "%s\n", ui.SelectRow(i == v.cursor, cmds[i].Title))
		fmt.Fprintf(&b, "%s%s\n", indent, ui.Muted.Render(cmds[i].Description))
	}
	return strings.TrimRight(b.String(), "\n")
}

// window returns the half-open range of entries to draw: as many as fit at two
// rows each in the height the shell gave us, centered on the cursor and clamped
// to both ends.
//
// It is a pure function of (cursor, total, height) — there is deliberately no
// scroll-offset field, because an offset that must be kept in sync with the
// cursor is a second source of truth and the usual source of "the cursor is
// selected but off screen" bugs.
//
// A zero height means no WindowSizeMsg has arrived yet (headless tests, the
// first frame), where drawing everything is the right answer.
func (v HelpView) window(total int) (int, int) {
	if v.height <= 0 {
		return 0, total
	}
	visible := (v.height - helpHeaderRows) / 2
	if visible < 1 {
		visible = 1
	}
	if visible >= total {
		return 0, total
	}
	start := v.cursor - visible/2
	if start < 0 {
		start = 0
	}
	if start > total-visible {
		start = total - visible
	}
	return start, start + visible
}
