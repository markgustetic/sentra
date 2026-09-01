package tui

import (
	"github.com/charmbracelet/bubbles/textinput"

	"github.com/markgustetic/sentra/internal/ui"
)

// boxedField renders a text input framed by ui.FieldBox when — and only
// when — it holds focus. The border IS the focus affordance: a glyph, so
// it survives NO_COLOR and the Ascii profile tests run under, unlike a
// color-only signal (the house "selection is a glyph, not a color" rule).
//
// Views with several fields side by side call this per field so exactly
// one box can ever appear; single-field views inline the same two lines
// with a site-specific comment. A caller that sizes its input from the
// pane's interior must also subtract ui.FieldBoxOverhead from that Width,
// or the framed line overflows the panel the moment it takes focus.
func boxedField(f textinput.Model) string {
	s := f.View()
	if f.Focused() {
		// Lipgloss draws border runs per line, so framing an
		// already-styled textinput.View() does not trip the "never wrap an
		// already-styled string" inline-reset bug (ModalBox precedent).
		s = ui.FieldBox.Render(s)
	}
	return s
}
