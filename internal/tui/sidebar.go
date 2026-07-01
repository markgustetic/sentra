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

func (sidebarDelegate) Height() int                         { return 1 }
func (sidebarDelegate) Spacing() int                        { return 0 }
func (sidebarDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
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
