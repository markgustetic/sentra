package ui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// RenderTable returns a styled string representation of headers + rows.
// Each row shorter than headers is padded with empty strings; each row
// longer is truncated. An empty rows slice still renders the header
// row, giving callers a consistent "no data" view rather than blank.
//
// Headers are styled with the package's Primary accent. Cells are
// padded by one space on each side; we let lipgloss/table own the
// framing characters so output adapts cleanly to terminal width.
func RenderTable(headers []string, rows [][]string) string {
	width := len(headers)

	// Normalize rows to exactly len(headers) cells. This is what keeps
	// "ragged" inputs (a finding row truncated upstream, an empty
	// trailing column) from crashing the renderer or rendering oddly.
	normalized := make([][]string, 0, len(rows))
	for _, row := range rows {
		n := make([]string, width)
		for i := 0; i < width && i < len(row); i++ {
			n[i] = row[i]
		}
		normalized = append(normalized, n)
	}

	t := table.New().
		Headers(headers...).
		Rows(normalized...).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return Primary.Padding(0, 1)
			}
			return lipgloss.NewStyle().Padding(0, 1)
		})
	return t.String()
}
