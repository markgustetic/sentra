package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	rootFlags := &cli.RootFlags{}
	root := cli.NewRootWithFlags(version, commit, date, rootFlags)

	configureRootLogging(root, rootFlags)
	addProductionCommands(root, rootFlags)

	if err := root.Execute(); err != nil {
		// cobra prints the error itself when SilenceErrors is false; we
		// just need to propagate the non-zero exit so scripts can detect
		// failure.
		os.Exit(1)
	}
}

// isUICommand reports whether cmd is the bare-sentra dispatch or the
// explicit `sentra ui` subcommand. Both take over the terminal with
// Bubbletea's alt-screen, so slog must not write to stderr during them.
func isUICommand(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if cmd.Parent() == nil {
		return true
	}
	return cmd.Use == "ui"
}

func configureRootLogging(root *cobra.Command, rootFlags *cli.RootFlags) {
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		tuiMode := isUICommand(cmd)
		cleanup, err := cli.ConfigureSlog(rootFlags, tuiMode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sentra: warning: log setup failed: %v (falling back to stderr)\n", err)
		}
		_ = cleanup
		return nil
	}
}
