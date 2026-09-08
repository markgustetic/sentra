package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// markedLines returns the lines of view that carry the "▍" selection
// glyph at their head. Under the Ascii profile the tests run in, lipgloss
// emits no ANSI, so the glyph is the ONLY trace of a selection — a
// colour-only Selected style renders the cursor row identically to its
// neighbours here, exactly as it does for NO_COLOR users.
func markedLines(view string) []string {
	var out []string
	for _, l := range strings.Split(view, "\n") {
		if strings.HasPrefix(strings.TrimLeft(l, " "), "▍") {
			out = append(out, l)
		}
	}
	return out
}

// TestTables_SelectionIsAGlyph asserts the rule for the CLASS of
// table-backed views: the cursor row is marked by the glyph on exactly one
// line, that line is the row under the cursor, and the mark follows the
// cursor down. Each view builds its table through ui.TableStyles and
// renders through ui.TableView; a view that reaches for table.New with
// bubbles' colour-only default, or renders tbl.View() raw, fails here.
func TestTables_SelectionIsAGlyph(t *testing.T) {
	cases := []struct {
		name string
		// view returns the view on its table stage, plus the first two
		// rows' distinguishing cell text in table order.
		view func(t *testing.T) (tea.Model, [2]string)
	}{
		{"snapshots", func(t *testing.T) (tea.Model, [2]string) {
			s := NewSnapshots(Deps{}).SetSnapshots(sampleSnaps())
			// Default sort is newest first.
			return s, [2]string{"snap-bbbb", "snap-aaaa"}
		}},
		{"restore", func(t *testing.T) (tea.Model, [2]string) {
			r := newFlowRepo(t)
			seedTaggedSnaps(t, r, "first", "second")
			v := NewRestoreView(Deps{Repo: r})
			return v, [2]string{v.snaps[0].Tag, v.snaps[1].Tag}
		}},
		{"diff", func(t *testing.T) (tea.Model, [2]string) {
			r := newFlowRepo(t)
			seedTaggedSnaps(t, r, "first", "second")
			d := NewDiff(Deps{Repo: r})
			return d, [2]string{d.snaps[0].Tag, d.snaps[1].Tag}
		}},
		{"jobs", func(t *testing.T) (tea.Model, [2]string) {
			deps, _ := jobsDeps(t)
			return newJobsForTest(t, deps), [2]string{"alpha", "beta"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, rows := tc.view(t)
			sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			v = sized

			marked := markedLines(v.View())
			if len(marked) != 1 {
				t.Fatalf("%d marked lines, want exactly 1 (the cursor row):\n%s", len(marked), v.View())
			}
			if !strings.Contains(marked[0], rows[0]) {
				t.Fatalf("marked line %q is not the first row %q", marked[0], rows[0])
			}

			moved, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
			marked = markedLines(moved.View())
			if len(marked) != 1 {
				t.Fatalf("after down: %d marked lines, want 1:\n%s", len(marked), moved.View())
			}
			if !strings.Contains(marked[0], rows[1]) {
				t.Fatalf("after down: marked line %q is not the second row %q", marked[0], rows[1])
			}
		})
	}
}

// TestTables_SelectedRowAlignsWithHeader: the gutter must indent every
// line by the same width, or the cursor row's columns drift out from
// under the header the moment it is selected.
func TestTables_SelectedRowAlignsWithHeader(t *testing.T) {
	r := newFlowRepo(t)
	seedTaggedSnaps(t, r, "weekly")
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	v, _ := NewDiff(Deps{Repo: r}).Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	lines := strings.Split(v.View(), "\n")
	var header, row string
	for _, l := range lines {
		switch {
		case strings.Contains(l, "Created") && header == "":
			header = l
		case strings.Contains(l, snaps[0].ID):
			row = l
		}
	}
	if header == "" || row == "" {
		t.Fatalf("header or row missing:\n%s", v.View())
	}
	// Compare display columns, not byte offsets: the glyph is three bytes wide.
	col := func(line, sub string) int { return lipgloss.Width(line[:strings.Index(line, sub)]) }
	if col(header, "Created") != col(row, snaps[0].CreatedAt.UTC().Format("2006-01-02")) {
		t.Fatalf("Created column misaligned between header and selected row:\n%s\n%s", header, row)
	}
}
