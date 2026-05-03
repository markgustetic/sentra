package ui

import (
	"strings"
	"testing"
)

// TestRenderTable_BasicShape asserts the rendered output mentions
// every header and every cell value. We deliberately avoid asserting
// the exact framing characters — lipgloss's table renderer owns
// that, and pinning to specific glyphs would make tests brittle
// across lipgloss versions.
func TestRenderTable_BasicShape(t *testing.T) {
	headers := []string{"Snapshot", "Time", "Bytes"}
	rows := [][]string{
		{"a1b2c3", "2026-05-02T10:00", "12.3 MiB"},
		{"d4e5f6", "2026-05-02T11:30", "8.7 MiB"},
	}

	got := RenderTable(headers, rows)
	if got == "" {
		t.Fatal("RenderTable returned empty string")
	}

	for _, h := range headers {
		if !strings.Contains(got, h) {
			t.Errorf("RenderTable output missing header %q\noutput:\n%s", h, got)
		}
	}
	for _, row := range rows {
		for _, cell := range row {
			if !strings.Contains(got, cell) {
				t.Errorf("RenderTable output missing cell %q\noutput:\n%s", cell, got)
			}
		}
	}
}

// TestRenderTable_HandlesShortRow ensures rows that are shorter than
// the header are padded with empty strings rather than crashing the
// underlying renderer. Real-world finding rows can be partial when
// agent output is truncated.
func TestRenderTable_HandlesShortRow(t *testing.T) {
	headers := []string{"A", "B", "C", "D"}
	rows := [][]string{
		{"x", "y"},           // 2 cells, header has 4
		{"p", "q", "r", "s"}, // matching length
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RenderTable panicked on short row: %v", r)
		}
	}()

	got := RenderTable(headers, rows)
	if got == "" {
		t.Fatal("RenderTable returned empty string for short-row input")
	}
	for _, h := range headers {
		if !strings.Contains(got, h) {
			t.Errorf("RenderTable output missing header %q\noutput:\n%s", h, got)
		}
	}
	for _, want := range []string{"x", "y", "p", "q", "r", "s"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderTable output missing cell %q\noutput:\n%s", want, got)
		}
	}
}

// TestRenderTable_EmptyRows verifies that an empty row slice still
// produces output containing the header row, so a "no data" view
// is still readable rather than blank.
func TestRenderTable_EmptyRows(t *testing.T) {
	headers := []string{"Path", "Size", "Modified"}
	got := RenderTable(headers, nil)
	if got == "" {
		t.Fatal("RenderTable returned empty string for nil rows")
	}
	for _, h := range headers {
		if !strings.Contains(got, h) {
			t.Errorf("RenderTable output missing header %q\noutput:\n%s", h, got)
		}
	}

	got = RenderTable(headers, [][]string{})
	if got == "" {
		t.Fatal("RenderTable returned empty string for empty-slice rows")
	}
}

// TestRenderTable_NoHeaders pins the documented behaviour: when no
// headers are supplied, every row is normalized to 0 cells. The
// renderer produces a header-less frame and the input cells are
// dropped. This is intentional — RenderTable treats headers as
// authoritative on column count — but the test must assert it
// explicitly so we don't silently regress.
func TestRenderTable_NoHeaders(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RenderTable panicked on empty headers: %v", r)
		}
	}()
	for _, headers := range [][]string{nil, {}} {
		got := RenderTable(headers, [][]string{{"a", "b"}})
		if strings.Contains(got, "a") || strings.Contains(got, "b") {
			t.Errorf("with no headers, row data should be dropped "+
				"(column count is authoritative); got %q", got)
		}
	}
}

// TestRenderTable_LongRowTruncated pins the truncation contract.
// Cells past len(headers) are dropped rather than silently rendered
// in some degraded form. Phase 7's tables (snapshots, agent
// recommendations) will rely on this when row widths drift from
// header widths during refactors.
func TestRenderTable_LongRowTruncated(t *testing.T) {
	headers := []string{"A", "B"}
	rows := [][]string{{"keep1", "keep2", "drop_me"}}
	got := RenderTable(headers, rows)
	if !strings.Contains(got, "keep1") || !strings.Contains(got, "keep2") {
		t.Errorf("expected first two cells to render, got %q", got)
	}
	if strings.Contains(got, "drop_me") {
		t.Errorf("expected third cell to be truncated, but it appeared in %q", got)
	}
}
