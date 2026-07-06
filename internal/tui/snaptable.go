package tui

import (
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/ui"
)

// contentPanelHPad is the horizontal padding the App's content panel
// (ui.Panel / ui.PanelFocused, both Padding(0,1)) reserves *inside* the
// Width the App forwards to a view. A view's real text region is the
// forwarded width minus this — border sits outside Width, padding inside —
// so sizing a table or line to the forwarded width alone lets the
// rightmost cell wrap; only trailing whitespace survives, by collapsing.
// Derived from ui.Panel so it tracks a theme change rather than a magic 2.
var contentPanelHPad = ui.Panel.GetHorizontalPadding()

// pickerContentWidth converts the width the App forwards on a
// WindowSizeMsg (its panel Width) into the interior text width a picker
// view may actually draw into.
func pickerContentWidth(forwarded int) int { return forwarded - contentPanelHPad }

// The snapshot-picker tables (Diff, Restore) share one column-sizing
// policy so they behave identically as the terminal narrows. bubbles/
// table renders each cell at its column Width plus 2 cells of padding
// (the default Cell/Header style is Padding(0,1)), and the App frames the
// whole view in a content panel only ~59 cells wide at the documented
// 80-col minimum. Hard-coded widths (ID=34, Created=17, …) summed past
// that interior, so lipgloss wrapped the rightmost column onto its own
// line and the header no longer lined up with the rows. Sizing columns to
// the width the App forwards keeps the table inside the panel.

const (
	// snapColPad is the horizontal padding bubbles/table adds per cell
	// (Padding(0,1) on both the header and cell styles). A column costs
	// Width + snapColPad cells on screen.
	snapColPad = 2

	// snapCreatedWidth fits "2006-01-02 15:04" exactly. A timestamp is
	// fixed-format, so it is never flexed or truncated — a clipped date is
	// worse than a clipped ID or tag.
	snapCreatedWidth = 16
	snapFilesWidth   = 6 // file counts rarely exceed 6 digits; header "Files" is 5

	// The ID is the main budget: "snap-<20060102T150405Z>-<8 hex>" renders
	// 30 cells. It flexes down to snapIDMin (truncated with an ellipsis by
	// bubbles/table) when space is scarce.
	snapIDIdeal = 30
	snapIDMin   = 12

	// Tag flexes and is dropped entirely (Width 0) when there isn't room
	// for a width still wide enough to show a typical tag.
	snapTagIdeal = 12
	snapTagMin   = 6

	// pickerIdealWidth is wide enough that snapshotPickerColumns returns
	// every column at its ideal width. Constructors use it to build the
	// initial table before the first WindowSizeMsg; the App re-sizes the
	// columns on resize().
	pickerIdealWidth = 100
)

// snapshotPickerColumns sizes the picker columns to fit within avail
// on-screen cells — the width bubbles/table draws into, which the App's
// content panel then frames. Created (and Files, for Restore) are fixed;
// the ID takes priority for the remaining space up to its ideal and is
// truncated below it; the Tag takes what's left and is dropped when a
// useful width no longer fits.
//
// The returned slice always carries the same number of columns as the
// rows have cells (3, or 4 with withFiles) — a dropped column keeps its
// slot at Width 0 rather than being removed, because bubbles/table indexes
// its columns by each row cell and panics if a row has more cells than
// columns.
func snapshotPickerColumns(avail int, withFiles bool) []table.Column {
	// Budget left for the flexible pair (ID, Tag) after the fixed columns
	// take their Width + padding.
	flex := avail - (snapCreatedWidth + snapColPad)
	if withFiles {
		flex -= snapFilesWidth + snapColPad
	}

	idW, tagW := snapIDIdeal, snapTagIdeal
	switch {
	case flex >= (snapIDIdeal+snapColPad)+(snapTagIdeal+snapColPad):
		// Roomy: both at their ideal. Any surplus is left as panel
		// whitespace rather than stretching a column past what it needs.
	case flex-(snapTagMin+snapColPad)-snapColPad >= snapIDMin:
		// Tight, but a useful Tag still fits. ID keeps priority up to its
		// ideal; Tag takes the remainder (>= snapTagMin by construction).
		idW = flex - (snapTagMin + snapColPad) - snapColPad
		if idW > snapIDIdeal {
			idW = snapIDIdeal
		}
		tagW = flex - (idW + snapColPad) - snapColPad
		if tagW > snapTagIdeal {
			tagW = snapTagIdeal
		}
	default:
		// No room for a useful Tag: drop it (Width 0 renders as nothing)
		// and let the ID take the freed space.
		tagW = 0
		idW = flex - snapColPad
		if idW > snapIDIdeal {
			idW = snapIDIdeal
		}
		if idW < snapIDMin {
			idW = snapIDMin
		}
	}

	cols := []table.Column{
		{Title: "ID", Width: idW},
		{Title: "Created", Width: snapCreatedWidth},
		{Title: "Tag", Width: tagW},
	}
	if withFiles {
		cols = append(cols, table.Column{Title: "Files", Width: snapFilesWidth})
	}
	return cols
}

// truncateToWidth shortens s to at most w display cells, marking a clip
// with a trailing ellipsis. w <= 0 yields "". It measures with
// lipgloss.Width so wide runes are counted correctly. Used by the prune
// preview, whose free-text "ID  verdict  reason" lines have no bubbles/
// table column widths to clip them.
func truncateToWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	const ellipsis = "…" // one cell
	if w == 1 {
		return ellipsis
	}
	budget := w - 1
	width := 0
	var out []rune
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if width+rw > budget {
			break
		}
		out = append(out, r)
		width += rw
	}
	return string(out) + ellipsis
}
