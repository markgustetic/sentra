package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
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

// pushModalMsg asks the App to push a modal onto the stack. Flows emit
// it (e.g. prune's typed confirm) so modal ownership stays with the
// shell — the single place that routes keys modal-first.
type pushModalMsg struct{ modal Modal }

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

// --- typed confirm modal --------------------------------------------

// TypedConfirmModal is the destructive-operation gate: the user must
// type an exact word (e.g. "prune") before enter confirms. A plain
// yes/no modal is too easy to blow through for operations that delete
// data; retyping the verb forces deliberate intent — same rationale
// as the CLI's --yes-less confirmation prompts.
type TypedConfirmModal struct {
	title  string
	body   string
	word   string
	id     string
	input  textinput.Model
	width  int
	height int

	// initBlink is the cmd ti.Focus() returned at construction, captured so
	// Init can return it. Focus() (not textinput.Blink) is the only source
	// of a REAL, tag-matched blink cmd — see unlock.go's initBlink doc
	// comment for why the bootstrap sentinel is a dead end and why a
	// value-receiver Init can't recompute it itself.
	initBlink tea.Cmd
}

func NewTypedConfirmModal(title, body, word, id string, width, height int) TypedConfirmModal {
	ti := textinput.New()
	ti.Prompt = "> "
	cmd := ti.Focus()
	return TypedConfirmModal{title: title, body: body, word: word, id: id,
		input: ti, width: width, height: height, initBlink: cmd}
}

// Init starts the typed field's cursor blinking. The field is constructed
// already focused (NewTypedConfirmModal) and never blurred while the modal
// is up — esc dismisses the whole modal rather than blurring the field — so
// there is no later Focus() transition to hang the blink cmd on; it returns
// the cmd Focus() produced back at construction (see initBlink's doc
// comment). Not part of the Modal interface: the App pushes modals via
// pushModalMsg without an Init hook, so this is a plain method the
// constructor's caller (or a test) invokes directly, mirroring
// UnlockView.Init for the same "focused from birth" shape.
func (m TypedConfirmModal) Init() tea.Cmd { return m.initBlink }

func (m TypedConfirmModal) Update(msg tea.Msg) (Modal, tea.Cmd) {
	// The typed field is always focused for as long as this modal exists
	// (see Init), so a blink tick always routes.
	if _, ok := msg.(cursor.BlinkMsg); ok {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.Type {
	case tea.KeyEnter:
		if m.input.Value() != m.word {
			return m, nil // wrong word: stay, let the user see the mismatch
		}
		id := m.id
		return m, func() tea.Msg { return confirmedMsg{id: id} }
	case tea.KeyEsc:
		return m, func() tea.Msg { return dismissModalMsg{} }
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m TypedConfirmModal) View() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render(m.title))
	b.WriteString("\n\n")
	b.WriteString(m.body)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Type %s to confirm:\n", ui.Danger.Bold(true).Render(m.word))
	b.WriteString(m.input.View())
	b.WriteString("\n\n")
	b.WriteString(ui.Muted.Render("⏎ confirm · esc cancel"))
	box := ui.ModalBox.Width(min(m.width-8, 64)).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m TypedConfirmModal) SetSize(w, h int) Modal { m.width, m.height = w, h; return m }
