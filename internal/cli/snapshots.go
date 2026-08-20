package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// SnapshotsDeps wires the side-effecting pieces of `sentra snapshots`.
// Production fills these from main.go; tests inject a memory store
// and static passphrase.
type SnapshotsDeps struct {
	RepoDeps
}

// NewSnapshots returns the cobra command for `sentra snapshots`.
// Flags:
//   - --json    emit a JSON array instead of the styled table
//   - --config  override the default sentra.yaml location
//
// The default output is a lipgloss table with columns
// `ID, Created, Tag, Files, Bytes` rendered via ui.RenderTable.
// The JSON output emits a stable schema: each row has id,
// created_at (RFC3339), tag, files (int), bytes (int), new_bytes (int).
func NewSnapshots(deps SnapshotsDeps) *cobra.Command {
	var (
		asJSON  bool
		cfgPath string
	)
	cmd := &cobra.Command{
		Use:   "snapshots",
		Short: "List snapshots in the configured repository",
		Long: "List every snapshot in the repository, ordered newest-first. " +
			"Pass --json for a parseable schema; default is a styled table.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSnapshots(cmd, deps, asJSON, cfgPath)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a styled table")
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
	return cmd
}

// snapshotJSONRow is the explicit JSON schema. Pulling it out lets us
// keep the JSON tag names stable across repo.SnapshotInfo refactors —
// the repo struct uses json tags too, but the CLI's wire format is
// the user-facing contract and lives here.
type snapshotJSONRow struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Tag       string    `json:"tag"`
	Files     int       `json:"files"`
	Bytes     int64     `json:"bytes"`
	NewBytes  int64     `json:"new_bytes"`
}

// runSnapshots is the body of `sentra snapshots`.
func runSnapshots(cmd *cobra.Command, deps SnapshotsDeps, asJSON bool, cfgPath string) error {
	cmd.SilenceUsage = true
	cfgPath = resolveConfigPath(cmd, cfgPath)

	r, pass, _, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	snaps, err := r.ListSnapshots(cmd.Context())
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}

	out := cmdStdout(cmd, deps.Stdout)

	if asJSON {
		return writeSnapshotsJSON(out, snaps)
	}
	return writeSnapshotsTable(out, snaps)
}

// writeSnapshotsJSON emits a JSON array of snapshotJSONRow. An empty
// repository emits `[]` rather than `null` so consumers can iterate
// without nil checks.
func writeSnapshotsJSON(w io.Writer, snaps []repo.SnapshotInfo) error {
	rows := make([]snapshotJSONRow, 0, len(snaps))
	for _, s := range snaps {
		rows = append(rows, snapshotJSONRow{
			ID:        s.ID,
			CreatedAt: s.CreatedAt,
			Tag:       s.Tag,
			Files:     s.Stats.Files,
			Bytes:     s.Stats.Bytes,
			NewBytes:  s.Stats.NewBytes,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rows); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

// writeSnapshotsTable emits the styled lipgloss table. We always
// render the headers, even on empty input, so the user sees a
// consistent "no rows" frame instead of a blank screen.
func writeSnapshotsTable(w io.Writer, snaps []repo.SnapshotInfo) error {
	headers := []string{"ID", "Created", "Tag", "Files", "Bytes"}
	rows := make([][]string, 0, len(snaps))
	for _, s := range snaps {
		rows = append(rows, []string{
			s.ID,
			s.CreatedAt.UTC().Format(time.RFC3339),
			emptyDash(s.Tag),
			fmt.Sprintf("%d", s.Stats.Files),
			ui.FormatBytes(s.Stats.Bytes),
		})
	}
	if _, err := fmt.Fprintln(w, ui.RenderTable(headers, rows)); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	return nil
}
