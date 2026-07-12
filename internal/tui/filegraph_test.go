package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/repo"
)

// sampleFileTree builds a small multi-level tree for the graph tests.
func sampleFileTree() *dirNode {
	var e []repo.FileEntry
	add := func(p string, n int) {
		for i := range n {
			e = append(e, repo.FileEntry{Path: fmt.Sprintf("%s/f%d.dat", p, i), Size: 1000})
		}
	}
	add("photos/2024", 75)
	add("photos/2023", 40)
	add("code/sentra", 120)
	add("music", 8)
	return buildDirTree(e)
}

// TestRenderFileGraph_FitsAndDraws: the graph must stay within the given
// width×height, draw a box for the root and its directories, put the file
// counts on the edges, and carry no trailing whitespace (so it is golden- and
// git-diff-clean).
func TestRenderFileGraph_FitsAndDraws(t *testing.T) {
	root := sampleFileTree()
	root.name = "root"
	const w, h = 90, 30
	out := renderFileGraph(layoutFileGraph(root, w, h), w, h)

	lines := strings.Split(out, "\n")
	if len(lines) > h {
		t.Errorf("graph is %d lines, exceeds height %d", len(lines), h)
	}
	for i, ln := range lines {
		if lw := lipgloss.Width(ln); lw > w {
			t.Errorf("line %d width %d exceeds %d: %q", i, lw, w, ln)
		}
		if ln != strings.TrimRight(ln, " ") {
			t.Errorf("line %d has trailing whitespace: %q", i, ln)
		}
	}
	if !strings.Contains(out, "┌") || !strings.Contains(out, "▶") {
		t.Errorf("graph must draw boxes and arrows:\n%s", out)
	}
	for _, want := range []string{"root", "photos", "code", "music", "115", "120"} {
		// 115 = photos subtree (75+40); 120 = code/sentra.
		if !strings.Contains(out, want) {
			t.Errorf("graph missing %q:\n%s", want, out)
		}
	}
}

// TestLayoutFileGraph_NoBoxOverlap: the tidy layout must never place two boxes
// on overlapping cells — every box's 3-row span at its column must be clear of
// its siblings.
func TestLayoutFileGraph_NoBoxOverlap(t *testing.T) {
	root := sampleFileTree()
	g := layoutFileGraph(root, 120, 40)

	type rect struct{ x, y int }
	seen := map[rect]string{}
	var walk func(n *gnode)
	walk = func(n *gnode) {
		if !n.isMore {
			for dy := range fgBoxH {
				k := rect{n.x, n.y + dy}
				if other, ok := seen[k]; ok {
					t.Errorf("box %q overlaps %q at (%d,%d)", n.label, other, k.x, k.y)
				}
				seen[k] = n.label
			}
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(g)
}

// TestLayoutFileGraph_ShrinksToFitHeight: a wide, bushy tree must fold children
// into "…+N more" markers until it fits a short viewport, rather than
// overflowing it.
func TestLayoutFileGraph_ShrinksToFitHeight(t *testing.T) {
	var e []repo.FileEntry
	for i := range 20 { // 20 top-level dirs — far more than a short viewport holds
		e = append(e, repo.FileEntry{Path: fmt.Sprintf("dir%02d/file.dat", i), Size: 1})
	}
	root := buildDirTree(e)

	const h = 14
	g := layoutFileGraph(root, 100, h)
	if _, gh := graphExtent(g); gh > h {
		t.Errorf("graph height %d exceeds viewport %d — did not shrink to fit", gh, h)
	}
	if !strings.Contains(renderFileGraph(g, 100, h), "more") {
		t.Errorf("a tree taller than the viewport must show a '…+N more' marker")
	}
}

// TestShortCount locks the compact edge-count format.
func TestShortCount(t *testing.T) {
	cases := map[int]string{0: "0", 75: "75", 999: "999", 1500: "1.5k", 2_400_000: "2.4M"}
	for in, want := range cases {
		if got := shortCount(in); got != want {
			t.Errorf("shortCount(%d) = %q, want %q", in, got, want)
		}
	}
}
