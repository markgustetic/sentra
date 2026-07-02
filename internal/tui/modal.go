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
	SetSize(width, height int) Modal
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

// --- info modal ------------------------------------------------------

// InfoModal is a neutral informational overlay (the `?` key reference,
// future about/detail panes). Same any-key-dismisses contract as
// ErrorModal, but with the default ModalBox accent instead of the
// error red — reaching for help must not look like something broke.
type InfoModal struct {
	title  string
	body   string
	width  int
	height int
}

func NewInfoModal(title, body string, width, height int) InfoModal {
	return InfoModal{title: title, body: body, width: width, height: height}
}

func (m InfoModal) Update(msg tea.Msg) (Modal, tea.Cmd) {
	if _, ok := msg.(tea.KeyMsg); ok {
		return m, func() tea.Msg { return dismissModalMsg{} }
	}
	return m, nil
}

func (m InfoModal) View() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render(m.title))
	b.WriteString("\n\n")
	b.WriteString(m.body)
	b.WriteString("\n\n")
	b.WriteString(ui.Muted.Render("press any key to dismiss"))
	box := ui.ModalBox.Width(min(m.width-8, 64)).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m InfoModal) SetSize(w, h int) Modal { m.width, m.height = w, h; return m }

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
