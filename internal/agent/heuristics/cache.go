package heuristics

import (
	"context"
	"strings"
)

// defaultCacheNames is the built-in cache-directory list. These are
// the kinds of directories users almost never want backed up: package
// managers regenerate them on demand, build tools rebuild them from
// source, and they balloon repository size for zero recoverable
// content.
//
// Override via InputConfig.IgnoreCacheDirNames.
var defaultCacheNames = []string{
	"node_modules",
	".venv",
	"__pycache__",
	"target",
	"build",
	"dist",
	".cache",
	".gradle",
}

// CacheDirs flags directories whose name matches a known cache pattern
// AND whose entries appear in the walk (i.e. they are NOT being
// excluded by `.sentraignore`). The presence of cache files in the
// walked set is the signal — the heuristic doesn't separately check
// the ignore matcher, it just trusts what the walker already filtered.
type CacheDirs struct{}

// NewCacheDirs constructs a CacheDirs heuristic.
func NewCacheDirs() *CacheDirs { return &CacheDirs{} }

// Name is the registry-visible name of this heuristic.
func (c *CacheDirs) Name() string { return "cache_dirs" }

// Run inspects each walked entry's relative path for cache-name
// segments. It emits one finding per (name, parent-prefix) pair —
// so 1000 files inside one node_modules tree produce ONE finding,
// but two distinct node_modules trees (e.g. nested in a monorepo)
// each produce their own.
//
// The dedup key is the *full relative path up to and including* the
// cache segment (e.g. "packages/foo/node_modules"), which lets nested
// caches surface independently while collapsing the per-file noise.
func (c *CacheDirs) Run(ctx context.Context, in Input) ([]Finding, error) {
	names := in.Config.IgnoreCacheDirNames
	if len(names) == 0 {
		names = defaultCacheNames
	}
	// Build a set for fast lookup. Sized once, lookups O(1).
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}

	// Track the set of cache-dir relative paths we've already seen so
	// each one produces exactly one finding regardless of how many
	// files live inside it.
	seen := make(map[string]struct{})
	var out []Finding

	for _, e := range in.Walked {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Walk the path segments left-to-right; the first time we hit
		// a cache name, record the prefix-up-to-here as the cache
		// dir's relative path. Any deeper segments are inside that
		// dir and shouldn't produce nested findings.
		segments := strings.Split(e.RelPath, "/")
		for i, seg := range segments {
			if _, ok := want[seg]; !ok {
				continue
			}
			rel := strings.Join(segments[:i+1], "/")
			if _, dup := seen[rel]; dup {
				break
			}
			seen[rel] = struct{}{}
			out = append(out, Finding{
				ID:       makeFindingID("cache_dirs", rel),
				Category: "cache_dirs",
				Severity: SeverityWarn,
				Target:   rel,
				Details: map[string]any{
					"name": seg,
				},
			})
			break
		}
	}
	return out, nil
}
