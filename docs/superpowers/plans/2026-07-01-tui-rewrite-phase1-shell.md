# TUI Rewrite Phase 1 (Shell) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild `internal/tui` around the approved shell — sidebar rail + `ctrl+p` command palette + status bar + modal stack + command registry, in the Charm-expressive theme — with the 5 existing views ported and every test green.

**Architecture:** A root `App` model owns layout, focus, overlays, and a command registry that drives both sidebar and palette. Views stay small models behind a `ViewModel` contract (`tea.Model` + `Title()` + `ShortHelp()`). The repo/agent layers are untouched; `cli/ui.go`'s `tui.NewApp(tui.Deps{...})` entrypoint keeps its signature.

**Tech Stack:** Go 1.25, bubbletea v1.x, bubbles (list, help, key, textinput), lipgloss v1.x (AdaptiveColor), huh (later phases).

Spec: `docs/superpowers/specs/2026-07-01-tui-rewrite-design.md` (this plan = Phase 1 of 3).

---

## Conventions for every task

- **Shell note:** `cat`/`tail`/`head` are aliased to `bat` in this environment — use `command tail -n N` or redirect output to a file and read it.
- **Green gate (per task):** `go build ./... && go test -race -count=1 ./internal/tui/ ./internal/ui/ && gofmt -l cmd internal && go vet ./...` — build exit 0, tests PASS, gofmt prints nothing, vet clean.
- **Full gate (Task 10):** `go test -race -count=1 ./...`, `golangci-lint run ./...`, `go mod tidy -diff`, `go test ./third_party/fastcdc-go/...`.
- **Branch:** `feature/tui-rewrite` (exists; spec is its first commit).
- **Tests:** colors are stripped when tests run without a TTY — assert on text content and structure, never on ANSI escape codes.

## File structure (Phase 1 target)

| File | Responsibility |
|---|---|
| `internal/ui/theme.go` | Charm-expressive adaptive theme: semantic styles (kept names) + new shell styles |
| `internal/tui/registry.go` | `Command` type, `Registry`, badge message |
| `internal/tui/keys.go` | Global keymap (`bubbles/key`) |
| `internal/tui/sidebar.go` | Nav rail on `bubbles/list` (compact delegate, badges) |
| `internal/tui/palette.go` | `ctrl+p` overlay: `textinput` + fuzzy-filtered registry list |
| `internal/tui/statusbar.go` | Bottom bar: `bubbles/help` short help (global + view keys) |
| `internal/tui/modal.go` | Modal stack: error modal, confirm modal (typed confirm arrives Phase 2) |
| `internal/tui/app.go` | Root model rewrite: layout, focus, routing, overlays, min-size guard |
| `internal/tui/{dashboard,snapshots,diff,agent,operations}.go` | +`Title()`, +`ShortHelp()`; internals unchanged |

---

## Task 1: Dependency bump (isolated commit)

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Bump the Charm v1 line**

```bash
cd /Users/markgustetic/Programming/portfolio/sentra
go get github.com/charmbracelet/bubbletea@v1 \
       github.com/charmbracelet/bubbles@latest \
       github.com/charmbracelet/lipgloss@v1 \
       github.com/charmbracelet/huh@latest \
       github.com/charmbracelet/harmonica@latest
go mod tidy
```

Constraint: bubbletea and lipgloss must stay on major v1 (`@v1`). If `go get` resolves bubbles/huh to versions requiring bubbletea v2, pin back to the newest versions that keep bubbletea v1 (check with `go mod graph | command grep bubbletea`).

- [ ] **Step 2: Full verification (deps changes can shift behavior anywhere)**

Run: `go build ./... && go test -race -count=1 ./... && go vet ./... && gofmt -l cmd internal && go mod tidy -diff && golangci-lint run ./...`
Expected: everything green. If a bubbles/bubbletea minor change breaks an existing TUI/UI test, fix the test to the new behavior ONLY if the behavior is equivalent; otherwise pin the dep back and note it.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "Bump Charm TUI dependencies to latest stable v1 line"
```

---

## Task 2: Charm-expressive theme system

**Files:**
- Modify: `internal/ui/theme.go` (rewrite)
- Test: `internal/ui/theme_test.go` (extend)

- [ ] **Step 1: Write failing tests**

Append to `internal/ui/theme_test.go`:

```go
// TestTheme_ShellStylesRenderContent: the new shell styles must wrap
// content without losing it. Colors are profile-dependent (stripped in
// tests); we assert content survives and structural styles differ.
func TestTheme_ShellStylesRenderContent(t *testing.T) {
	for name, st := range map[string]lipgloss.Style{
		"TitleBar":      TitleBar,
		"SidebarItem":   SidebarItem,
		"SidebarActive": SidebarActive,
		"StatusBar":     StatusBar,
		"ModalBox":      ModalBox,
		"PanelFocused":  PanelFocused,
	} {
		if got := st.Render("content"); !strings.Contains(got, "content") {
			t.Errorf("%s.Render dropped content: %q", name, got)
		}
	}
}

// TestTheme_ActiveAndInactiveSidebarDiffer: active items carry the
// "▍" accent marker so selection is visible even with colors stripped.
func TestTheme_ActiveAndInactiveSidebarDiffer(t *testing.T) {
	active := SidebarActive.Render("Dashboard")
	inactive := SidebarItem.Render("Dashboard")
	if active == inactive {
		t.Fatal("active and inactive sidebar styles render identically")
	}
}

// TestTheme_AdaptiveColorsDeclared: semantic styles must use adaptive
// colors so light terminals stay readable (spec requirement).
func TestTheme_AdaptiveColorsDeclared(t *testing.T) {
	// AccentPink etc. are the palette constants the styles derive from.
	for name, c := range map[string]lipgloss.AdaptiveColor{
		"AccentViolet": AccentViolet,
		"AccentPink":   AccentPink,
		"AccentAqua":   AccentAqua,
	} {
		if c.Dark == "" || c.Light == "" {
			t.Errorf("%s missing a variant: %+v", name, c)
		}
	}
}
```

Add `"strings"` to the test imports if missing.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/ui/ -run 'TestTheme_' -count=1`
Expected: FAIL — `undefined: TitleBar` (etc.).

- [ ] **Step 3: Rewrite `internal/ui/theme.go`**

Keep every existing exported name (`Primary`, `Success`, `Warn`, `Danger`, `Muted`, `Subtle`, `Panel`, `Severity`) — the CLI uses them — but re-point colors to the adaptive Charm-expressive palette, and add the shell styles:

```go
// Package ui provides the shared Charm-expressive theme used by both
// inline-mode CLI output and the TUI. Every exported style is safe to
// copy and tweak via lipgloss's builder API without mutating defaults.
//
// All colors are lipgloss.AdaptiveColor pairs so light-background
// terminals stay readable; lipgloss handles NO_COLOR and color-profile
// degradation automatically.
package ui

import "github.com/charmbracelet/lipgloss"

// Palette. Dark variants are the Charm-expressive rose-pine-adjacent
// tones approved in the design; Light variants are their saturated
// counterparts for white terminals.
var (
	AccentViolet = lipgloss.AdaptiveColor{Dark: "#C4A7E7", Light: "#7C3AED"}
	AccentPink   = lipgloss.AdaptiveColor{Dark: "#F6A8D8", Light: "#DB2777"}
	AccentAqua   = lipgloss.AdaptiveColor{Dark: "#9CCFD8", Light: "#0E7490"}
	GoodGreen    = lipgloss.AdaptiveColor{Dark: "#95E6B8", Light: "#059669"}
	WarnAmber    = lipgloss.AdaptiveColor{Dark: "#F6C177", Light: "#D97706"}
	BadRed       = lipgloss.AdaptiveColor{Dark: "#EB6F92", Light: "#DC2626"}
	FgMuted      = lipgloss.AdaptiveColor{Dark: "#6E6A86", Light: "#6B7280"}
	FgSubtle     = lipgloss.AdaptiveColor{Dark: "#908CAA", Light: "#9CA3AF"}
)

// Semantic styles (pre-existing names; CLI callers depend on them).
var (
	Primary = lipgloss.NewStyle().Foreground(AccentViolet).Bold(true)
	Success = lipgloss.NewStyle().Foreground(GoodGreen)
	Warn    = lipgloss.NewStyle().Foreground(WarnAmber)
	Danger  = lipgloss.NewStyle().Foreground(BadRed)
	Muted   = lipgloss.NewStyle().Foreground(FgMuted)
	Subtle  = lipgloss.NewStyle().Foreground(FgSubtle)
	Panel   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(AccentViolet).Padding(0, 1)
)

// Shell styles (TUI chrome).
var (
	// TitleBar renders the top brand bar: pink brand on a violet-
	// bordered row. (Gradients aren't native to lipgloss v1; the
	// pink-on-violet pairing is the approved approximation.)
	TitleBar = lipgloss.NewStyle().Foreground(AccentPink).Bold(true).Padding(0, 1)

	// SidebarItem / SidebarActive style nav-rail entries. The active
	// marker "▍" is part of the style contract (selection stays visible
	// with colors stripped) — the sidebar prepends it when rendering.
	SidebarItem   = lipgloss.NewStyle().Foreground(FgSubtle).PaddingLeft(2)
	SidebarActive = lipgloss.NewStyle().Foreground(AccentPink).Bold(true).
			SetString("▍ ")

	// StatusBar styles the bottom hint row.
	StatusBar = lipgloss.NewStyle().Foreground(FgMuted).Padding(0, 1)

	// ModalBox frames modal overlays (confirm / error dialogs).
	ModalBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(AccentPink).Padding(1, 2)

	// PanelFocused is Panel with the aqua focus accent, for the pane
	// that currently owns keyboard focus.
	PanelFocused = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(AccentAqua).Padding(0, 1)
)

// Severity returns the style for an agent finding's severity level.
// Unrecognized levels (including "") map to Muted so callers can pass
// raw values without special-casing.
func Severity(level string) lipgloss.Style {
	switch level {
	case "critical":
		return Danger.Bold(true)
	case "warn":
		return Warn
	case "info":
		return Subtle
	default:
		return Muted
	}
}
```

Note `SidebarActive` uses `SetString("▍ ")` so `Render("Dashboard")` yields `▍ Dashboard` — that's what makes the differ-test pass without relying on color output.

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./internal/ui/ ./internal/tui/ ./internal/cli/`
Expected: PASS. If a CLI/TUI test asserted an old hex color, update that assertion (the semantic names kept their meaning; only hues moved).

- [ ] **Step 5: Commit**

```bash
git add internal/ui internal/tui internal/cli
git commit -m "Rebuild ui theme as adaptive Charm-expressive palette with shell styles"
```

---

## Task 3: Command registry

**Files:**
- Create: `internal/tui/registry.go`
- Test: `internal/tui/registry_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/tui/registry_test.go`:

```go
package tui

import "testing"

func testRegistry() *Registry {
	r := NewRegistry()
	r.Add(Command{ID: "dashboard", Title: "Dashboard", Category: "Views"})
	r.Add(Command{ID: "snapshots", Title: "Snapshots", Category: "Views"})
	r.Add(Command{ID: "diff", Title: "Diff", Category: "Views"})
	return r
}

func TestRegistry_OrderIsInsertionOrder(t *testing.T) {
	r := testRegistry()
	cmds := r.Commands()
	if len(cmds) != 3 {
		t.Fatalf("len = %d, want 3", len(cmds))
	}
	if cmds[0].ID != "dashboard" || cmds[2].ID != "diff" {
		t.Fatalf("order not preserved: %+v", cmds)
	}
}

func TestRegistry_DuplicateIDPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate command ID")
		}
	}()
	r := testRegistry()
	r.Add(Command{ID: "dashboard", Title: "Again"})
}

// TestRegistry_FuzzyFilter: the palette's matcher. Case-insensitive
// subsequence match over Title; empty query returns everything.
func TestRegistry_FuzzyFilter(t *testing.T) {
	r := testRegistry()
	if got := r.Filter(""); len(got) != 3 {
		t.Fatalf("empty query: got %d, want 3", len(got))
	}
	if got := r.Filter("dsh"); len(got) != 1 || got[0].ID != "dashboard" {
		t.Fatalf("subsequence 'dsh': got %+v", got)
	}
	if got := r.Filter("SNAP"); len(got) != 1 || got[0].ID != "snapshots" {
		t.Fatalf("case-insensitive 'SNAP': got %+v", got)
	}
	if got := r.Filter("zzz"); len(got) != 0 {
		t.Fatalf("no-match query: got %+v", got)
	}
}

func TestRegistry_SetBadge(t *testing.T) {
	r := testRegistry()
	r.SetBadge("snapshots", "142")
	for _, c := range r.Commands() {
		if c.ID == "snapshots" && c.Badge != "142" {
			t.Fatalf("badge not set: %+v", c)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run 'TestRegistry_' -count=1`
Expected: FAIL — `undefined: Registry`.

- [ ] **Step 3: Implement `internal/tui/registry.go`**

```go
package tui

import (
	"fmt"
	"strings"
)

// Command is one entry in the app-wide command registry: a navigable
// view (Phase 1) or an executable action (Phase 2). The registry is
// the single source of truth for both the sidebar and the palette, so
// the two can never drift.
type Command struct {
	// ID is the stable identifier ("dashboard", "prune"). Unique.
	ID string
	// Title is the human label shown in sidebar and palette.
	Title string
	// Category groups palette results ("Views", "Operations").
	Category string
	// Badge is a short live annotation rendered after the title in
	// the sidebar (e.g. agent findings count). Empty hides it.
	Badge string
}

// badgeMsg updates a command's badge. Views emit it from their Update
// (e.g. the agent view after a scan completes); App routes it to the
// registry so the sidebar repaints with the new count.
type badgeMsg struct {
	id    string
	badge string
}

// Registry holds the ordered command list. Not concurrency-safe by
// design: all mutation happens on the Bubbletea update loop.
type Registry struct {
	order []string
	byID  map[string]*Command
}

func NewRegistry() *Registry {
	return &Registry{byID: make(map[string]*Command)}
}

// Add registers a command. Duplicate IDs are a programmer error —
// they'd silently shadow a sidebar entry — so Add panics.
func (r *Registry) Add(c Command) {
	if _, dup := r.byID[c.ID]; dup {
		panic(fmt.Sprintf("tui: duplicate command ID %q", c.ID))
	}
	r.order = append(r.order, c.ID)
	cc := c
	r.byID[c.ID] = &cc
}

// Commands returns the commands in registration order (copies).
func (r *Registry) Commands() []Command {
	out := make([]Command, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, *r.byID[id])
	}
	return out
}

// Filter returns commands whose Title matches query as a case-
// insensitive subsequence. Empty query matches everything. This is
// the palette's matcher — deliberately simple, no external dep.
func (r *Registry) Filter(query string) []Command {
	if query == "" {
		return r.Commands()
	}
	q := strings.ToLower(query)
	var out []Command
	for _, id := range r.order {
		c := r.byID[id]
		if isSubsequence(q, strings.ToLower(c.Title)) {
			out = append(out, *c)
		}
	}
	return out
}

// SetBadge updates a command's badge; unknown IDs are ignored (a view
// may emit a badge before registration in tests).
func (r *Registry) SetBadge(id, badge string) {
	if c, ok := r.byID[id]; ok {
		c.Badge = badge
	}
}

// isSubsequence reports whether every rune of needle appears in order
// within haystack.
func isSubsequence(needle, haystack string) bool {
	if needle == "" {
		return true
	}
	n := []rune(needle)
	i := 0
	for _, h := range haystack {
		if h == n[i] {
			i++
			if i == len(n) {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./internal/tui/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/registry.go internal/tui/registry_test.go
git commit -m "Add TUI command registry with fuzzy filter and badges"
```

---

## Task 4: Global keymap

**Files:**
- Create: `internal/tui/keys.go`
- Test: covered via statusbar/app tests (pure data, no logic)

- [ ] **Step 1: Implement `internal/tui/keys.go`**

```go
package tui

import "github.com/charmbracelet/bubbles/key"

// globalKeys are the shell-level bindings that work regardless of the
// active view (subject to overlay focus: the palette and modals see
// keys first so typing isn't hijacked).
type globalKeymap struct {
	Palette key.Binding
	Focus   key.Binding
	Help    key.Binding
	Quit    key.Binding
}

func newGlobalKeymap() globalKeymap {
	return globalKeymap{
		Palette: key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "palette")),
		Focus:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "focus")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// shortHelp is the always-visible subset for the status bar.
func (k globalKeymap) shortHelp() []key.Binding {
	return []key.Binding{k.Palette, k.Focus, k.Help, k.Quit}
}
```

- [ ] **Step 2: Build check**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 3: Commit**

```bash
git add internal/tui/keys.go
git commit -m "Add global TUI keymap"
```

---

## Task 5: Sidebar rail

**Files:**
- Create: `internal/tui/sidebar.go`
- Test: `internal/tui/sidebar_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/tui/sidebar_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testSidebar() Sidebar {
	return NewSidebar(testRegistry(), 18, 12)
}

func TestSidebar_RendersAllTitles(t *testing.T) {
	s := testSidebar()
	out := s.View()
	for _, want := range []string{"Dashboard", "Snapshots", "Diff"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing %q:\n%s", want, out)
		}
	}
}

func TestSidebar_ArrowMovesSelectionAndEnterActivates(t *testing.T) {
	s := testSidebar()
	s, _ = s.Update(tea.KeyMsg{Type: tea.KeyDown})
	s, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a sidebar item must produce a command")
	}
	msg := cmd()
	act, ok := msg.(activateMsg)
	if !ok {
		t.Fatalf("expected activateMsg, got %T", msg)
	}
	if act.id != "snapshots" {
		t.Fatalf("activated %q, want snapshots", act.id)
	}
}

func TestSidebar_BadgeVisible(t *testing.T) {
	r := testRegistry()
	r.SetBadge("diff", "3")
	s := NewSidebar(r, 18, 12)
	s.Refresh()
	if !strings.Contains(s.View(), "3") {
		t.Errorf("badge not rendered:\n%s", s.View())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run 'TestSidebar_' -count=1`
Expected: FAIL — `undefined: NewSidebar`.

- [ ] **Step 3: Implement `internal/tui/sidebar.go`**

```go
package tui

import (
	"fmt"
	"io"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/ui"
)

// activateMsg is emitted when the user activates a command from the
// sidebar or the palette. App routes it to view navigation (Phase 1)
// or action launch (Phase 2).
type activateMsg struct{ id string }

// sidebarItem adapts a Command to bubbles/list.
type sidebarItem struct{ cmd Command }

func (i sidebarItem) FilterValue() string { return i.cmd.Title }

// sidebarDelegate renders one compact row: active rows get the "▍"
// accent via ui.SidebarActive, inactive rows ui.SidebarItem. Badges
// render dimmed after the title.
type sidebarDelegate struct{}

func (sidebarDelegate) Height() int                             { return 1 }
func (sidebarDelegate) Spacing() int                            { return 0 }
func (sidebarDelegate) Update(tea.Msg, *list.Model) tea.Cmd     { return nil }
func (sidebarDelegate) Render(w io.Writer, m list.Model, index int, it list.Item) {
	si, ok := it.(sidebarItem)
	if !ok {
		return
	}
	label := si.cmd.Title
	if si.cmd.Badge != "" {
		label = fmt.Sprintf("%s %s", label, ui.Muted.Render(si.cmd.Badge))
	}
	if index == m.Index() {
		fmt.Fprint(w, ui.SidebarActive.Render(label))
		return
	}
	fmt.Fprint(w, ui.SidebarItem.Render(label))
}

// Sidebar is the persistent nav rail. It renders the registry in
// order and emits activateMsg on enter. It never mutates the registry.
type Sidebar struct {
	registry *Registry
	list     list.Model
	width    int
}

func NewSidebar(registry *Registry, width, height int) Sidebar {
	l := list.New(nil, sidebarDelegate{}, width, height)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings()
	s := Sidebar{registry: registry, list: l, width: width}
	s.Refresh()
	return s
}

// Refresh re-reads the registry into the list (registration order),
// preserving the current selection index. Called after badge updates
// and at construction.
func (s *Sidebar) Refresh() {
	idx := s.list.Index()
	cmds := s.registry.Commands()
	items := make([]list.Item, len(cmds))
	for i, c := range cmds {
		items[i] = sidebarItem{cmd: c}
	}
	s.list.SetItems(items)
	if idx < len(items) {
		s.list.Select(idx)
	}
}

// Select moves the highlight to the command with the given ID (used
// when navigation happens via palette or number keys, so the rail
// tracks the active view).
func (s *Sidebar) Select(id string) {
	for i, c := range s.registry.Commands() {
		if c.ID == id {
			s.list.Select(i)
			return
		}
	}
}

func (s Sidebar) Update(msg tea.Msg) (Sidebar, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.Type == tea.KeyEnter {
		if it, ok := s.list.SelectedItem().(sidebarItem); ok {
			id := it.cmd.ID
			return s, func() tea.Msg { return activateMsg{id: id} }
		}
		return s, nil
	}
	var cmd tea.Cmd
	s.list, cmd = s.list.Update(msg)
	return s, cmd
}

func (s Sidebar) View() string { return s.list.View() }

// SetSize resizes the underlying list (called on WindowSizeMsg).
func (s *Sidebar) SetSize(width, height int) {
	s.width = width
	s.list.SetSize(width, height)
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./internal/tui/`
Expected: PASS. (If the installed bubbles version's `list.Model.Select` or delegate signature differs, adapt to the actual API — check with `go doc github.com/charmbracelet/bubbles/list Model` — keeping the test behavior identical.)

- [ ] **Step 5: Commit**

```bash
git add internal/tui/sidebar.go internal/tui/sidebar_test.go
git commit -m "Add sidebar nav rail driven by the command registry"
```

---

## Task 6: Command palette

**Files:**
- Create: `internal/tui/palette.go`
- Test: `internal/tui/palette_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/tui/palette_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func typeString(p Palette, s string) Palette {
	for _, r := range s {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return p
}

func TestPalette_TypingFiltersResults(t *testing.T) {
	p := NewPalette(testRegistry(), 60, 20)
	p = typeString(p, "snap")
	out := p.View()
	if !strings.Contains(out, "Snapshots") {
		t.Errorf("filtered result missing:\n%s", out)
	}
	if strings.Contains(out, "Dashboard") {
		t.Errorf("non-match still visible:\n%s", out)
	}
}

func TestPalette_EnterActivatesTopMatch(t *testing.T) {
	p := NewPalette(testRegistry(), 60, 20)
	p = typeString(p, "diff")
	p, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter must produce a command")
	}
	act, ok := cmd().(activateMsg)
	if !ok || act.id != "diff" {
		t.Fatalf("got %v, want activateMsg{diff}", cmd())
	}
}

func TestPalette_EnterOnNoMatchesDoesNothing(t *testing.T) {
	p := NewPalette(testRegistry(), 60, 20)
	p = typeString(p, "zzzz")
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("enter with zero matches must not activate anything")
	}
}

// TestPalette_QIsTypedNotQuit guards the focus rule: while the palette
// is open, 'q' is input, not quit — the shell must not see it.
func TestPalette_QIsTypedNotQuit(t *testing.T) {
	p := NewPalette(testRegistry(), 60, 20)
	p = typeString(p, "q")
	if got := p.Query(); got != "q" {
		t.Fatalf("query = %q, want q", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run 'TestPalette_' -count=1`
Expected: FAIL — `undefined: NewPalette`.

- [ ] **Step 3: Implement `internal/tui/palette.go`**

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/ui"
)

// paletteMaxResults caps the rendered result rows so the overlay stays
// compact on small terminals.
const paletteMaxResults = 8

// Palette is the ctrl+p fuzzy command launcher. It owns a text input
// and a cursor over the filtered registry; enter activates the
// highlighted command via activateMsg. The App opens/closes it —
// Palette itself has no visibility state.
type Palette struct {
	registry *Registry
	input    textinput.Model
	matches  []Command
	cursor   int
	width    int
	height   int
}

func NewPalette(registry *Registry, width, height int) Palette {
	ti := textinput.New()
	ti.Placeholder = "type a command…"
	ti.Prompt = "> "
	ti.Focus()
	p := Palette{registry: registry, input: ti, width: width, height: height}
	p.refilter()
	return p
}

// Reset clears the query for the next open.
func (p *Palette) Reset() {
	p.input.SetValue("")
	p.cursor = 0
	p.refilter()
}

// Query exposes the current input text (tests + status display).
func (p Palette) Query() string { return p.input.Value() }

func (p *Palette) refilter() {
	p.matches = p.registry.Filter(p.input.Value())
	if p.cursor >= len(p.matches) {
		p.cursor = 0
	}
}

func (p Palette) Update(msg tea.Msg) (Palette, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.Type {
		case tea.KeyEnter:
			if len(p.matches) == 0 {
				return p, nil
			}
			id := p.matches[p.cursor].ID
			return p, func() tea.Msg { return activateMsg{id: id} }
		case tea.KeyUp:
			if p.cursor > 0 {
				p.cursor--
			}
			return p, nil
		case tea.KeyDown:
			if p.cursor < len(p.matches)-1 {
				p.cursor++
			}
			return p, nil
		}
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	p.refilter()
	return p, cmd
}

// View renders the boxed palette: input row, then up to
// paletteMaxResults matches with the cursor row accented.
func (p Palette) View() string {
	var b strings.Builder
	b.WriteString(p.input.View())
	b.WriteString("\n")
	shown := p.matches
	if len(shown) > paletteMaxResults {
		shown = shown[:paletteMaxResults]
	}
	if len(shown) == 0 {
		b.WriteString(ui.Muted.Render("no matches"))
	}
	for i, c := range shown {
		b.WriteString("\n")
		label := c.Title
		if c.Category != "" {
			label += "  " + ui.Muted.Render(c.Category)
		}
		if i == p.cursor {
			b.WriteString(ui.SidebarActive.Render(label))
		} else {
			b.WriteString(ui.SidebarItem.Render(label))
		}
	}
	box := ui.ModalBox.Width(min(p.width-8, 64)).Render(b.String())
	return lipgloss.Place(p.width, p.height, lipgloss.Center, lipgloss.Center, box)
}

// SetSize records the screen size the overlay centers within.
func (p *Palette) SetSize(width, height int) {
	p.width = width
	p.height = height
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./internal/tui/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/palette.go internal/tui/palette_test.go
git commit -m "Add ctrl+p command palette with fuzzy registry filtering"
```

---

## Task 7: Status bar

**Files:**
- Create: `internal/tui/statusbar.go`
- Test: `internal/tui/statusbar_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/tui/statusbar_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
)

func TestStatusBar_ShowsGlobalAndViewKeys(t *testing.T) {
	sb := NewStatusBar(newGlobalKeymap(), 100)
	viewKeys := []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "open")),
	}
	out := sb.View("s3://bucket", viewKeys, "")
	for _, want := range []string{"ctrl+p", "palette", "⏎", "open", "s3://bucket"} {
		if !strings.Contains(out, want) {
			t.Errorf("status bar missing %q:\n%s", want, out)
		}
	}
}

func TestStatusBar_ShowsRunningIndicator(t *testing.T) {
	sb := NewStatusBar(newGlobalKeymap(), 100)
	out := sb.View("repo", nil, "backup running")
	if !strings.Contains(out, "backup running") {
		t.Errorf("running indicator missing:\n%s", out)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run 'TestStatusBar_' -count=1`
Expected: FAIL — `undefined: NewStatusBar`.

- [ ] **Step 3: Implement `internal/tui/statusbar.go`**

```go
package tui

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/ui"
)

// StatusBar renders the bottom row: repo identity, the running-
// operation indicator (Phase 2 populates it), and contextual key
// hints (view keys first, then global keys) via bubbles/help.
type StatusBar struct {
	keys  globalKeymap
	help  help.Model
	width int
}

func NewStatusBar(keys globalKeymap, width int) StatusBar {
	h := help.New()
	return StatusBar{keys: keys, help: h, width: width}
}

// View renders one line. viewKeys are the active view's ShortHelp
// bindings; running is the global operation indicator ("" when idle).
func (s StatusBar) View(repoLabel string, viewKeys []key.Binding, running string) string {
	bindings := append(append([]key.Binding{}, viewKeys...), s.keys.shortHelp()...)
	hints := s.help.ShortHelpView(bindings)

	left := ui.Subtle.Render(repoLabel)
	if running != "" {
		left += "  " + ui.Warn.Render("⟳ "+running)
	}
	gap := s.width - lipgloss.Width(left) - lipgloss.Width(hints) - 2
	if gap < 1 {
		gap = 1
	}
	return ui.StatusBar.Render(left + lipgloss.NewStyle().Width(gap).Render("") + hints)
}

// SetWidth resizes the bar (called on WindowSizeMsg).
func (s *StatusBar) SetWidth(w int) {
	s.width = w
	s.help.Width = w
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./internal/tui/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/statusbar.go internal/tui/statusbar_test.go
git commit -m "Add status bar with contextual key hints and running indicator"
```

---

## Task 8: Modal stack

**Files:**
- Create: `internal/tui/modal.go`
- Test: `internal/tui/modal_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/tui/modal_test.go`:

```go
package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestErrorModal_RendersMessageAndAdvice(t *testing.T) {
	m := NewErrorModal(errors.New("open repo: boom"), "Check the bucket configuration.", 80, 24)
	out := m.View()
	for _, want := range []string{"open repo: boom", "Check the bucket"} {
		if !strings.Contains(out, want) {
			t.Errorf("error modal missing %q:\n%s", want, out)
		}
	}
}

func TestErrorModal_AnyKeyDismisses(t *testing.T) {
	m := NewErrorModal(errors.New("x"), "", 80, 24)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected dismiss command")
	}
	if _, ok := cmd().(dismissModalMsg); !ok {
		t.Fatalf("expected dismissModalMsg, got %T", cmd())
	}
}

func TestConfirmModal_EscCancelsEnterConfirms(t *testing.T) {
	m := NewConfirmModal("Quit during operation?", "The running backup will be canceled.", "confirm-quit", 80, 24)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, ok := cmd().(dismissModalMsg); !ok {
		t.Fatalf("esc: expected dismissModalMsg, got %T", cmd())
	}

	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	res, ok := cmd().(confirmedMsg)
	if !ok || res.id != "confirm-quit" {
		t.Fatalf("enter: expected confirmedMsg{confirm-quit}, got %v", cmd())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run 'TestErrorModal_|TestConfirmModal_' -count=1`
Expected: FAIL — `undefined: NewErrorModal`.

- [ ] **Step 3: Implement `internal/tui/modal.go`**

```go
package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/ui"
)

// Modal is an overlay dialog. The App holds a stack; the top modal
// captures all key input until it emits dismissModalMsg (pop) or a
// result message. Phase 2 adds the typed-confirmation modal for
// destructive operations.
type Modal interface {
	Update(tea.Msg) (Modal, tea.Cmd)
	View() string
	SetSize(width, height int)
}

// dismissModalMsg pops the top modal without a result.
type dismissModalMsg struct{}

// confirmedMsg reports that the user confirmed the modal with the
// given ID. The App maps IDs to pending actions.
type confirmedMsg struct{ id string }

// --- error modal ---------------------------------------------------

// ErrorModal shows an operation error plus operator advice. Any key
// dismisses it; the app stays fully usable afterwards (spec: nothing
// panics the app).
type ErrorModal struct {
	err    error
	advice string
	width  int
	height int
}

func NewErrorModal(err error, advice string, width, height int) ErrorModal {
	return ErrorModal{err: err, advice: advice, width: width, height: height}
}

func (m ErrorModal) Update(msg tea.Msg) (Modal, tea.Cmd) {
	if _, ok := msg.(tea.KeyMsg); ok {
		return m, func() tea.Msg { return dismissModalMsg{} }
	}
	return m, nil
}

func (m ErrorModal) View() string {
	var b strings.Builder
	b.WriteString(ui.Danger.Bold(true).Render("Error"))
	b.WriteString("\n\n")
	b.WriteString(m.err.Error())
	if m.advice != "" {
		b.WriteString("\n\n")
		b.WriteString(ui.Subtle.Render(m.advice))
	}
	b.WriteString("\n\n")
	b.WriteString(ui.Muted.Render("press any key to dismiss"))
	box := ui.ModalBox.BorderForeground(ui.BadRed).Width(min(m.width-8, 64)).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m ErrorModal) SetSize(w, h int) Modal { m.width, m.height = w, h; return m }

// --- confirm modal -------------------------------------------------

// ConfirmModal is a yes/no gate: enter confirms (emitting
// confirmedMsg{id}), esc cancels. Phase 1 uses it for quit-during-
// operation; Phase 2 reuses it for mutating flows.
type ConfirmModal struct {
	title  string
	body   string
	id     string
	width  int
	height int
}

func NewConfirmModal(title, body, id string, width, height int) ConfirmModal {
	return ConfirmModal{title: title, body: body, id: id, width: width, height: height}
}

func (m ConfirmModal) Update(msg tea.Msg) (Modal, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.Type {
	case tea.KeyEnter:
		id := m.id
		return m, func() tea.Msg { return confirmedMsg{id: id} }
	case tea.KeyEsc:
		return m, func() tea.Msg { return dismissModalMsg{} }
	}
	return m, nil
}

func (m ConfirmModal) View() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render(m.title))
	b.WriteString("\n\n")
	b.WriteString(m.body)
	b.WriteString("\n\n")
	b.WriteString(ui.Muted.Render("⏎ confirm · esc cancel"))
	box := ui.ModalBox.Width(min(m.width-8, 64)).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m ConfirmModal) SetSize(w, h int) Modal { m.width, m.height = w, h; return m }
```

Note the `SetSize` signature returns `Modal` because these are value receivers; adjust the interface accordingly:

```go
type Modal interface {
	Update(tea.Msg) (Modal, tea.Cmd)
	View() string
	SetSize(width, height int) Modal
}
```

(Use this interface form; the App reassigns the returned value.)

- [ ] **Step 4: Run tests**

Run: `go test -race -count=1 ./internal/tui/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/modal.go internal/tui/modal_test.go
git commit -m "Add modal stack primitives: error and confirm dialogs"
```

---

## Task 9: App shell rewrite + view ports

This is the integration task: rewrite `app.go` around the shell, add the view contract to all 5 views, rewrite `app_test.go`.

**Files:**
- Modify: `internal/tui/app.go` (rewrite below the `Deps` type — `Deps` itself is unchanged)
- Modify: `internal/tui/dashboard.go`, `snapshots.go`, `diff.go`, `agent.go`, `operations.go` (add `Title()`/`ShortHelp()`)
- Modify: `internal/tui/app_test.go` (rewrite)
- Test: existing view tests must stay green

- [ ] **Step 1: Add the view contract to each view**

Each of the 5 views gains two methods (values chosen per view). Dashboard (`internal/tui/dashboard.go`):

```go
// Title names the view in the sidebar, palette, and title bar.
func (Dashboard) Title() string { return "Dashboard" }

// ShortHelp lists the view-specific keys for the status bar.
func (Dashboard) ShortHelp() []key.Binding { return nil }
```

Snapshots (`internal/tui/snapshots.go`):

```go
func (Snapshots) Title() string { return "Snapshots" }

func (Snapshots) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "row")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "detail")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	}
}
```

Diff (`internal/tui/diff.go`):

```go
func (Diff) Title() string { return "Diff" }

func (Diff) ShortHelp() []key.Binding { return nil }
```

Agent (`internal/tui/agent.go`):

```go
func (AgentView) Title() string { return "Agent" }

func (AgentView) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "scan")),
	}
}
```

Operations (`internal/tui/operations.go`):

```go
func (Operations) Title() string { return "Operations" }

func (Operations) ShortHelp() []key.Binding { return nil }
```

Each file adds the import `"github.com/charmbracelet/bubbles/key"`.

- [ ] **Step 2: Rewrite `internal/tui/app.go`**

Keep the package doc, the `Deps` struct, and the cleanup semantics. Replace the `View` enum, `App`, and everything below with:

```go
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
```

Imports for the rewritten `app.go`: `context`, `fmt`, `github.com/charmbracelet/bubbles/key`, `tea "github.com/charmbracelet/bubbletea"`, `github.com/charmbracelet/lipgloss`, `github.com/markgustetic/sentra/internal/agent/llm`, `github.com/markgustetic/sentra/internal/repo`, `github.com/markgustetic/sentra/internal/ui`. Delete `joinSpaces` (tab bar is gone) and the old `View` enum.

- [ ] **Step 3: Rewrite `internal/tui/app_test.go`**

Replace the old tab-key tests with shell-behavior tests:

```go
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
```

Note: `tea.KeyCtrlP` must exist in the installed bubbletea version; if the constant differs, construct the msg as `tea.KeyMsg{Type: tea.KeyCtrlP}` equivalent per `go doc github.com/charmbracelet/bubbletea Key`.

- [ ] **Step 4: Run the failing tests, implement fixes, iterate**

Run: `go test -race -count=1 ./internal/tui/`
Expected first run: compile errors until Steps 1–2 are complete, then failures to fix. Iterate until PASS. The pre-existing view tests (`dashboard_test.go`, `snapshots_test.go`, `diff_test.go`, `agent_test.go`, `operations_test.go`) must pass **unchanged** — they don't touch the shell.

- [ ] **Step 5: Verify the CLI wiring still compiles and passes**

Run: `go build ./... && go test -race -count=1 ./internal/cli/ ./cmd/...`
Expected: PASS — `cli/ui.go` uses `tui.NewApp(tui.Deps{...})` and `tea.NewProgram(app, tea.WithAltScreen())`, both unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/tui
git commit -m "Rewrite TUI shell: sidebar + palette + status bar + modals + registry"
```

---

## Task 10: Full gate + branch verification

- [ ] **Step 1: Run the complete CI-equivalent gate**

```bash
go build ./... \
 && go vet ./... \
 && gofmt -l cmd internal \
 && go test -race -count=1 ./... \
 && go test ./third_party/fastcdc-go/... \
 && go mod tidy -diff \
 && golangci-lint run ./...
```
Expected: all green; `gofmt -l` and `go mod tidy -diff` print nothing; golangci-lint "0 issues".

- [ ] **Step 2: Manual smoke test**

Run: `go build -o bin/sentra ./cmd/sentra && ./bin/sentra ui` in a real terminal (needs a configured repo, or verify the nil-deps placeholder rendering via `go run ./cmd/sentra ui` against an empty config — the app must render, navigate, open the palette, and quit cleanly).
Expected: sidebar navigation, ctrl+p palette, number jumps, `?` help hints in status bar, clean quit with `q`.

- [ ] **Step 3: Commit any final fixes**

```bash
git add -A
git commit -m "Phase 1 shell: final gate fixes" # only if fixes were needed
```

---

## Self-review notes (author)

- **Spec coverage (Phase 1 scope):** deps bump → Task 1; theme system → Task 2; registry → Task 3; global keymap → Task 4; sidebar → Task 5; palette → Task 6; status bar → Task 7; modal stack → Task 8; shell + view ports + min-size guard + focus rules → Task 9; gate → Task 10. Phase 2/3 items (operation flows, typed confirm, setup wizard, unlock screen) are intentionally not here — separate plans per the spec.
- **Type consistency:** `activateMsg`/`badgeMsg`/`dismissModalMsg`/`confirmedMsg` defined in Tasks 3/5/8 and consumed in Task 9; `Modal.SetSize` returns `Modal` (value receivers) — interface defined accordingly in Task 8.
- **API risk flagged inline:** exact bubbles `list`/`key` API details may differ slightly across versions after the Task 1 bump — Tasks 5 and 9 carry explicit "check `go doc`" notes; behavior contracts in tests are version-independent.
- **`min`:** Go 1.21+ builtin — no helper needed.
