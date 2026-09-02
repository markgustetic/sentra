package tui

import (
	"github.com/charmbracelet/bubbles/textinput"

	"github.com/markgustetic/sentra/internal/ui"
)

// Field focus in the TUI has one contract, shared by every view that owns a
// textinput (fieldOwners in fieldfocus_test.go enumerates them and asserts
// each rule below over the whole set):
//
//   - A field is focused exactly while its view is ON SCREEN and its current
//     stage owns it. Constructors and Init focus nothing; a stage transition
//     inside the view or the shell's viewShownMsg focuses, and every stage
//     exit or viewHiddenMsg blurs. Focused() is therefore a truthful
//     predicate, and both the box below and each view's cursor.BlinkMsg
//     guard trust it.
//   - Every Focus() transition returns Focus()'s own cmd — the real, tagged
//     cursor.BlinkCmd — and that is the only way a blink chain starts. The
//     App routes ticks to on-screen focus owners only (App.Update).
//   - A view with several fields funnels every entry into its field stage
//     (tab, the stage's entry key, viewShownMsg, the one-op guard's
//     opRejectedMsg bounce) through one focusField/blurFields pair, so the
//     stage's own focus flag and the fields' Focused() cannot drift apart.
//   - A model swapped in on the spot (the "again" resets) is on screen at
//     once, so it takes viewShownMsg itself rather than a bare constructor.

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
