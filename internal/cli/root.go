package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewRoot(version, commit, date string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sentra",
		Short:   "Encrypted versioned S3 backups with an agentic sidekick",
		Long:    "Sentra backs up directories to S3 as encrypted, versioned snapshots and runs a hybrid heuristics+LLM agent that audits the repo.",
		Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	return cmd
}
