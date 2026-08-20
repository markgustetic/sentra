package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/ui"
	"github.com/markgustetic/sentra/internal/walker"
)

// ignoreAdvice aliases the shared heuristics type so the CLI's JSON
// wire format and the TUI's advice pane can never drift.
type ignoreAdvice = heuristics.IgnoreAdvice

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
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
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
	cfgPath = resolveConfigPath(cmd, cfgPath)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	advice, err := collectIgnoreAdvice(cmd.Context(), root, cfg, largeFileBytes)
	if err != nil {
		return err
	}

	out := cmdStdout(cmd, deps.Stdout)
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
	walkerOpts := walker.Options{
		IgnoreFile:    cfg.Backup.IgnoreFile,
		ExcludeCaches: cfg.Backup.ExcludeCaches,
	}
	normalizeBackupWalkerOptions(&walkerOpts)
	return heuristics.CollectIgnoreAdvice(ctx, root, walkerOpts, largeFileBytes)
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
