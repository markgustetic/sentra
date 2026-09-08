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
	addProductionCommands(root, rootFlags, version, commit)

	// Every command runs under a signal-cancelled context (see
	// signalContext) so deferred cleanup — the repo lock above all —
	// gets to run on SIGINT/SIGTERM.
	if code := execute(root, os.Exit); code != 0 {
		os.Exit(code)
	}
}

// isUICommand reports whether cmd ends in the Bubbletea alt-screen:
// the bare-sentra dispatch, `sentra ui`, or `sentra local` (which
// finishes by launching the same TUI against .sentra-local.yaml).
// During any of these, slog must not write to stderr — raw log lines
// interleave into the live screen buffer and corrupt the display.
func isUICommand(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if cmd.Parent() == nil {
		return true
	}
	return cmd.Name() == "ui" || cmd.Name() == "local"
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
