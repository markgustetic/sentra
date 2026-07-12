package tui

import (
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
}

// buildDirTree reconstructs the directory hierarchy from a manifest's flat
// []FileEntry (each Path is repo-root-relative, "/"-separated). The returned
// root represents the backup root itself; every path component before the file
// name becomes a nested dirNode.
func buildDirTree(entries []repo.FileEntry) *dirNode {
	root := &dirNode{name: "/", children: map[string]*dirNode{}}
	for _, fe := range entries {
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
	return root
}

// totalFiles / totalBytes are the subtree aggregates: this directory plus every
// directory beneath it. These are the numbers the graph puts on the edge INTO a
// directory ("how much lives under here").
func (n *dirNode) totalFiles() int {
	t := n.files
	for _, c := range n.children {
		t += c.totalFiles()
	}
	return t
}

func (n *dirNode) totalBytes() int64 {
	t := n.bytes
	for _, c := range n.children {
		t += c.totalBytes()
	}
	return t
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
