package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/markgustetic/sentra/internal/repo"
)

// dirNode is one directory in the tree reconstructed from a snapshot's flat
// file list. files/bytes are what sit DIRECTLY in this directory; the subtree
// totals (what the graph labels its edges with) are summed on demand so the
// build stays a single pass.
type dirNode struct {
	name     string
	children map[string]*dirNode
	files    int
	bytes    int64
	// subFiles/subBytes are the subtree aggregates, computed once by
	// computeAggregates after the tree is built. Precomputing them keeps the
	// layout's sort comparator and its fit-loop from re-walking the subtree on
	// every call (an O(n²) trap on large trees).
	subFiles int
	subBytes int64
}

// buildDirTree reconstructs the directory hierarchy from a manifest's flat
// []FileEntry (each Path is repo-root-relative, "/"-separated). The returned
// root represents the backup root itself; every path component before the file
// name becomes a nested dirNode.
func buildDirTree(entries []repo.FileEntry) *dirNode {
	root := &dirNode{name: "/", children: map[string]*dirNode{}}
	for _, fe := range entries {
		// Only chunk-backed files count as leaves. Dir entries (v2
		// manifests) would otherwise register as zero-byte files at
		// their own path; the directory structure they describe is
		// already derived from the file paths below.
		if !fe.IsFile() {
			continue
		}
		p := strings.Trim(strings.ReplaceAll(fe.Path, "\\", "/"), "/")
		node := root
		if p != "" {
			parts := strings.Split(p, "/")
			for _, dir := range parts[:len(parts)-1] {
				c, ok := node.children[dir]
				if !ok {
					c = &dirNode{name: dir, children: map[string]*dirNode{}}
					node.children[dir] = c
				}
				node = c
			}
		}
		node.files++
		node.bytes += fe.Size
	}
	computeAggregates(root)
	return root
}

// computeAggregates fills subFiles/subBytes in one post-order pass: a directory
// plus everything beneath it.
func computeAggregates(n *dirNode) (int, int64) {
	f, b := n.files, n.bytes
	for _, c := range n.children {
		cf, cb := computeAggregates(c)
		f += cf
		b += cb
	}
	n.subFiles, n.subBytes = f, b
	return f, b
}

// totalFiles / totalBytes are the subtree aggregates: this directory plus every
// directory beneath it. These are the numbers the graph puts on the edge INTO a
// directory ("how much lives under here"). O(1) — precomputed by buildDirTree.
func (n *dirNode) totalFiles() int   { return n.subFiles }
func (n *dirNode) totalBytes() int64 { return n.subBytes }

// renderDirTree renders the directory hierarchy as an indented summary — each
// directory with its subtree file count and total size, busiest first — so a
// snapshot's shape reads at a glance instead of as a thousand-line flat file
// list. Individual files are folded into their directory's count. Each line is
// bounded to width. Reuses the same tree the Files graph builds.
func renderDirTree(root *dirNode, width int) []string {
	if width < 16 {
		width = 16
	}
	var out []string
	line := func(left, right string) {
		out = append(out, spread(width, truncateToWidth(left, max(width-len(right)-1, 1)), right))
	}
	if root.files > 0 {
		line(fmt.Sprintf("· %d files here", root.files), shortBytes(root.bytes))
	}
	var walk func(n *dirNode, depth int)
	walk = func(n *dirNode, depth int) {
		for _, c := range n.sortedChildren() {
			line(strings.Repeat("  ", depth)+c.name+"/",
				fmt.Sprintf("%d files  %s", c.totalFiles(), shortBytes(c.totalBytes())))
			walk(c, depth+1)
		}
	}
	walk(root, 0)
	if len(out) == 0 {
		out = []string{"(no files)"}
	}
	return out
}

// sortedChildren returns the child directories ordered by subtree file count
// descending (ties broken by name), so the busiest directories lead — the ones
// worth showing when the graph can only fit a few per column.
func (n *dirNode) sortedChildren() []*dirNode {
	out := make([]*dirNode, 0, len(n.children))
	for _, c := range n.children {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		fi, fj := out[i].totalFiles(), out[j].totalFiles()
		if fi != fj {
			return fi > fj
		}
		return out[i].name < out[j].name
	})
	return out
}
