package heuristics

import (
	"context"
	"slices"
	"testing"

	"github.com/markgustetic/sentra/internal/walker"
)

// entry builds a walker.Entry with just the fields cache_dirs needs:
// AbsPath / RelPath. Size and MTime are irrelevant here.
func entry(rel string) walker.Entry {
	return walker.Entry{
		AbsPath: "/repo/" + rel,
		RelPath: rel,
	}
}

// TestCacheDirs_FlagsNodeModules: a tree with node_modules/ visible
// in the walk produces one warn finding for the cache directory.
func TestCacheDirs_FlagsNodeModules(t *testing.T) {
	in := Input{Walked: []walker.Entry{
		entry("src/main.go"),
		entry("node_modules/foo/bar.js"),
		entry("node_modules/baz/index.js"),
	}}

	h := NewCacheDirs()
	got, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// One finding per cache-dir occurrence — not per file inside it.
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Category != "cache_dirs" || f.Severity != SeverityWarn {
		t.Errorf("category/severity: got %s/%s", f.Category, f.Severity)
	}
	// Target is the cache dir's relative path under the walk root.
	if f.Target != "node_modules" {
		t.Errorf("target: got %s, want node_modules", f.Target)
	}
	if name, _ := f.Details["name"].(string); name != "node_modules" {
		t.Errorf("details name: got %v, want node_modules", f.Details["name"])
	}
}

// TestCacheDirs_NoFindingWhenIgnored: when the walk has already
// excluded node_modules (so no entries with that segment appear),
// the heuristic emits no finding.
func TestCacheDirs_NoFindingWhenIgnored(t *testing.T) {
	in := Input{Walked: []walker.Entry{
		entry("src/main.go"),
		entry("README.md"),
	}}

	h := NewCacheDirs()
	got, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no findings, got %+v", got)
	}
}

// TestCacheDirs_DedupesAcrossManyFiles: 1000 files inside a single
// node_modules tree produce one finding, not 1000.
func TestCacheDirs_DedupesAcrossManyFiles(t *testing.T) {
	walked := make([]walker.Entry, 1000)
	for i := range walked {
		walked[i] = entry("node_modules/pkg/file" + itoa(i) + ".js")
	}
	h := NewCacheDirs()
	got, err := h.Run(context.Background(), Input{Walked: walked})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
}

// TestCacheDirs_FlagsMultipleCacheKinds: a tree containing both
// node_modules/ and __pycache__/ produces two findings, one per
// distinct cache directory.
func TestCacheDirs_FlagsMultipleCacheKinds(t *testing.T) {
	in := Input{Walked: []walker.Entry{
		entry("node_modules/foo/bar.js"),
		entry("__pycache__/mod.pyc"),
		entry("src/main.go"),
	}}

	h := NewCacheDirs()
	got, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d findings, want 2: %+v", len(got), got)
	}
	targets := []string{got[0].Target, got[1].Target}
	slices.Sort(targets)
	want := []string{"__pycache__", "node_modules"}
	for i := range targets {
		if targets[i] != want[i] {
			t.Errorf("targets[%d] = %s, want %s", i, targets[i], want[i])
		}
	}
}

// TestCacheDirs_NestedCacheDirs: two distinct node_modules occurrences
// (one nested inside another package's tree) each produce their own
// finding because their relative paths differ.
func TestCacheDirs_NestedCacheDirs(t *testing.T) {
	in := Input{Walked: []walker.Entry{
		entry("node_modules/foo/file.js"),
		entry("packages/x/node_modules/dep/index.js"),
	}}

	h := NewCacheDirs()
	got, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d findings, want 2: %+v", len(got), got)
	}
}

// TestCacheDirs_HonorsConfigOverride: setting IgnoreCacheDirNames
// replaces the built-in list. With ["customcache"] in the config,
// node_modules is no longer flagged but a customcache/ tree IS.
func TestCacheDirs_HonorsConfigOverride(t *testing.T) {
	in := Input{
		Walked: []walker.Entry{
			entry("node_modules/foo.js"),
			entry("customcache/data.bin"),
		},
		Config: InputConfig{IgnoreCacheDirNames: []string{"customcache"}},
	}
	h := NewCacheDirs()
	got, err := h.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Target != "customcache" {
		t.Errorf("target: got %s, want customcache", got[0].Target)
	}
}

// TestCacheDirs_Name: heuristic name is "cache_dirs".
func TestCacheDirs_Name(t *testing.T) {
	if got, want := NewCacheDirs().Name(), "cache_dirs"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
}

// itoa is a tiny stack-allocating int-to-decimal helper for the
// many-files test. strconv works fine but pulling it in for one call
// site is fluffy.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
