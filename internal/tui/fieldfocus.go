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
		// Framing an already-styled textinput.View() is safe here — but for
		// a narrower reason than "it's a border". ui.FieldBox sets no
		// TEXT-level attributes (Border + BorderForeground + Padding only),
		// so Render puts SGR on the border runes and passes the content
		// through byte-for-byte; there is no outer style for the input's
		// own reset to terminate. Add a Foreground to FieldBox and that
		// stops being true — the house "never wrap an already-styled
		// string" bug comes straight back. See FieldBox's own comment.
		s = ui.FieldBox.Render(s)
	}
	return s
}
