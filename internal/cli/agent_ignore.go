package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/ui"
	"github.com/markgustetic/sentra/internal/walker"
)

type ignoreAdvice struct {
	Pattern  string `json:"pattern"`
	Category string `json:"category"`
	Target   string `json:"target"`
	Reason   string `json:"reason"`
	Size     int64  `json:"size,omitempty"`
}

// NewAgentAdviseIgnore returns `sentra agent advise-ignore [root]`.
func NewAgentAdviseIgnore(deps AgentDeps) *cobra.Command {
	var (
		cfgPath        string
		asJSON         bool
		largeFileBytes int64
	)
	cmd := &cobra.Command{
		Use:   "advise-ignore [root]",
		Short: "Suggest first-run .sentraignore patterns",
		Long: "Walk a project tree and suggest .sentraignore patterns for " +
			"cache directories and unusually large files. The command is read-only.",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) == 1 {
				root = args[0]
			}
			return runAgentAdviseIgnore(cmd, deps, root, cfgPath, asJSON, largeFileBytes)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	cmd.Flags().Int64Var(&largeFileBytes, "large-file-bytes", 0,
		"large-file threshold in bytes (0 uses the heuristic default)")
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	return cmd
}

func runAgentAdviseIgnore(
	cmd *cobra.Command,
	deps AgentDeps,
	root, cfgPath string,
	asJSON bool,
	largeFileBytes int64,
) error {
	cmd.SilenceUsage = true

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	advice, err := collectIgnoreAdvice(cmd.Context(), root, cfg, largeFileBytes)
	if err != nil {
		return err
	}

	out := deps.Stdout
	if out == nil {
		out = cmd.OutOrStdout()
	}
	if asJSON {
		return writeIgnoreAdviceJSON(out, advice)
	}
	return writeIgnoreAdviceTable(out, advice)
}

func collectIgnoreAdvice(
	ctx context.Context,
	root string,
	cfg *config.Config,
	largeFileBytes int64,
) ([]ignoreAdvice, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("agent ignore advice: abs root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	walkerOpts := walker.Options{
		IgnoreFile:    cfg.Backup.IgnoreFile,
		ExcludeCaches: cfg.Backup.ExcludeCaches,
	}
	normalizeBackupWalkerOptions(&walkerOpts)

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
		return nil, fmt.Errorf("agent ignore advice: walk: %w", err)
	}

	registry := heuristics.NewRegistry(heuristics.NewCacheDirs(), heuristics.NewLargeFiles())
	findings, err := registry.Run(ctx, heuristics.Input{
		Walked: walked,
		Config: heuristics.InputConfig{LargeFileBytes: largeFileBytes},
	})
	if err != nil {
		return nil, fmt.Errorf("agent ignore advice: heuristics: %w", err)
	}

	seen := make(map[string]struct{})
	advice := make([]ignoreAdvice, 0, len(findings))
	for _, finding := range findings {
		pattern := ignorePatternForFinding(absRoot, finding)
		if pattern == "" {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		advice = append(advice, ignoreAdvice{
			Pattern:  pattern,
			Category: finding.Category,
			Target:   finding.Target,
			Reason:   ignoreReason(finding.Category),
			Size:     findingSize(finding),
		})
	}
	slices.SortFunc(advice, func(a, b ignoreAdvice) int {
		return cmp.Compare(a.Pattern, b.Pattern)
	})
	return advice, nil
}

func ignorePatternForFinding(absRoot string, finding heuristics.Finding) string {
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

func findingSize(finding heuristics.Finding) int64 {
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

func writeIgnoreAdviceJSON(w io.Writer, advice []ignoreAdvice) error {
	if advice == nil {
		advice = []ignoreAdvice{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(advice); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

func writeIgnoreAdviceTable(w io.Writer, advice []ignoreAdvice) error {
	if len(advice) == 0 {
		fmt.Fprintln(w, ui.Subtle.Render("No obvious .sentraignore suggestions."))
		return nil
	}
	headers := []string{"Pattern", "Reason"}
	rows := make([][]string, 0, len(advice))
	for _, item := range advice {
		rows = append(rows, []string{item.Pattern, item.Reason})
	}
	fmt.Fprintln(w, ui.Primary.Render("Suggested .sentraignore patterns"))
	if _, err := fmt.Fprintln(w, ui.RenderTable(headers, rows)); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	return nil
}
