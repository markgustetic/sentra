package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// RootFlags collects the values of persistent flags that subcommand
// bodies need to read at runtime. Wired through Deps so production and
// tests share the same plumbing — no globals.
//
// Today this only carries --passphrase-file; future cross-cutting flags
// (a config-path override, a verbosity toggle) belong here too.
type RootFlags struct {
	// PassphraseFile is the path passed via --passphrase-file. Empty
	// means "no file source"; the resolver will fall through to the
	// next priority (env / keyring / prompt).
	PassphraseFile string
}

// NewRoot returns the root cobra command without exposing the flags
// struct. Useful for tests / callers that don't care about reading
// persistent flag values back. The flags are still registered (and
// parsed normally), they're just not surfaced to the caller.
func NewRoot(version, commit, date string) *cobra.Command {
	return NewRootWithFlags(version, commit, date, &RootFlags{})
}

// NewRootWithFlags is the wiring point for production: pass in a
// pointer to a RootFlags and the persistent flag values get written
// into it as cobra parses argv. Subcommand bodies capture the same
// pointer via their Deps and read the live values at run time.
//
// Persistent flags:
//   - --passphrase-file <path>   sourced first by the passphrase resolver
func NewRootWithFlags(version, commit, date string, flags *RootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sentra",
		Short:   "Encrypted versioned S3 backups with an agentic sidekick",
		Long:    "Sentra backs up directories to S3 as encrypted, versioned snapshots and runs a hybrid heuristics+LLM agent that audits the repo.",
		Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.PersistentFlags().StringVar(&flags.PassphraseFile, "passphrase-file", "",
		"path to a file containing the repository passphrase (overrides SENTRA_PASSPHRASE)")
	return cmd
}
