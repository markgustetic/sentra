package tui

import (
	"fmt"
	"strings"
)

// The Files view draws a snapshot's directory tree as a left-to-right graph of
// boxes joined by lines, with the file count on each edge — a filesystem
// "topology". The layout is a tidy horizontal tree onto a rune canvas: columns
// are depth, boxes in a column stack vertically, a parent sits centered on its
// children, and a bus of box-drawing lines routes each edge. Everything here is
// pure (no repo, no color) so it is unit-testable; the view adds loading, keys,
// and the panel frame.

const (
	fgBoxW   = 18 // box width (content is fgBoxW-2)
	fgBoxH   = 3  // top border, label, bottom border
	fgHGap   = 7  // horizontal gap between columns (room for the edge line + count)
	fgVGap   = 1  // blank rows between stacked sibling boxes
	fgStride = fgBoxW + fgHGap
)

// gnode is one laid-out box: a directory (or a "…+N more" marker). files is the
// subtree file count the edge INTO this node is labelled with.
type gnode struct {
	label    string
	files    int
	isMore   bool // rendered as plain text, not a box
	children []*gnode
	x, y     int
}

// layoutFileGraph turns a directory tree into a positioned graph that fits
// width×height: depth is capped by width, and breadth is reduced (fewer
// children shown per node, the rest folded into a "…+N more" marker) until the
// whole thing fits the height.
func layoutFileGraph(root *dirNode, width, height int) *gnode {
	maxCols := min(max(width/fgStride, 2), 5)
	for maxChildren := 8; maxChildren >= 1; maxChildren-- {
		g := buildGraph(root, maxCols, maxChildren)
		assignPositions(g)
		if _, gh := graphExtent(g); gh <= height {
			return g
		}
	}
	g := buildGraph(root, maxCols, 1)
	assignPositions(g)
	return g
}

// buildGraph shapes the layout tree: at most maxCols columns deep and
// maxChildren shown children per node (busiest first), the overflow becoming a
// "…+N more" leaf so nothing is silently dropped.
func buildGraph(root *dirNode, maxCols, maxChildren int) *gnode {
	var build func(n *dirNode, col int) *gnode
	build = func(n *dirNode, col int) *gnode {
		g := &gnode{label: n.name, files: n.totalFiles()}
		if col+1 >= maxCols || len(n.children) == 0 {
			return g
		}
		kids := n.sortedChildren()
		shown, more := kids, 0
		if len(kids) > maxChildren {
			shown, more = kids[:maxChildren], len(kids)-maxChildren
		}
		for _, c := range shown {
			g.children = append(g.children, build(c, col+1))
		}
		if more > 0 {
			g.children = append(g.children, &gnode{label: fmt.Sprintf("…+%d more", more), isMore: true})
		}
		return g
	}
	return build(root, 0)
}

// assignPositions places every box: x from its column, y from a tidy layout —
// leaves take the next free row band, a parent centers on its first/last child.
func assignPositions(g *gnode) {
	cursor := 0
	var place func(n *gnode, col int)
	place = func(n *gnode, col int) {
		n.x = col * fgStride
		if len(n.children) == 0 {
			n.y = cursor
			cursor += fgBoxH + fgVGap
			return
		}
		for _, c := range n.children {
			place(c, col+1)
		}
		n.y = (n.children[0].y + n.children[len(n.children)-1].y) / 2
	}
	place(g, 0)
}

// graphExtent is the drawn width and height (max box right edge, max box bottom).
func graphExtent(g *gnode) (w, h int) {
	var walk func(n *gnode)
	walk = func(n *gnode) {
		if r := n.x + fgBoxW; r > w {
			w = r
		}
		if b := n.y + fgBoxH; b > h {
			h = b
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(g)
	return w, h
}

// renderFileGraph draws the positioned graph onto a canvas and returns the
// text. The canvas is clipped to width×height so it can never overflow the
// view's panel.
func renderFileGraph(g *gnode, width, height int) string {
	gw, gh := graphExtent(g)
	c := newCanvas(min(gw, width), min(gh, height))
	drawNode(c, g)
	return c.String()
}

func drawNode(c *canvas, n *gnode) {
	if n.isMore {
		c.text(n.x, n.y+1, n.label)
	} else {
		c.box(n.x, n.y, fgBoxW, n.label)
	}
	if len(n.children) > 0 {
		drawEdges(c, n)
		for _, ch := range n.children {
			drawNode(c, ch)
		}
	}
}

// drawEdges routes the connector from a parent to its children: a short stub
// right of the parent, a vertical bus, then a horizontal run to each child's
// left edge ending in an arrowhead, with the subtree file count inline.
func drawEdges(c *canvas, parent *gnode) {
	px := parent.x + fgBoxW // just past the parent's right border
	py := parent.y + 1
	busX := px + 1

	for x := px; x < busX; x++ {
		c.set(x, py, '─')
	}

	rows := make(map[int]bool, len(parent.children))
	minY, maxY := py, py
	for _, ch := range parent.children {
		cy := ch.y + 1
		rows[cy] = true
		minY, maxY = min(minY, cy), max(maxY, cy)
	}
	for y := minY; y <= maxY; y++ {
		c.set(busX, y, junction(y > minY, y < maxY, y == py, rows[y]))
	}

	for _, ch := range parent.children {
		cy := ch.y + 1
		for x := busX + 1; x < ch.x; x++ {
			c.set(x, cy, '─')
		}
		c.set(ch.x-1, cy, '▶')
		if !ch.isMore && ch.files > 0 {
			c.text(busX+2, cy-1, shortCount(ch.files)) // count sits above the edge line
		}
	}
}

// junction picks the box-drawing glyph for a bus cell from which sides connect.
func junction(up, down, left, right bool) rune {
	switch {
	case up && down && left && right:
		return '┼'
	case up && down && right:
		return '├'
	case up && down && left:
		return '┤'
	case up && down:
		return '│'
	case down && right:
		return '┌'
	case up && right:
		return '└'
	case down && left:
		return '┐'
	case up && left:
		return '┘'
	case left && right:
		return '─'
	default:
		return '│'
	}
}

// shortCount formats an edge's file count compactly (75, 1.2k, 3.4M).
func shortCount(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// --- rune canvas ---------------------------------------------------------

// canvas is a fixed-size grid of runes the graph draws onto. Out-of-bounds
// writes are dropped, so a clipped graph degrades gracefully instead of
// panicking.
type canvas struct {
	w, h int
	grid [][]rune
}

func newCanvas(w, h int) *canvas {
	w, h = max(w, 1), max(h, 1)
	grid := make([][]rune, h)
	for y := range grid {
		grid[y] = make([]rune, w)
		for x := range grid[y] {
			grid[y][x] = ' '
		}
	}
	return &canvas{w: w, h: h, grid: grid}
}

func (c *canvas) set(x, y int, r rune) {
	if x >= 0 && x < c.w && y >= 0 && y < c.h {
		c.grid[y][x] = r
	}
}

func (c *canvas) text(x, y int, s string) {
	for i, r := range []rune(s) {
		c.set(x+i, y, r)
	}
}

// box draws a w-wide, fgBoxH-tall rounded rectangle with a centered, truncated
// label on its middle row.
func (c *canvas) box(x, y, w int, label string) {
	c.set(x, y, '┌')
	c.set(x+w-1, y, '┐')
	c.set(x, y+2, '└')
	c.set(x+w-1, y+2, '┘')
	for i := 1; i < w-1; i++ {
		c.set(x+i, y, '─')
		c.set(x+i, y+2, '─')
	}
	c.set(x, y+1, '│')
	c.set(x+w-1, y+1, '│')

	inner := w - 2
	lbl := []rune(label)
	if len(lbl) > inner {
		if inner >= 1 {
			lbl = append(lbl[:inner-1], '…')
		} else {
			lbl = nil
		}
	}
	c.text(x+1+(inner-len(lbl))/2, y+1, string(lbl))
}

// String joins the grid rows, trimming trailing spaces so the output carries no
// trailing whitespace (golden- and git-diff-clean).
func (c *canvas) String() string {
	rows := make([]string, c.h)
	for y, row := range c.grid {
		rows[y] = strings.TrimRight(string(row), " ")
	}
	return strings.Join(rows, "\n")
}
