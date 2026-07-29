# TUI Help View Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `Help` entry at the bottom of the TUI nav rail that lists every other screen with a one-line description of what it does, and jumps to the highlighted screen on enter.

**Architecture:** `Command` in the TUI's command registry gains a `Description` field, populated by `NewApp` from a static `viewDescriptions` map. A new `HelpView` renders `registry.Commands()` — the same list, in the same order, that drives the rail — so the two can never drift. The view is pure: no I/O, no repo access, no operation guard.

**Tech Stack:** Go 1.25, Bubbletea (`tea.Model`), Lipgloss via the repo's `internal/ui` style helpers. No new dependencies.

## Global Constraints

- Module `github.com/markgustetic/sentra`; all work is in package `tui` under `internal/tui/`.
- **TDD**: write the failing test first, run it, watch it fail *for the right reason*, then write the minimum code to pass.
- **Never wrap an already-styled string.** `ui.SelectRow(selected, label)` takes an **unstyled** label; styled fragments are appended after the call. Wrapping styled text embeds an ANSI reset that terminates the outer style mid-line.
- **Selection is a glyph, not a color.** Use `ui.SelectRow`, which prepends `▍`. Unit tests run under lipgloss's Ascii profile and emit no ANSI at all, so a color-only affordance is untestable.
- `internal/tui` must never import `internal/cli`.
- Doc comments explain *why* — rationale and failure modes. Match the surrounding density.
- Run tests with `go test ./internal/tui/...`. Before the final commit run the full gate: `just check`.
- Commit messages end with:
  ```
  Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
  ```

## File Structure

| File | Responsibility |
|---|---|
| `internal/tui/help.go` (create) | The `viewDescriptions` table and the `HelpView` model — table, cursor, windowing, render, keys |
| `internal/tui/help_test.go` (create) | Completeness/width rules for the table; behavior tests for the view |
| `internal/tui/registry.go` (modify) | `Command` gains `Description string` |
| `internal/tui/registry_test.go` (modify) | `Description` survives `Add` → `Commands` |
| `internal/tui/app.go` (modify) | Populate `Description` at registration; register the `help` view last; update the `NewApp` doc comment |
| `internal/tui/app_test.go` (modify) | Update four view-count/want-list assertions; add the rail-position and activation tests |
| `internal/tui/settings_test.go` (modify) | Update one view-count assertion |
| `CLAUDE.md`, `README.md` (modify) | Update the stated view count |

---

### Task 1: Registry carries a per-view description

Adds the data. No user-visible change yet — the field is populated and rule-tested, but nothing renders it.

**Files:**
- Create: `internal/tui/help.go`
- Create: `internal/tui/help_test.go`
- Modify: `internal/tui/registry.go` (the `Command` struct, ~line 12)
- Modify: `internal/tui/registry_test.go`
- Modify: `internal/tui/app.go` (the registration loop in `NewApp`, ~line 314)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `Command.Description string` — read by Task 2's `HelpView.View`.
  - `var viewDescriptions map[string]string` — keyed by command ID; Task 3 adds the `"help"` key.

- [ ] **Step 1: Write the failing registry test**

Append to `internal/tui/registry_test.go`:

```go
// TestRegistry_CarriesDescription: the Help view renders Command.Description,
// so the field has to survive Add -> Commands like Title and Category do.
func TestRegistry_CarriesDescription(t *testing.T) {
	r := NewRegistry()
	r.Add(Command{ID: "dashboard", Title: "Dashboard", Description: "Repo health at a glance"})
	cmds := r.Commands()
	if len(cmds) != 1 {
		t.Fatalf("len = %d, want 1", len(cmds))
	}
	if cmds[0].Description != "Repo health at a glance" {
		t.Fatalf("Description = %q, want %q", cmds[0].Description, "Repo health at a glance")
	}
}
```

- [ ] **Step 2: Run it and watch it fail for the right reason**

Run: `go test ./internal/tui/ -run TestRegistry_CarriesDescription`
Expected: **compile error** — `unknown field Description in struct literal of type Command`.

- [ ] **Step 3: Add the field**

In `internal/tui/registry.go`, inside the `Command` struct, after the `Badge` field:

```go
	// Description is the one-line "what does this screen do" text the Help
	// view renders under the title. It lives on the Command rather than in
	// the Help view so the description list and the rail are the same list,
	// read in the same order — they cannot drift apart. NewApp fills it from
	// viewDescriptions (see help.go); the sidebar and palette ignore it.
	Description string
```

- [ ] **Step 4: Run it and watch it pass**

Run: `go test ./internal/tui/ -run TestRegistry_CarriesDescription`
Expected: PASS

- [ ] **Step 5: Write the failing completeness test**

Create `internal/tui/help_test.go`:

```go
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
```

- [ ] **Step 6: Run it and watch it fail for the right reason**

Run: `go test ./internal/tui/ -run TestHelp_`
Expected: **compile error** — `undefined: viewDescriptions`, `undefined: helpDescIndent`.

- [ ] **Step 7: Create the description table**

Create `internal/tui/help.go`:

```go
package tui

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
```

- [ ] **Step 8: Run it and watch it fail for the new right reason**

Run: `go test ./internal/tui/ -run TestHelp_`
Expected: FAIL — `TestHelp_EveryCommandDescribed` reports `command "dashboard" has no description` (and one line per other command). The table exists but `NewApp` does not copy it into the registry yet. `TestHelp_DescriptionsFitTheNarrowestPane` should already PASS.

- [ ] **Step 9: Populate Description at registration**

In `internal/tui/app.go`, in `NewApp`'s registration loop, change:

```go
		registry.Add(Command{ID: v.id, Title: title, Category: cat})
```

to:

```go
		registry.Add(Command{ID: v.id, Title: title, Category: cat,
			Description: viewDescriptions[v.id]})
```

- [ ] **Step 10: Run it and watch it pass**

Run: `go test ./internal/tui/ -run 'TestHelp_|TestRegistry_'`
Expected: PASS

- [ ] **Step 11: Prove the completeness test can actually fail**

Temporarily delete the `"prune"` line from `viewDescriptions` and run:

Run: `go test ./internal/tui/ -run TestHelp_EveryCommandDescribed`
Expected: FAIL with `command "prune" has no description`.

Then temporarily add `"nonesuch": "x",` to the map and run the same command.
Expected: FAIL with `viewDescriptions has stale key "nonesuch"`.

**Restore both edits** and re-run to confirm PASS. A rule test that cannot fail is not a rule test.

- [ ] **Step 12: Run the whole package and commit**

Run: `go test ./internal/tui/...`
Expected: PASS

```bash
git add internal/tui/help.go internal/tui/help_test.go internal/tui/registry.go internal/tui/registry_test.go internal/tui/app.go
git commit -m "$(cat <<'EOF'
feat(tui): carry a per-view description on registry commands

Adds Command.Description and the viewDescriptions table that fills it,
so the Help view can render the rail's own list rather than a parallel
one. Rule-tested: every registered command must be described, and no
description may name a command that isn't registered.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: The HelpView model

Builds the view in isolation, driven by a registry constructed in the test. Not yet reachable from the shell.

**Files:**
- Modify: `internal/tui/help.go`
- Modify: `internal/tui/help_test.go`

**Interfaces:**
- Consumes: `Command.Description` and `helpDescIndent` from Task 1.
- Produces:
  - `func NewHelpView(registry *Registry) HelpView` — Task 3 calls this in `NewApp`.
  - `HelpView` implements `tea.Model` (`Init() tea.Cmd`, `Update(tea.Msg) (tea.Model, tea.Cmd)`, `View() string`), plus `Title() string`, `ShortHelp() []key.Binding`, and `ConsumesArrows() bool`.
  - Enter emits the existing `activateMsg{id string}`, which the App already routes.

- [ ] **Step 1: Write the failing behavior tests**

Append to `internal/tui/help_test.go`:

```go
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
```

Add the imports this file now needs — the import block becomes:

```go
import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)
```

- [ ] **Step 2: Run them and watch them fail for the right reason**

Run: `go test ./internal/tui/ -run TestHelpView_`
Expected: **compile error** — `undefined: NewHelpView`, `undefined: HelpView`.

- [ ] **Step 3: Implement the view**

Append to `internal/tui/help.go` (and extend its import block to `fmt`, `strings`, `github.com/charmbracelet/bubbles/key`, `tea "github.com/charmbracelet/bubbletea"`, `github.com/markgustetic/sentra/internal/ui`):

```go
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
```

- [ ] **Step 4: Run them and watch them pass**

Run: `go test ./internal/tui/ -run TestHelpView_ -v`
Expected: PASS — all five tests.

- [ ] **Step 5: Run the whole package and commit**

Run: `go test ./internal/tui/...`
Expected: PASS

```bash
git add internal/tui/help.go internal/tui/help_test.go
git commit -m "$(cat <<'EOF'
feat(tui): add the HelpView model

Renders the registry's commands with their descriptions in rail order,
with a cursor and enter-to-jump. The visible window is a pure function
of cursor and height, so the view never outgrows the content budget and
there is no scroll offset to keep in sync.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Register Help at the bottom of the rail

Wires the view into the shell, updates the five existing view-count assertions, and corrects the three places that state the old count.

**Files:**
- Modify: `internal/tui/app.go` (the `views` slice and `NewApp`'s doc comment, ~lines 229-286)
- Modify: `internal/tui/help.go` (add the `"help"` key to `viewDescriptions`)
- Modify: `internal/tui/help_test.go`
- Modify: `internal/tui/app_test.go` (lines ~119, ~953, and two `want` slices at ~1035 and ~1068)
- Modify: `internal/tui/settings_test.go` (line ~132)
- Modify: `CLAUDE.md` (line 16), `README.md` (line 15)

**Interfaces:**
- Consumes: `NewHelpView(registry *Registry) HelpView` from Task 2; `viewDescriptions` from Task 1.
- Produces: a registered command with ID `"help"`, last in both `app.views` and `app.registry.Commands()`.

- [ ] **Step 1: Write the failing shell tests**

Append to `internal/tui/help_test.go`:

```go
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
```

- [ ] **Step 2: Run them and watch them fail for the right reason**

Run: `go test ./internal/tui/ -run 'TestApp_HelpIsLastRailEntry|TestApp_ActivateHelpRendersDescriptions'`
Expected: FAIL — `last rail entry = "setup", want help`, and `active view = "dashboard", want help` (the `activateMsg` finds no matching view, so `active` never moves).

- [ ] **Step 3: Register the view and describe it**

In `internal/tui/app.go`, append to the end of the `views` slice literal in `NewApp`, after the `setup` entry:

```go
		// Help sits last so it renders at the BOTTOM of the rail: it is the
		// screen you reach for when you do not know which of the others you
		// want, not one you visit in the course of a backup.
		{id: "help", model: NewHelpView(registry)},
```

`registry` is already in scope — `NewApp` constructs it immediately above the slice. `HelpView` holds the pointer and reads it at render time, so the fact that the registration loop below has not run yet does not matter.

In `internal/tui/help.go`, add the final entry to `viewDescriptions`:

```go
	"help":         "What each screen in the rail does",
```

- [ ] **Step 4: Run them and watch them pass**

Run: `go test ./internal/tui/ -run 'TestApp_HelpIsLastRailEntry|TestApp_ActivateHelpRendersDescriptions|TestHelp_'`
Expected: PASS

- [ ] **Step 5: Update the five existing view-count assertions**

The shell now has 19 views (18 navigable + the hidden `unlock` gate). Five assertions still say 18:

In `internal/tui/app_test.go`, in `TestApp_OperationsRegisteredAndRunningIndicatorEndToEnd` (~line 119):

```go
	if got := len(app.views); got != 19 {
		t.Fatalf("views = %d, want 19 (3 read-only + files + check + doctor + recovery-kit + policies + schedule + agent + 3 operations + sync + password + unlock + settings + setup + help)", got)
	}
```

In `internal/tui/app_test.go`, in `TestApp_CheckReplacesOperationsInSidebar` (~line 953):

```go
	if got := len(app.views); got != 19 {
		t.Fatalf("views = %d, want 19 (Phase 2c end-state + files + the unlock gate + settings + setup + help)", got)
	}
}
```

In `internal/tui/app_test.go`, in `TestApp_Phase2cViewsRegistered` (~line 1035), append `"help"` to the `want` slice:

```go
	want := []string{
		"dashboard", "snapshots", "files", "diff", "check", "doctor", "recovery-kit",
		"policies", "schedule", "agent", "backup", "restore", "prune",
		"sync", "password", "unlock", "settings", "setup", "help",
	}
```

In `internal/tui/app_test.go`, in `TestApp_Phase3ViewsRegistered` (~line 1068), append `"help"` to that `want` slice too:

```go
	want := []string{
		"dashboard", "snapshots", "files", "diff", "check", "doctor", "recovery-kit",
		"policies", "schedule", "agent", "backup", "restore", "prune",
		"sync", "password", "setup", "settings", "unlock", "help",
	}
```

In `internal/tui/settings_test.go`, in `TestApp_SetupAndSettingsRegistered` (~line 132):

```go
	if got := len(app.views); got != 19 {
		t.Fatalf("views = %d, want 19 (15 Phase 2c+unlock + setup + settings + files + help)", got)
	}
```

- [ ] **Step 6: Run the whole package**

Run: `go test ./internal/tui/...`
Expected: PASS. `TestSmoke_EveryViewFitsTheFrame` discovers views from `app.views`, so it now walks Help too and asserts the frame is exactly 40 lines with nothing over 100 columns — that is the integration backstop on the windowing.

- [ ] **Step 7: Correct the three stated view counts**

In `internal/tui/app.go`, `NewApp`'s doc comment (~lines 229-235) currently reads:

```go
// NewApp constructs the shell with all 18 views — 17 navigable commands plus
// the unlock startup gate: the original read-only views (dashboard, snapshots,
// files, diff), the async-check views (check, doctor), the management views
// (recovery-kit, policies, schedule), the agent view (which now also hosts
// agent-apply in place, so it gets no separate id), the direct data operations
// (backup, restore, prune, sync, password), the "Settings" category (setup,
// settings), and the unlock gate.
```

Replace it with:

```go
// NewApp constructs the shell with all 19 views — 18 navigable commands plus
// the unlock startup gate: the original read-only views (dashboard, snapshots,
// files, diff), the async-check views (check, doctor), the management views
// (recovery-kit, policies, schedule), the agent view (which now also hosts
// agent-apply in place, so it gets no separate id), the direct data operations
// (backup, restore, prune, sync, password), the "Settings" category (setup,
// settings), the Help directory of the other screens, and the unlock gate.
```

In `CLAUDE.md`, line 16:

```
also operable from the TUI (19 views).
```

In `README.md`, line 15:

```
<sub>The default surface is a full-screen TUI — 19 views, a first-run wizard, and every CLI capability at your fingertips.</sub>
```

- [ ] **Step 8: Run the full local gate**

Run: `just check`
Expected: PASS — build, test, vet, lint, vuln, `go mod tidy -diff`, gofmt, and `git diff --check` all clean.

If `just check` is unavailable, run the equivalents: `go build ./...`, `go test -race ./internal/tui/...`, `go vet ./...`, `gofmt -l cmd internal`, `go mod tidy -diff`.

- [ ] **Step 9: Commit**

```bash
git add internal/tui/app.go internal/tui/help.go internal/tui/help_test.go internal/tui/app_test.go internal/tui/settings_test.go CLAUDE.md README.md
git commit -m "$(cat <<'EOF'
feat(tui): add a Help entry at the bottom of the nav rail

Registers the Help view last, so it renders at the bottom of the rail and
in the command palette. It lists every other screen with a one-line
description and jumps to the highlighted one on enter.

Updates the five view-count assertions and the three docs that stated 18
views; the shell now has 19 (18 navigable plus the unlock gate).

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Manual verification

Automated tests render headlessly under lipgloss's Ascii profile, which strips all color and cannot exercise a real terminal's key routing. After Task 3, drive it by hand:

```bash
just local
```

Check:
1. `Help` is the last entry in the left rail.
2. Scrolling onto it with ↓ previews it live; enter moves the focus border into the content pane.
3. ↑↓ move the `▍` marker; the list scrolls once the cursor passes the middle, and the highlighted entry is always on screen.
4. Enter on an entry jumps to that screen and the rail highlight follows.
5. `esc` returns focus to the rail; `ctrl+p` then `help` also reaches it.
6. Shrink the terminal toward 80×20 — no description wraps and the panel border stays intact.
