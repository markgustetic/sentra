package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// Diff renders the three-column added / removed / changed view for
// a pair of snapshots. For v1 the user picks the snapshot pair from
// outside the TUI (via the snapshots view's "compare" affordance,
// once that lands) — Diff just renders whatever DiffResult it was
// handed.
type Diff struct {
	deps Deps

	// idA, idB identify the snapshots being compared. Shown in the
	// header so the user knows what the columns are comparing
	// against. Empty values render the empty-state.
	idA, idB string

	// res is the latest diff result. Zero-value (empty slices) is
	// fine and shows three empty columns under the header.
	res repo.DiffResult
}

// NewDiff returns the v1 Diff model. Construction is cheap; the
// model is data-driven via SetResult.
func NewDiff(deps Deps) Diff {
	return Diff{deps: deps}
}

// SetResult replaces the rendered diff. Returns the updated model
// so callers can chain.
func (d Diff) SetResult(idA, idB string, res repo.DiffResult) Diff {
	d.idA = idA
	d.idB = idB
	d.res = res
	return d
}

// Init is a no-op — diff is data-driven, no background work.
func (Diff) Init() tea.Cmd { return nil }

// Update accepts any message and returns the model unchanged for
// v1. Future iterations will add an inline snapshot picker; for
// now the diff is set externally (via SetResult).
func (d Diff) Update(_ tea.Msg) (tea.Model, tea.Cmd) {
	return d, nil
}

// View renders the three columns. When idA / idB are empty (no
// snapshot pair selected yet) the user sees a hint to pick two
// snapshots from the snapshots view. The actual selection wiring
// is out of scope for v1.
func (d Diff) View() string {
	if d.idA == "" || d.idB == "" {
		body := ui.Subtle.Render("diff") + "\n" +
			ui.Muted.Render("select two snapshots from the snapshots view to compare")
		return ui.Panel.Render(body) + "\n"
	}

	header := fmt.Sprintf("%s ↔ %s",
		ui.Primary.Render(d.idA),
		ui.Primary.Render(d.idB),
	)

	added := renderDiffColumn("added", ui.Success, d.res.Added)
	removed := renderDiffColumn("removed", ui.Danger, d.res.Removed)
	changed := renderDiffColumn("changed", ui.Warn, d.res.Changed)

	cols := lipgloss.JoinHorizontal(lipgloss.Top, added, removed, changed)
	return header + "\n" + cols + "\n"
}

// renderDiffColumn formats one column: header in the given color,
// then each path one per line. Wrapped in ui.Panel for the visual
// frame; column width is set to a fixed 28 chars so the three
// columns fit a typical 100-col terminal.
//
// Empty paths render "(none)" so the user sees an explicit "this
// column had nothing" rather than a blank panel.
func renderDiffColumn(label string, color lipgloss.Style, paths []string) string {
	title := color.Bold(true).Render(label)
	count := ui.Subtle.Render(fmt.Sprintf("(%d)", len(paths)))
	body := title + " " + count + "\n"
	if len(paths) == 0 {
		body += ui.Muted.Render("(none)")
	} else {
		body += strings.Join(paths, "\n")
	}
	style := ui.Panel.Width(28)
	return style.Render(body)
}
