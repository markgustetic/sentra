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
// Every view renders every text field through this — one field or four —
// so the frame can only ever come from Focused(), never from a stage flag
// that merely decides the field is on screen. The two used to be allowed to
// drift (snapshots boxed on `filtering`, the recovery kit on `rkSaving`),
// which let a stage that forgot to blur on exit come back framed around a
// field nobody focused. A caller that sizes its input from the pane's
// interior must also subtract ui.FieldBoxOverhead from that Width, or the
// framed line overflows the panel the moment it takes focus.
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
