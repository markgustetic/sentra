package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/repo"
)

// TestBuildDirTree reconstructs the directory hierarchy from a flat file list
// and locks the subtree aggregates the graph labels edges with.
func TestBuildDirTree(t *testing.T) {
	entries := []repo.FileEntry{
		{Path: "2024/jan/a.jpg", Size: 100},
		{Path: "2024/jan/b.jpg", Size: 200},
		{Path: "2024/feb/c.jpg", Size: 50},
		{Path: "readme.txt", Size: 10}, // a file at the root
	}
	root := buildDirTree(entries)

	if got := root.totalFiles(); got != 4 {
		t.Errorf("root totalFiles = %d, want 4", got)
	}
	if got := root.totalBytes(); got != 360 {
		t.Errorf("root totalBytes = %d, want 360", got)
	}
	if root.files != 1 {
		t.Errorf("root direct files = %d, want 1 (readme.txt)", root.files)
	}

	y2024, ok := root.children["2024"]
	if !ok {
		t.Fatalf("missing 2024 directory: %+v", root.children)
	}
	if got := y2024.totalFiles(); got != 3 {
		t.Errorf("2024 totalFiles = %d, want 3", got)
	}
	if got := len(y2024.children); got != 2 {
		t.Errorf("2024 should have jan+feb, got %d children", got)
	}
	if got := y2024.children["jan"].totalFiles(); got != 2 {
		t.Errorf("jan totalFiles = %d, want 2", got)
	}

	// sortedChildren orders by subtree file count: jan (2) before feb (1).
	kids := y2024.sortedChildren()
	if len(kids) != 2 || kids[0].name != "jan" || kids[1].name != "feb" {
		t.Errorf("sortedChildren order = %v, want [jan feb]", names(kids))
	}
}

// TestBuildDirTree_Empty: a manifest with no files still yields a root, so the
// view renders an empty-state rather than nil-panicking.
func TestBuildDirTree_Empty(t *testing.T) {
	root := buildDirTree(nil)
	if root == nil {
		t.Fatal("buildDirTree(nil) must return a root node")
	}
	if root.totalFiles() != 0 || len(root.children) != 0 {
		t.Errorf("empty tree must have no files or children, got %+v", root)
	}
}

func names(ns []*dirNode) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.name
	}
	return out
}

// TestRenderDirTree: the indented directory summary shows each dir with its
// subtree file count and size, deepest nesting indented, and folds root-level
// files into a "· N files here" line — never the individual paths.
func TestRenderDirTree(t *testing.T) {
	entries := []repo.FileEntry{
		{Path: "photos/2024/a.jpg", Size: 1 << 20},
		{Path: "photos/2024/b.jpg", Size: 1 << 20},
		{Path: "photos/2023/c.jpg", Size: 2 << 20},
		{Path: "readme.txt", Size: 50},
	}
	lines := renderDirTree(buildDirTree(entries), 60)
	joined := strings.Join(lines, "\n")

	for _, want := range []string{"photos/", "2024/", "2023/", "3 files", "· 1 files here"} {
		if !strings.Contains(joined, want) {
			t.Errorf("tree missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "a.jpg") {
		t.Errorf("tree must summarize, not list individual files:\n%s", joined)
	}
	// 2024/ is nested one level under photos/, so it is indented.
	for _, ln := range lines {
		if strings.Contains(ln, "2024/") && !strings.HasPrefix(ln, "  ") {
			t.Errorf("nested dir 2024/ must be indented: %q", ln)
		}
	}
	// No line exceeds the width.
	for _, ln := range lines {
		if lipgloss.Width(ln) > 60 {
			t.Errorf("tree line exceeds width: %q", ln)
		}
	}
}
