package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// DiffDeps wires the side-effecting pieces of `sentra diff`.
type DiffDeps struct {
	RepoDeps
}

// NewDiff returns the cobra command for `sentra diff <snap-a> <snap-b>`.
// Default rendering is a styled table with columns "Status", "Path";
// --json emits a stable {added, removed, changed} schema.
func NewDiff(deps DiffDeps) *cobra.Command {
	var (
		asJSON  bool
		cfgPath string
	)
	cmd := &cobra.Command{
		Use:   "diff <snap-a> <snap-b>",
		Short: "Show added/removed/changed paths between two snapshots",
		Long: "Compare two snapshots and print the per-path delta. Three " +
			"categories: added (in B not A), removed (in A not B), changed " +
			"(in both, with different size or mtime).",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiff(cmd, deps, args[0], args[1], asJSON, cfgPath)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a styled table")
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
	return cmd
}

// diffJSONPayload is the public JSON schema for `sentra diff --json`.
// Stable across repo.DiffResult refactors so consumers can rely on
// the field names.
type diffJSONPayload struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

// runDiff is the body of `sentra diff`.
func runDiff(cmd *cobra.Command, deps DiffDeps, idA, idB string, asJSON bool, cfgPath string) error {
	cmd.SilenceUsage = true
	cfgPath, err := resolveConfigPath(cmd, cfgPath)
	if err != nil {
		return err
	}

	r, pass, _, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	// Accept "latest", unique prefixes, or trailing-hex shorthand for
	// either side.
	if idA, err = r.ResolveSnapshotID(cmd.Context(), idA); err != nil {
		return err
	}
	if idB, err = r.ResolveSnapshotID(cmd.Context(), idB); err != nil {
		return err
	}

	res, err := r.Diff(cmd.Context(), idA, idB)
	if err != nil {
		return fmt.Errorf("diff: %w", err)
	}

	// Sort each list so the output is stable regardless of map
	// iteration order in repo.Diff. Output stability matters for
	// scripts that pipe `sentra diff --json` into other tools.
	slices.Sort(res.Added)
	slices.Sort(res.Removed)
	slices.Sort(res.Changed)

	out := cmdStdout(cmd, deps.Stdout)

	if asJSON {
		return writeDiffJSON(out, res)
	}
	return writeDiffTable(out, res)
}

// writeDiffJSON emits the {added, removed, changed} schema. We use
// non-nil empty slices so the JSON shows `[]` rather than `null`,
// which is friendlier for scripted consumers.
func writeDiffJSON(w io.Writer, res repo.DiffResult) error {
	payload := diffJSONPayload{
		Added:   nilToEmpty(res.Added),
		Removed: nilToEmpty(res.Removed),
		Changed: nilToEmpty(res.Changed),
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

// writeDiffTable emits a single styled table with two columns:
// Status (added / removed / changed) and Path. We pick a single
// table over three side-by-side panels because terminal widths
// vary; one tall list scrolls cleanly. A future flag could opt
// into multi-column rendering for wide terminals.
func writeDiffTable(w io.Writer, res repo.DiffResult) error {
	headers := []string{"Status", "Path"}
	rows := make([][]string, 0, len(res.Added)+len(res.Removed)+len(res.Changed))
	for _, p := range res.Added {
		rows = append(rows, []string{ui.Success.Render("+ added"), p})
	}
	for _, p := range res.Removed {
		rows = append(rows, []string{ui.Danger.Render("- removed"), p})
	}
	for _, p := range res.Changed {
		rows = append(rows, []string{ui.Warn.Render("~ changed"), p})
	}
	if len(rows) == 0 {
		// Empty diffs deserve an explicit message, not a blank
		// header-only table that looks like a bug.
		_, err := fmt.Fprintln(w, ui.Subtle.Render("No differences."))
		return err
	}
	if _, err := fmt.Fprintln(w, ui.RenderTable(headers, rows)); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	// Also print a concise summary line so users get totals at a glance.
	fmt.Fprintf(w, "%d added, %d removed, %d changed\n",
		len(res.Added), len(res.Removed), len(res.Changed))
	return nil
}

// nilToEmpty returns an empty (but non-nil) slice if s is nil. Used
// so JSON encodes `[]` rather than `null` for empty categories.
func nilToEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
