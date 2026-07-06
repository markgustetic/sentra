// Package recoverykit builds and renders a Sentra "recovery kit": a
// non-secret record of a repository's identity, storage location, and
// latest snapshot, plus copyable check/list/restore commands. It exists
// so both the `sentra recovery-kit` CLI command and the TUI's
// Recovery-Kit view render byte-identical output from one source.
//
// Invariant: a kit contains ONLY non-secret repository and config data —
// repo ID, created timestamps, bucket/prefix/region/profile/endpoint,
// and snapshot summaries. It never reads or emits the passphrase,
// wrapped repo key, salt, MAC material, or AWS credentials. The renderers
// close with an explicit disclaimer to make that guarantee auditable.
package recoverykit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// Kit holds the non-secret recovery notes. Field names and JSON tags are
// preserved exactly from the former cli.recoveryKit so existing kit files
// and any external consumers of the JSON keep parsing unchanged.
type Kit struct {
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

// Build assembles a Kit from an opened repo and its resolved config. It
// reads only the snapshot list and the non-secret repo/config fields; it
// deliberately does not touch RepoConfig.Salt/WrappedRepoKey/MAC/KDF.
func Build(ctx context.Context, r *repo.Repo, cfg *config.Config, cfgPath string) (Kit, error) {
	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		return Kit{}, fmt.Errorf("list snapshots: %w", err)
	}

	repoCfg := r.Config()
	kit := Kit{
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

// MarshalJSON renders the kit as indented JSON with a trailing newline
// (so piping to a file leaves a POSIX-clean last line).
func MarshalJSON(k Kit) ([]byte, error) {
	body, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}
	return append(body, '\n'), nil
}

// RenderMarkdown renders a human-readable kit. Empty storage fields print
// as "-" via the local dash helper — emptyDash lives in internal/cli for
// its other callers and is intentionally not imported here (config must
// not depend on cli, and neither must this package).
func RenderMarkdown(k Kit) string {
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	var b strings.Builder
	fmt.Fprintln(&b, "# Sentra Recovery Kit")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Generated: %s\n\n", k.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintln(&b, "## Repository")
	fmt.Fprintf(&b, "- Repo ID: %s\n", k.RepoID)
	fmt.Fprintf(&b, "- Created: %s\n", k.RepoCreatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Config: %s\n", k.ConfigPath)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Storage")
	fmt.Fprintf(&b, "- Bucket: %s\n", dash(k.Bucket))
	fmt.Fprintf(&b, "- Prefix: %s\n", dash(k.Prefix))
	fmt.Fprintf(&b, "- Region: %s\n", dash(k.Region))
	fmt.Fprintf(&b, "- Profile: %s\n", dash(k.Profile))
	fmt.Fprintf(&b, "- Endpoint URL: %s\n", dash(k.EndpointURL))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Snapshots")
	fmt.Fprintf(&b, "- Snapshot count: %d\n", k.SnapshotCount)
	if k.LatestSnapshotID != "" {
		fmt.Fprintf(&b, "- Latest snapshot: %s\n", k.LatestSnapshotID)
		fmt.Fprintf(&b, "- Latest created: %s\n", k.LatestSnapshotAt.Format(time.RFC3339))
		fmt.Fprintf(&b, "- Latest tag: %s\n", dash(k.LatestSnapshotTag))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Recovery Commands")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "```bash")
	for _, command := range k.Commands {
		fmt.Fprintln(&b, command)
	}
	fmt.Fprintln(&b, "```")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "This file intentionally excludes passphrases, wrapped keys, salts, and MAC material.")
	return b.String()
}
