package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// RecoveryKitDeps wires `sentra recovery-kit`.
type RecoveryKitDeps struct {
	RepoDeps
}

type recoveryKit struct {
	GeneratedAt       time.Time `json:"generated_at"`
	ConfigPath        string    `json:"config_path"`
	RepoID            string    `json:"repo_id"`
	RepoCreatedAt     time.Time `json:"repo_created_at"`
	Bucket            string    `json:"bucket"`
	Prefix            string    `json:"prefix"`
	Region            string    `json:"region"`
	Profile           string    `json:"profile"`
	EndpointURL       string    `json:"endpoint_url"`
	SnapshotCount     int       `json:"snapshot_count"`
	LatestSnapshotID  string    `json:"latest_snapshot_id,omitempty"`
	LatestSnapshotAt  time.Time `json:"latest_snapshot_at,omitempty"`
	LatestSnapshotTag string    `json:"latest_snapshot_tag,omitempty"`
	Commands          []string  `json:"commands"`
}

// NewRecoveryKit returns `sentra recovery-kit`.
func NewRecoveryKit(deps RecoveryKitDeps) *cobra.Command {
	var (
		cfgPath string
		outPath string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "recovery-kit",
		Short: "Export non-secret repository recovery notes",
		Long: "Write a non-secret recovery kit containing repository identity, " +
			"storage location, latest snapshot, and copyable check/list/restore commands.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRecoveryKit(cmd, deps, cfgPath, outPath, asJSON)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	cmd.Flags().StringVar(&outPath, "out", "",
		"write the kit to this path instead of stdout")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"emit JSON instead of Markdown")
	return cmd
}

func runRecoveryKit(cmd *cobra.Command, deps RecoveryKitDeps, cfgPath, outPath string, asJSON bool) error {
	cmd.SilenceUsage = true

	r, pass, cfg, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	kit, err := buildRecoveryKit(cmd.Context(), r, cfg, cfgPath)
	if err != nil {
		return err
	}

	var body []byte
	if asJSON {
		body, err = marshalRecoveryKitJSON(kit)
	} else {
		body = []byte(renderRecoveryKitMarkdown(kit))
	}
	if err != nil {
		return err
	}

	out := cmdStdout(cmd, deps.Stdout)
	if outPath != "" {
		if err := os.WriteFile(outPath, body, 0o600); err != nil {
			return fmt.Errorf("write recovery kit: %w", err)
		}
		fmt.Fprintf(out, "%s %s\n", ui.Success.Render("Recovery kit written:"), outPath)
		return nil
	}
	_, err = out.Write(body)
	return err
}

func buildRecoveryKit(ctx context.Context, r *repo.Repo, cfg *config.Config, cfgPath string) (recoveryKit, error) {
	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		return recoveryKit{}, fmt.Errorf("list snapshots: %w", err)
	}

	repoCfg := r.Config()
	kit := recoveryKit{
		GeneratedAt:   time.Now().UTC(),
		ConfigPath:    cfgPath,
		RepoID:        repoCfg.ID,
		RepoCreatedAt: repoCfg.CreatedAt,
		Bucket:        cfg.Repo.S3.Bucket,
		Prefix:        cfg.Repo.S3.Prefix,
		Region:        cfg.Repo.S3.Region,
		Profile:       cfg.Repo.S3.Profile,
		EndpointURL:   cfg.Repo.S3.EndpointURL,
		SnapshotCount: len(snaps),
	}
	if len(snaps) > 0 {
		latest := snaps[0]
		kit.LatestSnapshotID = latest.ID
		kit.LatestSnapshotAt = latest.CreatedAt
		kit.LatestSnapshotTag = latest.Tag
	}

	restoreID := kit.LatestSnapshotID
	if restoreID == "" {
		restoreID = "<snapshot-id>"
	}
	kit.Commands = []string{
		fmt.Sprintf("sentra check --config %s", cfgPath),
		fmt.Sprintf("sentra snapshots --config %s", cfgPath),
		fmt.Sprintf("sentra restore %s <dest-dir> --config %s --verify", restoreID, cfgPath),
	}
	return kit, nil
}

func marshalRecoveryKitJSON(kit recoveryKit) ([]byte, error) {
	body, err := json.MarshalIndent(kit, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}
	return append(body, '\n'), nil
}

func renderRecoveryKitMarkdown(kit recoveryKit) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# Sentra Recovery Kit")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Generated: %s\n\n", kit.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintln(&b, "## Repository")
	fmt.Fprintf(&b, "- Repo ID: %s\n", kit.RepoID)
	fmt.Fprintf(&b, "- Created: %s\n", kit.RepoCreatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Config: %s\n", kit.ConfigPath)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Storage")
	fmt.Fprintf(&b, "- Bucket: %s\n", emptyDash(kit.Bucket))
	fmt.Fprintf(&b, "- Prefix: %s\n", emptyDash(kit.Prefix))
	fmt.Fprintf(&b, "- Region: %s\n", emptyDash(kit.Region))
	fmt.Fprintf(&b, "- Profile: %s\n", emptyDash(kit.Profile))
	fmt.Fprintf(&b, "- Endpoint URL: %s\n", emptyDash(kit.EndpointURL))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Snapshots")
	fmt.Fprintf(&b, "- Snapshot count: %d\n", kit.SnapshotCount)
	if kit.LatestSnapshotID != "" {
		fmt.Fprintf(&b, "- Latest snapshot: %s\n", kit.LatestSnapshotID)
		fmt.Fprintf(&b, "- Latest created: %s\n", kit.LatestSnapshotAt.Format(time.RFC3339))
		fmt.Fprintf(&b, "- Latest tag: %s\n", emptyDash(kit.LatestSnapshotTag))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Recovery Commands")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "```bash")
	for _, command := range kit.Commands {
		fmt.Fprintln(&b, command)
	}
	fmt.Fprintln(&b, "```")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "This file intentionally excludes passphrases, wrapped keys, salts, and MAC material.")
	return b.String()
}
