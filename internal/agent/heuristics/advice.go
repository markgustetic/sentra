package heuristics

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/markgustetic/sentra/internal/walker"
)

// IgnoreAdvice is one suggested .sentraignore pattern. Lives here —
// below both internal/cli and internal/tui — so `agent advise-ignore`
// and the TUI's advice pane compute identical suggestions.
type IgnoreAdvice struct {
	Pattern  string `json:"pattern"`
	Category string `json:"category"`
	Target   string `json:"target"`
	Reason   string `json:"reason"`
	Size     int64  `json:"size,omitempty"`
}

// CollectIgnoreAdvice walks root with the caller's walker options and
// suggests ignore patterns from the cache-dir and large-file
// heuristics. Read-only: it never edits .sentraignore.
func CollectIgnoreAdvice(
	ctx context.Context,
	root string,
	walkerOpts walker.Options,
	largeFileBytes int64,
) ([]IgnoreAdvice, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("ignore advice: abs root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	var (
		walkMu sync.Mutex
		walked []walker.Entry
	)
	if err := walker.Walk(ctx, absRoot, walkerOpts, func(e walker.Entry) error {
		walkMu.Lock()
		walked = append(walked, e)
		walkMu.Unlock()
		return nil
	}); err != nil {
		return nil, fmt.Errorf("ignore advice: walk: %w", err)
	}

	registry := NewRegistry(NewCacheDirs(), NewLargeFiles())
	findings, err := registry.Run(ctx, Input{
		Walked: walked,
		Config: InputConfig{LargeFileBytes: largeFileBytes},
	})
	if err != nil {
		return nil, fmt.Errorf("ignore advice: heuristics: %w", err)
	}

	seen := make(map[string]struct{})
	advice := make([]IgnoreAdvice, 0, len(findings))
	for _, finding := range findings {
		pattern := ignorePatternForFinding(absRoot, finding)
		if pattern == "" {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		advice = append(advice, IgnoreAdvice{
			Pattern:  pattern,
			Category: finding.Category,
			Target:   finding.Target,
			Reason:   ignoreReason(finding.Category),
			Size:     findingSize(finding),
		})
	}
	slices.SortFunc(advice, func(a, b IgnoreAdvice) int {
		return cmp.Compare(a.Pattern, b.Pattern)
	})
	return advice, nil
}

func ignorePatternForFinding(absRoot string, finding Finding) string {
	switch finding.Category {
	case "cache_dirs":
		pattern := strings.TrimSpace(finding.Target)
		if pattern == "" {
			return ""
		}
		return strings.TrimSuffix(filepath.ToSlash(pattern), "/") + "/"
	case "large_files":
		target := strings.TrimSpace(finding.Target)
		if target == "" {
			return ""
		}
		rel, err := filepath.Rel(absRoot, target)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
		return filepath.ToSlash(target)
	default:
		return ""
	}
}

func ignoreReason(category string) string {
	switch category {
	case "cache_dirs":
		return "regenerable cache/build directory"
	case "large_files":
		return "large file; review whether it belongs in encrypted backups"
	default:
		return "local heuristic finding"
	}
}

func findingSize(finding Finding) int64 {
	raw, ok := finding.Details["size"]
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}
