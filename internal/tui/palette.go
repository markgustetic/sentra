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
