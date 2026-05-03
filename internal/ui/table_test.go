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
		{"x", "y"},          // 2 cells, header has 4
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

// TestRenderTable_NoHeaders is a defensive case: an empty header
// slice with rows present shouldn't panic, even though it's not a
// useful real-world input.
func TestRenderTable_NoHeaders(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RenderTable panicked on empty headers: %v", r)
		}
	}()
	_ = RenderTable(nil, [][]string{{"a", "b"}})
	_ = RenderTable([]string{}, [][]string{{"a", "b"}})
}
