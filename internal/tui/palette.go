package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
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
	// top is the index of the first match rendered — a scroll window
	// offset. View() shows only paletteMaxResults rows, so without this
	// the cursor could walk past the visible window and Enter would
	// activate a command the user can't see. clampWindow keeps
	// [top, top+paletteMaxResults) always covering the cursor.
	top    int
	width  int
	height int

	// initBlink is the cmd ti.Focus() returned at construction, captured
	// so Init can return it. Focus() (not textinput.Blink) is the only
	// source of a REAL, tag-matched blink cmd — see unlock.go's initBlink
	// doc comment for why the bootstrap sentinel is a dead end. This
	// matters even more here: Init() has a value receiver and is called
	// fresh on every ctrl+p open, long after construction, so it can only
	// ever return what was captured once, back when Focus() actually ran.
	initBlink tea.Cmd
}

func NewPalette(registry *Registry, width, height int) Palette {
	ti := textinput.New()
	ti.Placeholder = "type a command…"
	ti.Prompt = "> "
	cmd := ti.Focus()
	p := Palette{registry: registry, input: ti, width: width, height: height, initBlink: cmd}
	p.refilter()
	return p
}

// Init starts the search field's cursor blinking. The field is constructed
// already focused (NewPalette) and never blurred for as long as the palette
// exists, so — mirroring UnlockView.Init, the same "focused from birth"
// shape — Init is where the blink schedule starts rather than a later
// Focus() transition; it returns the cmd Focus() produced back at
// construction (see initBlink's doc comment).
func (p Palette) Init() tea.Cmd { return p.initBlink }

// Reset clears the query for the next open.
func (p *Palette) Reset() {
	p.input.SetValue("")
	p.cursor = 0
	p.top = 0
	p.refilter()
}

// Query exposes the current input text (tests + status display).
func (p Palette) Query() string { return p.input.Value() }

func (p *Palette) refilter() {
	p.matches = p.registry.Filter(p.input.Value())
	if p.cursor >= len(p.matches) {
		p.cursor = 0
	}
	p.clampWindow()
}

// clampWindow keeps the scroll window [top, top+paletteMaxResults)
// covering the cursor and inside the match list. Called after any
// cursor move or refilter so View() always renders the cursor row.
func (p *Palette) clampWindow() {
	if p.cursor < p.top {
		p.top = p.cursor
	}
	if p.cursor >= p.top+paletteMaxResults {
		p.top = p.cursor - paletteMaxResults + 1
	}
	// Don't scroll past the end (leaves the last page anchored) and
	// never go negative on a short list.
	maxTop := len(p.matches) - paletteMaxResults
	if p.top > maxTop {
		p.top = maxTop
	}
	if p.top < 0 {
		p.top = 0
	}
}

func (p Palette) Update(msg tea.Msg) (Palette, tea.Cmd) {
	// The search field is always focused (see Init), so a blink tick always
	// routes — no Focused() guard needed, unlike a field that shares a view
	// with an unfocused sibling.
	if _, ok := msg.(cursor.BlinkMsg); ok {
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		return p, cmd
	}
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
				p.clampWindow()
			}
			return p, nil
		case tea.KeyDown:
			if p.cursor < len(p.matches)-1 {
				p.cursor++
				p.clampWindow()
			}
			return p, nil
		}
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	p.refilter()
	return p, cmd
}

// View renders the boxed palette: input row, then the scroll window of
// up to paletteMaxResults matches with the cursor row accented. The
// window starts at p.top (kept covering the cursor by clampWindow), so
// the highlighted row — the one Enter activates — is always on screen.
func (p Palette) View() string {
	var b strings.Builder
	b.WriteString(p.input.View())
	b.WriteString("\n")
	if len(p.matches) == 0 {
		b.WriteString(ui.Muted.Render("no matches"))
	}
	end := p.top + paletteMaxResults
	if end > len(p.matches) {
		end = len(p.matches)
	}
	for i := p.top; i < end; i++ {
		c := p.matches[i]
		b.WriteString("\n")
		// Category is styled separately and appended AFTER the row style:
		// rendering it inside the label would embed an ANSI reset that
		// terminates the row style mid-line.
		category := ""
		if c.Category != "" {
			category = "  " + ui.Muted.Render(c.Category)
		}
		if i == p.cursor {
			b.WriteString(ui.SidebarActive.Render(c.Title) + category)
		} else {
			b.WriteString(ui.SidebarItem.Render(c.Title) + category)
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
