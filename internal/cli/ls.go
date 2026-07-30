package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/ui"
)

// LsDeps wires the side-effecting pieces of `sentra ls`.
type LsDeps struct {
	RepoDeps
}

// lsJSONRow is the explicit JSON schema for one tree entry. Kind is
// always spelled out ("file", "dir", "symlink") even though the
// manifest's wire form uses "" for files — consumers of the CLI's
// JSON shouldn't need to know that encoding quirk.
type lsJSONRow struct {
	Path       string      `json:"path"`
	Kind       string      `json:"kind"`
	Size       int64       `json:"size"`
	Mode       os.FileMode `json:"mode"`
	MTime      time.Time   `json:"mtime"`
	LinkTarget string      `json:"link_target,omitempty"`
}

// NewLs returns the cobra command for `sentra ls <snapshot>` — the
// CLI answer to "is my file in this snapshot?". The TUI has detail
// views for this; scripts and terminals had nothing.
func NewLs(deps LsDeps) *cobra.Command {
	var (
		asJSON  bool
		cfgPath string
	)
	cmd := &cobra.Command{
		Use:   "ls <snapshot>",
		Short: "List the files in a snapshot",
		Long: "Print every entry in a snapshot's tree: files with sizes, " +
			"directories with a trailing slash, symlinks with their target. " +
			"The snapshot may be a full ID, \"latest\", a unique prefix, or " +
			"the trailing hex shown by `sentra snapshots`.",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLs(cmd, deps, args[0], asJSON, cfgPath)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a JSON array instead of text lines")
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	return cmd
}

func runLs(cmd *cobra.Command, deps LsDeps, ref string, asJSON bool, cfgPath string) error {
	cmd.SilenceUsage = true

	r, pass, _, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	snapID, err := r.ResolveSnapshotID(cmd.Context(), ref)
	if err != nil {
		return err
	}
	m, err := r.LoadSnapshot(cmd.Context(), snapID)
	if err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}

	out := cmdStdout(cmd, deps.Stdout)
	if asJSON {
		rows := make([]lsJSONRow, 0, len(m.Tree))
		for _, fe := range m.Tree {
			kind := "file"
			switch {
			case fe.IsDir():
				kind = "dir"
			case fe.IsSymlink():
				kind = "symlink"
			}
			rows = append(rows, lsJSONRow{
				Path:       fe.Path,
				Kind:       kind,
				Size:       fe.Size,
				Mode:       fe.Mode,
				MTime:      fe.MTime,
				LinkTarget: fe.LinkTarget,
			})
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			return fmt.Errorf("encode json: %w", err)
		}
		return nil
	}

	fmt.Fprintf(out, "%s  %s  %d files, %s\n",
		ui.Primary.Render(m.ID),
		m.CreatedAt.UTC().Format(time.RFC3339),
		m.Stats.Files,
		ui.FormatBytes(m.Stats.Bytes),
	)
	for _, fe := range m.Tree {
		switch {
		case fe.IsDir():
			fmt.Fprintf(out, "  %10s  %s/\n", "-", fe.Path)
		case fe.IsSymlink():
			fmt.Fprintf(out, "  %10s  %s -> %s\n", "-", fe.Path, fe.LinkTarget)
		default:
			fmt.Fprintf(out, "  %10s  %s\n", ui.FormatBytes(fe.Size), fe.Path)
		}
	}
	return nil
}
